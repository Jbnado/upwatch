package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

// Do sinal ao aviso: o push com estado atravessa a cadeia inteira.
//
// Os testes de unidade provam que o endpoint grava a causa, que o checker
// a devolve e que o motor confirma quedas. Este prova que as peças se
// encaixam: um script que diz "fila sem worker" faz chegar exatamente essa
// causa no aviso — e só depois da confirmação, como qualquer outra falha.

// avisos captura o que o motor mandaria aos canais.
type avisos struct {
	mu   sync.Mutex
	sent []notifier.Notification
}

func (a *avisos) Enqueue(n notifier.Notification, _ []notifier.Notifier) {
	a.mu.Lock()
	a.sent = append(a.sent, n)
	a.mu.Unlock()
}

func (a *avisos) all() []notifier.Notification {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]notifier.Notification(nil), a.sent...)
}

// descarte aceita a batida sem guardá-la; o que importa aqui é o motor.
type descarte struct{}

func (descarte) Submit(context.Context, domain.Heartbeat) error { return nil }

func TestPushReportedDownOpensIncidentWithTheCauseAfterConfirmation(t *testing.T) {
	ctx := context.Background()
	epoch := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	fake := clock.NewFake(epoch)

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	const token = "um-token-suficientemente-longo"
	m := domain.Monitor{
		Name: "fila de pedidos", Type: domain.MonitorPush,
		Interval: time.Minute, Timeout: 10 * time.Second,
		ConfirmationThreshold: 2, Enabled: true,
		Config: json.RawMessage(`{"token":"` + token + `"}`),
	}
	if err := st.Monitors().Create(ctx, &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	canal := domain.Channel{
		Name: "plantão", Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
	}
	if err := st.Channels().Create(ctx, &canal); err != nil {
		t.Fatalf("Create channel returned unexpected error: %v", err)
	}
	if err := st.Channels().Link(ctx, m.ID, canal.ID); err != nil {
		t.Fatalf("Link returned unexpected error: %v", err)
	}

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
	motor := incident.NewEngine(descarte{}, st, enviados, nil)
	motor.Upsert(m)

	// O script descobre a fila parada e diz isso, pelo caminho da issue.
	resp, err := http.Post(srv.URL+"/api/v1/push/"+token+"?status=down&msg=fila+sem+worker", "", nil)
	if err != nil {
		t.Fatalf("push returned unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("push status = %d, want 200", resp.StatusCode)
	}

	// verificar faz o que o agendador faz a cada intervalo.
	verificar := func() {
		t.Helper()
		res := push.Check(ctx, m)
		if err := motor.Submit(ctx, domain.Heartbeat{
			MonitorID: m.ID, Timestamp: fake.Now(), Status: res.Status,
			LatencyMS: res.LatencyMS, Message: res.Message,
		}); err != nil {
			t.Fatalf("Submit returned unexpected error: %v", err)
		}
	}

	// Primeira verificação: uma falha só. Um soluço do script não pode
	// abrir incidente sozinho, por isso o "down" explícito passa pela
	// mesma confirmação que qualquer outra falha.
	verificar()
	if _, err := st.Incidents().Current(ctx, m.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an incident opened before confirmation: %v", err)
	}
	if len(enviados.all()) != 0 {
		t.Fatal("a notification went out before confirmation")
	}

	// Segunda verificação, ainda dentro da janela do sinal: confirmada.
	fake.Advance(30 * time.Second)
	verificar()

	aberto, err := st.Incidents().Current(ctx, m.ID)
	if err != nil {
		t.Fatalf("no incident after confirmation: %v", err)
	}
	if aberto.Cause != "fila sem worker" {
		t.Errorf("incident cause = %q, want the cause reported by the script", aberto.Cause)
	}

	got := enviados.all()
	if len(got) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(got))
	}
	if !strings.Contains(got[0].Message, "fila sem worker") {
		t.Errorf("notification message = %q, want the reported cause", got[0].Message)
	}
}
