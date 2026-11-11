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

// O segundo fator vem numa subconsulta, e não numa coluna da conta: quem
// o liga e desliga é o fluxo próprio dele (ver twofactor.go), e uma
// coluna aqui seria mais um campo que a gravação da conta poderia
// regravar com um valor velho.
const userColumns = `id, username, role, password_hash, created_at, updated_at, all_groups,
	(SELECT COUNT(*) FROM user_totp t WHERE t.user_id = app_user.id AND t.enabled = 1)`

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
	return r.db.inTx(ctx, fn)
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

// SetPasswordHash grava só a coluna da senha.
func (r *userRepo) SetPasswordHash(ctx context.Context, id int64, hash string) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	res, err := r.db.ExecContext(ctx,
		`UPDATE app_user SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, toMillis(now), id)
	if err != nil {
		return fmt.Errorf("sqlstore: gravando senha da conta %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("usuário %d", id))
}

// SetPasswordHashIf preserva alterações concorrentes e o carimbo da conta.
func (r *userRepo) SetPasswordHashIf(ctx context.Context, id int64, expected, next string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE app_user SET password_hash = ? WHERE id = ? AND password_hash = ?`, next, id, expected)
	if err != nil {
		return fmt.Errorf("sqlstore: regravando senha da conta %d: %w", id, err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlstore: conferindo regravação da conta %d: %w", id, err)
	}
	if count == 0 {
		return fmt.Errorf("senha da conta %d mudou: %w", id, store.ErrConflict)
	}
	return nil
}

// SetAccess grava papel e, quando informado, escopo, sem tocar na senha.
func (r *userRepo) SetAccess(ctx context.Context, id int64, role domain.Role, scope *domain.Scope) error {
	now := time.Now().UTC().Truncate(time.Millisecond)

	return r.inTx(ctx, func(t *tx) error {
		antes, err := lockFullAdmins(ctx, t)
		if err != nil {
			return err
		}

		var res sql.Result
		if scope == nil {
			res, err = t.ExecContext(ctx,
				`UPDATE app_user SET role = ?, updated_at = ? WHERE id = ?`, role.String(), toMillis(now), id)
		} else {
			res, err = t.ExecContext(ctx,
				`UPDATE app_user SET role = ?, all_groups = ?, updated_at = ? WHERE id = ?`,
				role.String(), boolToInt(!scope.Normalize().Restricted), toMillis(now), id)
		}
		if err != nil {
			return fmt.Errorf("sqlstore: gravando acesso da conta %d: %w", id, err)
		}
		if err := requireAffected(res, fmt.Sprintf("usuário %d", id)); err != nil {
			return err
		}
		if scope != nil {
			if err := writeUserGroups(ctx, t, id, scope.Normalize()); err != nil {
				return err
			}
		}
		return keepFullAdmin(ctx, t, antes)
	})
}

// lockFullAdmins trava as contas que administram tudo até o fim da
// transação e devolve quantas são.
//
// Um UPDATE que não muda nada, e não SELECT ... FOR UPDATE, porque vale
// igual nos dois bancos: no PostgreSQL trava as linhas, e quem chega
// depois espera e relê já com a mudança de quem chegou antes; no SQLite
// pega a trava de escrita, que serializa as transações inteiras. Sem a
// trava, no PostgreSQL dois rebaixamentos simultâneos contariam cada um
// o outro ainda como administrador, e os dois gravariam.
func lockFullAdmins(ctx context.Context, t *tx) (int64, error) {
	res, err := t.ExecContext(ctx,
		`UPDATE app_user SET updated_at = updated_at WHERE role = ? AND all_groups = 1`,
		domain.RoleAdmin.String())
	if err != nil {
		return 0, fmt.Errorf("sqlstore: travando administradores: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: contando administradores: %w", err)
	}
	return n, nil
}

// keepFullAdmin recusa a escrita que levou de algum para nenhum
// administrador de escopo total.
//
// "De algum para nenhum", e não "nenhum": antes do primeiro acesso não há
// administrador, e mexer em outras contas não pode depender disso.
func keepFullAdmin(ctx context.Context, t *tx, antes int64) error {
	if antes == 0 {
		return nil
	}
	var depois int64
	err := t.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM app_user WHERE role = ? AND all_groups = 1`,
		domain.RoleAdmin.String()).Scan(&depois)
	if err != nil {
		return fmt.Errorf("sqlstore: contando administradores: %w", err)
	}
	if depois == 0 {
		return store.ErrLastFullAdmin
	}
	return nil
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
		totp      int64
	)
	if err := sc.Scan(&u.ID, &u.Username, &roleName, &u.PasswordHash, &createdMS, &updatedMS, &allGroups, &totp); err != nil {
		return domain.User{}, err
	}
	u.TOTPEnabled = totp > 0
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
	return r.inTx(ctx, func(t *tx) error {
		antes, err := lockFullAdmins(ctx, t)
		if err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `DELETE FROM app_user WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("sqlstore: apagando conta %d: %w", id, err)
		}
		if err := requireAffected(res, fmt.Sprintf("conta %d", id)); err != nil {
			return err
		}
		return keepFullAdmin(ctx, t, antes)
	})
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

// DeleteOthers encerra as sessões da conta, menos a indicada.
func (r *sessionRepo) DeleteOthers(ctx context.Context, userID int64, keep string) error {
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM session WHERE user_id = ? AND token_hash <> ?`, userID, keep); err != nil {
		return fmt.Errorf("sqlstore: apagando as outras sessões do usuário %d: %w", userID, err)
	}
	return nil
}

// ---------- tokens ----------

type tokenRepo struct{ db *db }

const tokenColumns = `id, user_id, name, token_hash, prefix, created_at, last_used_at, expires_at, rotated_at,
	revoked_at, revoked_reason, revoked_by`

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

// GetByHash é o caminho da autenticação: só token ativo. Um revogado tem a
// linha retida, mas não pode voltar a autenticar.
func (r *tokenRepo) GetByHash(ctx context.Context, hash string) (domain.APIToken, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+tokenColumns+` FROM api_token WHERE token_hash = ? AND revoked_at IS NULL`, hash)

	t, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.APIToken{}, fmt.Errorf("token: %w", store.ErrNotFound)
	}
	return t, err
}

// Get devolve o token pelo id, revogado ou não: é o registro retido.
func (r *tokenRepo) Get(ctx context.Context, id int64) (domain.APIToken, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM api_token WHERE id = ?`, id)

	t, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.APIToken{}, fmt.Errorf("token %d: %w", id, store.ErrNotFound)
	}
	return t, err
}

// List devolve os tokens ativos da conta: os revogados não entram na tela de
// gerência, embora o registro deles sobreviva.
func (r *tokenRepo) List(ctx context.Context, userID int64) ([]domain.APIToken, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+tokenColumns+` FROM api_token WHERE user_id = ? AND revoked_at IS NULL ORDER BY id`, userID)
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

// Revoke marca o token como revogado em vez de apagá-lo: a linha fica, com
// quando, por que e por quem. A condição revoked_at IS NULL torna a segunda
// revogação do mesmo token um ErrNotFound, e não uma sobrescrita silenciosa
// do ator e do motivo originais.
func (r *tokenRepo) Revoke(ctx context.Context, id int64, reason string, actor *int64) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	res, err := r.db.ExecContext(ctx,
		`UPDATE api_token SET revoked_at = ?, revoked_reason = ?, revoked_by = ?
		 WHERE id = ? AND revoked_at IS NULL`,
		toMillis(now), reason, idPtr(actor), id)
	if err != nil {
		return fmt.Errorf("sqlstore: revogando token %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("token %d", id))
}

// RevokeAllByUser revoga os tokens ativos da conta e devolve quantos. Sem
// requireAffected: conta que nunca emitiu token chama isto do mesmo jeito,
// na troca de senha, e zero é o resultado certo.
func (r *tokenRepo) RevokeAllByUser(ctx context.Context, userID int64, reason string, actor *int64) (int64, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	res, err := r.db.ExecContext(ctx,
		`UPDATE api_token SET revoked_at = ?, revoked_reason = ?, revoked_by = ?
		 WHERE user_id = ? AND revoked_at IS NULL`,
		toMillis(now), reason, idPtr(actor), userID)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: revogando tokens do usuário %d: %w", userID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: contando tokens revogados do usuário %d: %w", userID, err)
	}
	return n, nil
}

// RevokeOthers revoga os tokens ativos da conta menos o de keep.
func (r *tokenRepo) RevokeOthers(ctx context.Context, userID int64, keep string, reason string, actor *int64) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := r.db.ExecContext(ctx,
		`UPDATE api_token SET revoked_at = ?, revoked_reason = ?, revoked_by = ?
		 WHERE user_id = ? AND token_hash <> ? AND revoked_at IS NULL`,
		toMillis(now), reason, idPtr(actor), userID, keep); err != nil {
		return fmt.Errorf("sqlstore: revogando os outros tokens do usuário %d: %w", userID, err)
	}
	return nil
}

// Rotate marca o antigo como rotacionado e insere o substituto na mesma
// transação: ou os dois existem na sobreposição, ou nada muda. O antigo não
// é revogado — segue válido até o dono revogá-lo.
func (r *tokenRepo) Rotate(ctx context.Context, oldID int64, replacement *domain.APIToken) error {
	now := time.Now().UTC().Truncate(time.Millisecond)

	return r.db.inTx(ctx, func(t *tx) error {
		res, err := t.ExecContext(ctx,
			`UPDATE api_token SET rotated_at = ? WHERE id = ?`, toMillis(now), oldID)
		if err != nil {
			return fmt.Errorf("sqlstore: marcando o token %d como rotacionado: %w", oldID, err)
		}
		if err := requireAffected(res, fmt.Sprintf("token %d", oldID)); err != nil {
			return err
		}

		replacement.CreatedAt = now
		id, err := t.insertID(ctx, `
			INSERT INTO api_token (user_id, name, token_hash, prefix, created_at, expires_at, predecessor_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			replacement.UserID, replacement.Name, replacement.Hash, replacement.Prefix,
			toMillis(now), millisPtr(replacement.ExpiresAt), oldID)
		if err != nil {
			return translateAuthErr(err, "token")
		}
		replacement.ID = id
		return nil
	})
}

// SuccessorUsed lê o last_used_at do substituto (predecessor_id = tokenID).
// Sem substituto, sql.ErrNoRows, que é "não há reuso a detectar".
func (r *tokenRepo) SuccessorUsed(ctx context.Context, tokenID int64) (bool, error) {
	var lastUsed sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT last_used_at FROM api_token WHERE predecessor_id = ?`, tokenID).Scan(&lastUsed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("sqlstore: consultando o sucessor do token %d: %w", tokenID, err)
	}
	return lastUsed.Valid, nil
}

// SetExpiry grava só a coluna de expiração e rearma os avisos de
// vencimento. Sem tocar em created_at nem em last_used_at: editar a validade
// não pode reescrever quando o token nasceu ou foi usado por último.
//
// Zera expiry_warned_days porque o prazo mudou: esticar a validade recoloca
// os marcos de 30 e 7 dias à frente, e um marco já avisado para o prazo
// antigo calaria o aviso do novo. Encurtar também zera, de propósito — o
// verificador reavalia os marcos contra a data nova.
func (r *tokenRepo) SetExpiry(ctx context.Context, id int64, expiresAt *time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE api_token SET expires_at = ?, expiry_warned_days = NULL WHERE id = ?`,
		millisPtr(expiresAt), id)
	if err != nil {
		return fmt.Errorf("sqlstore: editando a validade do token %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("token %d", id))
}

// ListExpiring lê os tokens ativos com prazo até notAfter, com o dono e o
// marco de aviso já enviado. Junta app_user para o aviso dizer de quem é o
// token; não traz os revogados nem os sem prazo, e deixa o descarte dos já
// vencidos para quem decide o marco (domain.DueTokenExpiryMilestone).
func (r *tokenRepo) ListExpiring(ctx context.Context, notAfter time.Time) ([]store.ExpiringToken, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id, t.user_id, t.name, t.prefix, t.expires_at, u.username, t.expiry_warned_days
		FROM api_token t
		JOIN app_user u ON u.id = t.user_id
		WHERE t.revoked_at IS NULL AND t.expires_at IS NOT NULL AND t.expires_at <= ?
		ORDER BY t.expires_at, t.id`, toMillis(notAfter))
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando tokens a vencer: %w", err)
	}
	defer rows.Close()

	var out []store.ExpiringToken
	for rows.Next() {
		var (
			et        store.ExpiringToken
			expiresMS int64
			warned    sql.NullInt64
		)
		if err := rows.Scan(&et.TokenID, &et.UserID, &et.Name, &et.Prefix,
			&expiresMS, &et.Username, &warned); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo token a vencer: %w", err)
		}
		et.ExpiresAt = fromMillis(expiresMS)
		if warned.Valid {
			d := int(warned.Int64)
			et.WarnedDays = &d
		}
		out = append(out, et)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo tokens a vencer: %w", err)
	}
	return out, nil
}

// MarkExpiryWarned grava o marco de aviso mais fino já enviado, e só quando
// avança rumo ao vencimento: a condição vai na própria escrita para dois
// ticks não repetirem o aviso, e para um marco mais distante não sobrescrever
// um mais próximo já avisado. Token inexistente não grava e não é erro —
// pode ter sido revogado ou apagado entre a leitura e a marca.
func (r *tokenRepo) MarkExpiryWarned(ctx context.Context, id int64, days int) (bool, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE api_token SET expiry_warned_days = ?
		 WHERE id = ? AND (expiry_warned_days IS NULL OR expiry_warned_days > ?)`,
		days, id, days)
	if err != nil {
		return false, fmt.Errorf("sqlstore: marcando aviso de expiração do token %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("sqlstore: conferindo marca de aviso do token %d: %w", id, err)
	}
	return n > 0, nil
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
		rotatedMS  sql.NullInt64
		revokedMS  sql.NullInt64
		revReason  sql.NullString
		revBy      sql.NullInt64
	)
	err := sc.Scan(&t.ID, &t.UserID, &t.Name, &t.Hash, &t.Prefix,
		&createdMS, &lastUsedMS, &expiresMS, &rotatedMS, &revokedMS, &revReason, &revBy)
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
	if rotatedMS.Valid {
		at := fromMillis(rotatedMS.Int64)
		t.RotatedAt = &at
	}
	if revokedMS.Valid {
		at := fromMillis(revokedMS.Int64)
		t.RevokedAt = &at
	}
	t.RevokedReason = revReason.String
	if revBy.Valid {
		id := revBy.Int64
		t.RevokedBy = &id
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

// idPtr desembrulha um *int64 para o driver: nil vira NULL.
func idPtr(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
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
