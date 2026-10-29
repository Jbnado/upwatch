package api_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/api"
)

// Janelas de manutenção pela API.

type janelaResp struct {
	ID         int64      `json:"id"`
	Title      string     `json:"title"`
	Recurrence string     `json:"recurrence"`
	TimeZone   string     `json:"time_zone"`
	MonitorIDs []int64    `json:"monitor_ids"`
	GroupIDs   []int64    `json:"group_ids"`
	EndedAt    *time.Time `json:"ended_at"`
	State      string     `json:"state"`
	Next       *struct {
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	} `json:"next"`
}

// recarregador conta os avisos ao calendário do motor.
type recarregador struct {
	mu sync.Mutex
	n  int
}

func (r *recarregador) Reload(context.Context) error {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
	return nil
}

func (r *recarregador) vezes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func novoServidorComCalendario(t *testing.T) (*server, *recarregador) {
	t.Helper()
	cal := &recarregador{}
	s := newServerWith(t, func(o *api.Options) { o.Maintenance = cal })
	s.setup(t)
	return s, cal
}

// O ciclo da issue: criar, listar, encerrar antes do previsto. Cada
// escrita avisa o motor, senão a janela recém-criada só valeria na
// releitura seguinte — e o deploy das 19h avisaria às 19h.
func TestMaintenanceCreateListAndEndEarly(t *testing.T) {
	s, cal := novoServidorComCalendario(t)
	m := s.createMonitor(t, monitorNoGrupo("api", nil))

	resp := s.do(t, http.MethodPost, "/api/v1/maintenance-windows", map[string]any{
		"title": "deploy", "starts_at": epoch.Add(-time.Minute), "ends_at": epoch.Add(30 * time.Minute),
		"monitor_ids": []int64{m},
	})
	assertStatus(t, resp, http.StatusCreated)
	criada := decode[janelaResp](t, resp)
	if criada.State != "active" || criada.Recurrence != "none" || criada.TimeZone != "UTC" {
		t.Errorf("created = %+v, want active, one-off, UTC by default", criada)
	}
	if cal.vezes() != 1 {
		t.Errorf("reloads after create = %d, want 1", cal.vezes())
	}

	resp = s.do(t, http.MethodGet, "/api/v1/maintenance-windows", nil)
	assertStatus(t, resp, http.StatusOK)
	lista := decode[struct{ Items []janelaResp }](t, resp)
	if len(lista.Items) != 1 || lista.Items[0].ID != criada.ID {
		t.Fatalf("list = %+v, want the created window", lista.Items)
	}

	s.clock.Advance(5 * time.Minute)
	resp = s.do(t, http.MethodPost, "/api/v1/maintenance-windows/"+itoa(criada.ID)+"/end", nil)
	assertStatus(t, resp, http.StatusOK)
	encerrada := decode[janelaResp](t, resp)
	if encerrada.State != "finished" || encerrada.EndedAt == nil || !encerrada.EndedAt.Equal(epoch.Add(5*time.Minute)) {
		t.Errorf("ended = %+v, want finished at the current time", encerrada)
	}
	if cal.vezes() != 2 {
		t.Errorf("reloads after end = %d, want 2", cal.vezes())
	}
}

// A recorrente devolve a próxima ocorrência calculada, para a tela não
// precisar reimplementar a regra de fuso e de horário de verão.
func TestMaintenanceRecurringReportsTheNextOccurrence(t *testing.T) {
	s, _ := novoServidorComCalendario(t)
	m := s.createMonitor(t, monitorNoGrupo("api", nil))

	inicio := epoch.Add(-2 * 24 * time.Hour)
	resp := s.do(t, http.MethodPost, "/api/v1/maintenance-windows", map[string]any{
		"title": "janela semanal", "starts_at": inicio, "ends_at": inicio.Add(time.Hour),
		"recurrence": "weekly", "time_zone": "America/Sao_Paulo", "monitor_ids": []int64{m},
	})
	assertStatus(t, resp, http.StatusCreated)
	j := decode[janelaResp](t, resp)
	if j.State != "scheduled" || j.Next == nil || !j.Next.Start.Equal(inicio.Add(7*24*time.Hour)) {
		t.Errorf("window = %+v, want scheduled with the next occurrence a week after the first", j)
	}
}

func TestMaintenanceValidation(t *testing.T) {
	s, cal := novoServidorComCalendario(t)
	m := s.createMonitor(t, monitorNoGrupo("api", nil))

	base := func() map[string]any {
		return map[string]any{
			"title": "deploy", "starts_at": epoch, "ends_at": epoch.Add(time.Hour), "monitor_ids": []int64{m},
		}
	}
	casos := map[string]struct {
		muda  func(map[string]any)
		campo string
	}{
		"fim antes do início": {func(b map[string]any) { b["ends_at"] = epoch.Add(-time.Hour) }, "ends_at"},
		"sem alvo":            {func(b map[string]any) { delete(b, "monitor_ids") }, "monitor_ids"},
		"monitor inexistente": {func(b map[string]any) { b["monitor_ids"] = []int64{9999} }, "monitor_ids"},
		"grupo inexistente":   {func(b map[string]any) { b["group_ids"] = []int64{9999} }, "group_ids"},
		"recorrência":         {func(b map[string]any) { b["recurrence"] = "monthly" }, "recurrence"},
		"fuso":                {func(b map[string]any) { b["time_zone"] = "Lua/Base" }, "time_zone"},
	}
	for nome, c := range casos {
		t.Run(nome, func(t *testing.T) {
			corpo := base()
			c.muda(corpo)
			resp := s.do(t, http.MethodPost, "/api/v1/maintenance-windows", corpo)
			assertStatus(t, resp, http.StatusBadRequest)
			if !strings.Contains(readBody(t, resp), `"field":"`+c.campo+`"`) {
				t.Errorf("the error did not point at %s", c.campo)
			}
		})
	}
	if cal.vezes() != 0 {
		t.Errorf("a refused request reloaded the calendar %d times", cal.vezes())
	}
}

func TestMaintenanceUpdateAndDelete(t *testing.T) {
	s, cal := novoServidorComCalendario(t)
	a := s.createMonitor(t, monitorNoGrupo("a", nil))
	rpa := s.criarGrupo(t, "RPA", nil)

	resp := s.do(t, http.MethodPost, "/api/v1/maintenance-windows", map[string]any{
		"title": "deploy", "starts_at": epoch, "ends_at": epoch.Add(time.Hour), "monitor_ids": []int64{a},
	})
	assertStatus(t, resp, http.StatusCreated)
	id := decode[janelaResp](t, resp).ID

	resp = s.do(t, http.MethodPut, "/api/v1/maintenance-windows/"+itoa(id), map[string]any{
		"title": "deploy do RPA", "starts_at": epoch, "ends_at": epoch.Add(time.Hour), "group_ids": []int64{rpa},
	})
	assertStatus(t, resp, http.StatusOK)
	j := decode[janelaResp](t, resp)
	if j.Title != "deploy do RPA" || len(j.MonitorIDs) != 0 || len(j.GroupIDs) != 1 || j.GroupIDs[0] != rpa {
		t.Errorf("updated = %+v, want the new title and only the RPA group", j)
	}

	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/maintenance-windows/"+itoa(id), nil), http.StatusNoContent)
	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/maintenance-windows/"+itoa(id), nil), http.StatusNotFound)
	if cal.vezes() != 3 {
		t.Errorf("reloads = %d, want one per write (create, update, delete)", cal.vezes())
	}
}

// O observador lê as janelas — quer saber por que o monitor está calado —,
// mas não declara.
func TestMaintenanceViewerReadsButCannotWrite(t *testing.T) {
	s, _ := novoServidorComCalendario(t)
	m := s.createMonitor(t, monitorNoGrupo("api", nil))
	s.comoObservador(t)

	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/maintenance-windows", nil), http.StatusOK)
	resp := s.do(t, http.MethodPost, "/api/v1/maintenance-windows", map[string]any{
		"title": "deploy", "starts_at": epoch, "ends_at": epoch.Add(time.Hour), "monitor_ids": []int64{m},
	})
	assertStatus(t, resp, http.StatusForbidden)
}
