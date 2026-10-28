package probe

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
)

// WatchInterval é de quanto em quanto o daemon confere se o arquivo
// mudou.
//
// Conferência por conteúdo, e não por evento do sistema de arquivos: o
// volume pode ser bind mount, NFS ou overlay, e notificação de mudança
// falha em silêncio em vários deles. Ler um arquivo de poucos KB a cada
// dois segundos não custa nada.
const WatchInterval = 2 * time.Second

// DaemonOptions são as dependências do Daemon.
type DaemonOptions struct {
	Path     string
	Clock    clock.Clock
	Send     SendFunc
	Backoff  Backoff
	Registry *checker.Registry
	Logger   *slog.Logger
}

// Daemon é a sonda em execução: lê a configuração, roda as verificações
// e relê o arquivo quando ele muda.
type Daemon struct {
	opts   DaemonOptions
	state  *StateFile
	runner *Runner

	mu       sync.Mutex
	cfg      Config
	lastSeen [32]byte
	lastErr  string

	// statusMu é separado de mu de propósito. A recarga segura mu
	// enquanto para as tarefas e espera por elas; se a tarefa precisasse
	// de mu para registrar o resultado, as duas se esperariam para sempre.
	statusMu sync.Mutex
	statuses map[string]string
}

// NewDaemon monta o daemon.
func NewDaemon(opts DaemonOptions) *Daemon {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	d := &Daemon{opts: opts, state: NewStateFile(StatePath(opts.Path)), statuses: map[string]string{}}
	d.runner = NewRunner(RunnerOptions{
		Clock: opts.Clock, Send: opts.Send, Backoff: opts.Backoff,
		OnResult:   d.onResult,
		OnDelivery: d.onDelivery,
	})
	return d
}

// State devolve o estado atual das verificações.
func (d *Daemon) State() Status { return d.state.Snapshot() }

// Config devolve a última configuração válida.
func (d *Daemon) Config() Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

// Run roda até o contexto acabar. hup força uma releitura (SIGHUP).
func (d *Daemon) Run(ctx context.Context, hup <-chan os.Signal) error {
	defer d.runner.Stop()
	d.reload(true)

	for {
		t := d.opts.Clock.NewTimer(WatchInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-hup:
			t.Stop()
			d.opts.Logger.Info("SIGHUP recebido; relendo a configuração")
			d.reload(true)
		case <-t.C():
			d.reload(false)
		}
	}
}

// Reload relê a configuração agora, como o SIGHUP.
func (d *Daemon) Reload() { d.reload(true) }

// reload relê o arquivo se ele mudou (ou sempre, com force).
func (d *Daemon) reload(force bool) {
	data, err := os.ReadFile(d.opts.Path)
	sum := sha256.Sum256(data)

	d.mu.Lock()
	defer d.mu.Unlock()
	if !force && sum == d.lastSeen {
		return
	}
	d.lastSeen = sum

	switch {
	case errors.Is(err, os.ErrNotExist):
		d.cfg = Config{}
		d.runner.Apply(nil)
		d.opts.Logger.Info("aguardando a configuração; crie-a com upwatch-probe setup ou upwatch-probe add",
			"arquivo", d.opts.Path)
		return
	case err != nil:
		d.reportLocked(err)
		return
	}

	cfg, err := Load(d.opts.Path)
	if err != nil {
		d.reportLocked(err)
		return
	}
	d.lastErr = ""
	d.cfg = cfg

	jobs := Jobs(cfg, d.opts.Registry)
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	d.state.Keep(names)
	d.runner.Apply(jobs)

	d.opts.Logger.Info("configuração carregada",
		"sonda", cfg.Name, "verificacoes", len(cfg.Checks), "sinal_de_vida", cfg.Heartbeat.PushURL != "")
}

// reportLocked diz no log por que a configuração nova foi recusada, uma
// vez por versão do arquivo. A sonda segue com a última boa: uma edição
// com erro não pode desligar o monitoramento.
func (d *Daemon) reportLocked(err error) {
	if err.Error() == d.lastErr {
		return
	}
	d.lastErr = err.Error()
	d.opts.Logger.Error("configuração recusada; seguindo com a anterior", "erro", err)
}

func (d *Daemon) onResult(name string, at time.Time, sig Signal) {
	d.state.RecordResult(name, at, sig)

	d.statusMu.Lock()
	prev := d.statuses[name]
	d.statuses[name] = sig.Status
	d.statusMu.Unlock()

	// Só a mudança vai para o log: uma linha por minuto por verificação
	// enterraria a que interessa.
	if prev != sig.Status {
		d.opts.Logger.Info("verificação mudou de estado",
			"verificacao", name, "de", prev, "para", sig.Status, "causa", sig.Msg, "latencia_ms", sig.LatencyMS)
	}
}

func (d *Daemon) onDelivery(name string, del Delivery) {
	d.state.RecordDelivery(name, del)
	if del.OK {
		return
	}
	d.opts.Logger.Warn("falha ao enviar o push",
		"verificacao", name, "tentativa", del.Attempts, "desistiu", del.GaveUp, "erro", del.Err)
}
