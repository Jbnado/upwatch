package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

func requestOrigin(origins []string) string {
	if len(origins) > 0 && origins[0] != "" {
		return origins[0]
	}
	return "desconhecida"
}

func (s *Service) prepareDecoy(cost int) error {
	// O disfarce nasce antes da primeira requisição. Gerá-lo no primeiro
	// nome inexistente faria só aquela resposta pagar uma geração extra.
	token, err := domain.NewToken()
	if err != nil {
		return fmt.Errorf("auth: gerando disfarce do login: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(token.Secret), cost)
	if err != nil {
		return fmt.Errorf("auth: preparando disfarce do login: %w", err)
	}
	s.decoy = string(hash)
	return nil
}

// CalibrateLogin prepara a proteção de tempo antes de abrir a porta HTTP.
// Considera hashes mais caros já gravados, mesmo se a configuração caiu.
func (s *Service) CalibrateLogin(ctx context.Context) error {
	users, err := s.store.Users().List(ctx)
	if err != nil {
		return fmt.Errorf("auth: lendo custos de senha para calibração: %w", err)
	}
	cost := domain.PasswordHashCost
	for _, user := range users {
		stored, err := bcrypt.Cost([]byte(user.PasswordHash))
		if err != nil {
			continue
		}
		if stored > domain.MaxPasswordHashCost {
			return fmt.Errorf("auth: conta %d tem custo bcrypt %d acima do máximo suportado %d", user.ID, stored, domain.MaxPasswordHashCost)
		}
		cost = max(cost, stored)
	}
	if err := s.prepareDecoy(cost); err != nil {
		return err
	}
	s.decoyError = nil
	samples := make([]time.Duration, 3)
	for i := range samples {
		start := time.Now()
		s.checkDecoy("")
		samples[i] = time.Since(start)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	s.loginFloor = max(200*time.Millisecond, min(3*time.Second, 2*samples[1]))
	slog.Info("auth: piso de tempo do login calibrado", "piso", s.loginFloor, "custo_bcrypt", cost)
	return nil
}

func waitLoginFloor(ctx context.Context, deadline time.Time) error {
	delay := time.Until(deadline)
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) rehashPassword(ctx context.Context, user domain.User, password string) {
	expected := user.PasswordHash
	if err := user.SetPassword(password); err != nil {
		slog.Warn("auth: não foi possível atualizar o custo da senha", "conta_id", user.ID)
		return
	}
	// A escrita condicional não desfaz uma troca deliberada de senha
	// ocorrida enquanto o novo hash era calculado.
	if err := s.store.Users().SetPasswordHashIf(ctx, user.ID, expected, user.PasswordHash); err != nil {
		slog.Warn("auth: atualização do custo da senha não foi aplicada", "conta_id", user.ID)
	}
}
