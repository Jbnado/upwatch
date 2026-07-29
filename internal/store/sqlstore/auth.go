package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// ---------- usuários ----------

type userRepo struct{ db *sql.DB }

const userColumns = `id, username, password_hash, created_at, updated_at`

func (r *userRepo) Create(ctx context.Context, u *domain.User) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	u.CreatedAt, u.UpdatedAt = now, now

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO app_user (username, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?)`,
		u.Username, u.PasswordHash, toMillis(now), toMillis(now))
	if err != nil {
		return translateAuthErr(err, "usuário")
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("sqlstore: lendo id gerado: %w", err)
	}
	u.ID = id
	return nil
}

func (r *userRepo) Get(ctx context.Context, id int64) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = ?`, id)

	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, fmt.Errorf("usuário %d: %w", id, store.ErrNotFound)
	}
	return u, err
}

func (r *userRepo) GetByUsername(ctx context.Context, username string) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM app_user WHERE username = ?`, username)

	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, fmt.Errorf("usuário %q: %w", username, store.ErrNotFound)
	}
	return u, err
}

func (r *userRepo) Update(ctx context.Context, u domain.User) error {
	now := time.Now().UTC().Truncate(time.Millisecond)

	res, err := r.db.ExecContext(ctx, `
		UPDATE app_user SET username = ?, password_hash = ?, updated_at = ? WHERE id = ?`,
		u.Username, u.PasswordHash, toMillis(now), u.ID)
	if err != nil {
		return translateAuthErr(err, "usuário")
	}
	return requireAffected(res, fmt.Sprintf("usuário %d", u.ID))
}

func (r *userRepo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_user`).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlstore: contando usuários: %w", err)
	}
	return n, nil
}

func scanUser(sc scanner) (domain.User, error) {
	var (
		u         domain.User
		createdMS int64
		updatedMS int64
	)
	if err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &createdMS, &updatedMS); err != nil {
		return domain.User{}, err
	}
	u.CreatedAt = fromMillis(createdMS)
	u.UpdatedAt = fromMillis(updatedMS)
	return u, nil
}

// ---------- sessões ----------

type sessionRepo struct{ db *sql.DB }

func (r *sessionRepo) Create(ctx context.Context, s domain.Session) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO session (token_hash, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		s.Hash, s.UserID, toMillis(s.CreatedAt), toMillis(s.ExpiresAt))
	if err != nil {
		return translateAuthErr(err, "sessão")
	}
	return nil
}

func (r *sessionRepo) Get(ctx context.Context, hash string) (domain.Session, error) {
	var (
		s         domain.Session
		createdMS int64
		expiresMS int64
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT token_hash, user_id, created_at, expires_at FROM session WHERE token_hash = ?`,
		hash).Scan(&s.Hash, &s.UserID, &createdMS, &expiresMS)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.Session{}, fmt.Errorf("sessão: %w", store.ErrNotFound)
	case err != nil:
		return domain.Session{}, fmt.Errorf("sqlstore: lendo sessão: %w", err)
	}

	s.CreatedAt = fromMillis(createdMS)
	s.ExpiresAt = fromMillis(expiresMS)
	return s, nil
}

func (r *sessionRepo) Delete(ctx context.Context, hash string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM session WHERE token_hash = ?`, hash); err != nil {
		return fmt.Errorf("sqlstore: apagando sessão: %w", err)
	}
	return nil
}

func (r *sessionRepo) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM session WHERE expires_at < ?`, toMillis(before))
	if err != nil {
		return 0, fmt.Errorf("sqlstore: limpando sessões expiradas: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: contando sessões removidas: %w", err)
	}
	return n, nil
}

// DeleteByUser encerra todas as sessões de uma conta.
//
// Chamado ao trocar a senha: uma sessão que sobrevive à troca anularia o
// próprio motivo de trocá-la.
func (r *sessionRepo) DeleteByUser(ctx context.Context, userID int64) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM session WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("sqlstore: apagando sessões do usuário %d: %w", userID, err)
	}
	return nil
}

// ---------- tokens ----------

type tokenRepo struct{ db *sql.DB }

const tokenColumns = `id, user_id, name, token_hash, prefix, created_at, last_used_at, expires_at`

func (r *tokenRepo) Create(ctx context.Context, t *domain.APIToken) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.CreatedAt = now

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO api_token (user_id, name, token_hash, prefix, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		t.UserID, t.Name, t.Hash, t.Prefix, toMillis(now), millisPtr(t.ExpiresAt))
	if err != nil {
		return translateAuthErr(err, "token")
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("sqlstore: lendo id gerado: %w", err)
	}
	t.ID = id
	return nil
}

func (r *tokenRepo) GetByHash(ctx context.Context, hash string) (domain.APIToken, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM api_token WHERE token_hash = ?`, hash)

	t, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.APIToken{}, fmt.Errorf("token: %w", store.ErrNotFound)
	}
	return t, err
}

func (r *tokenRepo) List(ctx context.Context, userID int64) ([]domain.APIToken, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM api_token WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando tokens: %w", err)
	}
	defer rows.Close()

	var out []domain.APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo tokens: %w", err)
	}
	return out, nil
}

func (r *tokenRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM api_token WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando token %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("token %d", id))
}

func (r *tokenRepo) TouchLastUsed(ctx context.Context, id int64, at time.Time) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE api_token SET last_used_at = ? WHERE id = ?`, toMillis(at), id); err != nil {
		return fmt.Errorf("sqlstore: registrando uso do token %d: %w", id, err)
	}
	return nil
}

func scanToken(sc scanner) (domain.APIToken, error) {
	var (
		t          domain.APIToken
		createdMS  int64
		lastUsedMS sql.NullInt64
		expiresMS  sql.NullInt64
	)
	err := sc.Scan(&t.ID, &t.UserID, &t.Name, &t.Hash, &t.Prefix,
		&createdMS, &lastUsedMS, &expiresMS)
	if err != nil {
		return domain.APIToken{}, err
	}

	t.CreatedAt = fromMillis(createdMS)
	if lastUsedMS.Valid {
		at := fromMillis(lastUsedMS.Int64)
		t.LastUsedAt = &at
	}
	if expiresMS.Valid {
		at := fromMillis(expiresMS.Int64)
		t.ExpiresAt = &at
	}
	return t, nil
}

// ---------- auxiliares ----------

func millisPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return toMillis(*t)
}

// translateAuthErr converte violação de unicidade em store.ErrConflict.
func translateAuthErr(err error, subject string) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key") {
		return fmt.Errorf("%w: %s já existe", store.ErrConflict, subject)
	}
	return fmt.Errorf("sqlstore: gravando %s: %w", subject, err)
}
