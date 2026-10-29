// Package totp implementa senhas de uso único por tempo (RFC 6238), o
// código de seis dígitos dos aplicativos autenticadores.
//
// Feito aqui, com a biblioteca padrão, em vez de uma dependência: o
// algoritmo cabe em poucas linhas, e numa peça de autenticação cada
// dependência é mais código de terceiros no caminho do login.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Digits é o tamanho do código.
	Digits = 6

	// Period é a duração de cada passo.
	Period = 30 * time.Second

	// Skew é quantos passos de cada lado do atual são aceitos. Um passo
	// cobre o relógio do celular um pouco adiantado ou atrasado, e o
	// código digitado na virada da janela; mais que isso só alarga a
	// janela de quem tenta adivinhar.
	Skew = 1

	// SecretSize é o tamanho do segredo em bytes: os 160 bits que a
	// RFC 4226 recomenda para HMAC-SHA1.
	SecretSize = 20

	// Issuer é o nome que o aplicativo mostra ao lado da conta.
	Issuer = "UpWatch"
)

// ErrInvalidSecret indica segredo que não é base32 válido.
var ErrInvalidSecret = errors.New("totp: segredo inválido")

// encoding é o base32 sem preenchimento que os aplicativos esperam.
var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret sorteia um segredo e o devolve em base32, a forma que se
// digita no aplicativo quando o QR code não serve.
func NewSecret() (string, error) {
	buf := make([]byte, SecretSize)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("totp: sorteando segredo: %w", err)
	}
	return encoding.EncodeToString(buf), nil
}

// decodeSecret aceita o segredo como os aplicativos o mostram: com
// espaços, em minúsculas, com ou sem preenchimento.
func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	s = strings.TrimRight(s, "=")
	key, err := encoding.DecodeString(s)
	if err != nil || len(key) == 0 {
		return nil, ErrInvalidSecret
	}
	return key, nil
}

// Step é o passo de tempo do instante.
func Step(t time.Time) int64 {
	return t.Unix() / int64(Period/time.Second)
}

// hotp é o código da RFC 4226 para o contador.
func hotp(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	// Truncamento dinâmico: os quatro bits finais escolhem de onde ler.
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff

	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// Code é o código do segredo no instante dado.
func Code(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, uint64(Step(t)), Digits), nil
}

// LooksLikeCode informa se a entrada tem a forma de um código: seis
// dígitos, depois de tirar os espaços que algumas pessoas copiam junto.
func LooksLikeCode(input string) bool {
	c := normalizeCode(input)
	if len(c) != Digits {
		return false
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizeCode(input string) string {
	return strings.ReplaceAll(strings.TrimSpace(input), " ", "")
}

// Verify confere o código contra os passos em torno de now e devolve o
// passo aceito.
//
// Passos até lastStep, inclusive, são recusados: é o que impede o mesmo
// código de valer duas vezes. Quem chama grava o passo devolvido como o
// novo lastStep.
//
// SHA-1 porque é o que os aplicativos autenticadores implementam; dentro
// do HMAC, as fraquezas conhecidas dele não se aplicam.
//
// A comparação é em tempo constante: a comum para no primeiro dígito
// diferente, e o tempo de resposta diria quanto do palpite estava certo.
func Verify(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = normalizeCode(code)
	if !LooksLikeCode(code) {
		return 0, false
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}

	current := Step(now)
	var matched int64
	found := 0
	for d := int64(-Skew); d <= Skew; d++ {
		step := current + d
		if step < 0 {
			continue
		}
		ok := subtle.ConstantTimeCompare([]byte(hotp(key, uint64(step), Digits)), []byte(code))
		if ok == 1 && step > lastStep && found == 0 {
			matched, found = step, 1
		}
	}
	return matched, found == 1
}

// URI monta o endereço otpauth:// que vira o QR code.
//
// O rótulo leva o emissor antes do nome da conta, e o emissor repete no
// parâmetro: aplicativos antigos leem um, os novos o outro.
func URI(secret, account string) string {
	label := url.PathEscape(Issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", Issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(int(Period/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
