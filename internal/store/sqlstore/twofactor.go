package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store"
)

// totpTable é onde o segredo mora, e entra no dado associado da cifra:
// copiado para outra conta, o valor não abre.
const totpTable = "user_totp"

func totpSlot(userID int64) secretbox.Slot {
	return secretbox.Slot{Table: totpTable, ID: userID, Field: "secret"}
}

type twoFactorRepo struct{ db *db }

func (r *twoFactorRepo) Get(ctx context.Context, userID int64) (domain.TOTP, error) {
	var (
		f         domain.TOTP
		sealed    string
		enabled   int64
		createdMS int64
		enabledMS sql.NullInt64
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT user_id, secret, enabled, last_step, created_at, enabled_at
		FROM user_totp WHERE user_id = ?`, userID).
		Scan(&f.UserID, &sealed, &enabled, &f.LastStep, &createdMS, &enabledMS)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.TOTP{}, fmt.Errorf("segundo fator do usuário %d: %w", userID, store.ErrNotFound)
	case err != nil:
		return domain.TOTP{}, fmt.Errorf("sqlstore: lendo segundo fator: %w", err)
	}

	if r.db.secrets == nil {
		return domain.TOTP{}, fmt.Errorf("sqlstore: %w", secretbox.ErrKeyMissing)
	}
	plain, err := r.db.secrets.Open(sealed, totpSlot(userID))
	if err != nil {
		return domain.TOTP{}, fmt.Errorf("sqlstore: segundo fator do usuário %d: %w", userID, err)
	}
	f.Secret = plain
	f.Enabled = enabled == 1
	f.CreatedAt = fromMillis(createdMS)
	if enabledMS.Valid {
		at := fromMillis(enabledMS.Int64)
		f.EnabledAt = &at
	}
	return f, nil
}

// SetPending troca o pendente pelo novo. Apagar só o pendente e inserir
// sem sobrescrever é o que deixa o ligado intacto, sem precisar ler antes
// para decidir.
func (r *twoFactorRepo) SetPending(ctx context.Context, userID int64, secret string, at time.Time) error {
	if r.db.secrets == nil {
		return fmt.Errorf("sqlstore: %w", secretbox.ErrKeyMissing)
	}
	sealed, err := r.db.secrets.Seal(secret, totpSlot(userID))
	if err != nil {
		return err
	}

	return r.db.inTx(ctx, func(t *tx) error {
		if _, err := t.ExecContext(ctx,
			`DELETE FROM user_totp WHERE user_id = ? AND enabled = 0`, userID); err != nil {
			return fmt.Errorf("sqlstore: descartando cadastro pendente: %w", err)
		}
		res, err := t.ExecContext(ctx, `
			INSERT INTO user_totp (user_id, secret, enabled, last_step, created_at)
			VALUES (?, ?, 0, 0, ?) ON CONFLICT (user_id) DO NOTHING`,
			userID, sealed, toMillis(at))
		if err != nil {
			return translateAuthErr(translateFK(err, userID), "segundo fator")
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("sqlstore: gravando segundo fator: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: a conta %d já tem o segundo fator ligado", store.ErrConflict, userID)
		}
		return nil
	})
}

func (r *twoFactorRepo) Enable(ctx context.Context, userID, step int64, recoveryHashes []string, at time.Time) error {
	return r.db.inTx(ctx, func(t *tx) error {
		res, err := t.ExecContext(ctx, `
			UPDATE user_totp SET enabled = 1, last_step = ?, enabled_at = ?
			WHERE user_id = ? AND enabled = 0`, step, toMillis(at), userID)
		if err != nil {
			return fmt.Errorf("sqlstore: ligando segundo fator: %w", err)
		}
		if err := requireAffected(res, fmt.Sprintf("cadastro pendente do usuário %d", userID)); err != nil {
			return err
		}
		return writeRecoveryCodes(ctx, t, userID, recoveryHashes)
	})
}

func (r *twoFactorRepo) AdvanceStep(ctx context.Context, userID, step int64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE user_totp SET last_step = ?
		WHERE user_id = ? AND enabled = 1 AND last_step < ?`, step, userID, step)
	if err != nil {
		return false, fmt.Errorf("sqlstore: gravando passo do segundo fator: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlstore: gravando passo do segundo fator: %w", err)
	}
	return n > 0, nil
}

func (r *twoFactorRepo) Disable(ctx context.Context, userID int64) error {
	return r.db.inTx(ctx, func(t *tx) error {
		if _, err := t.ExecContext(ctx,
			`DELETE FROM user_recovery_code WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("sqlstore: apagando códigos de recuperação: %w", err)
		}
		res, err := t.ExecContext(ctx, `DELETE FROM user_totp WHERE user_id = ?`, userID)
		if err != nil {
			return fmt.Errorf("sqlstore: desligando segundo fator: %w", err)
		}
		return requireAffected(res, fmt.Sprintf("segundo fator do usuário %d", userID))
	})
}

func (r *twoFactorRepo) ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	return r.db.inTx(ctx, func(t *tx) error {
		return writeRecoveryCodes(ctx, t, userID, hashes)
	})
}

// writeRecoveryCodes substitui os códigos. Os antigos saem todos, usados
// ou não: gerar códigos novos é justamente para os anteriores, talvez
// vistos por alguém, deixarem de valer.
func writeRecoveryCodes(ctx context.Context, t *tx, userID int64, hashes []string) error {
	if _, err := t.ExecContext(ctx, `DELETE FROM user_recovery_code WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("sqlstore: apagando códigos de recuperação: %w", err)
	}
	for _, h := range hashes {
		if _, err := t.ExecContext(ctx,
			`INSERT INTO user_recovery_code (user_id, code_hash) VALUES (?, ?)`, userID, h); err != nil {
			return translateAuthErr(translateFK(err, userID), "código de recuperação")
		}
	}
	return nil
}

func (r *twoFactorRepo) UseRecoveryCode(ctx context.Context, userID int64, hash string, at time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE user_recovery_code SET used_at = ?
		WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`, toMillis(at), userID, hash)
	if err != nil {
		return false, fmt.Errorf("sqlstore: gastando código de recuperação: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlstore: gastando código de recuperação: %w", err)
	}
	return n > 0, nil
}

func (r *twoFactorRepo) RecoveryCodesLeft(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_recovery_code WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: contando códigos de recuperação: %w", err)
	}
	return n, nil
}

// ---------- desafios de login ----------

func (r *twoFactorRepo) CreateChallenge(ctx context.Context, c domain.LoginChallenge) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO login_challenge (token_hash, user_id, attempts, created_at, expires_at)
		VALUES (?, ?, 0, ?, ?)`,
		c.Hash, c.UserID, toMillis(c.CreatedAt), toMillis(c.ExpiresAt))
	if err != nil {
		return translateAuthErr(translateFK(err, c.UserID), "desafio de login")
	}
	return nil
}

// AttemptChallenge conta a tentativa antes de devolver o desafio. A
// contagem vem primeiro, na mesma escrita que confere validade e limite:
// conferir o código antes de contar deixaria pedidos simultâneos testarem
// mais palpites que o limite.
func (r *twoFactorRepo) AttemptChallenge(ctx context.Context, hash string, now time.Time, max int) (domain.LoginChallenge, error) {
	var c domain.LoginChallenge
	err := r.db.inTx(ctx, func(t *tx) error {
		res, err := t.ExecContext(ctx, `
			UPDATE login_challenge SET attempts = attempts + 1
			WHERE token_hash = ? AND expires_at > ? AND attempts < ?`, hash, toMillis(now), max)
		if err != nil {
			return fmt.Errorf("sqlstore: contando tentativa do desafio: %w", err)
		}
		if err := requireAffected(res, "desafio de login"); err != nil {
			return err
		}

		var createdMS, expiresMS int64
		err = t.QueryRowContext(ctx, `
			SELECT token_hash, user_id, attempts, created_at, expires_at
			FROM login_challenge WHERE token_hash = ?`, hash).
			Scan(&c.Hash, &c.UserID, &c.Attempts, &createdMS, &expiresMS)
		if err != nil {
			return fmt.Errorf("sqlstore: lendo desafio de login: %w", err)
		}
		c.CreatedAt = fromMillis(createdMS)
		c.ExpiresAt = fromMillis(expiresMS)
		return nil
	})
	if err != nil {
		return domain.LoginChallenge{}, err
	}
	return c, nil
}

func (r *twoFactorRepo) ConsumeChallenge(ctx context.Context, hash string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM login_challenge WHERE token_hash = ?`, hash)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando desafio de login: %w", err)
	}
	return requireAffected(res, "desafio de login")
}

func (r *twoFactorRepo) DeleteExpiredChallenges(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM login_challenge WHERE expires_at <= ?`, toMillis(before))
	if err != nil {
		return 0, fmt.Errorf("sqlstore: limpando desafios vencidos: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: contando desafios removidos: %w", err)
	}
	return n, nil
}

// translateFK converte a conta inexistente em ErrNotFound.
func translateFK(err error, userID int64) error {
	if isForeignKeyViolation(err) {
		return fmt.Errorf("usuário %d: %w", userID, store.ErrNotFound)
	}
	return err
}
