package secretbox_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/secretbox"
)

// httpLike imita o monitor HTTP: senha em campo próprio e cabeçalhos em
// que só os de nome sensível são credencial.
var httpLike = secretbox.Fields{
	Names: []string{"basic_auth_pass"},
	Map:   "headers",
	MapSecret: func(name string) bool {
		return strings.Contains(strings.ToLower(name), "auth")
	},
}

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("config %q is not a JSON object: %v", raw, err)
	}
	return out
}

func TestSealConfigHidesOnlyTheSecretFields(t *testing.T) {
	box := mustBox(t, mustKey(t))
	raw := json.RawMessage(`{"method":"GET","basic_auth_user":"ana","basic_auth_pass":"senha-http",` +
		`"headers":{"Authorization":"Bearer tok-123","Accept":"application/json"}}`)

	sealed, err := box.SealConfig(raw, httpLike, "monitor", 3)
	if err != nil {
		t.Fatalf("SealConfig returned unexpected error: %v", err)
	}
	for _, secret := range []string{"senha-http", "tok-123"} {
		if strings.Contains(string(sealed), secret) {
			t.Errorf("sealed config %s still carries %q", sealed, secret)
		}
	}
	got := decode(t, sealed)
	if got["method"] != "GET" || got["basic_auth_user"] != "ana" {
		t.Errorf("sealing touched fields that are not secret: %s", sealed)
	}
	if h := got["headers"].(map[string]any); h["Accept"] != "application/json" {
		t.Errorf("sealing touched a header that is not a credential: %s", sealed)
	}

	opened, err := box.OpenConfig(sealed, httpLike, "monitor", 3)
	if err != nil {
		t.Fatalf("OpenConfig returned unexpected error: %v", err)
	}
	back := decode(t, opened)
	if back["basic_auth_pass"] != "senha-http" ||
		back["headers"].(map[string]any)["Authorization"] != "Bearer tok-123" {
		t.Errorf("OpenConfig = %s, want the secrets back", opened)
	}
}

// O encoding/json casa "Password" e "PASSWORD" com o campo password, e o
// checker usaria a senha escrita em qualquer dessas grafias. Cifrar só a
// grafia canônica deixaria as outras em texto puro no banco.
func TestSealConfigMatchesFieldsLikeEncodingJSON(t *testing.T) {
	box := mustBox(t, mustKey(t))
	fields := secretbox.Fields{Names: []string{"password"}}
	raw := json.RawMessage(`{"PASSWORD":"um","Password":"dois","paſſword":"tres"}`)

	sealed, err := box.SealConfig(raw, fields, "monitor", 1)
	if err != nil {
		t.Fatalf("SealConfig returned unexpected error: %v", err)
	}
	for _, secret := range []string{`"um"`, `"dois"`, `"tres"`} {
		if strings.Contains(string(sealed), secret) {
			t.Errorf("sealed config %s still carries %s", sealed, secret)
		}
	}
}

// Configuração sem segredo sai byte a byte como entrou. O store compara a
// configuração gravada com a nova para decidir se a espera do push
// recomeça, e reescrever o JSON à toa mudaria a resposta.
func TestSealConfigLeavesConfigsWithoutSecretsAlone(t *testing.T) {
	box := mustBox(t, mustKey(t))
	for _, raw := range []string{
		`{"method":"GET",  "headers":{"Accept":"*/*"}}`,
		`{"basic_auth_pass":""}`,
		`{}`,
		``,
	} {
		got, err := box.SealConfig(json.RawMessage(raw), httpLike, "monitor", 1)
		if err != nil || string(got) != raw {
			t.Errorf("SealConfig(%q) = (%q, %v), want it untouched", raw, got, err)
		}
	}
}

// Quem não tem campo secreto não é interpretado: um tipo sem segredo pode
// guardar o que quiser.
func TestSealConfigWithoutFieldsIsANoop(t *testing.T) {
	box := mustBox(t, mustKey(t))
	raw := json.RawMessage(`não é json`)
	got, err := box.SealConfig(raw, secretbox.Fields{}, "monitor", 1)
	if err != nil || string(got) != string(raw) {
		t.Errorf("SealConfig with no fields = (%q, %v), want it untouched", got, err)
	}
}

// Na dúvida, não se grava: uma configuração que não se lê pode conter a
// senha, e gravá-la como veio a deixaria em texto puro.
func TestSealConfigRefusesWhatItCannotRead(t *testing.T) {
	box := mustBox(t, mustKey(t))
	for _, raw := range []string{`{"password":`, `["password","x"]`, `"x"`} {
		if _, err := box.SealConfig(json.RawMessage(raw), secretbox.Fields{Names: []string{"password"}}, "monitor", 1); err == nil {
			t.Errorf("SealConfig(%q) accepted a config it cannot read", raw)
		}
	}
}

// Uma instalação antiga tem as senhas em texto puro. Ler continua
// funcionando, senão a atualização travaria antes de a migração cifrá-las.
func TestOpenConfigReadsLegacyPlaintext(t *testing.T) {
	box := mustBox(t, mustKey(t))
	raw := json.RawMessage(`{"basic_auth_pass":"antiga"}`)

	got, err := box.OpenConfig(raw, httpLike, "monitor", 1)
	if err != nil || decode(t, got)["basic_auth_pass"] != "antiga" {
		t.Errorf("OpenConfig of plaintext = (%s, %v), want it read as is", got, err)
	}
	if !secretbox.HasPlaintext(raw, httpLike) {
		t.Error("HasPlaintext did not spot the legacy password")
	}
	sealed, _ := box.SealConfig(raw, httpLike, "monitor", 1)
	if secretbox.HasPlaintext(sealed, httpLike) {
		t.Errorf("HasPlaintext(%s) = true after sealing", sealed)
	}
}

// Um valor cifrado só abre no registro e no campo em que nasceu, também
// quando quem o move é um cabeçalho trocado de nome.
func TestOpenConfigRefusesSecretMovedAround(t *testing.T) {
	box := mustBox(t, mustKey(t))
	sealed, _ := box.SealConfig(json.RawMessage(`{"headers":{"Authorization":"x"}}`), httpLike, "monitor", 1)

	if _, err := box.OpenConfig(sealed, httpLike, "monitor", 2); err == nil {
		t.Error("a config sealed for monitor 1 opened as monitor 2")
	}
	moved := strings.Replace(string(sealed), "Authorization", "X-Auth", 1)
	if _, err := box.OpenConfig(json.RawMessage(moved), httpLike, "monitor", 1); err == nil {
		t.Error("a header value opened under another header name")
	}
}
