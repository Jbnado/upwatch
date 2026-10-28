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
//
// O administrador também não a recebe: ao contrário do token do push, a
// senha não é algo que ele precise copiar de volta, e quem a cadastrou a
// tem na origem.
func TestRedactConfigRemovesThePassword(t *testing.T) {
	for _, typ := range []domain.MonitorType{domain.MonitorRedis, domain.MonitorPostgres, domain.MonitorMySQL} {
		for _, to := range audiences {
			raw := json.RawMessage(`{"username":"monitor","password":"segredo-de-producao","db":1}`)

			got := checker.RedactConfig(typ, raw, to)

			if strings.Contains(string(got), "segredo-de-producao") {
				t.Errorf("%s para %v: redacted config still carries the password: %s", typ, to, got)
			}
			m := configMap(t, got)
			// A interface precisa saber que existe senha para dizer "deixe em
			// branco para manter", sem saber qual é.
			if m["password_set"] != true {
				t.Errorf("%s para %v: password_set = %v, want true", typ, to, m["password_set"])
			}
			if m["username"] != "monitor" {
				t.Errorf("%s para %v: username = %v, want the non-secret fields kept", typ, to, m["username"])
			}
		}
	}
}

func TestRedactConfigWithoutPassword(t *testing.T) {
	got := checker.RedactConfig(domain.MonitorRedis, json.RawMessage(`{"db":1}`), checker.Readers)

	if m := configMap(t, got); m["password_set"] != nil {
		t.Errorf("password_set = %v, want absent when there is no password", m["password_set"])
	}
}

// audiences são todos os públicos da API, para os testes de "ninguém
// recebe" não passarem só porque olharam um deles.
var audiences = []checker.Audience{checker.Readers, checker.Admins}

// Tipos sem credencial passam intactos, para qualquer público.
func TestRedactConfigLeavesOtherTypesAlone(t *testing.T) {
	raw := json.RawMessage(`{"host":"exemplo.com","expect":"1.2.3.4"}`)

	for _, to := range audiences {
		if got := checker.RedactConfig(domain.MonitorDNS, raw, to); string(got) != string(raw) {
			t.Errorf("RedactConfig(dns) para %v = %s, want %s unchanged", to, got, raw)
		}
	}
}

// O token do push precisa voltar ao administrador, porque a interface
// monta com ele o endereço que a rotina chama, e ele o copia para lá.
func TestRedactConfigRevealsThePushTokenToAdmins(t *testing.T) {
	raw := json.RawMessage(`{"token":"abcdefghijklmnopqrstuvwx"}`)

	got := checker.RedactConfig(domain.MonitorPush, raw, checker.Admins)

	if string(got) != string(raw) {
		t.Errorf("RedactConfig(push) para o administrador = %s, want %s unchanged", got, raw)
	}
}

// Para quem só lê, o token é a credencial do /push: com ele se mantém o
// monitor verde com o serviço parado, ou se derruba o monitor com um
// "down" falso. A máscara diz que há token sem dizer qual, e o resto da
// configuração continua legível. O encoding/json casa "Token", "TOKEN" e
// "toKen" (com o K do kelvin) com o campo token, então toda grafia que o
// checker lê como token sai mascarada.
func TestRedactConfigMasksThePushTokenForReaders(t *testing.T) {
	for _, key := range []string{"token", "Token", "TOKEN", "toKen"} {
		raw := json.RawMessage(`{"` + key + `":"abcdefghijklmnopqrstuvwx","schedule":"2 9 * * *","timezone":"America/Sao_Paulo"}`)

		got := checker.RedactConfig(domain.MonitorPush, raw, checker.Readers)

		if strings.Contains(string(got), "abcdefghijklmnopqrstuvwx") {
			t.Errorf("%q: redacted config carries the push token: %s", key, got)
		}
		m := configMap(t, got)
		if m["timezone"] != "America/Sao_Paulo" || m["schedule"] != "2 9 * * *" {
			t.Errorf("%q: redacted config = %s, want the rest of the config kept", key, got)
		}
	}

	got := configMap(t, checker.RedactConfig(domain.MonitorPush, json.RawMessage(`{"token":"abcdefghijklmnopqrstuvwx"}`), checker.Readers))
	if got["token"] != "***" {
		t.Errorf("token = %v, want the mask, so the reader knows there is one", got["token"])
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
		// Sem senha, a forma canônica omite o campo.
		if pw, _ := configMap(t, got)["password"].(string); pw != tt.want {
			t.Errorf("%s: password = %q, want %q", tt.cfg, pw, tt.want)
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

// O encoding/json casa o nome do campo sem diferenciar caixa, e até por
// equivalência Unicode: "Password", "PASSWORD" e "paſſword" (com o s longo)
// preenchem a mesma senha que o checker usa. A ocultação olhava só a
// grafia canônica, então qualquer outra passava inteira pelo GET.
func TestRedactConfigHidesThePasswordInAnySpelling(t *testing.T) {
	for _, typ := range []domain.MonitorType{domain.MonitorRedis, domain.MonitorPostgres, domain.MonitorMySQL} {
		for _, key := range []string{"Password", "PASSWORD", "paſſword"} {
			raw := json.RawMessage(`{"username":"monitor","` + key + `":"segredo-de-producao"}`)

			for _, to := range audiences {
				got := checker.RedactConfig(typ, raw, to)

				if strings.Contains(string(got), "segredo-de-producao") {
					t.Errorf("%s %q para %v: redacted config carries the password: %s", typ, key, to, got)
				}
				if m := configMap(t, got); m["password_set"] != true {
					t.Errorf("%s %q para %v: password_set = %v, want true", typ, key, to, m["password_set"])
				}
			}
		}
	}
}

// Normalizar na entrada faz só a grafia canônica chegar ao banco: o que o
// checker lê é exatamente o que a ocultação e a preservação olham, e uma
// grafia alternativa conta como senha enviada.
func TestCanonicalConfigStoresOnlyTheCanonicalSpelling(t *testing.T) {
	m := redisWith("cache:6379", `{"PASSWORD":"segredo","Db":2,"password_set":true,"campo_inventado":1}`)

	got, err := checker.CanonicalConfig(m)
	if err != nil {
		t.Fatalf("CanonicalConfig returned unexpected error: %v", err)
	}

	cfg := configMap(t, got)
	if cfg["password"] != "segredo" || cfg["db"] != float64(2) {
		t.Errorf("canonical config = %s, want password and db in canonical form", got)
	}
	for _, k := range []string{"PASSWORD", "Db", "password_set", "campo_inventado"} {
		if _, ok := cfg[k]; ok {
			t.Errorf("canonical config kept %q: %s", k, got)
		}
	}

	// Na edição, a grafia alternativa é a senha nova, não ausência.
	prev := redisWith("cache:6379", `{"password":"antiga"}`)
	got, err = checker.CarrySecrets(redisWith("cache:6379", `{"Password":"nova"}`), prev)
	if err != nil {
		t.Fatalf("CarrySecrets returned unexpected error: %v", err)
	}
	if cfg := configMap(t, got); cfg["password"] != "nova" {
		t.Errorf("password = %v, want the new one sent as Password", cfg["password"])
	}
}

// Trocar o TLS por um modo mais fraco é outro jeito de levar a senha
// guardada a quem não devia vê-la: sem verificação de certificado, ou sem
// TLS nenhum, quem estiver no caminho recebe a credencial na verificação
// seguinte. Como na troca de endereço, a senha precisa ser informada de
// novo. Fortalecer, ou não mexer, mantém a guardada.
func TestCarrySecretsRefusesToCarryToWeakerTLS(t *testing.T) {
	for _, tt := range []struct {
		name       string
		typ        domain.MonitorType
		prev, next string
		refuse     bool
	}{
		{"redis tls desligado", domain.MonitorRedis, `{"password":"p","tls":true}`, `{}`, true},
		{"redis sem verificar", domain.MonitorRedis, `{"password":"p","tls":true}`, `{"tls":true,"skip_tls_verify":true}`, true},
		{"redis igual", domain.MonitorRedis, `{"password":"p","tls":true}`, `{"tls":true}`, false},
		{"redis ligando tls", domain.MonitorRedis, `{"password":"p"}`, `{"tls":true}`, false},
		{"postgres verify-full para prefer", domain.MonitorPostgres, `{"username":"u","password":"p","sslmode":"verify-full"}`, `{"username":"u"}`, true},
		{"postgres require para disable", domain.MonitorPostgres, `{"username":"u","password":"p","sslmode":"require"}`, `{"username":"u","sslmode":"disable"}`, true},
		{"postgres require para verify-full", domain.MonitorPostgres, `{"username":"u","password":"p","sslmode":"require"}`, `{"username":"u","sslmode":"verify-full"}`, false},
		{"mysql true para preferred", domain.MonitorMySQL, `{"username":"u","password":"p","tls":"true"}`, `{"username":"u","tls":"preferred"}`, true},
		{"mysql true para skip-verify", domain.MonitorMySQL, `{"username":"u","password":"p","tls":"true"}`, `{"username":"u","tls":"skip-verify"}`, true},
		{"mysql false para true", domain.MonitorMySQL, `{"username":"u","password":"p"}`, `{"username":"u","tls":"true"}`, false},
		{"http sem verificar", domain.MonitorHTTP, `{"basic_auth_user":"u","basic_auth_pass":"p"}`, `{"basic_auth_user":"u","skip_tls_verify":true}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prev := domain.Monitor{Type: tt.typ, Target: "db:5432", Config: json.RawMessage(tt.prev)}
			next := domain.Monitor{Type: tt.typ, Target: "db:5432", Config: json.RawMessage(tt.next)}

			_, err := checker.CarrySecrets(next, prev)

			if tt.refuse && err == nil {
				t.Error("CarrySecrets carried the password to a weaker TLS")
			}
			if !tt.refuse && err != nil {
				t.Errorf("CarrySecrets returned unexpected error: %v", err)
			}
		})
	}
}

const httpWithSecrets = `{
	"basic_auth_user": "monitor",
	"basic_auth_pass": "senha-do-basic",
	"headers": {
		"Authorization": "Bearer tok-do-bearer",
		"X-Api-Key": "chave-da-api",
		"x-auth-token": "tok-do-header",
		"X-Client-Secret": "segredo-do-cliente",
		"Accept": "application/json"
	}
}`

// A senha do basic auth e os cabeçalhos de credencial do monitor HTTP são
// tão segredo quanto a senha de um banco, e voltavam inteiros no GET e no
// fluxo de eventos (issue #54). Os cabeçalhos ficam na lista, com o valor
// mascarado: a interface precisa mostrar que existem, e o que não tem
// segredo nenhum (Accept) continua legível.
func TestRedactConfigHidesHTTPCredentials(t *testing.T) {
	for _, to := range audiences {
		got := checker.RedactConfig(domain.MonitorHTTP, json.RawMessage(httpWithSecrets), to)
		for _, secret := range []string{"senha-do-basic", "tok-do-bearer", "chave-da-api", "tok-do-header", "segredo-do-cliente"} {
			if strings.Contains(string(got), secret) {
				t.Errorf("redacted config para %v carries %q: %s", to, secret, got)
			}
		}
	}

	got := checker.RedactConfig(domain.MonitorHTTP, json.RawMessage(httpWithSecrets), checker.Readers)
	m := configMap(t, got)
	if m["basic_auth_pass_set"] != true || m["basic_auth_user"] != "monitor" {
		t.Errorf("redacted config = %s, want basic_auth_pass_set and the user kept", got)
	}
	headers, _ := m["headers"].(map[string]any)
	if headers["Accept"] != "application/json" {
		t.Errorf("Accept = %v, want non-secret headers kept", headers["Accept"])
	}
	if headers["Authorization"] != "***" {
		t.Errorf("Authorization = %v, want it listed with the value masked", headers["Authorization"])
	}
}

// O formulário reenvia o que recebeu: a senha do basic auth ausente e os
// cabeçalhos mascarados. Salvar não pode apagar nem gravar a máscara no
// lugar da credencial; e, como a senha, os valores guardados não
// acompanham o monitor para outro endereço.
func TestCarrySecretsKeepsHTTPCredentials(t *testing.T) {
	prev := domain.Monitor{Type: domain.MonitorHTTP, Target: "https://api.exemplo.com/saude", Config: json.RawMessage(httpWithSecrets)}
	redacted := checker.RedactConfig(domain.MonitorHTTP, prev.Config, checker.Admins)
	next := domain.Monitor{Type: domain.MonitorHTTP, Target: prev.Target, Config: redacted}

	got, err := checker.CarrySecrets(next, prev)
	if err != nil {
		t.Fatalf("CarrySecrets returned unexpected error: %v", err)
	}
	m := configMap(t, got)
	if m["basic_auth_pass"] != "senha-do-basic" {
		t.Errorf("basic_auth_pass = %v, want the stored one kept", m["basic_auth_pass"])
	}
	headers, _ := m["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer tok-do-bearer" || headers["X-Api-Key"] != "chave-da-api" {
		t.Errorf("headers = %v, want the stored values back in place of the mask", headers)
	}
	if _, ok := m["basic_auth_pass_set"]; ok {
		t.Errorf("basic_auth_pass_set was stored: %s", got)
	}

	next.Target = "https://servidor-de-quem-quer-a-senha/"
	if _, err := checker.CarrySecrets(next, prev); err == nil {
		t.Error("CarrySecrets carried the HTTP credentials to a new target")
	}

	// Máscara sem valor guardado não pode virar o valor literal "***".
	fresh := domain.Monitor{Type: domain.MonitorHTTP, Target: prev.Target, Config: json.RawMessage(`{"headers":{"Authorization":"***"}}`)}
	if _, err := checker.CanonicalConfig(fresh); err == nil {
		t.Error("CanonicalConfig stored a masked header with nothing to carry")
	}
}
