package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// ---------- usuários ----------

type userRepo struct{ db *db }

const userColumns = `id, username, role, password_hash, created_at, updated_at, all_groups`

// Create grava a conta e o escopo numa transação: uma conta gravada sem
// as concessões apareceria, por um instante, com o escopo zero — que é
// "todos os grupos".
func (r *userRepo) Create(ctx context.Context, u *domain.User) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	u.CreatedAt, u.UpdatedAt = now, now
	u.Scope = u.Scope.Normalize()

	return r.inTx(ctx, func(t *tx) error {
		id, err := t.insertID(ctx, `
			INSERT INTO app_user (username, role, password_hash, created_at, updated_at, all_groups)
			VALUES (?, ?, ?, ?, ?, ?)`,
			u.Username, u.Role.String(), u.PasswordHash, toMillis(now), toMillis(now),
			boolToInt(!u.Scope.Restricted))
		if err != nil {
			return translateAuthErr(err, "usuário")
		}
		if err := writeUserGroups(ctx, t, id, u.Scope); err != nil {
			return err
		}
		u.ID = id
		return nil
	})
}

// inTx roda fn numa transação da conexão do repositório.
func (r *userRepo) inTx(ctx context.Context, fn func(*tx) error) error {
	t, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: iniciando transação: %w", err)
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}

// writeUserGroups substitui as concessões da conta.
func writeUserGroups(ctx context.Context, t *tx, userID int64, s domain.Scope) error {
	if _, err := t.ExecContext(ctx, `DELETE FROM user_group WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("sqlstore: limpando escopo da conta %d: %w", userID, err)
	}
	if !s.Restricted {
		return nil
	}
	for _, g := range s.GroupIDs {
		if _, err := t.ExecContext(ctx,
			`INSERT INTO user_group (user_id, group_id) VALUES (?, ?)`, userID, g); err != nil {
			return fmt.Errorf("sqlstore: gravando escopo da conta %d: %w", userID, err)
		}
	}
	return nil
}

// loadScopes completa o escopo das contas restritas.
//
// Uma consulta para a lista inteira, e não uma por conta.
func (r *userRepo) loadScopes(ctx context.Context, users []domain.User) error {
	restritas := map[int64]int{}
	for i, u := range users {
		if u.Scope.Restricted {
			restritas[u.ID] = i
		}
	}
	if len(restritas) == 0 {
		return nil
	}

	rows, err := r.db.QueryContext(ctx, `SELECT user_id, group_id FROM user_group ORDER BY user_id, group_id`)
	if err != nil {
		return fmt.Errorf("sqlstore: lendo escopos: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var userID, groupID int64
		if err := rows.Scan(&userID, &groupID); err != nil {
			return fmt.Errorf("sqlstore: lendo escopo: %w", err)
		}
		if i, ok := restritas[userID]; ok {
			users[i].Scope.GroupIDs = append(users[i].Scope.GroupIDs, groupID)
		}
	}
	return rows.Err()
}

// loadScope completa o escopo de uma conta.
func (r *userRepo) loadScope(ctx context.Context, u domain.User, err error) (domain.User, error) {
	if err != nil || !u.Scope.Restricted {
		return u, err
	}
	contas := []domain.User{u}
	if err := r.loadScopes(ctx, contas); err != nil {
		return domain.User{}, err
	}
	return contas[0], nil
}

func (r *userRepo) Get(ctx context.Context, id int64) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = ?`, id)

	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, fmt.Errorf("usuário %d: %w", id, store.ErrNotFound)
	}
	return r.loadScope(ctx, u, err)
}

func (r *userRepo) GetByUsername(ctx context.Context, username string) (domain.User, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM app_user WHERE username = ?`, username)

	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, fmt.Errorf("usuário %q: %w", username, store.ErrNotFound)
	}
	return r.loadScope(ctx, u, err)
}

func (r *userRepo) Update(ctx context.Context, u domain.User) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	escopo := u.Scope.Normalize()

	return r.inTx(ctx, func(t *tx) error {
		res, err := t.ExecContext(ctx, `
			UPDATE app_user SET username = ?, role = ?, password_hash = ?, updated_at = ?, all_groups = ?
			WHERE id = ?`,
			u.Username, u.Role.String(), u.PasswordHash, toMillis(now),
			boolToInt(!escopo.Restricted), u.ID)
		if err != nil {
			return translateAuthErr(err, "usuário")
		}
		if err := requireAffected(res, fmt.Sprintf("usuário %d", u.ID)); err != nil {
			return err
		}
		return writeUserGroups(ctx, t, u.ID, escopo)
	})
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
		roleName  string
		createdMS int64
		updatedMS int64
		allGroups int64
	)
	if err := sc.Scan(&u.ID, &u.Username, &roleName, &u.PasswordHash, &createdMS, &updatedMS, &allGroups); err != nil {
		return domain.User{}, err
	}
	// Só o valor 1 alcança tudo. Qualquer outra coisa vinda do banco é
	// restrita: no caso em que não se sabe o que está gravado, negar é o
	// desfecho seguro.
	u.Scope.Restricted = allGroups != 1

	// Papel desconhecido falha alto em vez de virar admin por omissão:
	// no caso em que não se sabe o que está gravado, conceder acesso é o
	// pior desfecho possível.
	role, err := domain.ParseRole(roleName)
	if err != nil {
		return domain.User{}, fmt.Errorf("sqlstore: conta %d: %w", u.ID, err)
	}
	u.Role = role
	u.CreatedAt = fromMillis(createdMS)
	u.UpdatedAt = fromMillis(updatedMS)
	return u, nil
}

// List devolve as contas, da mais antiga para a mais nova.
func (r *userRepo) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userColumns+` FROM app_user ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando contas: %w", err)
	}
	defer rows.Close()

	var out []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Fecha antes da segunda consulta: com o pool pequeno do SQLite, manter
	// o cursor aberto enquanto se pede outra conexão pode esperar à toa.
	rows.Close()

	if err := r.loadScopes(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Delete remove a conta. As sessões saem em cascata pelo schema, então
// quem for removido perde o acesso na hora, e não no vencimento do
// cookie.
func (r *userRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM app_user WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando conta %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("conta %d", id))
}

// CountByRole conta as contas de um papel.
func (r *userRepo) CountByRole(ctx context.Context, role domain.Role) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM app_user WHERE role = ?`, role.String()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: contando contas do papel %s: %w", role, err)
	}
	return n, nil
}

// ---------- sessões ----------

type sessionRepo struct{ db *db }

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

type tokenRepo struct{ db *db }

const tokenColumns = `id, user_id, name, token_hash, prefix, created_at, last_used_at, expires_at`

func (r *tokenRepo) Create(ctx context.Context, t *domain.APIToken) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.CreatedAt = now

	id, err := r.db.insertID(ctx, `
		INSERT INTO api_token (user_id, name, token_hash, prefix, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		t.UserID, t.Name, t.Hash, t.Prefix, toMillis(now), millisPtr(t.ExpiresAt))
	if err != nil {
		return translateAuthErr(err, "token")
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
