package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
	"github.com/Jbnado/upwatch/internal/testdir"
)

const (
	tokenSonda = "token-do-sinal-de-vida-da-sonda"
	tokenRedis = "token-da-verificacao-do-redis"
)

// sonda é o caso real que motivou os monitores dependentes: uma sonda
// numa VM de rede privada, com o sinal de vida dela (o pai) e uma
// verificação que ela faz e reporta por push (o filho).
type sonda struct {
	t        *testing.T
	st       *sqlstore.Store
	fake     *clock.Fake
	srv      *httptest.Server
	push     *checker.Push
	motor    *incident.Engine
	enviados *avisos
	inicio   time.Time
	vida     domain.Monitor
	redis    domain.Monitor
}

func novaSonda(t *testing.T) *sonda {
	t.Helper()
	ctx := context.Background()

	st, err := sqlstore.OpenSQLite(filepath.Join(testdir.New(t), "upwatch.db"), secretbox.Ephemeral())
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	criar := func(nome, token string, pai *int64) domain.Monitor {
		m := domain.Monitor{
			Name: nome, Type: domain.MonitorPush,
			Interval: time.Minute, Timeout: 10 * time.Second,
			ConfirmationThreshold: 2, Enabled: true, ParentID: pai,
			Config:    json.RawMessage(`{"token":"` + token + `"}`),
			PushToken: token,
		}
		if err := st.Monitors().Create(ctx, &m); err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		return m
	}
	vida := criar("sonda vm-01", tokenSonda, nil)
	redis := criar("redis da vm-01", tokenRedis, &vida.ID)

	canal := domain.Channel{
		Name: "plantão", Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
	}
	if err := st.Channels().Create(ctx, &canal); err != nil {
		t.Fatalf("Create channel returned unexpected error: %v", err)
	}
	for _, m := range []domain.Monitor{vida, redis} {
		if err := st.Channels().Link(ctx, m.ID, canal.ID); err != nil {
			t.Fatalf("Link returned unexpected error: %v", err)
		}
	}

	fake := clock.NewFake(redis.CreatedAt)
	push := checker.NewPush(st, fake)
	reg, err := checker.NewRegistry(push)
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}
	srv := httptest.NewServer(api.New(api.Options{
		Store: st, Auth: auth.New(st, auth.Options{Clock: fake}), Checkers: reg, Clock: fake,
	}))
	t.Cleanup(srv.Close)

	enviados := &avisos{}
	motor := incident.NewEngine(gravacao{st}, st, enviados, nil)
	motor.Upsert(vida)
	motor.Upsert(redis)

	return &sonda{t: t, st: st, fake: fake, srv: srv, push: push, motor: motor,
		enviados: enviados, inicio: fake.Now(), vida: vida, redis: redis}
}

// em leva o relógio ao instante, em segundos desde o início.
func (s *sonda) em(segundos int) {
	s.t.Helper()
	alvo := s.inicio.Add(time.Duration(segundos) * time.Second)
	if d := alvo.Sub(s.fake.Now()); d > 0 {
		s.fake.Advance(d)
	}
}

// sinal é a sonda reportando pelo endpoint de push.
func (s *sonda) sinal(token, query string) {
	s.t.Helper()
	resp, err := http.Post(s.srv.URL+"/api/v1/push/"+token+query, "", nil)
	if err != nil {
		s.t.Fatalf("push returned unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("push status = %d, want 200", resp.StatusCode)
	}
}

// verificar faz o que o agendador faz para o monitor.
func (s *sonda) verificar(m domain.Monitor) {
	s.t.Helper()
	res := s.push.Check(context.Background(), m)
	if err := s.motor.Submit(context.Background(), domain.Heartbeat{
		MonitorID: m.ID, Timestamp: s.fake.Now(), Status: res.Status,
		LatencyMS: res.LatencyMS, Message: res.Message, Repeated: res.Repeated,
	}); err != nil {
		s.t.Fatalf("Submit returned unexpected error: %v", err)
	}
}

func (s *sonda) quedasAvisadas(m domain.Monitor) []string {
	var causas []string
	for _, n := range s.enviados.all() {
		if n.Monitor.ID == m.ID && n.Event.Kind == incident.KindDown {
			causas = append(causas, n.Message)
		}
	}
	return causas
}

// Pai cai → filho calado → pai volta → filho ainda fora avisa.
//
// A VM da sonda perde a saída para a internet: o sinal de vida e a
// verificação param juntos. O sinal da verificação chegou antes do de
// vida, então é ela quem percebe o silêncio primeiro — a corrida em que
// o filho avisaria antes de o pai ter como cair. Quando a sonda volta, o
// Redis dela continua fora, e aí a queda é dele: avisa.
func TestProbeOutageSilencesItsChecksUntilItReturns(t *testing.T) {
	s := novaSonda(t)

	// Os dois confirmados no ar: o limiar é 2, e cada sinal conta uma vez.
	s.em(0)
	s.sinal(tokenRedis, "")
	s.verificar(s.redis)
	s.sinal(tokenSonda, "")
	s.verificar(s.vida)
	s.em(60)
	s.sinal(tokenRedis, "")
	s.verificar(s.redis)
	s.em(90)
	s.sinal(tokenSonda, "")
	s.verificar(s.vida)
	if s.motor.StateOf(s.vida.ID).Status != domain.StatusUp || s.motor.StateOf(s.redis.ID).Status != domain.StatusUp {
		t.Fatal("setup: the probe and its check are not confirmed up")
	}

	// A VM perde a saída. O agendador continua verificando os dois a cada
	// 30 s; o Redis, com o último sinal mais antigo, cai primeiro.
	for seg := 120; seg <= 570; seg += 30 {
		s.em(seg)
		s.verificar(s.redis)
		s.verificar(s.vida)
	}
	if s.motor.StateOf(s.vida.ID).Status != domain.StatusDown {
		t.Fatal("the probe's own silence did not bring it down")
	}
	if got := s.quedasAvisadas(s.vida); len(got) != 1 {
		t.Errorf("probe sent %d down notifications, want 1", len(got))
	}
	if got := s.quedasAvisadas(s.redis); len(got) != 0 {
		t.Errorf("the check notified %v while its probe was down, want nothing", got)
	}

	// A sonda volta, e manda o resultado do Redis: fora.
	s.em(600)
	s.sinal(tokenSonda, "")
	s.verificar(s.vida)
	s.sinal(tokenRedis, "?status=down&msg=redis+recusou+a+conexao")
	s.verificar(s.redis)
	s.em(630)
	s.sinal(tokenSonda, "")
	s.verificar(s.vida)
	if s.motor.StateOf(s.vida.ID).Status != domain.StatusUp {
		t.Fatal("the probe did not come back")
	}
	for _, seg := range []int{630, 660} {
		s.em(seg)
		s.sinal(tokenRedis, "?status=down&msg=redis+recusou+a+conexao")
		s.verificar(s.redis)
	}

	causas := s.quedasAvisadas(s.redis)
	if len(causas) != 1 || !strings.Contains(causas[0], "redis recusou") {
		t.Errorf("check notifications after the probe returned = %v, want one with its own cause", causas)
	}

	// O intervalo sem a sonda é lacuna no histórico do Redis, não queda:
	// a disponibilidade dele não pode pagar pela rede da VM.
	batidas, err := s.st.QueryHeartbeats(context.Background(), store.HeartbeatQuery{
		MonitorID: s.redis.ID,
		Range:     store.TimeRange{From: s.inicio.Add(120 * time.Second), To: s.inicio.Add(630 * time.Second)},
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	for _, hb := range batidas {
		if hb.Status == domain.StatusDown {
			t.Errorf("heartbeat at %v stored as down while the probe was out; want a gap",
				hb.Timestamp.Sub(s.inicio))
		}
	}
}
