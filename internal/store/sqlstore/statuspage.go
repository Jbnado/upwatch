package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// ---------- páginas públicas ----------

type statusPageRepo struct{ db *sql.DB }

const statusPageColumns = `id, slug, title, description, show_latency, time_zone, enabled, created_at, updated_at`

func (r *statusPageRepo) Create(ctx context.Context, p *domain.StatusPage) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	p.CreatedAt, p.UpdatedAt = now, now

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO status_page (slug, title, description, show_latency, time_zone, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Slug, p.Title, p.Description, boolToInt(p.ShowLatency), p.TimeZone,
		boolToInt(p.Enabled), toMillis(now), toMillis(now))
	if err != nil {
		// Slug repetido vira conflito: duas páginas no mesmo endereço
		// fariam a resposta depender da ordem da varredura.
		return translateAuthErr(err, "página de estado")
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("sqlstore: lendo id gerado: %w", err)
	}
	p.ID = id
	return nil
}

func (r *statusPageRepo) Get(ctx context.Context, id int64) (domain.StatusPage, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+statusPageColumns+` FROM status_page WHERE id = ?`, id)

	p, err := scanStatusPage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.StatusPage{}, fmt.Errorf("página de estado %d: %w", id, store.ErrNotFound)
	}
	return p, err
}

func (r *statusPageRepo) GetBySlug(ctx context.Context, slug string) (domain.StatusPage, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+statusPageColumns+` FROM status_page WHERE slug = ?`, slug)

	p, err := scanStatusPage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.StatusPage{}, fmt.Errorf("página de estado %q: %w", slug, store.ErrNotFound)
	}
	return p, err
}

func (r *statusPageRepo) Update(ctx context.Context, p domain.StatusPage) error {
	now := time.Now().UTC().Truncate(time.Millisecond)

	res, err := r.db.ExecContext(ctx, `
		UPDATE status_page
		SET slug = ?, title = ?, description = ?, show_latency = ?, time_zone = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		p.Slug, p.Title, p.Description, boolToInt(p.ShowLatency), p.TimeZone,
		boolToInt(p.Enabled), toMillis(now), p.ID)
	if err != nil {
		return translateAuthErr(err, "página de estado")
	}
	return requireAffected(res, fmt.Sprintf("página de estado %d", p.ID))
}

func (r *statusPageRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM status_page WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando página de estado %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("página de estado %d", id))
}

func (r *statusPageRepo) List(ctx context.Context) ([]domain.StatusPage, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+statusPageColumns+` FROM status_page ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando páginas de estado: %w", err)
	}
	defer rows.Close()

	var out []domain.StatusPage
	for rows.Next() {
		p, err := scanStatusPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo páginas de estado: %w", err)
	}
	return out, nil
}

func scanStatusPage(sc scanner) (domain.StatusPage, error) {
	var (
		p           domain.StatusPage
		showLatency int64
		enabled     int64
		createdMS   int64
		updatedMS   int64
	)
	err := sc.Scan(&p.ID, &p.Slug, &p.Title, &p.Description, &showLatency,
		&p.TimeZone, &enabled, &createdMS, &updatedMS)
	if err != nil {
		return domain.StatusPage{}, err
	}

	p.ShowLatency = showLatency != 0
	p.Enabled = enabled != 0
	p.CreatedAt = fromMillis(createdMS)
	p.UpdatedAt = fromMillis(updatedMS)
	return p, nil
}

// ---------- grupos ----------

func (r *statusPageRepo) CreateGroup(ctx context.Context, g *domain.StatusPageGroup) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO status_page_group (page_id, name, position) VALUES (?, ?, ?)`,
		g.PageID, g.Name, g.Position)
	if err != nil {
		return fmt.Errorf("sqlstore: criando grupo da página %d: %w", g.PageID, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("sqlstore: lendo id gerado: %w", err)
	}
	g.ID = id
	return nil
}

func (r *statusPageRepo) UpdateGroup(ctx context.Context, g domain.StatusPageGroup) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE status_page_group SET name = ?, position = ? WHERE id = ?`,
		g.Name, g.Position, g.ID)
	if err != nil {
		return fmt.Errorf("sqlstore: atualizando grupo %d: %w", g.ID, err)
	}
	return requireAffected(res, fmt.Sprintf("grupo %d", g.ID))
}

func (r *statusPageRepo) DeleteGroup(ctx context.Context, id int64) error {
	// Os componentes ficam: a chave estrangeira é SET NULL, então eles
	// apenas saem do agrupamento. Quem tira um grupo espera reorganizar a
	// página, não despublicar o que estava dentro dele.
	res, err := r.db.ExecContext(ctx, `DELETE FROM status_page_group WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando grupo %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("grupo %d", id))
}

func (r *statusPageRepo) Groups(ctx context.Context, pageID int64) ([]domain.StatusPageGroup, error) {
	// Ordena por posição e desempata pelo id, para a listagem ser estável
	// quando duas posições coincidem.
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, page_id, name, position
		FROM status_page_group
		WHERE page_id = ?
		ORDER BY position, id`, pageID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando grupos da página %d: %w", pageID, err)
	}
	defer rows.Close()

	var out []domain.StatusPageGroup
	for rows.Next() {
		var g domain.StatusPageGroup
		if err := rows.Scan(&g.ID, &g.PageID, &g.Name, &g.Position); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo grupo: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ---------- componentes ----------

func (r *statusPageRepo) SetComponent(ctx context.Context, c domain.StatusPageComponent) error {
	// Idempotente: a interface reenvia o conjunto inteiro em vez de
	// calcular a diferença, e publicar duas vezes precisa atualizar em vez
	// de duplicar.
	const q = `
		INSERT INTO status_page_component (page_id, monitor_id, group_id, label, position)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (page_id, monitor_id) DO UPDATE SET
			group_id = excluded.group_id,
			label    = excluded.label,
			position = excluded.position`

	var groupID any
	if c.GroupID != nil {
		groupID = *c.GroupID
	}

	_, err := r.db.ExecContext(ctx, q, c.PageID, c.MonitorID, groupID, c.Label, c.Position)
	if err != nil {
		return fmt.Errorf("sqlstore: publicando monitor %d na página %d: %w", c.MonitorID, c.PageID, err)
	}
	return nil
}

func (r *statusPageRepo) RemoveComponent(ctx context.Context, pageID, monitorID int64) error {
	// O par inteiro na cláusula: despublicar um alvo da página de um
	// cliente não pode despublicá-lo da página de outro.
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM status_page_component WHERE page_id = ? AND monitor_id = ?`,
		pageID, monitorID)
	if err != nil {
		return fmt.Errorf("sqlstore: despublicando monitor %d da página %d: %w", monitorID, pageID, err)
	}
	return nil
}

func (r *statusPageRepo) Components(ctx context.Context, pageID int64) ([]domain.StatusPageComponent, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT page_id, monitor_id, group_id, label, position
		FROM status_page_component
		WHERE page_id = ?
		ORDER BY position, monitor_id`, pageID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando componentes da página %d: %w", pageID, err)
	}
	defer rows.Close()

	var out []domain.StatusPageComponent
	for rows.Next() {
		var (
			c       domain.StatusPageComponent
			groupID sql.NullInt64
		)
		if err := rows.Scan(&c.PageID, &c.MonitorID, &groupID, &c.Label, &c.Position); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo componente: %w", err)
		}
		if groupID.Valid {
			id := groupID.Int64
			c.GroupID = &id
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------- relatos ----------

type announcementRepo struct{ db *sql.DB }

const announcementColumns = `id, title, impact, phase, global, incident_id, started_at, resolved_at, created_at, updated_at`

func (r *announcementRepo) Create(ctx context.Context, a *domain.Announcement) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	a.CreatedAt, a.UpdatedAt = now, now

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: abrindo transação: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO announcement (title, impact, phase, global, incident_id, started_at, resolved_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Title, a.Impact.String(), a.Phase.String(), boolToInt(a.Global),
		nullableID(a.IncidentID), toMillis(a.StartedAt), nullableMillis(a.ResolvedAt),
		toMillis(now), toMillis(now))
	if err != nil {
		return fmt.Errorf("sqlstore: criando relato: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("sqlstore: lendo id gerado: %w", err)
	}
	a.ID = id

	if err := replaceComponents(ctx, tx, a.ID, a.Components); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *announcementRepo) Get(ctx context.Context, id int64) (domain.Announcement, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+announcementColumns+` FROM announcement WHERE id = ?`, id)

	a, err := scanAnnouncement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Announcement{}, fmt.Errorf("relato %d: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return domain.Announcement{}, err
	}

	a.Components, err = r.components(ctx, id)
	return a, err
}

func (r *announcementRepo) Update(ctx context.Context, a domain.Announcement) error {
	now := time.Now().UTC().Truncate(time.Millisecond)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: abrindo transação: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE announcement
		SET title = ?, impact = ?, phase = ?, global = ?, incident_id = ?,
		    started_at = ?, resolved_at = ?, updated_at = ?
		WHERE id = ?`,
		a.Title, a.Impact.String(), a.Phase.String(), boolToInt(a.Global),
		nullableID(a.IncidentID), toMillis(a.StartedAt), nullableMillis(a.ResolvedAt),
		toMillis(now), a.ID)
	if err != nil {
		return fmt.Errorf("sqlstore: atualizando relato %d: %w", a.ID, err)
	}
	if err := requireAffected(res, fmt.Sprintf("relato %d", a.ID)); err != nil {
		return err
	}

	// Substitui em vez de acumular: um relato que agora só afeta o console
	// não pode continuar aparecendo na página que publica a API.
	if err := replaceComponents(ctx, tx, a.ID, a.Components); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *announcementRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM announcement WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando relato %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("relato %d", id))
}

func (r *announcementRepo) List(ctx context.Context, f store.AnnouncementFilter) (store.Page[domain.Announcement], error) {
	f.Page = f.Page.Normalize()

	conds := []string{}
	args := []any{}
	if !f.Since.IsZero() {
		conds = append(conds, "started_at >= ?")
		args = append(args, toMillis(f.Since))
	}
	if f.OnlyOpen {
		conds = append(conds, "phase <> ?")
		args = append(args, domain.PhaseResolved.String())
	}

	q := `SELECT ` + announcementColumns + ` FROM announcement`
	if len(conds) > 0 {
		q += ` WHERE ` + joinAnd(conds)
	}
	// Do mais recente para o mais antigo: "incidentes anteriores" se lê
	// assim, e é a queda de ontem que interessa, não a do ano passado.
	q += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, f.Page.Limit+1)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return store.Page[domain.Announcement]{}, fmt.Errorf("sqlstore: listando relatos: %w", err)
	}
	defer rows.Close()

	var items []domain.Announcement
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			return store.Page[domain.Announcement]{}, err
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return store.Page[domain.Announcement]{}, fmt.Errorf("sqlstore: lendo relatos: %w", err)
	}

	page := store.Page[domain.Announcement]{}
	if len(items) > f.Page.Limit {
		page.HasMore = true
		items = items[:f.Page.Limit]
	}

	// Os componentes vêm numa segunda passada em vez de num JOIN: com o
	// JOIN cada relato viria repetido uma vez por componente, e montar a
	// lista de volta exigiria agrupar em Go de qualquer jeito.
	for i := range items {
		comps, err := r.components(ctx, items[i].ID)
		if err != nil {
			return store.Page[domain.Announcement]{}, err
		}
		items[i].Components = comps
	}

	page.Items = items
	return page, nil
}

func (r *announcementRepo) AddUpdate(ctx context.Context, u *domain.AnnouncementUpdate) error {
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO announcement_update (announcement_id, phase, body, published_at)
		VALUES (?, ?, ?, ?)`,
		u.AnnouncementID, u.Phase.String(), u.Body, toMillis(u.PublishedAt))
	if err != nil {
		return fmt.Errorf("sqlstore: publicando atualização do relato %d: %w", u.AnnouncementID, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("sqlstore: lendo id gerado: %w", err)
	}
	u.ID = id
	return nil
}

func (r *announcementRepo) Updates(ctx context.Context, announcementID int64) ([]domain.AnnouncementUpdate, error) {
	// Cronológica: a linha do tempo de um incidente se lê do começo para o
	// fim, e a ordem de inserção não é a ordem dos fatos quando alguém
	// corrige um horário.
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, announcement_id, phase, body, published_at
		FROM announcement_update
		WHERE announcement_id = ?
		ORDER BY published_at, id`, announcementID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando atualizações do relato %d: %w", announcementID, err)
	}
	defer rows.Close()

	var out []domain.AnnouncementUpdate
	for rows.Next() {
		var (
			u           domain.AnnouncementUpdate
			phaseName   string
			publishedMS int64
		)
		if err := rows.Scan(&u.ID, &u.AnnouncementID, &phaseName, &u.Body, &publishedMS); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo atualização: %w", err)
		}

		phase, err := domain.ParseIncidentPhase(phaseName)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: fase inválida no relato %d: %w", announcementID, err)
		}
		u.Phase = phase
		u.PublishedAt = fromMillis(publishedMS)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *announcementRepo) components(ctx context.Context, announcementID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT monitor_id FROM announcement_component WHERE announcement_id = ? ORDER BY monitor_id`,
		announcementID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando componentes do relato %d: %w", announcementID, err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo componente do relato: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// replaceComponents troca o conjunto inteiro dentro da transação.
func replaceComponents(ctx context.Context, tx *sql.Tx, announcementID int64, monitorIDs []int64) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM announcement_component WHERE announcement_id = ?`, announcementID)
	if err != nil {
		return fmt.Errorf("sqlstore: limpando componentes do relato %d: %w", announcementID, err)
	}

	for _, id := range monitorIDs {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO announcement_component (announcement_id, monitor_id) VALUES (?, ?)`,
			announcementID, id)
		if err != nil {
			return fmt.Errorf("sqlstore: vinculando monitor %d ao relato %d: %w", id, announcementID, err)
		}
	}
	return nil
}

func scanAnnouncement(sc scanner) (domain.Announcement, error) {
	var (
		a          domain.Announcement
		impactName string
		phaseName  string
		global     int64
		incidentID sql.NullInt64
		startedMS  int64
		resolvedMS sql.NullInt64
		createdMS  int64
		updatedMS  int64
	)
	err := sc.Scan(&a.ID, &a.Title, &impactName, &phaseName, &global, &incidentID,
		&startedMS, &resolvedMS, &createdMS, &updatedMS)
	if err != nil {
		return domain.Announcement{}, err
	}

	impact, err := domain.ParseIncidentImpact(impactName)
	if err != nil {
		return domain.Announcement{}, fmt.Errorf("sqlstore: impacto inválido no relato %d: %w", a.ID, err)
	}
	phase, err := domain.ParseIncidentPhase(phaseName)
	if err != nil {
		return domain.Announcement{}, fmt.Errorf("sqlstore: fase inválida no relato %d: %w", a.ID, err)
	}

	a.Impact = impact
	a.Phase = phase
	a.Global = global != 0
	if incidentID.Valid {
		id := incidentID.Int64
		a.IncidentID = &id
	}
	a.StartedAt = fromMillis(startedMS)
	if resolvedMS.Valid {
		at := fromMillis(resolvedMS.Int64)
		a.ResolvedAt = &at
	}
	a.CreatedAt = fromMillis(createdMS)
	a.UpdatedAt = fromMillis(updatedMS)
	return a, nil
}

func nullableID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func nullableMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return toMillis(*t)
}
