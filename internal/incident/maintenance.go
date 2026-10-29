package incident

import (
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Maintenance responde se o monitor está numa janela de manutenção.
//
// Interface estreita, como Sink e Dispatcher: o motor não precisa saber
// de onde vêm as janelas, só perguntar.
type Maintenance interface {
	Covering(m domain.Monitor, lookup domain.MonitorLookup, at time.Time) (domain.MaintenanceWindow, bool)
}

// WithMaintenance liga o motor às janelas de manutenção.
//
// Opcional, e fora de NewEngine: quem não declara janelas — e todo teste
// que não trata delas — monta o motor como antes.
func (e *Engine) WithMaintenance(m Maintenance) *Engine {
	e.mu.Lock()
	e.maintenance = m
	e.mu.Unlock()
	return e
}

// inMaintenance decide se a batida cai numa janela de manutenção.
//
// Só a falha é afetada: queda e degradação viram "sem medição" com o
// motivo na mensagem, e a máquina de confirmação não conta observação sem
// medição. Daí saem as três garantias da janela sem que nada adiante da
// triagem precise saber que ela existe: não abre incidente, não avisa, e
// a disponibilidade não conta o intervalo como queda. A degradação entra
// junto porque também avisa, e porque a latência de um deploy em curso
// não descreve o serviço — publicá-la no p95 da página seria o mesmo
// engano que contar a queda.
//
// A batida no ar passa intacta, e com ela a volta de uma queda confirmada
// antes da janela começar: essa queda já tinha avisado, e saber que
// acabou é notícia.
//
// Ao fim da janela, o alvo que continua fora volta a ser contado e avisa
// depois das confirmações de sempre, uma vez, como uma queda nova. As
// falhas de logo antes da janela continuam na contagem, porque observação
// sem medição não a zera.
//
// Quem chama já segura mu.
func (e *Engine) inMaintenance(hb domain.Heartbeat) (domain.MaintenanceWindow, bool) {
	if e.maintenance == nil || (hb.Status != domain.StatusDown && hb.Status != domain.StatusDegraded) {
		return domain.MaintenanceWindow{}, false
	}
	m, ok := e.monitors[hb.MonitorID]
	if !ok {
		return domain.MaintenanceWindow{}, false
	}
	return e.maintenance.Covering(m, e.lookup, hb.Timestamp)
}

// originalCause é o que a batida dizia antes de ser descartada. A
// degradação por latência pode vir sem mensagem, e "falha original: " sem
// nada depois não diria o que foi descartado.
func originalCause(hb domain.Heartbeat) string {
	if hb.Message != "" {
		return hb.Message
	}
	return hb.Status.String()
}
