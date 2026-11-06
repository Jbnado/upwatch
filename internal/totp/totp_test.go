package totp

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"
)

// rfcSecret é a chave SHA-1 dos vetores do apêndice B da RFC 6238.
var rfcSecret = base32.StdEncoding.WithPadding(base32.NoPadding).
	EncodeToString([]byte("12345678901234567890"))

// Os vetores oficiais, com oito dígitos como na RFC. Seis dígitos são os
// últimos seis desses oito, conferidos à parte.
func TestHOTPMatchesRFC6238Vectors(t *testing.T) {
	key, err := decodeSecret(rfcSecret)
	if err != nil {
		t.Fatalf("decodeSecret returned unexpected error: %v", err)
	}
	vectors := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, v := range vectors {
		got := hotp(key, uint64(Step(time.Unix(v.unix, 0))), 8)
		if got != v.want {
			t.Errorf("hotp(T=%d, 8 dígitos) = %s, want %s", v.unix, got, v.want)
		}
		six, err := Code(rfcSecret, time.Unix(v.unix, 0))
		if err != nil {
			t.Fatalf("Code returned unexpected error: %v", err)
		}
		if six != v.want[2:] {
			t.Errorf("Code(T=%d) = %s, want %s", v.unix, six, v.want[2:])
		}
	}
}

func TestVerifyAcceptsOneStepEachSide(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, d := range []time.Duration{-Period, 0, Period} {
		code, _ := Code(rfcSecret, now.Add(d))
		step, ok := Verify(rfcSecret, code, now, 0)
		if !ok {
			t.Errorf("Verify recusou o código de %v; want aceito", d)
		}
		if want := Step(now.Add(d)); step != want {
			t.Errorf("Verify devolveu o passo %d, want %d", step, want)
		}
	}
}

func TestVerifyRejectsTwoStepsAway(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, d := range []time.Duration{-2 * Period, 2 * Period} {
		code, _ := Code(rfcSecret, now.Add(d))
		if _, ok := Verify(rfcSecret, code, now, 0); ok {
			t.Errorf("Verify aceitou o código de %v; want recusado", d)
		}
	}
}

// O mesmo código não vale duas vezes, nem um código de passo anterior
// ao último usado.
func TestVerifyRejectsReplay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	code, _ := Code(rfcSecret, now)

	step, ok := Verify(rfcSecret, code, now, 0)
	if !ok {
		t.Fatal("primeiro uso recusado; want aceito")
	}
	if _, ok := Verify(rfcSecret, code, now, step); ok {
		t.Error("Verify aceitou o código já usado; want recusado")
	}
	older, _ := Code(rfcSecret, now.Add(-Period))
	if _, ok := Verify(rfcSecret, older, now, step); ok {
		t.Error("Verify aceitou um código anterior ao último usado; want recusado")
	}
	next, _ := Code(rfcSecret, now.Add(Period))
	if _, ok := Verify(rfcSecret, next, now, step); !ok {
		t.Error("Verify recusou o código do passo seguinte; want aceito")
	}
}

func TestVerifyRejectsMalformedInput(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, code := range []string{"", "12345", "1234567", "abcdef", "12 34 5x"} {
		if _, ok := Verify(rfcSecret, code, now, 0); ok {
			t.Errorf("Verify(%q) aceitou; want recusado", code)
		}
	}
	if _, ok := Verify("não é base32!", "123456", now, 0); ok {
		t.Error("Verify aceitou com segredo inválido")
	}
}

func TestVerifyIgnoresSpacesInCode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	code, _ := Code(rfcSecret, now)
	if _, ok := Verify(rfcSecret, code[:3]+" "+code[3:], now, 0); !ok {
		t.Error("Verify recusou o código com espaço no meio; want aceito")
	}
}

func TestNewSecretHasTwentyBytes(t *testing.T) {
	s, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret returned unexpected error: %v", err)
	}
	key, err := decodeSecret(s)
	if err != nil {
		t.Fatalf("decodeSecret returned unexpected error: %v", err)
	}
	if len(key) != SecretSize {
		t.Errorf("len(key) = %d, want %d", len(key), SecretSize)
	}
	other, _ := NewSecret()
	if other == s {
		t.Error("dois segredos sorteados saíram iguais")
	}
}

func TestURICarriesWhatAuthenticatorsRead(t *testing.T) {
	raw := URI("JBSWY3DPEHPK3PXP", "maria silva")

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse returned unexpected error: %v", err)
	}
	if u.Scheme != "otpauth" || u.Host != "totp" {
		t.Errorf("URI = %q, want otpauth://totp/...", raw)
	}
	if !strings.HasPrefix(u.Path, "/UpWatch:maria silva") {
		t.Errorf("rótulo = %q, want UpWatch:maria silva", u.Path)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"secret": "JBSWY3DPEHPK3PXP", "issuer": "UpWatch",
		"algorithm": "SHA1", "digits": "6", "period": "30",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
