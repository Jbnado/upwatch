package incident

import (
	"context"
	"log/slog"

	"github.com/Jbnado/upwatch/internal/domain"
)

// groupPath devolve o caminho do grupo do monitor ("RPA / prod"), vazio
// sem grupo.
//
// Lido do banco a cada aviso, e não guardado no motor: aviso é raro, e um
// grupo renomeado precisa aparecer no próximo sem que o motor tenha de
// ouvir mudanças de grupo. Falhar aqui não pode calar o aviso — sai sem o
// grupo, que é como saía antes de ele existir.
func (e *Engine) groupPath(ctx context.Context, m domain.Monitor) string {
	if m.GroupID == nil {
		return ""
	}
	grupos, err := e.store.Groups().List(ctx)
	if err != nil {
		slog.Error("falha ao ler os grupos para o aviso", "monitor", m.Name, "erro", err)
		return ""
	}
	return domain.NewGroupTree(grupos).Path(*m.GroupID)
}
