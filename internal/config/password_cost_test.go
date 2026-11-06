package config_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/config"
	"github.com/Jbnado/upwatch/internal/domain"
)

// O padrão de produção é o piso, e não o padrão da biblioteca de bcrypt:
// dez vem de quando GPU era mais lenta, e a recomendação corrente pede
// doze. É este teste que guarda o valor, porque a suíte de internal/auth
// baixa domain.PasswordHashCost de propósito e não consegue vê-lo.
func TestTheDefaultPasswordHashCostIsTheFloor(t *testing.T) {
	if got := config.Default().PasswordHashCost; got != domain.MinPasswordHashCost {
		t.Errorf("custo padrão = %d, queria %d", got, domain.MinPasswordHashCost)
	}
	if domain.MinPasswordHashCost < 12 {
		t.Errorf("o piso caiu para %d; a recomendação pede ao menos 12", domain.MinPasswordHashCost)
	}
}

// Quem escreveu um número menor acha que configurou algo. Baixar em
// silêncio para o piso deixaria a instalação com um custo diferente do que
// o operador pediu, e um custo de senha mais fraco do que o pedido é o tipo
// de coisa que ninguém descobre até o banco vazar.
func TestAPasswordHashCostBelowTheFloorIsRefused(t *testing.T) {
	cfg := config.Default()
	cfg.PasswordHashCost = domain.MinPasswordHashCost - 1

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate aceitou um custo abaixo do piso")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(domain.MinPasswordHashCost)) {
		t.Errorf("o erro não diz qual é o piso: %v", err)
	}
}

// E subir continua valendo: é o motivo de a variável existir.
func TestAHigherPasswordHashCostIsAccepted(t *testing.T) {
	cfg := config.Default()
	cfg.PasswordHashCost = domain.MinPasswordHashCost + 2

	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate recusou um custo maior que o piso: %v", err)
	}
}

// A variável de ambiente alcança a configuração. Sem isto o comentário do
// domínio voltaria a prometer um ajuste que não existe.
func TestPasswordHashCostComesFromTheEnvironment(t *testing.T) {
	t.Setenv("UPWATCH_PASSWORD_HASH_COST", strconv.Itoa(domain.MinPasswordHashCost+3))

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.PasswordHashCost != domain.MinPasswordHashCost+3 {
		t.Errorf("custo lido = %d, queria %d", cfg.PasswordHashCost, domain.MinPasswordHashCost+3)
	}
}
