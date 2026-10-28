package checker_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

func configMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("config %s is not a JSON object: %v", raw, err)
	}
	return out
}

// A senha do banco ou do Redis monitorado é credencial de produção. Quem
// tem acesso de leitura ao painel não pode obtê-la pela API, do mesmo jeito
// que a URL de um canal de aviso não volta.
func TestRedactConfigRemovesThePassword(t *testing.T) {
	for _, typ := range []domain.MonitorType{domain.MonitorRedis, domain.MonitorPostgres, domain.MonitorMySQL} {
		raw := json.RawMessage(`{"username":"monitor","password":"segredo-de-producao","db":1}`)

		got := checker.RedactConfig(typ, raw)

		if strings.Contains(string(got), "segredo-de-producao") {
			t.Errorf("%s: redacted config still carries the password: %s", typ, got)
		}
		m := configMap(t, got)
		// A interface precisa saber que existe senha para dizer "deixe em
		// branco para manter", sem saber qual é.
		if m["password_set"] != true {
			t.Errorf("%s: password_set = %v, want true", typ, m["password_set"])
		}
		if m["username"] != "monitor" {
			t.Errorf("%s: username = %v, want the non-secret fields kept", typ, m["username"])
		}
	}
}

func TestRedactConfigWithoutPassword(t *testing.T) {
	got := checker.RedactConfig(domain.MonitorRedis, json.RawMessage(`{"db":1}`))

	if m := configMap(t, got); m["password_set"] != nil {
		t.Errorf("password_set = %v, want absent when there is no password", m["password_set"])
	}
}

// Tipos sem credencial passam intactos: o token do push, por exemplo,
// precisa voltar, porque a interface monta com ele o endereço a chamar.
func TestRedactConfigLeavesOtherTypesAlone(t *testing.T) {
	raw := json.RawMessage(`{"token":"abcdefghijklmnopqrstuvwx"}`)

	got := checker.RedactConfig(domain.MonitorPush, raw)

	if string(got) != string(raw) {
		t.Errorf("RedactConfig(push) = %s, want %s unchanged", got, raw)
	}
}

func redisWith(target, cfg string) domain.Monitor {
	return domain.Monitor{Type: domain.MonitorRedis, Target: target, Config: json.RawMessage(cfg)}
}

// Como a senha não volta no GET, a edição de qualquer outro campo chega
// sem ela. Tratar ausência como "sem senha" apagaria a credencial a cada
// salvamento do formulário.
func TestCarrySecretsKeepsThePasswordWhenOmitted(t *testing.T) {
	prev := redisWith("cache:6379", `{"password":"antiga","db":0}`)
	next := redisWith("cache:6379", `{"db":2,"password_set":true}`)

	got, err := checker.CarrySecrets(next, prev)
	if err != nil {
		t.Fatalf("CarrySecrets returned unexpected error: %v", err)
	}

	m := configMap(t, got)
	if m["password"] != "antiga" {
		t.Errorf("password = %v, want the stored one kept", m["password"])
	}
	if m["db"] != float64(2) {
		t.Errorf("db = %v, want the new value", m["db"])
	}
	// password_set é só de saída; gravá-lo faria o campo mentir quando a
	// senha for removida depois.
	if _, ok := m["password_set"]; ok {
		t.Errorf("password_set was stored: %s", got)
	}
}

func TestCarrySecretsUsesTheNewPasswordWhenSent(t *testing.T) {
	prev := redisWith("cache:6379", `{"password":"antiga"}`)

	for _, tt := range []struct{ cfg, want string }{
		{`{"password":"nova"}`, "nova"},
		// Senha vazia enviada de propósito remove a credencial.
		{`{"password":""}`, ""},
	} {
		got, err := checker.CarrySecrets(redisWith("cache:6379", tt.cfg), prev)
		if err != nil {
			t.Fatalf("CarrySecrets returned unexpected error: %v", err)
		}
		if m := configMap(t, got); m["password"] != tt.want {
			t.Errorf("%s: password = %v, want %q", tt.cfg, m["password"], tt.want)
		}
	}
}

// Sem esta regra, quem pode editar monitores mas não ler a senha a
// obteria trocando o endereço para um servidor seu: o UpWatch levaria a
// credencial guardada até lá na verificação seguinte.
func TestCarrySecretsRefusesToCarryToAnotherTarget(t *testing.T) {
	prev := redisWith("cache:6379", `{"password":"antiga"}`)
	next := redisWith("servidor-de-quem-quer-a-senha:6379", `{}`)

	if _, err := checker.CarrySecrets(next, prev); err == nil {
		t.Fatal("CarrySecrets carried the password to a new target")
	}

	// Informando a senha de novo, a troca de endereço é legítima.
	next.Config = json.RawMessage(`{"password":"informada"}`)
	if _, err := checker.CarrySecrets(next, prev); err != nil {
		t.Errorf("CarrySecrets with the password re-entered returned unexpected error: %v", err)
	}
}

func TestCarrySecretsDoesNotCrossTypes(t *testing.T) {
	prev := domain.Monitor{Type: domain.MonitorPostgres, Target: "db:5432", Config: json.RawMessage(`{"username":"u","password":"pg"}`)}
	next := domain.Monitor{Type: domain.MonitorMySQL, Target: "db:5432", Config: json.RawMessage(`{"username":"u"}`)}

	got, err := checker.CarrySecrets(next, prev)
	if err != nil {
		t.Fatalf("CarrySecrets returned unexpected error: %v", err)
	}
	if strings.Contains(string(got), "pg") {
		t.Errorf("password crossed from postgres to mysql: %s", got)
	}
}
