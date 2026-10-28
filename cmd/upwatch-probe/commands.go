package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Jbnado/upwatch/internal/probe"
)

// newToken gera o token da interface web: 32 bytes aleatórios.
func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand não falha desde o Go 1.24
	return hex.EncodeToString(b)
}

// setFlags devolve os nomes das flags passadas, para distinguir "não
// informado" de "informado vazio ou falso".
func setFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func (a *app) cmdInit(_ context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("init", "--name vm-prod-1 --heartbeat-url https://.../api/v1/push/TOKEN")
	name := fs.String("name", "", "nome da sonda, que aparece no sinal de vida")
	hbURL := fs.String("heartbeat-url", "", "endereço de push do monitor do sinal de vida")
	hbEvery := fs.Duration("heartbeat-interval", probe.DefaultInterval, "intervalo do sinal de vida")
	allowHTTP := fs.Bool("allow-http-push", false, "aceita push em HTTP sem TLS (o token viaja em claro)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	set := setFlags(fs)

	cfg, err := probe.Update(cfgPath, func(c *probe.Config) error {
		if set["allow-http-push"] {
			c.AllowHTTPPush = *allowHTTP
		}
		if set["name"] {
			c.Name = *name
		}
		if set["heartbeat-url"] {
			c.Heartbeat.PushURL = *hbURL
		}
		if set["heartbeat-interval"] || c.Heartbeat.Interval == 0 {
			c.Heartbeat.Interval = probe.Duration(*hbEvery)
		}
		if c.Heartbeat.PushURL == "" {
			c.Heartbeat.Interval = 0
		}
		// A interface web nunca sobe sem token. Gerado aqui, ela já
		// nasce fechada, mesmo que ninguém pense nela.
		if c.UIToken == "" {
			c.UIToken = newToken()
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "sonda %q gravada em %s", cfg.Name, cfgPath)
	if cfg.Heartbeat.PushURL != "" {
		fmt.Fprintf(a.stdout, "; sinal de vida a cada %s para %s", cfg.Heartbeat.Interval, probe.MaskURL(cfg.Heartbeat.PushURL))
	} else {
		fmt.Fprint(a.stdout, "; sem sinal de vida (configure com --heartbeat-url)")
	}
	fmt.Fprintln(a.stdout)
	return nil
}

// statusList é uma flag com uma lista de códigos HTTP ("200,204").
type statusList []int

func (s *statusList) String() string { return fmt.Sprint(*s) }
func (s *statusList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		code, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return fmt.Errorf("status HTTP %q não é número", part)
		}
		*s = append(*s, code)
	}
	return nil
}

func (a *app) cmdAdd(_ context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("add", `--name rq-workers --type redis --target redis:6379 --assert "SCARD rq:workers:default >= 1" --push-url https://.../api/v1/push/TOKEN`)
	var in probe.CheckInput
	var asserts stringList
	var codes statusList
	fs.StringVar(&in.Name, "name", "", "nome da verificação (letras, números, . - _)")
	fs.StringVar(&in.Type, "type", "", "tipo: "+strings.Join(probe.Types, ", "))
	fs.StringVar(&in.Target, "target", "", "alvo: URL para http, host:porta para tcp, redis, postgres, mysql e tls")
	fs.StringVar(&in.PushURL, "push-url", "", "endereço do monitor push no central, com o token")
	fs.DurationVar(&in.Interval, "interval", probe.DefaultInterval, "intervalo entre verificações")
	fs.DurationVar(&in.Timeout, "timeout", 0, "timeout de cada verificação (padrão 10s, ou metade do intervalo)")
	degraded := fs.Duration("degraded-latency", 0, "acima disto a verificação é degradada (0 desliga)")
	fs.Var(&asserts, "assert", `asserção de Redis, repetível: "SCARD rq:workers:default >= 1"`)
	fs.Var(&codes, "expect-status", "status HTTP aceitos, separados por vírgula (padrão 2xx)")
	fs.StringVar(&in.BodyContains, "body-contains", "", "texto que o corpo HTTP precisa conter")
	fs.StringVar(&in.Username, "username", "", "usuário do Redis ou do banco, ou do basic auth HTTP")
	passwordStdin := fs.Bool("password-stdin", false, "lê a senha da primeira linha da entrada padrão")
	fs.StringVar(&in.Database, "database", "", "banco (postgres, mysql)")
	fs.StringVar(&in.Query, "query", "", "consulta de leitura (postgres, mysql; padrão SELECT 1)")
	fs.StringVar(&in.ConfigJSON, "config-json", "", "configuração completa do checker em JSON, para o que não tem flag")
	replace := fs.Bool("replace", false, "substitui a verificação de mesmo nome")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	in.Asserts, in.ExpectStatus = asserts, codes

	if *passwordStdin {
		line, err := bufio.NewReader(a.stdin).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("--password-stdin: a entrada padrão terminou sem senha")
		}
		in.Password = strings.TrimRight(line, "\r\n")
	}

	ch, err := in.Check()
	if err != nil {
		return err
	}
	ch.DegradedLatency = probe.Duration(*degraded)
	if _, err := probe.Update(cfgPath, func(c *probe.Config) error { return c.Add(ch, *replace) }); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "verificação %q gravada: %s %s a cada %s, push para %s\n",
		ch.Name, ch.Type, ch.Target, ch.Interval, probe.MaskURL(ch.PushURL))
	fmt.Fprintf(a.stdout, "o daemon aplica sozinho; confira com: upwatch-probe test %s\n", ch.Name)
	return nil
}

func (a *app) cmdRemove(_ context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("remove", "<nome>")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("informe o nome: upwatch-probe remove <nome>")
	}
	if _, err := probe.Update(cfgPath, func(c *probe.Config) error { return c.Remove(pos[0]) }); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "verificação %q removida\n", pos[0])
	return nil
}

type listedCheck struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Target   string `json:"target"`
	Interval string `json:"interval"`
	Timeout  string `json:"timeout"`
	PushURL  string `json:"push_url"`
	Summary  string `json:"summary,omitempty"`
}

func (a *app) cmdList(_ context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("list", "[--json]")
	asJSON := fs.Bool("json", false, "saída em JSON")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := probe.Load(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		cfg, err = probe.Config{}, nil
	}
	if err != nil {
		return err
	}

	items := make([]listedCheck, 0, len(cfg.Checks))
	for _, c := range cfg.Checks {
		items = append(items, listedCheck{
			Name: c.Name, Type: c.Type, Target: c.Target, Interval: c.Interval.String(),
			Timeout: c.Timeout.String(), PushURL: probe.MaskURL(c.PushURL), Summary: probe.Describe(c),
		})
	}
	if *asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}

	if cfg.Name != "" || cfg.Heartbeat.PushURL != "" {
		fmt.Fprintf(a.stdout, "sonda %s", cfg.Name)
		if cfg.Heartbeat.PushURL != "" {
			fmt.Fprintf(a.stdout, " · sinal de vida a cada %s para %s", cfg.Heartbeat.Interval, probe.MaskURL(cfg.Heartbeat.PushURL))
		}
		fmt.Fprintln(a.stdout)
	}
	if len(items) == 0 {
		fmt.Fprintln(a.stdout, "nenhuma verificação; inclua com upwatch-probe add ou upwatch-probe setup")
		return nil
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NOME\tTIPO\tALVO\tINTERVALO\tPUSH")
	for _, c := range items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.Type, c.Target, c.Interval, c.PushURL)
		if c.Summary != "" {
			fmt.Fprintf(tw, "\t\t  %s\t\t\n", c.Summary)
		}
	}
	return tw.Flush()
}

// measureOnce roda a verificação uma vez, com os checkers reais ou com o
// substituto dos testes.
func (a *app) measureOnce(ctx context.Context, c probe.Check) probe.Signal {
	if a.measure != nil {
		return a.measure(ctx, c)
	}
	m, err := c.Monitor(1)
	if err != nil {
		return probe.Signal{Status: "down", Msg: "configuração inválida: " + err.Error()}
	}
	return probe.SignalFrom(probe.NewRegistry().Check(ctx, m))
}

func (a *app) cmdTest(ctx context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("test", "<nome> [--dry-run]")
	dryRun := fs.Bool("dry-run", false, "mostra o que seria enviado, sem enviar")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("informe o nome: upwatch-probe test <nome>")
	}
	cfg, err := probe.Load(cfgPath)
	if err != nil {
		return err
	}
	c, ok := cfg.Find(pos[0])
	if !ok {
		return fmt.Errorf("nenhuma verificação chamada %q; veja upwatch-probe list", pos[0])
	}

	sig := a.measureOnce(ctx, *c)
	w := a.stdout
	fmt.Fprintf(w, "verificação: %s (%s %s)\n", c.Name, c.Type, c.Target)
	fmt.Fprintf(w, "resultado:   %s, %dms\n", a.paint(sig.Status), sig.LatencyMS)
	if sig.Msg != "" {
		fmt.Fprintf(w, "causa:       %s\n", sig.Msg)
	}
	fmt.Fprintf(w, "envio:       POST %s\n", probe.MaskURL(c.PushURL))
	fmt.Fprintf(w, "corpo:       %s\n", sig.Body())

	if *dryRun {
		fmt.Fprintln(w, "dry-run:     nada foi enviado")
		return nil
	}
	if err := probe.NewSender(a.version).Send(ctx, c.PushURL, sig); err != nil {
		return fmt.Errorf("envio falhou: %w", err)
	}
	fmt.Fprintln(w, "entregue:    ok")
	return nil
}

func (a *app) cmdUIToken(_ context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("ui-token", "[--rotate]")
	rotate := fs.Bool("rotate", false, "gera um token novo e invalida o anterior")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := probe.Update(cfgPath, func(c *probe.Config) error {
		if *rotate || c.UIToken == "" {
			c.UIToken = newToken()
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, cfg.UIToken)
	return nil
}

// colorOn diz se a saída leva cor: só num terminal de verdade e só sem
// NO_COLOR. O terminal web do jump server mostra o código de escape como
// lixo, e quem redireciona para arquivo não quer escape no meio.
func (a *app) colorOn() bool { return a.tty && a.getenv("NO_COLOR") == "" }

var statusColors = map[string]string{"up": "32", "down": "31", "degraded": "33"}

func (a *app) paint(status string) string {
	code, ok := statusColors[status]
	if !ok || !a.colorOn() {
		return status
	}
	return "\x1b[" + code + "m" + status + "\x1b[0m"
}

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "nunca"
	}
	d := now.Sub(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("há %ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("há %dmin", int(d.Minutes()))
	default:
		return fmt.Sprintf("há %.1fh", d.Hours())
	}
}

func (a *app) cmdStatus(_ context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("status", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := probe.Load(cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	statePath := probe.StatePath(cfgPath)
	st, err := probe.ReadState(statePath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(a.stdout, "o daemon ainda não gravou estado em %s; ele está rodando? (docker ps, docker logs)\n", statePath)
		return nil
	}
	if err != nil {
		return err
	}

	now := time.Now()
	// Estado parado há muito mais que o maior intervalo é daemon parado:
	// o resultado mostrado é velho, e dizer isso evita confiar nele.
	longest := time.Duration(cfg.Heartbeat.Interval)
	for _, c := range cfg.Checks {
		longest = max(longest, time.Duration(c.Interval))
	}
	if longest > 0 && now.Sub(st.UpdatedAt) > 3*longest {
		fmt.Fprintf(a.stdout, "atenção: o estado não muda %s; o daemon está rodando?\n", ago(now, st.UpdatedAt))
	}

	names := make([]string, 0, len(cfg.Checks)+1)
	if cfg.Heartbeat.PushURL != "" {
		names = append(names, probe.HeartbeatName)
	}
	for _, c := range cfg.Checks {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		for n := range st.Checks {
			names = append(names, n)
		}
		sort.Strings(names)
	}

	for _, n := range names {
		label := n
		if n == probe.HeartbeatName {
			label = "sinal de vida"
		}
		s, ok := st.Checks[n]
		if !ok || s.Result == nil {
			fmt.Fprintf(a.stdout, "%s: aguardando a primeira medição\n", label)
			continue
		}
		r := s.Result
		fmt.Fprintf(a.stdout, "%s: %s, %dms, %s", label, a.paint(r.Status), r.LatencyMS, ago(now, r.At))
		if r.Msg != "" && n != probe.HeartbeatName {
			fmt.Fprintf(a.stdout, " · %s", r.Msg)
		}
		fmt.Fprintln(a.stdout)
		fmt.Fprintf(a.stdout, "  envio: %s\n", describeDelivery(now, s.Delivery))
	}
	return nil
}

func describeDelivery(now time.Time, d *probe.DeliveryStatus) string {
	if d == nil {
		return "nenhum ainda"
	}
	if d.OK {
		return "ok " + ago(now, d.At)
	}
	out := "falhou " + ago(now, d.At)
	if d.HTTPStatus != 0 {
		out += fmt.Sprintf(" (HTTP %d)", d.HTTPStatus)
	}
	out += fmt.Sprintf(", %d tentativa(s)", d.Attempts)
	if d.GaveUp {
		out += ", desistiu até a próxima medição"
	}
	if d.Error != "" {
		out += ": " + d.Error
	}
	return out
}
