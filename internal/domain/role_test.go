package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Papéis de acesso.
//
// Dois, e não uma matriz de permissões: quem administra e quem só olha.
// A pergunta que o time faz é "essa pessoa pode mexer?", e uma lista de
// caixinhas por endpoint transformaria isso numa configuração que
// ninguém revisa.

func TestRoleIdaEVolta(t *testing.T) {
	// Os nomes vão para o banco e para o JSON. Se a ida e a volta
	// divergirem, uma conta gravada hoje é lida com outro papel amanhã —
	// e o modo de falhar é conceder acesso, não negar.
	for _, papel := range []domain.Role{domain.RoleAdmin, domain.RoleViewer} {
		t.Run(papel.String(), func(t *testing.T) {
			volta, err := domain.ParseRole(papel.String())
			if err != nil {
				t.Fatalf("nome %q não voltou: %v", papel, err)
			}
			if volta != papel {
				t.Fatalf("ida e volta divergiram: %v -> %v", papel, volta)
			}
		})
	}
}

func TestParseRoleRecusaDesconhecido(t *testing.T) {
	// Sem valor padrão silencioso: um papel desconhecido vindo do banco
	// precisa falhar alto, e não virar admin por omissão.
	if _, err := domain.ParseRole("superadmin"); err == nil {
		t.Error("papel desconhecido foi aceito")
	}
	if _, err := domain.ParseRole(""); err == nil {
		t.Error("papel vazio foi aceito")
	}
}

func TestCanWriteSeparaOsPapeis(t *testing.T) {
	if !domain.RoleAdmin.CanWrite() {
		t.Error("administrador deveria poder escrever")
	}
	if domain.RoleViewer.CanWrite() {
		t.Error("observador não deveria poder escrever")
	}
}

func TestUserValidateExigePapelConhecido(t *testing.T) {
	u := domain.User{Username: "ana", Role: domain.Role(99)}

	err := u.Validate()
	if err == nil {
		t.Fatal("papel inválido passou pela validação")
	}
	assertCampo(t, err, "role")
}

func TestUserValidateAceitaOsDoisPapeis(t *testing.T) {
	for _, papel := range []domain.Role{domain.RoleAdmin, domain.RoleViewer} {
		t.Run(papel.String(), func(t *testing.T) {
			u := domain.User{Username: "ana", Role: papel}

			if err := u.Validate(); err != nil {
				t.Fatalf("papel %s reprovado: %v", papel, err)
			}
		})
	}
}

func TestUserMarshalTrazOPapel(t *testing.T) {
	// A interface precisa do papel para decidir o que mostrar; sem ele
	// na resposta, ela teria que adivinhar pelo que a API recusa.
	bruto, err := json.Marshal(domain.User{ID: 1, Username: "ana", Role: domain.RoleViewer})
	if err != nil {
		t.Fatalf("serializando: %v", err)
	}

	if !strings.Contains(string(bruto), `"role":"viewer"`) {
		t.Fatalf("papel ausente na resposta: %s", bruto)
	}
	// E o hash continua fora, que é a razão de MarshalJSON existir.
	if strings.Contains(string(bruto), "password") {
		t.Errorf("resposta vazou campo de senha: %s", bruto)
	}
}
