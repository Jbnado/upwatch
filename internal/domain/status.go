package domain

import (
	"encoding/json"
	"fmt"
)

// Status é o resultado de um check num instante.
//
// É serializado como string na API: o zero value do Go não pode aparecer
// como `0` no JSON, e nomes sobrevivem a reordenação das constantes.
type Status uint8

const (
	// StatusUnknown é o zero value: ainda não há resultado.
	StatusUnknown Status = iota
	// StatusUp indica que o alvo respondeu dentro do esperado.
	StatusUp
	// StatusDown indica que o alvo não respondeu ou violou a condição.
	StatusDown
	// StatusDegraded indica que o alvo respondeu, porém fora do limiar
	// aceitável (latência alta, certificado perto de expirar).
	StatusDegraded
)

var statusNames = map[Status]string{
	StatusUnknown:  "unknown",
	StatusUp:       "up",
	StatusDown:     "down",
	StatusDegraded: "degraded",
}

// String devolve o nome canônico do status. Valores fora do conjunto
// conhecido caem em "unknown" em vez de exibir o número cru.
func (s Status) String() string {
	if name, ok := statusNames[s]; ok {
		return name
	}
	return "unknown"
}

// ParseStatus converte o nome canônico de volta em Status.
func ParseStatus(name string) (Status, error) {
	for status, n := range statusNames {
		if n == name {
			return status, nil
		}
	}
	return StatusUnknown, fmt.Errorf("domain: status inválido %q", name)
}

// Responsive informa se o alvo respondeu. Latência só tem significado
// quando houve resposta, então agregações usam isto para decidir se a
// amostra entra no cálculo.
func (s Status) Responsive() bool {
	return s == StatusUp || s == StatusDegraded
}

// MarshalJSON implementa json.Marshaler.
func (s Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON implementa json.Unmarshaler.
func (s *Status) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	parsed, err := ParseStatus(name)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}
