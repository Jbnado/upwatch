package secretbox_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/secretbox"
)

func mustKey(t *testing.T) secretbox.Key {
	t.Helper()
	k, err := secretbox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey returned unexpected error: %v", err)
	}
	return k
}

func mustBox(t *testing.T, primary secretbox.Key, fallbacks ...secretbox.Key) *secretbox.Box {
	t.Helper()
	b, err := secretbox.New(primary, fallbacks...)
	if err != nil {
		t.Fatalf("New returned unexpected error: %v", err)
	}
	return b
}

var redisSlot = secretbox.Slot{Table: "monitor", ID: 7, Field: "password"}

func TestSealOpensBackToThePlaintext(t *testing.T) {
	box := mustBox(t, mustKey(t))

	sealed, err := box.Seal("senha-do-redis", redisSlot)
	if err != nil {
		t.Fatalf("Seal returned unexpected error: %v", err)
	}
	if strings.Contains(sealed, "senha-do-redis") {
		t.Fatalf("sealed value %q carries the plaintext", sealed)
	}
	got, err := box.Open(sealed, redisSlot)
	if err != nil || got != "senha-do-redis" {
		t.Errorf("Open = (%q, %v), want the original password", got, err)
	}
}

// O valor gravado diz como foi cifrado: o prefixo separa o cifrado do
// texto puro de uma instalação antiga, e a versão deixa um formato futuro
// conviver com este sem adivinhação.
func TestSealedValueCarriesFormatVersion(t *testing.T) {
	box := mustBox(t, mustKey(t))

	sealed, err := box.Seal("x", redisSlot)
	if err != nil {
		t.Fatalf("Seal returned unexpected error: %v", err)
	}
	parts := strings.Split(sealed, ":")
	if len(parts) != 4 || parts[0] != "enc" || parts[1] != "v1" {
		t.Fatalf("sealed = %q, want enc:v1:<nonce>:<cifra>", sealed)
	}
	if !secretbox.IsSealed(sealed) || secretbox.IsSealed("senha comum") {
		t.Error("IsSealed does not tell sealed from plaintext")
	}
}

// Nonce repetido no GCM entrega a chave de autenticação a quem tiver duas
// cifras, e um valor igual cifrando igual conta a quem lê o banco que dois
// monitores usam a mesma senha. Cada gravação sorteia o seu.
func TestSealDrawsAFreshNonceEveryTime(t *testing.T) {
	box := mustBox(t, mustKey(t))

	a, _ := box.Seal("mesma senha", redisSlot)
	b, _ := box.Seal("mesma senha", redisSlot)
	if a == b {
		t.Fatalf("sealing twice gave the same value %q", a)
	}
	if strings.Split(a, ":")[2] == strings.Split(b, ":")[2] {
		t.Error("two seals reused the nonce")
	}
}

// Quem tem acesso de escrita ao banco, mas não à chave, não pode mover uma
// credencial de lugar: copiar a senha cifrada do Redis de produção para um
// monitor apontado a um servidor seu faria o UpWatch entregá-la decifrada
// na próxima verificação. O registro e o campo entram como dado associado.
func TestSealedValueOnlyOpensInItsOwnSlot(t *testing.T) {
	box := mustBox(t, mustKey(t))
	sealed, err := box.Seal("senha-de-producao", redisSlot)
	if err != nil {
		t.Fatalf("Seal returned unexpected error: %v", err)
	}

	for name, other := range map[string]secretbox.Slot{
		"outro registro": {Table: "monitor", ID: 8, Field: "password"},
		"outra tabela":   {Table: "notification_channel", ID: 7, Field: "password"},
		"outro campo":    {Table: "monitor", ID: 7, Field: "basic_auth_pass"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := box.Open(sealed, other)
			if !errors.Is(err, secretbox.ErrCannotOpen) {
				t.Errorf("Open in %+v = (%q, %v), want ErrCannotOpen", other, got, err)
			}
		})
	}
}

func TestOpenRefusesAnotherKey(t *testing.T) {
	sealed, _ := mustBox(t, mustKey(t)).Seal("segredo", redisSlot)

	if _, err := mustBox(t, mustKey(t)).Open(sealed, redisSlot); !errors.Is(err, secretbox.ErrCannotOpen) {
		t.Errorf("Open with another key error = %v, want ErrCannotOpen", err)
	}
}

// Um byte trocado na cifra precisa ser recusado, e não virar uma senha
// diferente: o GCM autentica, e é por isso que ele foi escolhido.
func TestOpenRefusesTamperedValue(t *testing.T) {
	box := mustBox(t, mustKey(t))
	sealed, _ := box.Seal("segredo", redisSlot)

	parts := strings.Split(sealed, ":")
	cipher, _ := base64.RawURLEncoding.DecodeString(parts[3])
	cipher[0] ^= 0xff
	parts[3] = base64.RawURLEncoding.EncodeToString(cipher)

	if _, err := box.Open(strings.Join(parts, ":"), redisSlot); !errors.Is(err, secretbox.ErrCannotOpen) {
		t.Errorf("Open of a tampered value error = %v, want ErrCannotOpen", err)
	}
	for _, broken := range []string{"enc:v1:", "enc:v1:abc", "enc:v1:!!:??", "enc:v9:abc:def"} {
		if _, err := box.Open(broken, redisSlot); err == nil {
			t.Errorf("Open(%q) succeeded, want an error", broken)
		}
	}
}

// Na troca de chave, o que já foi recifrado e o que ainda não foi convivem.
// A caixa lê com a antiga também, mas grava só com a nova: o que passa por
// ela sai sempre na chave que vai ficar.
func TestFallbackKeyReadsButNeverSeals(t *testing.T) {
	oldKey, newKey := mustKey(t), mustKey(t)
	sealedOld, _ := mustBox(t, oldKey).Seal("segredo", redisSlot)

	rotating := mustBox(t, newKey, oldKey)
	if got, err := rotating.Open(sealedOld, redisSlot); err != nil || got != "segredo" {
		t.Fatalf("Open with the old key as fallback = (%q, %v), want it read", got, err)
	}

	sealedNew, _ := rotating.Seal("segredo", redisSlot)
	if _, err := mustBox(t, oldKey).Open(sealedNew, redisSlot); err == nil {
		t.Error("a value sealed during rotation opens with the old key, want only the new one")
	}
	if got, err := mustBox(t, newKey).Open(sealedNew, redisSlot); err != nil || got != "segredo" {
		t.Errorf("Open with the new key = (%q, %v), want it read", got, err)
	}
}

func TestParseKeyAcceptsBase64Of32Bytes(t *testing.T) {
	k := mustKey(t)

	got, err := secretbox.ParseKey(k.Encode() + "\n")
	if err != nil || got != k {
		t.Fatalf("ParseKey(Encode()) = (%v, %v), want the same key", got == k, err)
	}

	raw := make([]byte, 32)
	for name, value := range map[string]string{
		"sem preenchimento": base64.RawStdEncoding.EncodeToString(raw),
		"alfabeto de URL":   base64.URLEncoding.EncodeToString(raw),
	} {
		if _, err := secretbox.ParseKey(value); err != nil {
			t.Errorf("ParseKey(%s) returned unexpected error: %v", name, err)
		}
	}
}

// Uma chave curta não é AES-256, e aceitá-la cifraria tudo com menos do que
// a documentação promete. Uma senha qualquer no lugar da chave costuma ser
// o engano, e a mensagem precisa dizer o que se espera.
func TestParseKeyRefusesWhatIsNotA256BitKey(t *testing.T) {
	for name, value := range map[string]string{
		"vazia":         "",
		"16 bytes":      base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"33 bytes":      base64.StdEncoding.EncodeToString(make([]byte, 33)),
		"não é base64":  "minha senha secreta",
		"hex de 32":     strings.Repeat("ab", 32),
		"base64 errado": "@@@@",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := secretbox.ParseKey(value)
			if err == nil {
				t.Fatalf("ParseKey(%q) accepted it", value)
			}
			if !strings.Contains(err.Error(), "32 bytes") {
				t.Errorf("error %q does not say what a key looks like", err)
			}
		})
	}
}

// A chave não pode sair num log por descuido: %v de uma struct com a chave
// dentro a imprimiria inteira.
func TestKeyDoesNotPrintItself(t *testing.T) {
	k := mustKey(t)
	for _, form := range []string{k.String(), k.GoString()} {
		if strings.Contains(form, k.Encode()) {
			t.Errorf("formatting the key printed it: %q", form)
		}
	}
}
