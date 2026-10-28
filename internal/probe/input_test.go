package probe_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/probe"
)

// O add da CLI e o formulário da interface montam a verificação pelo
// mesmo caminho: duas leituras das mesmas opções divergiriam.
func TestCheckInputBuildsARedisCheck(t *testing.T) {
	c, err := probe.CheckInput{
		Name: "rq-workers", Type: "redis", Target: "redis:6379", PushURL: checkURL,
		Interval: time.Minute,
		Asserts:  []string{"SCARD rq:workers:default >= 1", "LLEN rq:queue:default < 100"},
	}.Check()
	if err != nil {
		t.Fatalf("Check returned unexpected error: %v", err)
	}
	cfg := probe.Config{Checks: []probe.Check{c}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("built check does not validate: %v", err)
	}
	got := probe.Describe(c)
	if !strings.Contains(got, "SCARD rq:workers:default >= 1") || !strings.Contains(got, "LLEN rq:queue:default < 100") {
		t.Errorf("Describe = %q, want both assertions in one-line form", got)
	}
}

func TestCheckInputBuildsAnHTTPCheck(t *testing.T) {
	c, err := probe.CheckInput{
		Name: "bff", Type: "http", Target: "http://bff-api:3000/health", PushURL: checkURL,
		ExpectStatus: []int{200, 204}, BodyContains: "ok",
	}.Check()
	if err != nil {
		t.Fatalf("Check returned unexpected error: %v", err)
	}
	cfg := probe.Config{Checks: []probe.Check{c}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("built check does not validate: %v", err)
	}
	if time.Duration(c.Interval) != probe.DefaultInterval {
		t.Errorf("interval = %s, want the default", c.Interval)
	}
}

// Opção que não pertence ao tipo é erro, não silêncio: --assert num
// monitor HTTP ignorado faria a pessoa achar que a fila está vigiada.
func TestCheckInputRejectsOptionsOfAnotherType(t *testing.T) {
	cases := map[string]probe.CheckInput{
		"assert em http":         {Name: "a", Type: "http", Target: "http://x/", PushURL: checkURL, Asserts: []string{"DBSIZE > 0"}},
		"expect-status em tcp":   {Name: "a", Type: "tcp", Target: "x:1", PushURL: checkURL, ExpectStatus: []int{200}},
		"senha em tcp":           {Name: "a", Type: "tcp", Target: "x:1", PushURL: checkURL, Password: "x"},
		"consulta em redis":      {Name: "a", Type: "redis", Target: "x:1", PushURL: checkURL, Query: "SELECT 1"},
		"asserção inválida":      {Name: "a", Type: "redis", Target: "x:1", PushURL: checkURL, Asserts: []string{"FLUSHALL > 0"}},
		"config-json quebrado":   {Name: "a", Type: "redis", Target: "x:1", PushURL: checkURL, ConfigJSON: "{"},
		"config-json não-objeto": {Name: "a", Type: "redis", Target: "x:1", PushURL: checkURL, ConfigJSON: "[1]"},
	}
	for name, in := range cases {
		if _, err := in.Check(); err == nil {
			t.Errorf("%s: Check accepted %+v", name, in)
		}
	}
}

// A senha vai para o arquivo (600), mas nunca volta numa listagem.
func TestDescribeNeverShowsSecrets(t *testing.T) {
	c, err := probe.CheckInput{
		Name: "fila", Type: "redis", Target: "redis:6379", PushURL: checkURL,
		Username: "upwatch", Password: "s3nha-que-nao-pode-vazar",
	}.Check()
	if err != nil {
		t.Fatal(err)
	}
	if c.Config["password"] != "s3nha-que-nao-pode-vazar" {
		t.Fatalf("password not stored in the check config: %+v", c.Config)
	}
	if got := probe.Describe(c); strings.Contains(got, "s3nha") || strings.Contains(got, "token-da-verificacao") {
		t.Errorf("Describe shows a secret: %q", got)
	}
}
