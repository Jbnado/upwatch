package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// reminderCases cobrem o lembrete de queda aberta e o reconhecimento.
func reminderCases() []conformanceCase {
	return []conformanceCase{
		{"ReminderIntervalRoundTripsOnMonitor", testReminderIntervalRoundTripsOnMonitor},
		{"ReminderIntervalRoundTripsOnGroup", testReminderIntervalRoundTripsOnGroup},
		{"IncidentGetByID", testIncidentGetByID},
		{"IncidentAcknowledgeRecordsWhoAndWhen", testIncidentAcknowledgeRecordsWhoAndWhen},
		{"IncidentAcknowledgeKeepsTheFirst", testIncidentAcknowledgeKeepsTheFirst},
		{"IncidentAcknowledgeRefusesResolved", testIncidentAcknowledgeRefusesResolved},
		{"IncidentAcknowledgeMissingIsNotFound", testIncidentAcknowledgeMissingIsNotFound},
		{"IncidentMarkRemindedIsConditional", testIncidentMarkRemindedIsConditional},
		{"IncidentMarkRemindedStopsAfterAcknowledge", testIncidentMarkRemindedStopsAfterAcknowledge},
	}
}

func durationPtr(d time.Duration) *time.Duration { return &d }

// Nulo é "herda" e zero é "desligado". Um banco que devolvesse zero no
// lugar do nulo desligaria o lembrete de todo monitor que nunca escolheu.
func testReminderIntervalRoundTripsOnMonitor(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	for _, quer := range []*time.Duration{nil, durationPtr(0), durationPtr(30 * time.Minute)} {
		m := newMonitor("api")
		m.ReminderInterval = quer
		if err := s.Monitors().Create(ctx, &m); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.Monitors().Get(ctx, m.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !sameDuration(got.ReminderInterval, quer) {
			t.Errorf("criado com %s, lido %s", fmtDuration(quer), fmtDuration(got.ReminderInterval))
		}

		got.ReminderInterval = durationPtr(time.Hour)
		if err := s.Monitors().Update(ctx, got); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got, err = s.Monitors().Get(ctx, m.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !sameDuration(got.ReminderInterval, durationPtr(time.Hour)) {
			t.Errorf("depois do Update: %s, quer 1h", fmtDuration(got.ReminderInterval))
		}
		if err := s.Monitors().Delete(ctx, m.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
}

func testReminderIntervalRoundTripsOnGroup(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := domain.Group{Name: "RPA", Slug: "rpa", ReminderInterval: durationPtr(time.Hour)}
	if err := s.Groups().Create(ctx, &g); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Groups().Get(ctx, g.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !sameDuration(got.ReminderInterval, durationPtr(time.Hour)) {
		t.Errorf("lido %s, quer 1h", fmtDuration(got.ReminderInterval))
	}

	got.ReminderInterval = nil
	if err := s.Groups().Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	lista, err := s.Groups().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(lista) != 1 || lista[0].ReminderInterval != nil {
		t.Errorf("depois de voltar a herdar: %+v", lista)
	}
}

func testIncidentGetByID(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	i := domain.Incident{MonitorID: id, StartedAt: epoch, Cause: "status 502"}
	if err := s.Incidents().Open(ctx, &i); err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := s.Incidents().Get(ctx, i.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MonitorID != id || got.Cause != "status 502" || got.Acknowledgement != nil || got.RemindedAt != nil {
		t.Errorf("Get = %+v", got)
	}
	if _, err := s.Incidents().Get(ctx, i.ID+100); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get de inexistente: %v, quer ErrNotFound", err)
	}
}

func mustOpenIncident(t *testing.T, s store.Store, monitor string) domain.Incident {
	t.Helper()
	i := domain.Incident{MonitorID: mustCreateMonitor(t, s, monitor), StartedAt: epoch}
	if err := s.Incidents().Open(context.Background(), &i); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return i
}

func testIncidentAcknowledgeRecordsWhoAndWhen(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	i := mustOpenIncident(t, s, "api")

	ack := domain.Acknowledgement{By: "ana", At: epoch.Add(time.Hour), Note: "reiniciando a fila"}
	got, err := s.Incidents().Acknowledge(ctx, i.ID, ack)
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if got.Acknowledgement == nil || !sameAck(*got.Acknowledgement, ack) {
		t.Fatalf("Acknowledge devolveu %+v, quer %+v", got.Acknowledgement, ack)
	}

	lida, err := s.Incidents().Current(ctx, i.MonitorID)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if lida.Acknowledgement == nil || !sameAck(*lida.Acknowledgement, ack) {
		t.Errorf("Current depois do reconhecimento: %+v", lida.Acknowledgement)
	}
}

// Dois cliques ao mesmo tempo, de duas pessoas, não podem fazer o
// segundo apagar o primeiro: o histórico diria que quem chegou depois
// estava olhando desde antes.
func testIncidentAcknowledgeKeepsTheFirst(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	i := mustOpenIncident(t, s, "api")

	if _, err := s.Incidents().Acknowledge(ctx, i.ID, domain.Acknowledgement{By: "ana", At: epoch}); err != nil {
		t.Fatalf("primeiro: %v", err)
	}
	got, err := s.Incidents().Acknowledge(ctx, i.ID, domain.Acknowledgement{By: "bruno", At: epoch.Add(time.Minute)})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("segundo: %v, quer ErrConflict", err)
	}
	if got.Acknowledgement == nil || got.Acknowledgement.By != "ana" {
		t.Errorf("o segundo sobrescreveu o primeiro: %+v", got.Acknowledgement)
	}
}

func testIncidentAcknowledgeRefusesResolved(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	i := mustOpenIncident(t, s, "api")
	if err := s.Incidents().Resolve(ctx, i.MonitorID, epoch.Add(time.Minute)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	_, err := s.Incidents().Acknowledge(ctx, i.ID, domain.Acknowledgement{By: "ana", At: epoch.Add(time.Hour)})
	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("reconhecer encerrado: %v, quer ErrConflict", err)
	}
}

func testIncidentAcknowledgeMissingIsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)
	_, err := s.Incidents().Acknowledge(context.Background(), 404, domain.Acknowledgement{By: "ana", At: epoch})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("reconhecer inexistente: %v, quer ErrNotFound", err)
	}
}

// A condição sobre o lembrete anterior é o que impede dois ciclos do laço
// que se atropelam de enviarem o mesmo lembrete duas vezes.
func testIncidentMarkRemindedIsConditional(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	i := mustOpenIncident(t, s, "api")

	primeiro := epoch.Add(30 * time.Minute)
	if ok, err := s.Incidents().MarkReminded(ctx, i.ID, nil, primeiro); err != nil || !ok {
		t.Fatalf("primeiro lembrete: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Incidents().MarkReminded(ctx, i.ID, nil, primeiro.Add(time.Second)); err != nil || ok {
		t.Errorf("o mesmo lembrete registrado duas vezes: ok=%v err=%v", ok, err)
	}

	segundo := primeiro.Add(30 * time.Minute)
	if ok, err := s.Incidents().MarkReminded(ctx, i.ID, &primeiro, segundo); err != nil || !ok {
		t.Fatalf("segundo lembrete: ok=%v err=%v", ok, err)
	}
	got, err := s.Incidents().Get(ctx, i.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RemindedAt == nil || !got.RemindedAt.Equal(segundo) {
		t.Errorf("RemindedAt = %v, quer %v", got.RemindedAt, segundo)
	}
}

func testIncidentMarkRemindedStopsAfterAcknowledge(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	i := mustOpenIncident(t, s, "api")

	if _, err := s.Incidents().Acknowledge(ctx, i.ID, domain.Acknowledgement{By: "ana", At: epoch}); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if ok, err := s.Incidents().MarkReminded(ctx, i.ID, nil, epoch.Add(time.Hour)); err != nil || ok {
		t.Errorf("incidente reconhecido aceitou lembrete: ok=%v err=%v", ok, err)
	}

	outro := mustOpenIncident(t, s, "front")
	if err := s.Incidents().Resolve(ctx, outro.MonitorID, epoch.Add(time.Minute)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok, err := s.Incidents().MarkReminded(ctx, outro.ID, nil, epoch.Add(time.Hour)); err != nil || ok {
		t.Errorf("incidente encerrado aceitou lembrete: ok=%v err=%v", ok, err)
	}
}

func sameAck(a, b domain.Acknowledgement) bool {
	return a.By == b.By && a.Note == b.Note && a.At.Equal(b.At)
}

func sameDuration(a, b *time.Duration) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func fmtDuration(d *time.Duration) string {
	if d == nil {
		return "nulo"
	}
	return d.String()
}
