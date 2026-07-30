package sqlstore_test

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Jbnado/upwatch/internal/domain"
)

// TestMain reduz o custo do bcrypt na suíte de conformidade.
//
// Vários casos criam contas, e o custo de produção é deliberadamente alto
// para inviabilizar força bruta. Pagá-lo aqui só tornaria a suíte lenta
// sem verificar nada a mais: o que está sob teste é a persistência do
// hash, não quantas rodadas o algoritmo executa.
func TestMain(m *testing.M) {
	domain.PasswordHashCost = bcrypt.MinCost
	os.Exit(m.Run())
}
