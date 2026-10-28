package secretbox

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Fields diz onde a configuração JSON de um tipo guarda segredo.
//
// Quem sabe quais campos são segredo é o dono do tipo: o checker para os
// monitores, o notifier para os canais. Este pacote só sabe percorrer o
// JSON e cifrar o que lhe apontam.
type Fields struct {
	// Names são campos de primeiro nível cujo valor é segredo.
	Names []string
	// Map é um objeto de primeiro nível de texto para texto, como os
	// cabeçalhos HTTP, em que cada valor pode ser segredo.
	Map string
	// MapSecret decide pelo nome da chave se o valor dela é segredo. Nil
	// vale para todas.
	MapSecret func(key string) bool
}

// Empty informa que o tipo não guarda segredo.
func (f Fields) Empty() bool { return len(f.Names) == 0 && f.Map == "" }

// SealConfig cifra os segredos da configuração do registro dado.
//
// Os nomes casam sem diferenciar caixa, como o encoding/json casa campo:
// o checker leria a senha escrita "Password", e cifrar só "password" a
// deixaria em texto puro. Configuração sem segredo presente volta byte a
// byte como veio.
func (b *Box) SealConfig(raw json.RawMessage, f Fields, table string, id int64) (json.RawMessage, error) {
	return walk(raw, f, func(field, v string) (string, bool, error) {
		if v == "" {
			return v, false, nil
		}
		sealed, err := b.Seal(v, Slot{Table: table, ID: id, Field: field})
		return sealed, true, err
	})
}

// OpenConfig decifra os segredos da configuração do registro dado. Valor
// em texto puro, de antes da cifragem, é lido como está.
//
// O que não é objeto JSON volta como está: o store nunca grava assim um
// valor cifrado, então não há o que abrir.
func (b *Box) OpenConfig(raw json.RawMessage, f Fields, table string, id int64) (json.RawMessage, error) {
	if !isObject(raw) {
		return raw, nil
	}
	out, err := walk(raw, f, func(field, v string) (string, bool, error) {
		if !IsSealed(v) {
			return v, false, nil
		}
		plain, err := b.Open(v, Slot{Table: table, ID: id, Field: field})
		if err != nil {
			return "", false, fmt.Errorf("%s %d, campo %s: %w", table, id, field, err)
		}
		return plain, true, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// HasPlaintext informa se a configuração ainda tem segredo em texto puro.
// É o que a migração procura.
func HasPlaintext(raw json.RawMessage, f Fields) bool {
	found := false
	_, _ = walk(raw, f, func(_, v string) (string, bool, error) {
		if v != "" && !IsSealed(v) {
			found = true
		}
		return v, false, nil
	})
	return found
}

// walk aplica fn a cada valor secreto de texto e remonta o JSON só se
// algum mudou. O caminho do campo, que entra no dado associado, é o nome
// como está gravado; num cabeçalho, o do objeto e o do cabeçalho
// separados por NUL.
func walk(raw json.RawMessage, f Fields, fn func(field, v string) (string, bool, error)) (json.RawMessage, error) {
	if f.Empty() || len(raw) == 0 {
		return raw, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("secretbox: configuração não é um objeto JSON, e um segredo nela não teria onde ser achado: %w", err)
	}

	changed := false
	// apply devolve o valor novo e se ele mudou.
	apply := func(field string, value json.RawMessage) (json.RawMessage, bool, error) {
		var s string
		if json.Unmarshal(value, &s) != nil {
			// Não é texto: nem o checker o leria como senha.
			return value, false, nil
		}
		next, ok, err := fn(field, s)
		if err != nil || !ok {
			return value, false, err
		}
		encoded, err := json.Marshal(next)
		return encoded, err == nil, err
	}

	for key, value := range top {
		if matchesAny(key, f.Names) {
			v, ok, err := apply(key, value)
			if err != nil {
				return nil, err
			}
			top[key] = v
			changed = changed || ok
			continue
		}
		if f.Map == "" || !strings.EqualFold(key, f.Map) {
			continue
		}
		var inner map[string]json.RawMessage
		if json.Unmarshal(value, &inner) != nil || inner == nil {
			continue
		}
		innerChanged := false
		for name, hv := range inner {
			if f.MapSecret != nil && !f.MapSecret(name) {
				continue
			}
			v, ok, err := apply(key+"\x00"+name, hv)
			if err != nil {
				return nil, err
			}
			inner[name] = v
			innerChanged = innerChanged || ok
		}
		if innerChanged {
			encoded, err := json.Marshal(inner)
			if err != nil {
				return nil, err
			}
			top[key] = encoded
			changed = true
		}
	}

	if !changed {
		return raw, nil
	}
	out, err := json.Marshal(top)
	if err != nil {
		return nil, fmt.Errorf("secretbox: remontando a configuração: %w", err)
	}
	return out, nil
}

func matchesAny(key string, names []string) bool {
	for _, n := range names {
		if strings.EqualFold(key, n) {
			return true
		}
	}
	return false
}

// isObject informa se raw é um objeto JSON (ou null).
func isObject(raw json.RawMessage) bool {
	var top map[string]json.RawMessage
	return json.Unmarshal(raw, &top) == nil
}
