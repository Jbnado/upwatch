package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// incidentCases são os casos de conformidade de estado, incidentes e
// canais.
func incidentCases() []conformanceCase {
	return []conformanceCase{
		{"StateStartsZero", testStateStartsZero},
		{"StateRoundTrips", testStateRoundTrips},
		{"StateOverwritesOnSave", testStateOverwritesOnSave},
		{"StateAllLoadsEveryMonitor", testStateAllLoadsEveryMonitor},
		{"StateCascadesOnMonitorDelete", testStateCascadesOnMonitorDelete},

		{"IncidentOpenAndCurrent", testIncidentOpenAndCurrent},
		{"IncidentCurrentMissingIsNotFound", testIncidentCurrentMissingIsNotFound},
		{"IncidentRefusesTwoOpenAtOnce", testIncidentRefusesTwoOpenAtOnce},
		{"IncidentResolveClosesIt", testIncidentResolveClosesIt},
		{"IncidentResolveWithoutOpenIsHarmless", testIncidentResolveWithoutOpenIsHarmless},
		{"IncidentReopensAfterResolution", testIncidentReopensAfterResolution},
		{"IncidentListFiltersByMonitor", testIncidentListFiltersByMonitor},
		{"IncidentListFiltersOpenOnly", testIncidentListFiltersOpenOnly},
		{"IncidentCascadesOnMonitorDelete", testIncidentCascadesOnMonitorDelete},

		{"ChannelCRUD", testChannelCRUD},
		{"ChannelDuplicateNameIsConflict", testChannelDuplicateNameIsConflict},
		{"ChannelLinkIsIdempotent", testChannelLinkIsIdempotent},
		{"ChannelForMonitorIsScoped", testChannelForMonitorIsScoped},
		{"ChannelForMonitorSkipsDisabled", testChannelForMonitorSkipsDisabled},
		{"ChannelUnlink", testChannelUnlink},
		{"ChannelLinkCascadesOnDelete", testChannelLinkCascadesOnDelete},
	}
}

// ---------- estado ----------

func testStateStartsZero(t *testing.T, newStore Factory) {
	s := newStore(t)
	id := mustCreateMonitor(t, s, "api")

	got, err := s.States().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.Status != domain.StatusUnknown || got.Consecutive != 0 {
		t.Errorf("state on a never-checked monitor = %+v, want the zero value", got)
	}
}

// Sem persistir, um reinício zeraria a contagem e um alvo prestes a ser
// declarado fora do ar voltaria à estaca zero.
func testStateRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	want := domain.MonitorState{
		Status: domain.StatusUp, Candidate: domain.StatusDown,
		Consecutive: 2, Since: epoch,
	}
	if err := s.States().Save(ctx, id, want); err != nil {
		t.Fatalf("Save returned unexpected error: %v", err)
	}

	got, err := s.States().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.Status != want.Status || got.Candidate != want.Candidate {
		t.Errorf("state = %+v, want %+v", got, want)
	}
	if got.Consecutive != 2 {
		t.Errorf("Consecutive = %d, want 2", got.Consecutive)
	}
	if !got.Since.Equal(epoch) {
		t.Errorf("Since = %v, want %v", got.Since, epoch)
	}
}

func testStateOverwritesOnSave(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	for _, status := range []domain.Status{domain.StatusUp, domain.StatusDown} {
		if err := s.States().Save(ctx, id, domain.MonitorState{Status: status, Since: epoch}); err != nil {
			t.Fatalf("Save returned unexpected error: %v", err)
		}
	}

	got, _ := s.States().Get(ctx, id)
	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want the latest save to win", got.Status)
	}
}

// O motor carrega tudo de uma vez no arranque; uma consulta por monitor
// tornaria a partida lenta numa instalação grande.
func testStateAllLoadsEveryMonitor(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	primeiro := mustCreateMonitor(t, s, "primeiro")
	segundo := mustCreateMonitor(t, s, "segundo")

	for _, id := range []int64{primeiro, segundo} {
		if err := s.States().Save(ctx, id, domain.MonitorState{Status: domain.StatusUp, Since: epoch}); err != nil {
			t.Fatalf("Save returned unexpected error: %v", err)
		}
	}

	todos, err := s.States().All(ctx)
	if err != nil {
		t.Fatalf("All returned unexpected error: %v", err)
	}
	if len(todos) != 2 {
		t.Fatalf("All returned %d states, want 2", len(todos))
	}
	if todos[primeiro].Status != domain.StatusUp {
		t.Errorf("state of monitor %d = %v, want %v", primeiro, todos[primeiro].Status, domain.StatusUp)
	}
}

func testStateCascadesOnMonitorDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.States().Save(ctx, id, domain.MonitorState{Status: domain.StatusUp, Since: epoch}); err != nil {
		t.Fatalf("Save returned unexpected error: %v", err)
	}
	if err := s.Monitors().Delete(ctx, id); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	todos, _ := s.States().All(ctx)
	if _, existe := todos[id]; existe {
		t.Error("the state survived the monitor being deleted")
	}
}

// ---------- incidentes ----------

func testIncidentOpenAndCurrent(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	i := domain.Incident{MonitorID: id, StartedAt: epoch, Cause: "status 502"}
	if err := s.Incidents().Open(ctx, &i); err != nil {
		t.Fatalf("Open returned unexpected error: %v", err)
	}
	if i.ID == 0 {
		t.Error("Open left ID as zero")
	}

	got, err := s.Incidents().Current(ctx, id)
	if err != nil {
		t.Fatalf("Current returned unexpected error: %v", err)
	}
	if !got.Open() {
		t.Error("the incident is not reported as open")
	}
	if got.Cause != "status 502" {
		t.Errorf("Cause = %q, want %q", got.Cause, "status 502")
	}
}

func testIncidentCurrentMissingIsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)
	id := mustCreateMonitor(t, s, "api")

	_, err := s.Incidents().Current(context.Background(), id)

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Current with no open incident returned %v, want store.ErrNotFound", err)
	}
}

// Duas quedas abertas ao mesmo tempo tornariam a duração indefinida; o
// banco recusa em vez de depender de disciplina no código.
func testIncidentRefusesTwoOpenAtOnce(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	primeiro := domain.Incident{MonitorID: id, StartedAt: epoch}
	if err := s.Incidents().Open(ctx, &primeiro); err != nil {
		t.Fatalf("Open returned unexpected error: %v", err)
	}

	segundo := domain.Incident{MonitorID: id, StartedAt: epoch.Add(time.Minute)}
	if err := s.Incidents().Open(ctx, &segundo); !errors.Is(err, store.ErrConflict) {
		t.Errorf("second Open returned %v, want store.ErrConflict", err)
	}
}

func testIncidentResolveClosesIt(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	i := domain.Incident{MonitorID: id, StartedAt: epoch}
	if err := s.Incidents().Open(ctx, &i); err != nil {
		t.Fatalf("Open returned unexpected error: %v", err)
	}

	fim := epoch.Add(17 * time.Minute)
	if err := s.Incidents().Resolve(ctx, id, fim); err != nil {
		t.Fatalf("Resolve returned unexpected error: %v", err)
	}

	if _, err := s.Incidents().Current(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Current after Resolve returned %v, want store.ErrNotFound", err)
	}

	page, err := s.Incidents().List(ctx, store.IncidentFilter{MonitorID: id})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("List returned %d incidents, want 1", len(page.Items))
	}
	if page.Items[0].Open() {
		t.Error("the incident is still open after Resolve")
	}
	if got := page.Items[0].Duration(fim); got != 17*time.Minute {
		t.Errorf("Duration = %v, want 17m", got)
	}
}

// Encerrar o que já acabou não é erro; tratá-lo como tal obrigaria o
// motor a consultar antes de agir.
func testIncidentResolveWithoutOpenIsHarmless(t *testing.T, newStore Factory) {
	s := newStore(t)
	id := mustCreateMonitor(t, s, "api")

	if err := s.Incidents().Resolve(context.Background(), id, epoch); err != nil {
		t.Errorf("Resolve with no open incident returned %v, want it to be a no-op", err)
	}
}

func testIncidentReopensAfterResolution(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	primeiro := domain.Incident{MonitorID: id, StartedAt: epoch}
	if err := s.Incidents().Open(ctx, &primeiro); err != nil {
		t.Fatalf("Open returned unexpected error: %v", err)
	}
	if err := s.Incidents().Resolve(ctx, id, epoch.Add(time.Minute)); err != nil {
		t.Fatalf("Resolve returned unexpected error: %v", err)
	}

	segundo := domain.Incident{MonitorID: id, StartedAt: epoch.Add(time.Hour)}
	if err := s.Incidents().Open(ctx, &segundo); err != nil {
		t.Errorf("reopening after resolution returned %v, want success", err)
	}
}

func testIncidentListFiltersByMonitor(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	meu := mustCreateMonitor(t, s, "meu")
	outro := mustCreateMonitor(t, s, "outro")

	for _, id := range []int64{meu, outro} {
		i := domain.Incident{MonitorID: id, StartedAt: epoch}
		if err := s.Incidents().Open(ctx, &i); err != nil {
			t.Fatalf("Open returned unexpected error: %v", err)
		}
	}

	page, err := s.Incidents().List(ctx, store.IncidentFilter{MonitorID: meu})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].MonitorID != meu {
		t.Errorf("List returned %v, want only the incidents of monitor %d", page.Items, meu)
	}
}

func testIncidentListFiltersOpenOnly(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	fechado := mustCreateMonitor(t, s, "fechado")
	aberto := mustCreateMonitor(t, s, "aberto")

	resolvido := domain.Incident{MonitorID: fechado, StartedAt: epoch}
	if err := s.Incidents().Open(ctx, &resolvido); err != nil {
		t.Fatalf("Open returned unexpected error: %v", err)
	}
	if err := s.Incidents().Resolve(ctx, fechado, epoch.Add(time.Minute)); err != nil {
		t.Fatalf("Resolve returned unexpected error: %v", err)
	}

	emCurso := domain.Incident{MonitorID: aberto, StartedAt: epoch}
	if err := s.Incidents().Open(ctx, &emCurso); err != nil {
		t.Fatalf("Open returned unexpected error: %v", err)
	}

	page, err := s.Incidents().List(ctx, store.IncidentFilter{OnlyOpen: true})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].MonitorID != aberto {
		t.Errorf("List returned %v, want only the ongoing incident", page.Items)
	}
}

func testIncidentCascadesOnMonitorDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	i := domain.Incident{MonitorID: id, StartedAt: epoch}
	if err := s.Incidents().Open(ctx, &i); err != nil {
		t.Fatalf("Open returned unexpected error: %v", err)
	}
	if err := s.Monitors().Delete(ctx, id); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	page, _ := s.Incidents().List(ctx, store.IncidentFilter{})
	if len(page.Items) != 0 {
		t.Errorf("found %d incidents after deleting the monitor, want 0", len(page.Items))
	}
}

// ---------- canais ----------

func mustCreateChannel(t *testing.T, s store.Store, name string) domain.Channel {
	t.Helper()

	c := domain.Channel{
		Name: name, Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
	}
	if err := s.Channels().Create(context.Background(), &c); err != nil {
		t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
	}
	return c
}

func testChannelCRUD(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	criado := mustCreateChannel(t, s, "discord do time")
	if criado.ID == 0 {
		t.Fatal("Create left ID as zero")
	}

	got, err := s.Channels().Get(ctx, criado.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.Name != "discord do time" || got.Type != "webhook" {
		t.Errorf("channel = %+v, want the created values", got)
	}
	// A configuração precisa sobreviver: é onde mora a URL do destino.
	if len(got.Config) == 0 {
		t.Error("Config did not survive the round trip")
	}

	got.Enabled = false
	if err := s.Channels().Update(ctx, got); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}
	depois, _ := s.Channels().Get(ctx, criado.ID)
	if depois.Enabled {
		t.Error("Enabled = true after disabling the channel")
	}

	if err := s.Channels().Delete(ctx, criado.ID); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}
	if _, err := s.Channels().Get(ctx, criado.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after Delete returned %v, want store.ErrNotFound", err)
	}
}

func testChannelDuplicateNameIsConflict(t *testing.T, newStore Factory) {
	s := newStore(t)
	mustCreateChannel(t, s, "discord")

	dup := domain.Channel{Name: "discord", Type: "webhook", Enabled: true}
	if err := s.Channels().Create(context.Background(), &dup); !errors.Is(err, store.ErrConflict) {
		t.Errorf("Create with a duplicate name returned %v, want store.ErrConflict", err)
	}
}

// A interface pode reenviar o conjunto inteiro de vínculos sem calcular a
// diferença; vincular duas vezes não pode falhar.
func testChannelLinkIsIdempotent(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	monitorID := mustCreateMonitor(t, s, "api")
	canal := mustCreateChannel(t, s, "discord")

	for i := 0; i < 3; i++ {
		if err := s.Channels().Link(ctx, monitorID, canal.ID); err != nil {
			t.Fatalf("Link returned unexpected error on attempt %d: %v", i+1, err)
		}
	}

	canais, err := s.Channels().ForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatalf("ForMonitor returned unexpected error: %v", err)
	}
	if len(canais) != 1 {
		t.Errorf("ForMonitor returned %d channels, want 1", len(canais))
	}
}

func testChannelForMonitorIsScoped(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	meu := mustCreateMonitor(t, s, "meu")
	outro := mustCreateMonitor(t, s, "outro")
	canal := mustCreateChannel(t, s, "discord")

	if err := s.Channels().Link(ctx, meu, canal.ID); err != nil {
		t.Fatalf("Link returned unexpected error: %v", err)
	}

	if canais, _ := s.Channels().ForMonitor(ctx, outro); len(canais) != 0 {
		t.Errorf("ForMonitor on an unlinked monitor returned %d channels, want 0", len(canais))
	}
}

// Um canal desligado que ainda recebesse avisos tornaria o botão de
// desligar decorativo.
func testChannelForMonitorSkipsDisabled(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	monitorID := mustCreateMonitor(t, s, "api")
	canal := mustCreateChannel(t, s, "discord")

	if err := s.Channels().Link(ctx, monitorID, canal.ID); err != nil {
		t.Fatalf("Link returned unexpected error: %v", err)
	}

	canal.Enabled = false
	if err := s.Channels().Update(ctx, canal); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}

	canais, err := s.Channels().ForMonitor(ctx, monitorID)
	if err != nil {
		t.Fatalf("ForMonitor returned unexpected error: %v", err)
	}
	if len(canais) != 0 {
		t.Errorf("ForMonitor returned %d channels, want the disabled one skipped", len(canais))
	}
}

func testChannelUnlink(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	monitorID := mustCreateMonitor(t, s, "api")
	canal := mustCreateChannel(t, s, "discord")

	if err := s.Channels().Link(ctx, monitorID, canal.ID); err != nil {
		t.Fatalf("Link returned unexpected error: %v", err)
	}
	if err := s.Channels().Unlink(ctx, monitorID, canal.ID); err != nil {
		t.Fatalf("Unlink returned unexpected error: %v", err)
	}

	if canais, _ := s.Channels().ForMonitor(ctx, monitorID); len(canais) != 0 {
		t.Errorf("ForMonitor after Unlink returned %d channels, want 0", len(canais))
	}
}

func testChannelLinkCascadesOnDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	monitorID := mustCreateMonitor(t, s, "api")
	canal := mustCreateChannel(t, s, "discord")

	if err := s.Channels().Link(ctx, monitorID, canal.ID); err != nil {
		t.Fatalf("Link returned unexpected error: %v", err)
	}
	if err := s.Channels().Delete(ctx, canal.ID); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	if canais, _ := s.Channels().ForMonitor(ctx, monitorID); len(canais) != 0 {
		t.Errorf("the link survived the channel being deleted: %v", canais)
	}
}
