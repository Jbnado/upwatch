package storetest

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// secretCases cobre a cifragem em repouso das credenciais.
func secretCases() []conformanceCase {
	return []conformanceCase{
		{"SecretsAreCipheredAtRest", testSecretsAreCipheredAtRest},
		{"SecretsStayCipheredAfterUpdate", testSecretsStayCipheredAfterUpdate},
		{"PushTokenColumnHoldsNoToken", testPushTokenColumnHoldsNoToken},
		{"MonitorRearmIgnoresTheCipher", testMonitorRearmIgnoresTheCipher},
	}
}

// rawDB é o acesso direto ao banco, por baixo do store.
//
// A garantia da cifragem é sobre o que fica em repouso — o backup, a
// cópia do volume, o pg_dump —, e só um SELECT que não passa pelo store
// enxerga isso. Um backend que não exponha o banco não tem como provar
// que cumpre a garantia, e não entra.
type rawDB interface{ DB() *sql.DB }

// restingConfigs devolve, como estão gravados, a configuração de todos os
// monitores e canais e as colunas de token do push.
func restingConfigs(t *testing.T, s store.Store) string {
	t.Helper()
	raw, ok := s.(rawDB)
	if !ok {
		t.Fatalf("o store %T não expõe DB(); a suíte precisa ler o que fica em repouso", s)
	}

	var all strings.Builder
	for _, q := range []string{
		`SELECT config FROM monitor`,
		`SELECT config FROM notification_channel`,
		`SELECT push_token FROM monitor WHERE push_token IS NOT NULL`,
	} {
		rows, err := raw.DB().QueryContext(context.Background(), q)
		if err != nil {
			t.Fatalf("%s returned unexpected error: %v", q, err)
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatalf("scanning %s returned unexpected error: %v", q, err)
			}
			all.WriteString(v)
			all.WriteString("\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("reading %s returned unexpected error: %v", q, err)
		}
		_ = rows.Close()
	}
	return all.String()
}

// secretMonitors são monitores com cada forma de credencial, e o valor
// dela.
func secretMonitors() ([]domain.Monitor, []string) {
	mk := func(name string, typ domain.MonitorType, cfg string) domain.Monitor {
		m := newMonitor(name)
		m.Type, m.Config = typ, json.RawMessage(cfg)
		return m
	}
	push := mk("cron", domain.MonitorPush, `{"token":"token-do-push-0123456789"}`)
	push.Target, push.PushToken = "", "token-do-push-0123456789"

	monitores := []domain.Monitor{
		mk("redis", domain.MonitorRedis, `{"password":"senha-do-redis"}`),
		mk("postgres", domain.MonitorPostgres, `{"username":"mon","password":"senha-do-pg"}`),
		mk("mysql", domain.MonitorMySQL, `{"username":"mon","password":"senha-do-mysql"}`),
		mk("http", domain.MonitorHTTP, `{"basic_auth_user":"ana","basic_auth_pass":"senha-do-basic",`+
			`"headers":{"Authorization":"Bearer token-do-cabecalho","Accept":"text/plain"}}`),
		push,
	}
	segredos := []string{
		"senha-do-redis", "senha-do-pg", "senha-do-mysql", "senha-do-basic",
		"token-do-cabecalho", "token-do-push-0123456789",
	}
	return monitores, segredos
}

// secretChannels são canais com cada forma de credencial, e o valor dela.
func secretChannels() ([]domain.Channel, []string) {
	mk := func(name, typ, cfg string) domain.Channel {
		return domain.Channel{Name: name, Type: typ, Enabled: true, Config: json.RawMessage(cfg)}
	}
	canais := []domain.Channel{
		mk("webhook", "webhook", `{"url":"https://hooks.exemplo.com/token-do-webhook","headers":{"X-Token":"token-do-header"}}`),
		mk("discord", "discord", `{"url":"https://discord.com/api/webhooks/1/token-do-discord"}`),
		mk("slack", "slack", `{"url":"https://hooks.slack.com/services/T/B/token-do-slack"}`),
		mk("email", "email", `{"host":"smtp.exemplo.com","password":"senha-do-smtp","from":"a@exemplo.com","to":["b@exemplo.com"]}`),
		mk("ntfy", "ntfy", `{"topic":"plantao","token":"token-do-ntfy"}`),
	}
	segredos := []string{
		"token-do-webhook", "token-do-header", "token-do-discord", "token-do-slack",
		"senha-do-smtp", "token-do-ntfy",
	}
	return canais, segredos
}

func createSecrets(t *testing.T, s store.Store) ([]domain.Monitor, []domain.Channel, []string) {
	t.Helper()
	ctx := context.Background()

	monitors, monitorSecrets := secretMonitors()
	for i := range monitors {
		if err := s.Monitors().Create(ctx, &monitors[i]); err != nil {
			t.Fatalf("Create(%s) returned unexpected error: %v", monitors[i].Name, err)
		}
	}
	channels, channelSecrets := secretChannels()
	for i := range channels {
		if err := s.Channels().Create(ctx, &channels[i]); err != nil {
			t.Fatalf("Create(%s) returned unexpected error: %v", channels[i].Name, err)
		}
	}
	return monitors, channels, append(monitorSecrets, channelSecrets...)
}

func assertNothingInPlaintext(t *testing.T, s store.Store, secrets []string) {
	t.Helper()
	resting := restingConfigs(t, s)
	for _, secret := range secrets {
		if strings.Contains(resting, secret) {
			t.Errorf("%q is stored in plaintext:\n%s", secret, resting)
		}
	}
}

// Quem obtém o banco sem passar pelo UpWatch — um backup, uma cópia do
// volume, um pg_dump — não pode sair com as credenciais dos serviços
// monitorados. A API já as esconde; o banco precisa escondê-las também.
// E quem lê pelo store continua vendo o texto puro: é com ele que o
// checker se autentica e o canal publica.
func testSecretsAreCipheredAtRest(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	monitors, channels, secrets := createSecrets(t, s)

	assertNothingInPlaintext(t, s, secrets)

	for _, want := range monitors {
		got, err := s.Monitors().Get(ctx, want.ID)
		if err != nil {
			t.Fatalf("Get(%s) returned unexpected error: %v", want.Name, err)
		}
		assertSameJSON(t, want.Name, got.Config, want.Config)
		if got.PushToken != want.PushToken {
			t.Errorf("%s: PushToken = %q, want %q", want.Name, got.PushToken, want.PushToken)
		}
	}
	for _, want := range channels {
		got, err := s.Channels().Get(ctx, want.ID)
		if err != nil {
			t.Fatalf("Get(%s) returned unexpected error: %v", want.Name, err)
		}
		assertSameJSON(t, want.Name, got.Config, want.Config)
	}

	// As listagens leem pelo mesmo caminho que o Get.
	listed, err := s.Channels().List(ctx)
	if err != nil || len(listed) != len(channels) {
		t.Fatalf("List = (%d, %v), want %d channels", len(listed), err, len(channels))
	}
	for i := range listed {
		assertSameJSON(t, listed[i].Name, listed[i].Config, channels[i].Config)
	}
	page, err := s.Monitors().List(ctx, store.MonitorFilter{})
	if err != nil || len(page.Items) != len(monitors) {
		t.Fatalf("List = (%d, %v), want %d monitors", len(page.Items), err, len(monitors))
	}
	for i := range page.Items {
		assertSameJSON(t, page.Items[i].Name, page.Items[i].Config, monitors[i].Config)
	}
}

// Editar regrava a configuração; a credencial nova não pode escapar em
// texto puro só porque chegou por outro caminho que o da criação.
func testSecretsStayCipheredAfterUpdate(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	monitors, channels, secrets := createSecrets(t, s)

	redis := monitors[0]
	redis.Config = json.RawMessage(`{"password":"senha-nova-do-redis"}`)
	if err := s.Monitors().Update(ctx, redis); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}
	ntfy := channels[len(channels)-1]
	ntfy.Config = json.RawMessage(`{"topic":"plantao","token":"token-novo-do-ntfy"}`)
	if err := s.Channels().Update(ctx, ntfy); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}

	assertNothingInPlaintext(t, s, append(secrets, "senha-nova-do-redis", "token-novo-do-ntfy"))

	got, err := s.Monitors().Get(ctx, redis.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	assertSameJSON(t, "redis", got.Config, redis.Config)
	gotChannel, err := s.Channels().Get(ctx, ntfy.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	assertSameJSON(t, "ntfy", gotChannel.Config, ntfy.Config)
}

// A coluna do token do push serve à busca do /push, e uma busca por
// igualdade não precisa do valor: guarda o hash. O token inteiro, que o
// administrador copia para a rotina, fica cifrado na configuração. A
// busca pelo token apresentado continua achando o dono.
func testPushTokenColumnHoldsNoToken(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	m := pushMonitor("cron", "token-do-cron-de-backup")
	if err := s.Monitors().Create(ctx, &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	assertNothingInPlaintext(t, s, []string{"token-do-cron-de-backup"})
	got, err := s.Monitors().FindByPushToken(ctx, "token-do-cron-de-backup")
	if err != nil || got.ID != m.ID || got.PushToken != "token-do-cron-de-backup" {
		t.Errorf("FindByPushToken = (%d, %q, %v), want monitor %d with its token", got.ID, got.PushToken, err, m.ID)
	}
}

// Cada gravação sorteia um nonce, então a mesma senha cifrada duas vezes
// dá dois textos diferentes. A espera do push recomeça quando a
// configuração muda, e essa comparação não pode confundir cifra nova com
// configuração nova: renomear um monitor com senha não adia um alarme.
func testMonitorRearmIgnoresTheCipher(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	m := pushMonitor("cron", "token-do-cron-noturno")
	if err := s.Monitors().Create(ctx, &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	created, err := s.Monitors().Get(ctx, m.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	renamed := created
	renamed.Name = "cron renomeado"
	if err := s.Monitors().Update(ctx, renamed); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}
	got, _ := s.Monitors().Get(ctx, m.ID)
	if !got.RearmedAt.Equal(created.RearmedAt) {
		t.Errorf("rename of a monitor with a secret moved RearmedAt to %v, want %v", got.RearmedAt, created.RearmedAt)
	}

	time.Sleep(5 * time.Millisecond)
	retokened := got
	retokened.Config = json.RawMessage(`{"token":"token-novo-do-cron"}`)
	retokened.PushToken = "token-novo-do-cron"
	if err := s.Monitors().Update(ctx, retokened); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}
	got, _ = s.Monitors().Get(ctx, m.ID)
	if !got.RearmedAt.Equal(got.UpdatedAt) {
		t.Errorf("a new token: RearmedAt = %v, want the update time %v", got.RearmedAt, got.UpdatedAt)
	}
}

// assertSameJSON compara configurações pelo conteúdo: a ordem das chaves
// não é parte do contrato.
func assertSameJSON(t *testing.T, what string, got, want json.RawMessage) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Errorf("%s: config %q is not JSON: %v", what, got, err)
		return
	}
	_ = json.Unmarshal(want, &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("%s: config = %s, want %s", what, gb, wb)
	}
}
