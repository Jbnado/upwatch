package auth_test

import (
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Jbnado/upwatch/internal/domain"
)

// TestMain reduz o custo do bcrypt para o mínimo aceito pela biblioteca.
//
// O custo de produção é deliberadamente alto para inviabilizar força
// bruta; pagá-lo em dezenas de casos tornaria a suíte lenta o bastante
// para as pessoas deixarem de rodá-la, que é o pior desfecho possível.
// O comportamento verificado é o mesmo — o que muda é só quantas rodadas
// o algoritmo executa.
func TestMain(m *testing.M) {
	domain.PasswordHashCost = bcrypt.MinCost
	os.Exit(m.Run())
}
