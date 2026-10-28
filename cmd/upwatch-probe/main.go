// Comando upwatch-probe é a sonda para redes privadas.
//
// Roda dentro da rede que o UpWatch central não alcança, executa as
// verificações com os mesmos checkers dele e reporta cada resultado por
// push. Só abre conexão de saída e nunca recebe comando nem configuração
// do central.
//
// A CLI foi pensada para o terminal web de um jump server: uma linha por
// comando, uma pergunta por linha no assistente, sem tela cheia e sem cor
// obrigatória.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/Jbnado/upwatch/internal/probe"
)

// version é preenchido no build pelo ldflags.
var version = "dev"

// defaultConfigPath é o arquivo no volume do contêiner.
const defaultConfigPath = "/config/probe.yaml"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := &app{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		getenv: os.Getenv, tty: isTerminal(os.Stdout), version: version,
	}
	os.Exit(a.main(ctx, os.Args[1:]))
}

// isTerminal informa se a saída é um terminal. Sem dependência nova: um
// dispositivo de caractere é terminal, e arquivo ou pipe não é.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// app é a CLI com as dependências de fora injetadas, para os testes
// rodarem sem terminal, sem rede e sem processo novo.
type app struct {
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	getenv  func(string) string
	tty     bool
	version string

	// measure roda uma verificação uma vez. Nil usa os checkers reais.
	measure func(context.Context, probe.Check) probe.Signal

	// outErr é o primeiro erro de escrita na saída padrão.
	outErr error
}

// printf escreve na saída padrão e guarda o primeiro erro. Uma saída que
// falha (pipe fechado, disco cheio no redirecionamento) vira erro do
// comando uma vez, em vez de cada linha conferir o próprio retorno.
func (a *app) printf(format string, args ...any) {
	if a.outErr == nil {
		_, a.outErr = fmt.Fprintf(a.stdout, format, args...)
	}
}

func (a *app) println(args ...any) {
	if a.outErr == nil {
		_, a.outErr = fmt.Fprintln(a.stdout, args...)
	}
}

// eprintf escreve na saída de erro. A falha dela é ignorada: não sobra
// lugar onde dizer que não deu para dizer o erro.
func (a *app) eprintf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.stderr, format, args...)
}

type command struct {
	summary string
	run     func(a *app, ctx context.Context, cfgPath string, args []string) error
}

var commands = map[string]command{
	"run":      {"roda o daemon (padrão do contêiner)", (*app).cmdRun},
	"setup":    {"assistente: uma pergunta por linha", (*app).cmdSetup},
	"init":     {"define o nome da sonda e o sinal de vida", (*app).cmdInit},
	"add":      {"inclui uma verificação, tudo por flags", (*app).cmdAdd},
	"list":     {"lista as verificações", (*app).cmdList},
	"test":     {"roda uma verificação uma vez e envia (ou só mostra, com --dry-run)", (*app).cmdTest},
	"remove":   {"remove uma verificação", (*app).cmdRemove},
	"status":   {"último resultado e último envio de cada verificação", (*app).cmdStatus},
	"ui-token": {"mostra (ou troca, com --rotate) o token da interface web", (*app).cmdUIToken},
}

func (a *app) main(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.usage()
		return 2
	}
	name, rest := args[0], args[1:]
	switch name {
	case "version", "--version", "-version":
		a.println("upwatch-probe", a.version)
		return 0
	case "help", "--help", "-h", "-help":
		a.usage()
		return 0
	}
	cmd, ok := commands[name]
	if !ok {
		a.eprintf("comando desconhecido: %s\n\n", name)
		a.usage()
		return 2
	}

	cfgPath, rest := a.configFlag(rest)
	err := cmd.run(a, ctx, cfgPath, rest)
	if err == nil {
		err = a.outErr
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		a.eprintf("erro: %v\n", err)
		return 1
	}
	return 0
}

// configFlag tira --config dos argumentos, onde quer que esteja. Ele vale
// para todo comando, e repeti-lo em cada FlagSet seria uma decisão em
// nove lugares.
func (a *app) configFlag(args []string) (string, []string) {
	path := a.getenv("UPWATCH_PROBE_CONFIG")
	if path == "" {
		path = defaultConfigPath
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case (arg == "--config" || arg == "-config") && i+1 < len(args):
			path = args[i+1]
			i++
		case strings.HasPrefix(arg, "--config="), strings.HasPrefix(arg, "-config="):
			path = arg[strings.Index(arg, "=")+1:]
		default:
			out = append(out, arg)
		}
	}
	return path, out
}

func (a *app) usage() {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)

	a.eprintf("uso: upwatch-probe <comando> [opções] [--config /config/probe.yaml]\n\n")
	for _, n := range names {
		a.eprintf("  %-9s %s\n", n, commands[n].summary)
	}
	a.eprintf("\nCada comando aceita --help. O arquivo também vem de UPWATCH_PROBE_CONFIG.\n")
}

// newFlags cria o FlagSet do comando, escrevendo a ajuda na saída de erro.
func (a *app) newFlags(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		a.eprintf("uso: upwatch-probe %s %s\n", name, usage)
		fs.PrintDefaults()
	}
	return fs
}

// parse aceita flags antes e depois dos argumentos posicionais: "test
// fila --dry-run" e "test --dry-run fila" são o mesmo comando, e quem cola
// no terminal não deveria precisar saber a ordem que o pacote flag exige.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// stringList é uma flag que pode se repetir (--assert).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }
