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

// ---------- grupos ----------

type groupRepo struct{ db *db }

const groupColumns = `id, name, slug, parent_id, created_at, updated_at`

// Create grava o grupo, recusando um terceiro nível.
//
// A forma da árvore é conferida na API (domain.GroupTree), mas a árvore
// que ela leu pode mudar até aqui: o pai pode ganhar um pai no meio. Por
// isso a gravação trava o pai e leva a condição na própria instrução.
func (r *groupRepo) Create(ctx context.Context, g *domain.Group) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	g.CreatedAt, g.UpdatedAt = now, now

	return r.db.inTx(ctx, func(t *tx) error {
		if err := lockGroups(ctx, t, g.ParentID); err != nil {
			return err
		}
		id, err := t.insertID(ctx, `
			INSERT INTO monitor_group (name, slug, parent_id, created_at, updated_at)
			SELECT ?, ?, ?, ?, ?
			WHERE `+parentIsTopLevel,
			g.Name, g.Slug, g.ParentID, toMillis(now), toMillis(now), g.ParentID, g.ParentID, int64(0))
		if errors.Is(err, sql.ErrNoRows) {
			return errThirdLevel
		}
		if err != nil {
			return translateGroupErr(err)
		}
		g.ID = id
		return nil
	})
}

// parentIsTopLevel é a condição de dois níveis do lado do pai: sem pai,
// ou com um pai que existe, é de topo e não é o próprio grupo. Recebe o
// pai duas vezes e o id do grupo (zero na criação).
const parentIsTopLevel = `(CAST(? AS BIGINT) IS NULL OR EXISTS (
	SELECT 1 FROM monitor_group p WHERE p.id = ? AND p.parent_id IS NULL AND p.id <> ?))`

// errThirdLevel é a recusa do store a um terceiro nível.
var errThirdLevel = fmt.Errorf("grupos têm no máximo dois níveis: %w", store.ErrConflict)

// lockGroups trava os grupos indicados até o fim da transação, com um
// UPDATE que não muda nada — vale nos dois bancos, como lockFullAdmins.
// Quem reorganiza a mesma parte da árvore ao mesmo tempo espera, e relê
// já com a mudança de quem chegou antes; sem isso, no PostgreSQL, criar
// um ambiente em P e pôr P dentro de outro grupo passavam os dois.
//
// Grupo que não existe é ErrNotFound: o pai sumiu, ou nunca existiu.
func lockGroups(ctx context.Context, t *tx, ids ...*int64) error {
	for _, id := range ids {
		if id == nil {
			continue
		}
		res, err := t.ExecContext(ctx, `UPDATE monitor_group SET updated_at = updated_at WHERE id = ?`, *id)
		if err != nil {
			return fmt.Errorf("sqlstore: travando grupo %d: %w", *id, err)
		}
		if err := requireAffected(res, fmt.Sprintf("grupo %d", *id)); err != nil {
			return err
		}
	}
	return nil
}

// translateGroupErr traduz unicidade em ErrConflict e a chave estrangeira
// do pai em ErrNotFound.
func translateGroupErr(err error) error {
	if isForeignKeyViolation(err) {
		return fmt.Errorf("grupo pai: %w", store.ErrNotFound)
	}
	return translateAuthErr(err, "grupo")
}

func (r *groupRepo) Get(ctx context.Context, id int64) (domain.Group, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+groupColumns+` FROM monitor_group WHERE id = ?`, id)

	g, err := scanGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Group{}, fmt.Errorf("grupo %d: %w", id, store.ErrNotFound)
	}
	return g, err
}

// Update grava o grupo, recusando um terceiro nível: o pai precisa ser de
// topo, e o grupo que ganha pai não pode ter ambientes.
func (r *groupRepo) Update(ctx context.Context, g domain.Group) error {
	now := time.Now().UTC().Truncate(time.Millisecond)

	return r.db.inTx(ctx, func(t *tx) error {
		if err := lockGroups(ctx, t, &g.ID, g.ParentID); err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `
			UPDATE monitor_group SET name = ?, slug = ?, parent_id = ?, updated_at = ?
			WHERE id = ? AND `+parentIsTopLevel+`
			  AND (CAST(? AS BIGINT) IS NULL OR NOT EXISTS (
				SELECT 1 FROM monitor_group filho WHERE filho.parent_id = ?))`,
			g.Name, g.Slug, g.ParentID, toMillis(now), g.ID,
			g.ParentID, g.ParentID, g.ID, g.ParentID, g.ID)
		if err != nil {
			return translateGroupErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("sqlstore: contando linhas afetadas: %w", err)
		}
		if n == 0 {
			// O grupo e o pai existem (a trava acima conferiu): o que
			// barrou foi a forma.
			return errThirdLevel
		}
		return nil
	})
}

// Delete apaga o grupo só se ele estiver vazio.
//
// A condição vai na própria instrução, e não numa consulta anterior: entre
// "conferi que está vazio" e "apaguei" outro pedido poderia cadastrar um
// monitor no grupo, que ficaria apontando para nada.
func (r *groupRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM monitor_group
		WHERE id = ?
		  AND NOT EXISTS (SELECT 1 FROM monitor WHERE group_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM monitor_group filho WHERE filho.parent_id = ?)`,
		id, id, id)
	if isForeignKeyViolation(err) {
		// Só no PostgreSQL: um monitor entrou no grupo numa transação que
		// a condição acima não enxergava, e a chave estrangeira recusou.
		return fmt.Errorf("grupo %d ganhou um monitor no meio: %w", id, store.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("sqlstore: apagando grupo %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlstore: contando linhas afetadas: %w", err)
	}
	if n > 0 {
		return nil
	}

	// Nada saiu: ou não existe, ou está em uso. A distinção importa para
	// quem chamou saber se deve mover os monitores antes.
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("grupo %d tem monitores ou ambientes: %w", id, store.ErrConflict)
}

func (r *groupRepo) List(ctx context.Context) ([]domain.Group, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+groupColumns+` FROM monitor_group ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando grupos: %w", err)
	}
	defer rows.Close()

	var out []domain.Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo grupos: %w", err)
	}
	return out, nil
}

func scanGroup(sc scanner) (domain.Group, error) {
	var (
		g         domain.Group
		parentID  sql.NullInt64
		createdMS int64
		updatedMS int64
	)
	if err := sc.Scan(&g.ID, &g.Name, &g.Slug, &parentID, &createdMS, &updatedMS); err != nil {
		return domain.Group{}, err
	}
	if parentID.Valid {
		p := parentID.Int64
		g.ParentID = &p
	}
	g.CreatedAt = fromMillis(createdMS)
	g.UpdatedAt = fromMillis(updatedMS)
	return g, nil
}

// groupCondition traduz a seleção de grupos para SQL sobre a coluna dada.
//
// Seleção vazia vira uma condição falsa, e não condição nenhuma: uma conta
// com escopo esvaziado não pode enxergar a instalação inteira por cair no
// caso "sem filtro".
func groupCondition(column string, sel *store.GroupSelection) (string, []any) {
	if sel == nil {
		return "", nil
	}

	var (
		parts []string
		args  []any
	)
	if len(sel.IDs) > 0 {
		marks := make([]string, len(sel.IDs))
		for i, id := range sel.IDs {
			marks[i] = "?"
			args = append(args, id)
		}
		parts = append(parts, column+" IN ("+strings.Join(marks, ", ")+")")
	}
	if sel.Ungrouped {
		parts = append(parts, column+" IS NULL")
	}
	if len(parts) == 0 {
		return "1 = 0", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// ---------- canais por grupo ----------

func (r *channelRepo) LinkGroup(ctx context.Context, groupID, channelID int64) error {
	const q = `
		INSERT INTO group_channel (group_id, channel_id) VALUES (?, ?)
		ON CONFLICT (group_id, channel_id) DO NOTHING`

	if _, err := r.db.ExecContext(ctx, q, groupID, channelID); err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("canal %d ou grupo %d: %w", channelID, groupID, store.ErrNotFound)
		}
		return fmt.Errorf("sqlstore: vinculando canal %d ao grupo %d: %w", channelID, groupID, err)
	}
	return nil
}

func (r *channelRepo) UnlinkGroup(ctx context.Context, groupID, channelID int64) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM group_channel WHERE group_id = ? AND channel_id = ?`, groupID, channelID)
	if err != nil {
		return fmt.Errorf("sqlstore: desvinculando canal %d do grupo %d: %w", channelID, groupID, err)
	}
	return nil
}

// ListFor devolve os canais vinculados a monitores dos grupos selecionados
// ou aos próprios grupos.
func (r *channelRepo) ListFor(ctx context.Context, sel store.GroupSelection) ([]domain.Channel, error) {
	doMonitor, argsMonitor := groupCondition("m.group_id", &sel)
	doGrupo, argsGrupo := groupCondition("gc.group_id", &sel)

	rows, err := r.db.QueryContext(ctx, `
		SELECT `+channelColumns+`
		FROM notification_channel
		WHERE id IN (
			SELECT mc.channel_id FROM monitor_channel mc
			JOIN monitor m ON m.id = mc.monitor_id
			WHERE `+doMonitor+`
			UNION
			SELECT gc.channel_id FROM group_channel gc
			WHERE `+doGrupo+`
		)
		ORDER BY id`, append(argsMonitor, argsGrupo...)...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando canais do escopo: %w", err)
	}
	defer rows.Close()

	return r.db.collectChannels(rows)
}

// ForGroup devolve os canais habilitados vinculados direto ao grupo.
func (r *channelRepo) ForGroup(ctx context.Context, groupID int64) ([]domain.Channel, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.type, c.config, c.enabled, c.created_at, c.updated_at
		FROM notification_channel c
		JOIN group_channel gc ON gc.channel_id = c.id
		WHERE gc.group_id = ? AND c.enabled = 1
		ORDER BY c.id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando canais do grupo %d: %w", groupID, err)
	}
	defer rows.Close()

	return r.db.collectChannels(rows)
}

// Destinations reúne os canais do monitor, do grupo dele e do sistema do
// grupo.
//
// O vínculo é resolvido na hora do aviso, e não copiado para o monitor no
// cadastro: é o que faz um canal vinculado ao grupo valer para monitores
// criados depois.
func (r *channelRepo) Destinations(ctx context.Context, monitorID int64) ([]domain.Channel, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.type, c.config, c.enabled, c.created_at, c.updated_at
		FROM notification_channel c
		WHERE c.enabled = 1 AND c.id IN (
			SELECT mc.channel_id FROM monitor_channel mc WHERE mc.monitor_id = ?
			UNION
			SELECT gc.channel_id
			FROM group_channel gc
			JOIN monitor m ON m.id = ?
			LEFT JOIN monitor_group g ON g.id = m.group_id
			WHERE gc.group_id = m.group_id OR gc.group_id = g.parent_id
		)
		ORDER BY c.id`, monitorID, monitorID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando destinos do monitor %d: %w", monitorID, err)
	}
	defer rows.Close()

	return r.db.collectChannels(rows)
}

// ---------- mudanças de estado ----------

type transitionRepo struct{ db *db }

func (r *transitionRepo) Record(ctx context.Context, t *domain.Transition) error {
	id, err := r.db.insertID(ctx, `
		INSERT INTO state_transition (monitor_id, from_status, to_status, at, cause)
		VALUES (?, ?, ?, ?, ?)`,
		t.MonitorID, t.From.String(), t.To.String(), toMillis(t.At), t.Cause)
	if err != nil {
		return fmt.Errorf("sqlstore: registrando mudança do monitor %d: %w", t.MonitorID, err)
	}
	t.ID = id
	return nil
}

func (r *transitionRepo) List(ctx context.Context, f store.TransitionFilter) ([]domain.Transition, error) {
	limit := store.PageFilter{Limit: f.Limit}.Normalize().Limit

	q := `SELECT id, monitor_id, from_status, to_status, at, cause
		FROM state_transition WHERE monitor_id = ?`
	args := []any{f.MonitorID}
	switch {
	case !f.Before.IsZero() && f.BeforeID > 0:
		q += ` AND (at < ? OR (at = ? AND id < ?))`
		args = append(args, toMillis(f.Before), toMillis(f.Before), f.BeforeID)
	case !f.Before.IsZero():
		q += ` AND at < ?`
		args = append(args, toMillis(f.Before))
	}
	q += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando mudanças do monitor %d: %w", f.MonitorID, err)
	}
	defer rows.Close()

	var out []domain.Transition
	for rows.Next() {
		var (
			t        domain.Transition
			from, to string
			atMS     int64
		)
		if err := rows.Scan(&t.ID, &t.MonitorID, &from, &to, &atMS, &t.Cause); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo mudança: %w", err)
		}
		if t.From, err = domain.ParseStatus(from); err != nil {
			return nil, fmt.Errorf("sqlstore: mudança %d: %w", t.ID, err)
		}
		if t.To, err = domain.ParseStatus(to); err != nil {
			return nil, fmt.Errorf("sqlstore: mudança %d: %w", t.ID, err)
		}
		t.At = fromMillis(atMS)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo mudanças: %w", err)
	}
	return out, nil
}
