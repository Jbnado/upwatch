// Comando upwatch é o servidor: agendador, API e interface num processo.
//
// Um binário só, sem processo auxiliar nem servidor web ao lado, porque a
// promessa da ferramenta é subir uma imagem e usar.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bernardojoao/upwatch/internal/api"
	"github.com/bernardojoao/upwatch/internal/auth"
	"github.com/bernardojoao/upwatch/internal/checker"
	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/config"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/incident"
	"github.com/bernardojoao/upwatch/internal/notifier"
	"github.com/bernardojoao/upwatch/internal/rollup"
	"github.com/bernardojoao/upwatch/internal/scheduler"
	"github.com/bernardojoao/upwatch/internal/sentinel"
	"github.com/bernardojoao/upwatch/internal/store"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
	"github.com/bernardojoao/upwatch/internal/web"
)

// version é preenchido no build pelo ldflags.
var version = "dev"

// shutdownGrace é quanto tempo o processo espera para encerrar sozinho
// antes de o orquestrador matá-lo. O flush final das batidas precisa
// caber aqui: perdê-lo apagaria justamente a janela em que algo caiu.
const shutdownGrace = 20 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("upwatch encerrou com erro", "erro", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", os.Getenv("UPWATCH_CONFIG"),
		"caminho do arquivo de configuração (opcional)")
	showVersion := flag.Bool("version", false, "mostra a versão e sai")
	flag.Parse()

	if *showVersion {
		fmt.Println("upwatch", version)
		return nil
	}

	setupLogging()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	// O contexto encerra no primeiro sinal. O segundo deixa o processo
	// morrer de imediato, para quem estiver com pressa não ficar preso a
	// um desligamento que travou.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := build(cfg, st)
	if err != nil {
		return err
	}

	slog.Info("upwatch no ar",
		"versao", version,
		"endereco", cfg.Listen,
		"banco", cfg.Database.Driver,
		"retencao_crua", cfg.Retention.Raw,
		"retencao_horaria", cfg.Retention.Hourly,
		"retencao_diaria", cfg.Retention.Daily,
	)

	return app.run(ctx, cfg)
}

// application reúne as peças em execução.
type application struct {
	store     store.Store
	writer    *store.BatchWriter
	scheduler *scheduler.Scheduler
	incidents *incident.Engine
	alerts    *notifier.Dispatcher
	rollups   *rollup.Worker
	auth      *auth.Service
	handler   http.Handler
}

// monitorFanout entrega a mudança de monitor a todos os interessados.
//
// O agendador precisa saber quando verificar; o motor de incidentes
// precisa do limiar de confirmação. Sem o repasse, um monitor criado pela
// interface passaria a ser verificado mas nunca geraria alerta.
type monitorFanout []api.MonitorSink

func (f monitorFanout) Upsert(m domain.Monitor) {
	for _, sink := range f {
		sink.Upsert(m)
	}
}

func (f monitorFanout) Remove(id int64) {
	for _, sink := range f {
		sink.Remove(id)
	}
}

// build monta o grafo de dependências.
func build(cfg config.Config, st store.Store) (*application, error) {
	real := clock.Real()

	writer := store.NewBatchWriter(st, store.BatchWriterOptions{Clock: real})

	registry, err := checker.NewRegistry(
		checker.NewHTTP(),
		checker.NewTCP(),
		checker.NewTLS(),
		checker.NewDNS(),
		checker.NewICMP(),
		checker.NewPush(st, real),
	)
	if err != nil {
		return nil, err
	}

	// A sentinela evita que perder conectividade dispare alerta sobre
	// todos os alvos ao mesmo tempo, quando eles seguem no ar.
	if len(cfg.SentinelTargets) > 0 {
		registry.WithNetworkProbe(sentinel.New(sentinel.Options{
			Targets: cfg.SentinelTargets,
			Clock:   real,
		}))
	}

	alerts := notifier.NewDispatcher(notifier.DispatcherOptions{Clock: real})

	// O motor fica entre o agendador e o escritor: observa cada batida a
	// caminho do banco. Assim nem o agendador precisa saber de incidentes,
	// nem o escritor de alertas.
	engine := incident.NewEngine(writer, st, alerts)

	sched := scheduler.New(registry, engine, scheduler.Options{
		Workers: cfg.Workers,
		Clock:   real,
	})

	authSvc := auth.New(st, auth.Options{Clock: real, SessionTTL: cfg.SessionTTL})

	apiHandler := api.New(api.Options{
		Store:         st,
		Auth:          authSvc,
		Checkers:      registry,
		Clock:         real,
		Scheduler:     monitorFanout{sched, engine},
		SecureCookies: cfg.SecureCookies,
		SessionTTL:    cfg.SessionTTL,
	})

	return &application{
		store:     st,
		writer:    writer,
		scheduler: sched,
		incidents: engine,
		alerts:    alerts,
		rollups: rollup.NewWorker(st, rollup.Options{
			Interval:  cfg.RollupInterval,
			Retention: cfg.RetentionPolicy(),
			Clock:     real,
		}),
		auth:    authSvc,
		handler: mux(apiHandler),
	}, nil
}

// mux encaminha a API e entrega o resto à interface.
func mux(apiHandler http.Handler) http.Handler {
	spa := web.Handler()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
			apiHandler.ServeHTTP(w, r)
			return
		}
		spa.ServeHTTP(w, r)
	})
}

// run sobe todos os componentes e aguarda o sinal de encerramento.
func (a *application) run(ctx context.Context, cfg config.Config) error {
	if err := a.loadMonitors(ctx); err != nil {
		return err
	}
	// Retomar o estado confirmado antes de aceitar batidas: sem isso o
	// reinício zeraria a contagem, e um alvo prestes a ser declarado fora
	// do ar recomeçaria do zero.
	if err := a.incidents.Load(ctx); err != nil {
		return fmt.Errorf("carregando estado dos monitores: %w", err)
	}

	var wg sync.WaitGroup
	spawn := func(name string, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx)
			slog.Debug("componente encerrado", "componente", name)
		}()
	}

	spawn("batch-writer", a.writer.Run)
	spawn("scheduler", a.scheduler.Run)
	spawn("rollup", a.rollups.Run)
	spawn("alertas", a.alerts.Run)
	spawn("sessoes", a.sweepSessions)

	server := &http.Server{
		Addr:    cfg.Listen,
		Handler: a.handler,
		// Sem prazo de leitura de cabeçalho, uma conexão aberta e ociosa
		// segura um descritor indefinidamente.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	erros := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			erros <- err
		}
	}()

	select {
	case err := <-erros:
		return err
	case <-ctx.Done():
		slog.Info("encerrando; aguardando o trabalho em andamento")
	}

	// O servidor para de aceitar antes de os componentes encerrarem, para
	// nenhuma requisição chegar a um agendador que já saiu.
	desliga, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	if err := server.Shutdown(desliga); err != nil {
		slog.Warn("servidor HTTP nao encerrou no prazo", "erro", err)
	}

	wg.Wait()
	slog.Info("upwatch encerrado")
	return nil
}

// loadMonitors entrega ao agendador o que já está cadastrado.
func (a *application) loadMonitors(ctx context.Context) error {
	filter := store.MonitorFilter{Page: store.PageFilter{Limit: store.MaxPageSize}}
	total := 0

	for {
		page, err := a.store.Monitors().List(ctx, filter)
		if err != nil {
			return fmt.Errorf("carregando monitores: %w", err)
		}
		for _, m := range page.Items {
			a.scheduler.Upsert(m)
			// O motor precisa do limiar de confirmação de cada monitor;
			// sem isso, uma queda depois do arranque não viraria alerta.
			a.incidents.Upsert(m)
			total++
		}
		if !page.HasMore || len(page.Items) == 0 {
			break
		}
		filter.Page.AfterID = page.Items[len(page.Items)-1].ID
	}

	slog.Info("monitores carregados", "total", total)
	return nil
}

// sweepSessions descarta sessões vencidas periodicamente.
//
// Sem isso a tabela cresceria para sempre numa instalação de longa
// duração, guardando credenciais que já não valem.
func (a *application) sweepSessions(ctx context.Context) {
	const intervalo = time.Hour

	ticker := time.NewTicker(intervalo)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := a.auth.SweepExpiredSessions(ctx); err != nil {
				slog.Warn("falha ao limpar sessoes vencidas", "erro", err)
			} else if n > 0 {
				slog.Debug("sessoes vencidas removidas", "total", n)
			}
		}
	}
}

// openStore abre o armazenamento configurado.
func openStore(cfg config.Config) (store.Store, error) {
	switch cfg.Database.Driver {
	case "sqlite":
		return sqlstore.OpenSQLite(cfg.Database.DSN)
	case "postgres":
		// O driver PostgreSQL chega no M7; recusar aqui com a causa é
		// melhor que falhar depois com erro de conexão enigmático.
		return nil, fmt.Errorf("o driver postgres ainda nao esta disponivel nesta versao")
	default:
		return nil, fmt.Errorf("driver de banco desconhecido %q", cfg.Database.Driver)
	}
}

// setupLogging configura a saída estruturada.
//
// Texto por padrão, porque quem sobe em casa lê o log no terminal; JSON
// quando pedido, para quem coleta.
func setupLogging() {
	nivel := slog.LevelInfo
	if strings.EqualFold(os.Getenv("UPWATCH_LOG_LEVEL"), "debug") {
		nivel = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{Level: nivel}
	var handler slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if strings.EqualFold(os.Getenv("UPWATCH_LOG_FORMAT"), "json") {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler))
}

// Garante em tempo de compilação que o agendador atende ao que a API
// espera. É o que faz um monitor cadastrado na interface passar a ser
// verificado na hora, sem reinício.
var (
	_ api.MonitorSink = (*scheduler.Scheduler)(nil)
	_ api.MonitorSink = (*incident.Engine)(nil)
	_ incident.Sink   = (*store.BatchWriter)(nil)
)
