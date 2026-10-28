// Package secretbox cifra em repouso as credenciais que o UpWatch precisa
// usar de volta.
//
// Senha de usuário vira bcrypt e token de sessão vira hash, porque o
// UpWatch só precisa conferi-los. A senha do Redis monitorado, o token do
// ntfy e a URL do webhook do Discord são outra coisa: o UpWatch os envia,
// então precisa do valor. Guardados em texto puro, quem obtivesse o banco
// — um backup, uma cópia do volume, um pg_dump — ficaria com a credencial
// de todos os serviços monitorados de uma vez.
//
// O pacote é a única camada que cifra. O store o chama ao gravar e ao ler
// a configuração; o resto do código continua vendo o texto puro em
// memória e não sabe que a cifragem existe.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// KeySize é o tamanho da chave: AES-256.
const KeySize = 32

// Key é a chave de cifragem.
type Key [KeySize]byte

// prefix marca o valor cifrado. O que não começa com ele é texto puro de
// uma instalação anterior à cifragem.
const prefix = "enc:"

// version é o formato do valor gravado: AES-256-GCM, nonce de 12 bytes e
// o dado associado de aad. Um formato novo ganha outro número e convive
// com este.
const version = "v1"

var (
	// ErrCannotOpen é o valor cifrado que não abre: chave errada, valor
	// adulterado ou copiado de outro registro ou campo.
	ErrCannotOpen = errors.New("secretbox: o valor cifrado não abre com esta chave (chave errada, ou valor adulterado ou copiado de outro registro)")

	// ErrWrongKey é a chave que não é a do banco.
	ErrWrongKey = errors.New("secretbox: a chave de segredos não é a deste banco")

	// ErrKeyMissing é o banco que precisa de chave sem nenhuma à vista.
	ErrKeyMissing = errors.New("secretbox: nenhuma chave de segredos configurada")
)

// GenerateKey sorteia uma chave nova.
func GenerateKey() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return Key{}, fmt.Errorf("secretbox: sorteando a chave: %w", err)
	}
	return k, nil
}

// ParseKey lê a chave em base64, com ou sem preenchimento, no alfabeto
// padrão ou no de URL. Espaço e quebra de linha em volta são ignorados,
// porque a chave costuma chegar de um arquivo.
func ParseKey(s string) (Key, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		raw, err := enc.DecodeString(s)
		if err != nil {
			continue
		}
		if len(raw) != KeySize {
			return Key{}, fmt.Errorf("secretbox: chave de segredos inválida: esperado base64 de 32 bytes, veio de %d bytes; gere uma com `openssl rand -base64 32`", len(raw))
		}
		var k Key
		copy(k[:], raw)
		return k, nil
	}
	return Key{}, errors.New("secretbox: chave de segredos inválida: esperado base64 de 32 bytes; gere uma com `openssl rand -base64 32`")
}

// Encode devolve a chave em base64, na forma que ParseKey lê.
func (k Key) Encode() string { return base64.StdEncoding.EncodeToString(k[:]) }

// String esconde a chave. Uma struct com a chave dentro, impressa com %v
// num log de depuração, a entregaria inteira.
func (k Key) String() string { return "secretbox.Key(***)" }

// GoString esconde a chave também no %#v.
func (k Key) GoString() string { return k.String() }

// Slot é onde o valor mora: tabela, registro e campo. Entra como dado
// associado da cifra, então o valor só abre no lugar em que nasceu.
type Slot struct {
	Table string
	ID    int64
	Field string
}

// aad monta o dado associado. Os separadores são NUL, que não aparece em
// nome de tabela nem em número, então dois lugares nunca dão o mesmo
// dado.
func (s Slot) aad() []byte {
	return []byte("upwatch/" + version + "\x00" + s.Table + "\x00" + strconv.FormatInt(s.ID, 10) + "\x00" + s.Field)
}

// Box cifra com a chave principal e decifra com ela ou com as anteriores.
type Box struct {
	seal cipher.AEAD
	open []cipher.AEAD
}

// New monta a caixa. As chaves de fallbacks só leem: existem para a troca
// de chave, quando parte dos valores ainda está na antiga.
func New(primary Key, fallbacks ...Key) (*Box, error) {
	b := &Box{}
	for i, k := range append([]Key{primary}, fallbacks...) {
		block, err := aes.NewCipher(k[:])
		if err != nil {
			return nil, fmt.Errorf("secretbox: preparando a cifra: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("secretbox: preparando o GCM: %w", err)
		}
		if i == 0 {
			b.seal = aead
		}
		b.open = append(b.open, aead)
	}
	return b, nil
}

// IsSealed informa se o valor foi gravado cifrado.
func IsSealed(v string) bool { return strings.HasPrefix(v, prefix) }

// Seal cifra o valor para o lugar dado, no formato enc:v1:<nonce>:<cifra>,
// com nonce e cifra em base64 de URL sem preenchimento.
//
// O nonce é sorteado a cada chamada. Repetido, o GCM entrega a chave de
// autenticação a quem tiver as duas cifras.
func (b *Box) Seal(plaintext string, slot Slot) (string, error) {
	nonce := make([]byte, b.seal.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secretbox: sorteando o nonce: %w", err)
	}
	ct := b.seal.Seal(nil, nonce, []byte(plaintext), slot.aad())
	return prefix + version + ":" +
		base64.RawURLEncoding.EncodeToString(nonce) + ":" +
		base64.RawURLEncoding.EncodeToString(ct), nil
}

// Open decifra o valor gravado por Seal no mesmo lugar.
func (b *Box) Open(sealed string, slot Slot) (string, error) {
	rest, ok := strings.CutPrefix(sealed, prefix)
	if !ok {
		return "", fmt.Errorf("%w: o valor não está cifrado", ErrCannotOpen)
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return "", fmt.Errorf("%w: formato desconhecido", ErrCannotOpen)
	}
	if parts[0] != version {
		return "", fmt.Errorf("secretbox: formato de cifra %q desconhecido; o banco foi gravado por uma versão mais nova do UpWatch", parts[0])
	}
	nonce, errN := base64.RawURLEncoding.DecodeString(parts[1])
	ct, errC := base64.RawURLEncoding.DecodeString(parts[2])
	if errN != nil || errC != nil || len(nonce) != b.seal.NonceSize() {
		return "", fmt.Errorf("%w: formato corrompido", ErrCannotOpen)
	}
	for _, aead := range b.open {
		if pt, err := aead.Open(nil, nonce, ct, slot.aad()); err == nil {
			return string(pt), nil
		}
	}
	return "", ErrCannotOpen
}
