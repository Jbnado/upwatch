package tokenexpiry_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
	"github.com/Jbnado/upwatch/internal/testdir"
	"github.com/Jbnado/upwatch/internal/tokenexpiry"
)

// recordingDispatcher captura os avisos enfileirados.
type recordingDispatcher struct {
	mu    sync.Mutex
	sent  []notifier.Notification
	chans [][]notifier.Notifier
}

func (d *recordingDispatcher) Enqueue(n notifier.Notification, c []notifier.Notifier) {
	d.mu.Lock()
	d.sent = append(d.sent, n)
	d.chans = append(d.chans, c)
	d.mu.Unlock()
}

func (d *recordingDispatcher) all() []notifier.Notification {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]notifier.Notification(nil), d.sent...)
}

func (d *recordingDispatcher) channels(i int) []notifier.Notifier {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.chans[i]
}

func newStore(t *testing.T) *sqlstore.Store {
	t.Helper()
	st, err := sqlstore.OpenSQLite(filepath.Join(testdir.New(t), "upwatch.db"), secretbox.Ephemeral())
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func mustUser(t *testing.T, st store.Store, name string) domain.User {
	t.Helper()
	u := domain.User{Username: name, Role: domain.RoleAdmin, PasswordHash: "x"}
	if err := st.Users().Create(context.Background(), &u); err != nil {
		t.Fatalf("criando usuário: %v", err)
	}
	return u
}

// mustToken cria um token do dono e grava a expiração dada.
func mustToken(t *testing.T, st store.Store, userID int64, name string, expires time.Time) domain.APIToken {
	t.Helper()
	secret, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	tok := domain.APIToken{UserID: userID, Name: name, Hash: secret.Hash, Prefix: secret.Prefix}
	if err := st.Tokens().Create(context.Background(), &tok); err != nil {
		t.Fatalf("criando token: %v", err)
	}
	if err := st.Tokens().SetExpiry(context.Background(), tok.ID, &expires); err != nil {
		t.Fatalf("gravando expiração: %v", err)
	}
	return tok
}

// mustChannel cria um canal habilitado com o filtro de eventos dado (nil =
// sem filtro, recebe tudo, inclusive os avisos de conta).
func mustChannel(t *testing.T, st store.Store, events []domain.EventType) {
	t.Helper()
	c := domain.Channel{
		Name: "plantão", Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
		Events: events,
	}
	if err := st.Channels().Create(context.Background(), &c); err != nil {
		t.Fatalf("criando canal: %v", err)
	}
}

// O marco de 30 dias avisa uma vez e identifica o token e o dono; repetir o
// tick no mesmo instante não avisa de novo.
func TestWarnsAtThirtyDayMilestoneOnce(t *testing.T) {
	st := newStore(t)
	u := mustUser(t, st, "joao")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mustToken(t, st, u.ID, "deploy", now.Add(20*24*time.Hour))
	mustChannel(t, st, nil)

	d := &recordingDispatcher{}
	w := tokenexpiry.NewWarner(st, d, tokenexpiry.Options{Clock: clock.NewFake(now)})

	w.Tick(context.Background())
	if n := len(d.all()); n != 1 {
		t.Fatalf("avisos no primeiro tick = %d, want 1", n)
	}
	note := d.all()[0]
	if !note.IsAccount() || note.Account.Event != domain.EventAccountTokenExpiring {
		t.Fatalf("o aviso não é de expiração de conta: %+v", note.Account)
	}
	if note.Account.TokenName != "deploy" || note.Account.Username != "joao" {
		t.Errorf("o aviso identifica errado: %+v", note.Account)
	}
	if len(d.channels(0)) != 1 {
		t.Errorf("o aviso foi para %d canais, want 1", len(d.channels(0)))
	}

	// Mesmo instante, de novo: o marco de 30 já foi avisado.
	w.Tick(context.Background())
	if n := len(d.all()); n != 1 {
		t.Fatalf("avisos após repetir o tick = %d, want 1 (uma vez por marco)", n)
	}
}

// Dois marcos, dois avisos: um aos 30 dias, outro aos 7, e o segundo conta
// os dias reais que faltam, não o número do marco.
func TestWarnsAgainAtSevenDayMilestone(t *testing.T) {
	st := newStore(t)
	u := mustUser(t, st, "joao")
	venc := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	mustToken(t, st, u.ID, "deploy", venc)
	mustChannel(t, st, nil)
	d := &recordingDispatcher{}

	tokenexpiry.NewWarner(st, d, tokenexpiry.Options{Clock: clock.NewFake(venc.Add(-20 * 24 * time.Hour))}).
		Tick(context.Background())
	tokenexpiry.NewWarner(st, d, tokenexpiry.Options{Clock: clock.NewFake(venc.Add(-5 * 24 * time.Hour))}).
		Tick(context.Background())

	avisos := d.all()
	if len(avisos) != 2 {
		t.Fatalf("avisos = %d, want 2 (um por marco: 30 e 7)", len(avisos))
	}
	if dl := avisos[1].Account.DaysLeft; dl != 5 {
		t.Errorf("DaysLeft do aviso de 7 = %d, want 5 (dias reais que faltam)", dl)
	}
}

// Sem ninguém assinando, nada a avisar — e nada a marcar: quando um canal
// for criado depois, o aviso ainda precisa sair.
func TestNoChannelNoWarningAndNoMark(t *testing.T) {
	st := newStore(t)
	u := mustUser(t, st, "joao")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tok := mustToken(t, st, u.ID, "deploy", now.Add(20*24*time.Hour))

	d := &recordingDispatcher{}
	tokenexpiry.NewWarner(st, d, tokenexpiry.Options{Clock: clock.NewFake(now)}).Tick(context.Background())
	if n := len(d.all()); n != 0 {
		t.Fatalf("avisos sem canal = %d, want 0", n)
	}

	got, err := st.Tokens().ListExpiring(context.Background(), now.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, et := range got {
		if et.TokenID == tok.ID && et.WarnedDays != nil {
			t.Errorf("marcou o aviso sem ninguém para avisar: %v", et.WarnedDays)
		}
	}
}

// Token que vence muito além do marco mais distante não gera aviso.
func TestFarTokenNotWarned(t *testing.T) {
	st := newStore(t)
	u := mustUser(t, st, "joao")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mustToken(t, st, u.ID, "longe", now.Add(60*24*time.Hour))
	mustChannel(t, st, nil)

	d := &recordingDispatcher{}
	tokenexpiry.NewWarner(st, d, tokenexpiry.Options{Clock: clock.NewFake(now)}).Tick(context.Background())
	if n := len(d.all()); n != 0 {
		t.Errorf("avisou um token que vence em 60 dias = %d, want 0", n)
	}
}

// Canal que filtra e só assina a queda não recebe os avisos de conta.
func TestFilteredChannelDoesNotReceiveAccountEvents(t *testing.T) {
	st := newStore(t)
	u := mustUser(t, st, "joao")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mustToken(t, st, u.ID, "deploy", now.Add(20*24*time.Hour))
	mustChannel(t, st, []domain.EventType{domain.EventMonitorDown})

	d := &recordingDispatcher{}
	tokenexpiry.NewWarner(st, d, tokenexpiry.Options{Clock: clock.NewFake(now)}).Tick(context.Background())
	if n := len(d.all()); n != 0 {
		t.Errorf("canal que só assina a queda recebeu %d avisos de conta, want 0", n)
	}
}
