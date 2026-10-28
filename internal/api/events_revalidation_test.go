package api_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// O fluxo de eventos fica aberto por dias numa aba esquecida.
//
// A credencial só era conferida ao abrir. Sessão encerrada, token
// revogado ou senha trocada não fechavam o fluxo já aberto: quem perdeu o
// acesso continuava recebendo os eventos até fechar a aba. Agora o fluxo
// reconfere a credencial periodicamente e se encerra quando ela não vale
// mais.

// abrirFluxo abre /events com a credencial dada e devolve um canal que
// fecha quando o servidor encerra o fluxo.
func (s *server) abrirFluxo(t *testing.T, autenticar func(*http.Request)) (<-chan string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	autenticar(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, resp, http.StatusOK)

	linhas := make(chan string, 100)
	go func() {
		defer close(linhas)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			linhas <- sc.Text()
		}
	}()
	// Dá tempo de o handler assinar e armar a reconferência.
	time.Sleep(150 * time.Millisecond)
	return linhas, cancel
}

func esperarFim(t *testing.T, linhas <-chan string) {
	t.Helper()
	limite := time.After(5 * time.Second)
	for {
		select {
		case _, aberto := <-linhas:
			if !aberto {
				return
			}
		case <-limite:
			t.Fatal("o fluxo continuou aberto depois de a credencial deixar de valer")
		}
	}
}

func TestEventStreamEndsWhenTheSessionEnds(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	linhas, cancel := s.abrirFluxo(t, func(r *http.Request) { r.AddCookie(s.cookie) })
	defer cancel()

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/logout", nil), http.StatusNoContent)
	s.clock.Advance(2 * time.Minute)

	esperarFim(t, linhas)
}

func TestEventStreamEndsWhenTheTokenIsRevoked(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "painel"})
	assertStatus(t, resp, http.StatusCreated)
	tok := decode[struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}](t, resp)

	linhas, cancel := s.abrirFluxo(t, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok.Token) })
	defer cancel()

	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+itoa(tok.ID), nil), http.StatusNoContent)
	s.clock.Advance(2 * time.Minute)

	esperarFim(t, linhas)
}

// contaLeituras conta as leituras de uma conta, para medir quanto o fluxo
// consulta o banco por evento.
type contaLeituras struct {
	store.Store
	alvo     atomic.Int64
	leituras atomic.Int64
}

func (c *contaLeituras) Users() store.UserRepo { return &usuariosContados{c.Store.Users(), c} }

type usuariosContados struct {
	store.UserRepo
	c *contaLeituras
}

func (u *usuariosContados) Get(ctx context.Context, id int64) (domain.User, error) {
	if id == u.c.alvo.Load() {
		u.c.leituras.Add(1)
	}
	return u.UserRepo.Get(ctx, id)
}

// O escopo é resolvido uma vez e serve aos eventos seguintes por alguns
// segundos. Antes, cada evento relia a conta e a árvore de grupos — duas
// consultas por evento por aba aberta, justamente quando muita coisa
// muda ao mesmo tempo.
func TestEventStreamCachesTheScope(t *testing.T) {
	var contador *contaLeituras
	s := newServerWith(t, func(o *api.Options) {
		contador = &contaLeituras{Store: o.Store}
		o.Store = contador
	})
	s.setup(t)

	g := s.criarGrupo(t, "Retaguarda", nil)
	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "ret", "password": "senha-de-teste-12345", "role": "viewer",
		"scope": map[string]any{"all_groups": false, "group_ids": []int64{g}},
	})
	assertStatus(t, resp, http.StatusCreated)
	conta := decode[idResp](t, resp).ID
	cookie := s.entrar(t, "ret", "senha-de-teste-12345")

	linhas, cancel := s.abrirFluxo(t, func(r *http.Request) { r.AddCookie(cookie) })
	defer cancel()
	contador.alvo.Store(conta)

	const n = 10
	for i := 0; i < n; i++ {
		s.createMonitor(t, monitorNoGrupo("m"+itoa(int64(i)), &g))
	}
	recebidos := 0
	limite := time.After(5 * time.Second)
	for recebidos < n {
		select {
		case l := <-linhas:
			if strings.HasPrefix(l, "event: monitor.created") {
				recebidos++
			}
		case <-limite:
			t.Fatalf("recebeu %d de %d eventos", recebidos, n)
		}
	}

	if got := contador.leituras.Load(); got > 1 {
		t.Errorf("o fluxo leu a conta %d vezes para %d eventos; want no máximo 1", got, n)
	}
}
