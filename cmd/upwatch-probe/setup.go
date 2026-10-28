package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/probe"
)

// errInputClosed é a entrada que acabou no meio do assistente.
var errInputClosed = errors.New("a entrada terminou antes do fim do assistente; nada foi gravado")

// asker faz uma pergunta por linha.
//
// Sem tela cheia, sem setas e sem apagar a linha anterior: o terminal web
// do jump server não entende nada disso. O padrão fica entre colchetes e
// vale com Enter; a resposta recusada volta com o motivo, e a pergunta se
// repete sem perder as anteriores.
type asker struct {
	r *bufio.Reader
	w io.Writer
}

func (q *asker) readLine() (string, error) {
	s, err := q.r.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		return "", errInputClosed
	}
	return strings.TrimSpace(s), nil
}

func (q *asker) ask(prompt, def string, check func(string) error) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(q.w, "%s [%s]: ", prompt, def)
		} else {
			fmt.Fprintf(q.w, "%s: ", prompt)
		}
		s, err := q.readLine()
		if err != nil {
			fmt.Fprintln(q.w)
			return "", err
		}
		if s == "" {
			s = def
		}
		if err := check(s); err != nil {
			fmt.Fprintf(q.w, "  não aceito: %v\n", err)
			continue
		}
		return s, nil
	}
}

func (q *asker) yesNo(prompt string, def bool) (bool, error) {
	d := "n"
	if def {
		d = "s"
	}
	s, err := q.ask(prompt+" (s/n)", d, func(s string) error {
		switch strings.ToLower(s) {
		case "s", "sim", "y", "yes", "n", "nao", "não", "no":
			return nil
		}
		return errors.New("responda s ou n")
	})
	if err != nil {
		return false, err
	}
	return slices.Contains([]string{"s", "sim", "y", "yes"}, strings.ToLower(s)), nil
}

func validName(s string) error {
	if !probe.ValidName(s) {
		return errors.New("use até 64 letras, números, ponto, hífen ou sublinhado, sem espaço")
	}
	return nil
}

func validInterval(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return errors.New("use algo como 30s, 1m ou 5m")
	}
	if d < domain.MinInterval {
		return fmt.Errorf("o mínimo é %s", domain.MinInterval)
	}
	return nil
}

// placeholderPush é um endereço válido usado só para validar alvo e
// configuração antes de a pessoa informar o push de verdade.
const placeholderPush = "https://upwatch.invalid/api/v1/push/0000000000000000"

// validCheck confere uma verificação parcial, com o push provisório.
func validCheck(in probe.CheckInput) error {
	in.Name = "x"
	in.PushURL = placeholderPush
	c, err := in.Check()
	if err != nil {
		return err
	}
	cfg := probe.Config{Checks: []probe.Check{c}}
	if err := cfg.Validate(); err != nil {
		_, msg, _ := strings.Cut(err.Error(), ": ")
		return errors.New(msg)
	}
	return nil
}

// shortDuration escreve a duração como se digita: "60s", e não "1m0s".
func shortDuration(d time.Duration) string {
	if d%time.Second == 0 && d < time.Hour {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return d.String()
}

// maskIfSet esconde o token do endereço já gravado, que aparece como
// padrão da pergunta: a tela do jump server costuma ser gravada.
func maskIfSet(u string) string {
	if u == "" {
		return ""
	}
	return probe.MaskURL(u)
}

var targetExamples = map[string]string{
	"http":     "http://bff-api:3000/health",
	"tcp":      "host:porta",
	"redis":    "redis:6379",
	"postgres": "postgres:5432",
	"mysql":    "mysql:3306",
	"dns":      "exemplo.com",
	"tls":      "exemplo.com:443",
	"icmp":     "10.0.0.1",
}

// wizard guarda o que o assistente já decidiu entre as perguntas.
type wizard struct {
	app       *app
	q         *asker
	allowHTTP bool
}

// askPush pergunta um endereço de push. O já gravado aparece mascarado
// como padrão, e Enter o mantém.
func (z *wizard) askPush(prompt, current string, optional bool) (string, error) {
	shown := maskIfSet(current)
	for {
		s, err := z.q.ask(prompt, shown, func(s string) error {
			if (optional && s == "") || (s != "" && s == shown) {
				return nil
			}
			return probe.ValidatePushURL(s, true)
		})
		switch {
		case err != nil:
			return "", err
		case s == "":
			return "", nil
		case s == shown:
			return current, nil
		}
		if strings.HasPrefix(s, "http://") && !z.allowHTTP {
			ok, err := z.q.yesNo("  O endereço usa http sem TLS e o token viaja em claro. Aceitar mesmo assim?", false)
			if err != nil {
				return "", err
			}
			if !ok {
				continue
			}
			z.allowHTTP = true
		}
		return s, nil
	}
}

func (a *app) cmdSetup(ctx context.Context, cfgPath string, args []string) error {
	fs := a.newFlags("setup", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	current, err := probe.Load(cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	z := &wizard{app: a, q: &asker{r: bufio.NewReader(a.stdin), w: a.stdout}, allowHTTP: current.AllowHTTPPush}
	fmt.Fprintln(a.stdout, "upwatch-probe setup: uma pergunta por linha. Enter aceita o valor entre colchetes.")
	fmt.Fprintln(a.stdout, "Cada verificação precisa de um monitor push no UpWatch central; tenha os endereços de push à mão.")
	fmt.Fprintln(a.stdout)

	defName := current.Name
	if defName == "" {
		if h, err := os.Hostname(); err == nil && probe.ValidName(h) {
			defName = h
		}
	}
	name, err := z.q.ask("Nome desta sonda", defName, validName)
	if err != nil {
		return err
	}

	hbURL, err := z.askPush(
		"Endereço de push do sinal de vida (monitor push \"sonda "+name+"\" no central; vazio para nenhum)",
		current.Heartbeat.PushURL, true)
	if err != nil {
		return err
	}
	var hbEvery time.Duration
	if hbURL != "" {
		def := shortDuration(probe.DefaultInterval)
		if current.Heartbeat.Interval != 0 {
			def = shortDuration(time.Duration(current.Heartbeat.Interval))
		}
		s, err := z.q.ask("Intervalo do sinal de vida", def, validInterval)
		if err != nil {
			return err
		}
		hbEvery, _ = time.ParseDuration(s)
	}

	var checks []probe.Check
	more, err := z.q.yesNo("Adicionar uma verificação?", true)
	for err == nil && more {
		var c *probe.Check
		if c, err = z.askCheck(ctx); err != nil {
			break
		}
		if c != nil {
			checks = append(checks, *c)
		}
		more, err = z.q.yesNo("Adicionar outra verificação?", false)
	}
	if err != nil {
		return err
	}

	cfg, err := probe.Update(cfgPath, func(c *probe.Config) error {
		c.Name, c.AllowHTTPPush = name, z.allowHTTP
		c.Heartbeat = probe.Heartbeat{PushURL: hbURL, Interval: probe.Duration(hbEvery)}
		for _, ch := range checks {
			if err := c.Add(ch, true); err != nil {
				return err
			}
		}
		if c.UIToken == "" {
			c.UIToken = newToken()
		}
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(a.stdout)
	fmt.Fprintf(a.stdout, "Configuração gravada em %s, com %d verificação(ões). O daemon aplica sozinho.\n", cfgPath, len(cfg.Checks))
	fmt.Fprintf(a.stdout, "Token da interface web (só se usar --ui): %s\n", cfg.UIToken)
	fmt.Fprintln(a.stdout, "Confira com: upwatch-probe status")
	return nil
}

// askCheck pergunta uma verificação, testa e confirma. Devolve nil quando
// a pessoa desiste de gravá-la.
func (z *wizard) askCheck(ctx context.Context) (*probe.Check, error) {
	q, out := z.q, z.app.stdout
	var in probe.CheckInput
	var err error

	if in.Name, err = q.ask("  Nome da verificação", "", validName); err != nil {
		return nil, err
	}
	if in.Type, err = q.ask("  Tipo ("+strings.Join(probe.Types, ", ")+")", "http", func(s string) error {
		if !slices.Contains(probe.Types, s) {
			return fmt.Errorf("tipo desconhecido; use um de %s", strings.Join(probe.Types, ", "))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if in.Target, err = q.ask("  Alvo (ex.: "+targetExamples[in.Type]+")", "", func(s string) error {
		if s == "" {
			return errors.New("o alvo é obrigatório")
		}
		return validCheck(probe.CheckInput{Type: in.Type, Target: s})
	}); err != nil {
		return nil, err
	}

	switch in.Type {
	case "redis":
		fmt.Fprintln(out, "  Asserções de leitura, uma por linha (ex.: SCARD rq:workers:default >= 1); vazio termina.")
		for {
			s, err := q.ask("  Asserção", "", func(s string) error {
				if s == "" {
					return nil
				}
				_, err := probe.ParseRedisAssertion(s)
				return err
			})
			if err != nil {
				return nil, err
			}
			if s == "" {
				break
			}
			in.Asserts = append(in.Asserts, s)
		}
	case "http":
		const anyOK = "qualquer 2xx"
		var codes statusList
		if _, err := q.ask("  Status HTTP esperados, separados por vírgula", anyOK, func(s string) error {
			codes = nil
			if s == anyOK {
				return nil
			}
			if err := codes.Set(s); err != nil {
				return err
			}
			return validCheck(probe.CheckInput{Type: "http", Target: in.Target, ExpectStatus: codes})
		}); err != nil {
			return nil, err
		}
		in.ExpectStatus = codes
	}

	s, err := q.ask("  Intervalo", shortDuration(probe.DefaultInterval), validInterval)
	if err != nil {
		return nil, err
	}
	in.Interval, _ = time.ParseDuration(s)

	if in.PushURL, err = z.askPush("  Endereço de push desta verificação", "", false); err != nil {
		return nil, err
	}

	c, err := in.Check()
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(out, "  Testando %s...\n", c.Name)
	sig := z.app.measureOnce(ctx, c)
	fmt.Fprintf(out, "  Resultado: %s, %dms", z.app.paint(sig.Status), sig.LatencyMS)
	if sig.Msg != "" {
		fmt.Fprintf(out, " · %s", sig.Msg)
	}
	fmt.Fprintln(out)

	prompt, def := "  Salvar esta verificação?", true
	if sig.Status != "up" {
		prompt, def = "  A verificação não passou. Salvar mesmo assim?", false
	}
	ok, err := q.yesNo(prompt, def)
	if err != nil || !ok {
		return nil, err
	}
	return &c, nil
}
