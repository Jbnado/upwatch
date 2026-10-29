// Package maintenance mantém em memória as janelas de manutenção, para o
// motor de incidentes perguntar a cada batida sem ir ao banco.
//
// A regra — qual janela cobre qual monitor, e quando — mora no domínio
// (domain.MaintenanceFor). Aqui fica só o cache dela.
package maintenance

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// refreshInterval é de quanto em quanto tempo o calendário relê o banco
// mesmo sem ninguém avisar. A API avisa a cada janela gravada; a releitura
// cobre o resto — um grupo que mudou de sistema, ou a janela gravada por
// outra instância.
const refreshInterval = time.Minute

// Calendar é o cache das janelas e da árvore de grupos.
type Calendar struct {
	store store.MetadataStore

	mu      sync.RWMutex
	windows []domain.MaintenanceWindow
	tree    domain.GroupTree
}

// New cria o calendário vazio; Reload o preenche.
func New(s store.MetadataStore) *Calendar {
	return &Calendar{store: s}
}

// Reload relê as janelas e os grupos.
//
// Em caso de erro o calendário fica como estava: perder a leitura não
// pode apagar as janelas que já se conhecia e fazer o pager tocar no meio
// de uma manutenção.
func (c *Calendar) Reload(ctx context.Context) error {
	janelas, err := c.store.Maintenance().List(ctx)
	if err != nil {
		return err
	}
	grupos, err := c.store.Groups().List(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.windows = janelas
	c.tree = domain.NewGroupTree(grupos)
	c.mu.Unlock()
	return nil
}

// Run relê o banco periodicamente até o contexto acabar.
func (c *Calendar) Run(ctx context.Context) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Reload(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("falha ao reler as janelas de manutenção", "erro", err)
			}
		}
	}
}

// Covering devolve a janela que deixa o monitor em manutenção no instante.
func (c *Calendar) Covering(m domain.Monitor, lookup domain.MonitorLookup, at time.Time) (domain.MaintenanceWindow, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return domain.MaintenanceFor(m, lookup, c.windows, c.tree, at)
}
