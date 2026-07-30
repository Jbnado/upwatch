package domain

import (
	"encoding/json"
	"fmt"
)

// Papéis de acesso.
//
// Dois, e não uma matriz de permissões por endpoint. A pergunta que um
// time faz sobre uma conta é "essa pessoa pode mexer?", e uma lista de
// caixinhas por recurso vira configuração que ninguém revisa — e que,
// por isso, acaba com todo mundo marcado.
//
// O observador enxerga tudo o que o administrador enxerga, inclusive
// endereço de alvo e causa de falha. Ele é de dentro; quem não é vê a
// página pública, que é outra superfície com outras regras.

// Role é o papel de uma conta.
type Role uint8

const (
	// RoleAdmin administra: cadastra alvos, canais, páginas e contas.
	RoleAdmin Role = iota + 1
	// RoleViewer só lê. Serve para quem precisa acompanhar sem poder
	// mexer — plantão de suporte, gerência, time vizinho.
	RoleViewer
)

var roleNames = map[Role]string{
	RoleAdmin:  "admin",
	RoleViewer: "viewer",
}

// String devolve o nome canônico do papel.
func (r Role) String() string {
	if nome, ok := roleNames[r]; ok {
		return nome
	}
	return "unknown"
}

// Valid informa se o papel pertence ao conjunto conhecido.
func (r Role) Valid() bool {
	_, ok := roleNames[r]
	return ok
}

// CanWrite informa se o papel pode alterar o estado do sistema.
func (r Role) CanWrite() bool { return r == RoleAdmin }

// ParseRole converte o nome canônico de volta.
//
// Sem valor padrão para o desconhecido: um papel que o banco devolva
// fora do conjunto precisa falhar alto. Cair em admin por omissão seria
// conceder acesso justamente no caso em que não se sabe o que está
// acontecendo.
func ParseRole(name string) (Role, error) {
	for papel, n := range roleNames {
		if n == name {
			return papel, nil
		}
	}
	return 0, fmt.Errorf("domain: papel inválido %q", name)
}

// MarshalJSON implementa json.Marshaler.
func (r Role) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.String())
}

// UnmarshalJSON implementa json.Unmarshaler.
func (r *Role) UnmarshalJSON(data []byte) error {
	var nome string
	if err := json.Unmarshal(data, &nome); err != nil {
		return err
	}
	parsed, err := ParseRole(nome)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}
