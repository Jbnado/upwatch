package auth_test

import (
	"context"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/Jbnado/upwatch/internal/domain"
)

// O piso em si é conferido em internal/config, que é onde o padrão de
// produção vive: esta suíte baixa domain.PasswordHashCost para o mínimo
// do bcrypt, de propósito, para não pagar o custo deliberado em cada
// caso de teste — então aqui não há como enxergar o valor de produção.

// Subir o custo não alcança hash nenhum já gravado: a conta só ganharia o
// custo novo se a pessoa trocasse a senha por conta própria, e ninguém
// troca a senha porque o projeto mudou um padrão.
//
// O login é o único momento em que a senha em claro existe, então é ali
// que o hash antigo é regravado.
func TestLoginRehashesAWeakerStoredHash(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	// A suíte roda no custo mínimo, então sobe um degrau: é o bastante
	// para um hash gravado no custo de antes ficar atrás do atual, que é a
	// situação de quem atualizou a instalação.
	antes := domain.PasswordHashCost
	domain.PasswordHashCost = antes + 1
	t.Cleanup(func() { domain.PasswordHashCost = antes })

	fraco, err := bcrypt.GenerateFromPassword([]byte(goodPassword), antes)
	if err != nil {
		t.Fatalf("GenerateFromPassword returned unexpected error: %v", err)
	}
	if err := f.store.Users().SetPasswordHash(ctx, u.ID, string(fraco)); err != nil {
		t.Fatalf("SetPasswordHash returned unexpected error: %v", err)
	}

	if _, err := f.svc.Login(ctx, u.Username, goodPassword); err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}

	depois, err := f.store.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	custo, err := bcrypt.Cost([]byte(depois.PasswordHash))
	if err != nil {
		t.Fatalf("Cost returned unexpected error: %v", err)
	}
	if custo != domain.PasswordHashCost {
		t.Errorf("custo depois do login = %d, queria %d", custo, domain.PasswordHashCost)
	}
	// E a senha continua valendo, que é o que seria catastrófico errar: um
	// re-hash que gravasse outra coisa trancaria a conta para fora.
	if !depois.CheckPassword(goodPassword) {
		t.Error("a senha deixou de valer depois do re-hash")
	}
	if _, err := f.svc.Login(ctx, u.Username, goodPassword); err != nil {
		t.Errorf("o login seguinte falhou: %v", err)
	}
}

// Senha errada não regrava nada: o re-hash acontece depois da conferência,
// e não antes.
func TestAWrongPasswordDoesNotRehash(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	antes := domain.PasswordHashCost
	domain.PasswordHashCost = antes + 1
	t.Cleanup(func() { domain.PasswordHashCost = antes })

	fraco, err := bcrypt.GenerateFromPassword([]byte(goodPassword), antes)
	if err != nil {
		t.Fatalf("GenerateFromPassword returned unexpected error: %v", err)
	}
	if err := f.store.Users().SetPasswordHash(ctx, u.ID, string(fraco)); err != nil {
		t.Fatalf("SetPasswordHash returned unexpected error: %v", err)
	}

	if _, err := f.svc.Login(ctx, u.Username, "senha-errada-porem-longa"); err == nil {
		t.Fatal("Login aceitou a senha errada")
	}

	depois, err := f.store.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if depois.PasswordHash != string(fraco) {
		t.Error("o hash foi regravado numa tentativa recusada")
	}
}
