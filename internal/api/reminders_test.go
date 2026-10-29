package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// openIncident abre uma queda do monitor direto no store.
func (s *server) openIncident(t *testing.T, monitorID int64) int64 {
	t.Helper()
	i := domain.Incident{MonitorID: monitorID, StartedAt: epoch, Cause: "status 502"}
	if err := s.store.Incidents().Open(context.Background(), &i); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return i.ID
}

type ackResp struct {
	ID              int64 `json:"id"`
	Acknowledgement *struct {
		By   string    `json:"by"`
		At   time.Time `json:"at"`
		Note string    `json:"note"`
	} `json:"acknowledgement"`
}

func ackPath(monitorID, incidentID int64) string {
	return "/api/v1/monitors/" + itoa(monitorID) + "/incidents/" + itoa(incidentID) + "/ack"
}

// O reconhecimento registra quem e quando a partir da sessão, e não do
// corpo: um "estou vendo" assinado com o nome de outra pessoa mandaria a
// equipe cobrar quem nem sabia da queda.
func TestAcknowledgeRecordsWhoAndWhen(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	inc := s.openIncident(t, id)

	resp := s.do(t, http.MethodPost, ackPath(id, inc), map[string]any{"note": "  reiniciando a fila  "})
	assertStatus(t, resp, http.StatusOK)
	got := decode[ackResp](t, resp)
	if got.Acknowledgement == nil {
		t.Fatal("resposta sem acknowledgement")
	}
	if got.Acknowledgement.By != "admin" || got.Acknowledgement.Note != "reiniciando a fila" || !got.Acknowledgement.At.Equal(epoch) {
		t.Errorf("acknowledgement = %+v", got.Acknowledgement)
	}

	// O histórico e o painel mostram o mesmo reconhecimento.
	lista := decode[struct {
		Items []ackResp `json:"items"`
	}](t, s.do(t, http.MethodGet, "/api/v1/incidents?monitor_id="+itoa(id), nil))
	if len(lista.Items) != 1 || lista.Items[0].Acknowledgement == nil {
		t.Errorf("listagem sem o reconhecimento: %+v", lista.Items)
	}
	resumo := decode[struct {
		Items []struct {
			MonitorID       int64 `json:"monitor_id"`
			Acknowledgement *struct {
				By string `json:"by"`
			} `json:"acknowledgement"`
		} `json:"items"`
	}](t, s.do(t, http.MethodGet, "/api/v1/summaries", nil))
	if len(resumo.Items) != 1 || resumo.Items[0].Acknowledgement == nil || resumo.Items[0].Acknowledgement.By != "admin" {
		t.Errorf("resumo sem o reconhecimento: %+v", resumo.Items)
	}
}

// Sem corpo também vale: quem reconhece do celular às três da manhã não
// deveria precisar escrever nada.
func TestAcknowledgeWithoutBody(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	inc := s.openIncident(t, id)

	assertStatus(t, s.do(t, http.MethodPost, ackPath(id, inc), nil), http.StatusOK)
}

// O segundo a chegar fica sabendo quem chegou antes, em vez de um "já
// existe" genérico: é o que evita duas pessoas mexendo no mesmo problema.
func TestAcknowledgeTwiceSaysWhoWasFirst(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	inc := s.openIncident(t, id)

	assertStatus(t, s.do(t, http.MethodPost, ackPath(id, inc), nil), http.StatusOK)
	resp := s.do(t, http.MethodPost, ackPath(id, inc), nil)
	assertStatus(t, resp, http.StatusConflict)
	if corpo := readBody(t, resp); !strings.Contains(corpo, "admin") {
		t.Errorf("409 sem dizer quem reconheceu: %s", corpo)
	}
}

func TestAcknowledgeResolvedIsConflict(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	inc := s.openIncident(t, id)
	if err := s.store.Incidents().Resolve(context.Background(), id, epoch.Add(time.Minute)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	assertStatus(t, s.do(t, http.MethodPost, ackPath(id, inc), nil), http.StatusConflict)
}

// O incidente precisa ser do monitor do caminho. Sem isso, a guarda de
// escopo conferiria um monitor e o reconhecimento cairia na queda de
// outro, de um grupo que a conta não enxerga.
func TestAcknowledgeIncidentOfAnotherMonitorIsNotFound(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	meu := s.createMonitor(t, newMonitorBody("api"))
	outro := s.createMonitor(t, newMonitorBody("front"))
	inc := s.openIncident(t, outro)

	assertStatus(t, s.do(t, http.MethodPost, ackPath(meu, inc), nil), http.StatusNotFound)
	assertStatus(t, s.do(t, http.MethodPost, ackPath(meu, 9999), nil), http.StatusNotFound)

	got, err := s.store.Incidents().Get(context.Background(), inc)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Acknowledgement != nil {
		t.Error("o incidente de outro monitor foi reconhecido")
	}
}

// Reconhecer cala o lembrete de todo mundo: é escrita, e segue o papel.
func TestViewerCannotAcknowledge(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	inc := s.openIncident(t, id)
	s.comoObservador(t)

	assertStatus(t, s.do(t, http.MethodPost, ackPath(id, inc), nil), http.StatusForbidden)
}

func TestAcknowledgeRejectsLongNote(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	inc := s.openIncident(t, id)

	resp := s.do(t, http.MethodPost, ackPath(id, inc), map[string]any{"note": strings.Repeat("a", 501)})
	assertStatus(t, resp, http.StatusBadRequest)
}

type reminderResp struct {
	ReminderMinutes *int `json:"reminder_minutes"`
}

// Nulo é "herda" e zero é "desligado"; a API precisa devolver os dois
// distintos, senão a interface mostraria "herda do grupo" num monitor que
// alguém desligou de propósito.
func TestMonitorReminderMinutesRoundTrip(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	corpo := newMonitorBody("api")
	corpo["reminder_minutes"] = 30
	id := s.createMonitor(t, corpo)

	got := decode[reminderResp](t, s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil))
	if got.ReminderMinutes == nil || *got.ReminderMinutes != 30 {
		t.Errorf("reminder_minutes = %v, quer 30", got.ReminderMinutes)
	}

	corpo["reminder_minutes"] = 0
	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), corpo)
	assertStatus(t, resp, http.StatusOK)
	if got := decode[reminderResp](t, resp); got.ReminderMinutes == nil || *got.ReminderMinutes != 0 {
		t.Errorf("depois de desligar: %v, quer 0", got.ReminderMinutes)
	}

	delete(corpo, "reminder_minutes")
	resp = s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), corpo)
	assertStatus(t, resp, http.StatusOK)
	if got := decode[reminderResp](t, resp); got.ReminderMinutes != nil {
		t.Errorf("PUT sem o campo: %v, quer nulo (herda)", *got.ReminderMinutes)
	}

	corpo["reminder_minutes"] = 1
	resp = s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), corpo)
	assertStatus(t, resp, http.StatusBadRequest)
	if !strings.Contains(readBody(t, resp), "reminder_minutes") {
		t.Error("a recusa não aponta o campo")
	}
}

func TestGroupReminderMinutesRoundTrip(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/groups", map[string]any{"name": "RPA", "reminder_minutes": 60})
	assertStatus(t, resp, http.StatusCreated)
	g := decode[struct {
		ID int64 `json:"id"`
		reminderResp
	}](t, resp)
	if g.ReminderMinutes == nil || *g.ReminderMinutes != 60 {
		t.Fatalf("reminder_minutes = %v, quer 60", g.ReminderMinutes)
	}

	got := decode[reminderResp](t, s.do(t, http.MethodGet, "/api/v1/groups/"+itoa(g.ID), nil))
	if got.ReminderMinutes == nil || *got.ReminderMinutes != 60 {
		t.Errorf("GET: reminder_minutes = %v, quer 60", got.ReminderMinutes)
	}

	resp = s.do(t, http.MethodPut, "/api/v1/groups/"+itoa(g.ID), map[string]any{"name": "RPA", "reminder_minutes": 2})
	assertStatus(t, resp, http.StatusBadRequest)
}
