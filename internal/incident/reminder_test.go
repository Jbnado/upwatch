package incident_test

import (
	"context"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/maintenance"
	"github.com/Jbnado/upwatch/internal/notifier"
)

// quedaConfirmada é quando observe(up, down, down, down) confirma a queda.
var quedaConfirmada = epoch.Add(threshold * time.Minute)

// derruba confirma a queda do monitor do fixture e devolve o incidente.
func (f *engineFixture) derruba(t *testing.T) domain.Incident {
	t.Helper()
	f.observe(t, domain.StatusUp, domain.StatusDown, domain.StatusDown, domain.StatusDown)
	inc, err := f.store.Incidents().Current(context.Background(), f.monitor.ID)
	if err != nil {
		t.Fatalf("setup: a queda não abriu incidente: %v", err)
	}
	return inc
}

func lembretes(d *recordingDispatcher) []notifier.Notification {
	var out []notifier.Notification
	for _, n := range d.all() {
		if n.Reminder {
			out = append(out, n)
		}
	}
	return out
}

func novosLembretes(e *incident.Engine, c clock.Clock, padrao time.Duration) *incident.Reminders {
	return incident.NewReminders(e, incident.ReminderOptions{Clock: c, Default: padrao})
}

// O caso que motivou o recurso: a queda das duas da manhã avisa uma vez,
// ninguém vê, e às oito o canal está calado. Com intervalo de 30 minutos,
// o lembrete sai a cada 30 minutos, com a duração atual — e não antes.
func TestRemindersRepeatWhileTheIncidentIsOpen(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)
	f.derruba(t)

	relogio := clock.NewFake(quedaConfirmada.Add(29 * time.Minute))
	r := novosLembretes(f.engine, relogio, 30*time.Minute)

	r.Tick(context.Background())
	if n := len(lembretes(f.dispatch)); n != 0 {
		t.Fatalf("%d lembrete(s) antes de vencer o intervalo", n)
	}

	relogio.Advance(time.Minute)
	r.Tick(context.Background())
	r.Tick(context.Background())
	got := lembretes(f.dispatch)
	if len(got) != 1 {
		t.Fatalf("%d lembretes ao vencer o intervalo, quer 1", len(got))
	}
	if got[0].Event.Duration != 30*time.Minute || got[0].Status() != "down" {
		t.Errorf("lembrete = %+v, quer down há 30 min", got[0].Event)
	}
	if got[0].Message != "status 502 fora do esperado" {
		t.Errorf("Message = %q, quer a causa da queda", got[0].Message)
	}

	relogio.Advance(30 * time.Minute)
	r.Tick(context.Background())
	got = lembretes(f.dispatch)
	if len(got) != 2 || got[1].Event.Duration != time.Hour {
		t.Errorf("segundo lembrete: %d enviados, último %v", len(got), got[len(got)-1].Event.Duration)
	}
}

// Reconhecer é o que impede o lembrete de virar spam para quem já está
// resolvendo. E só daquele incidente: a queda de outro monitor no mesmo
// canal continua sendo lembrada.
func TestAcknowledgeStopsRemindersOfThatIncidentOnly(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)
	inc := f.derruba(t)

	outro := f.monitor
	outro.ID, outro.Name = 0, "front"
	if err := f.store.Monitors().Create(context.Background(), &outro); err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.engine.Upsert(outro)
	for i := range threshold {
		hb := domain.Heartbeat{MonitorID: outro.ID, Timestamp: epoch.Add(time.Duration(i) * time.Minute), Status: domain.StatusDown}
		if err := f.engine.Submit(context.Background(), hb); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	canais, _ := f.store.Channels().List(context.Background())
	if err := f.store.Channels().Link(context.Background(), outro.ID, canais[0].ID); err != nil {
		t.Fatalf("Link: %v", err)
	}

	if _, err := f.store.Incidents().Acknowledge(context.Background(), inc.ID,
		domain.Acknowledgement{By: "ana", At: quedaConfirmada.Add(time.Minute)}); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	relogio := clock.NewFake(quedaConfirmada.Add(2 * time.Hour))
	novosLembretes(f.engine, relogio, 30*time.Minute).Tick(context.Background())

	got := lembretes(f.dispatch)
	if len(got) != 1 || got[0].Monitor.ID != outro.ID {
		t.Fatalf("lembretes = %+v, quer só o do monitor não reconhecido", got)
	}
}

// A volta continua avisando como sempre, e o incidente encerrado não é
// mais lembrado.
func TestRecoveryStillNotifiesAndEndsReminders(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)
	f.derruba(t)

	relogio := clock.NewFake(quedaConfirmada.Add(time.Hour))
	r := novosLembretes(f.engine, relogio, 30*time.Minute)
	r.Tick(context.Background())

	for i := range threshold {
		hb := domain.Heartbeat{MonitorID: f.monitor.ID, Timestamp: quedaConfirmada.Add(time.Hour + time.Duration(i)*time.Minute), Status: domain.StatusUp}
		if err := f.engine.Submit(context.Background(), hb); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	voltas := 0
	for _, n := range f.dispatch.all() {
		if n.Event.Kind == incident.KindUp && !n.Reminder {
			voltas++
		}
	}
	if voltas != 1 {
		t.Errorf("%d avisos de volta, quer 1", voltas)
	}

	relogio.Advance(2 * time.Hour)
	r.Tick(context.Background())
	if n := len(lembretes(f.dispatch)); n != 1 {
		t.Errorf("%d lembretes, quer só o de antes da volta", n)
	}
}

// Lembrete é opt-in: a instalação sem intervalo em lugar nenhum segue
// avisando uma vez só, como antes do recurso existir. E o zero no monitor
// é escolha que vence o padrão da instalação.
func TestRemindersOffUnlessConfigured(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)
	f.derruba(t)

	relogio := clock.NewFake(quedaConfirmada.Add(24 * time.Hour))
	novosLembretes(f.engine, relogio, 0).Tick(context.Background())
	if n := len(lembretes(f.dispatch)); n != 0 {
		t.Fatalf("%d lembretes sem intervalo configurado", n)
	}

	desligado := time.Duration(0)
	m := f.monitor
	m.ReminderInterval = &desligado
	f.engine.Upsert(m)
	novosLembretes(f.engine, relogio, 30*time.Minute).Tick(context.Background())
	if n := len(lembretes(f.dispatch)); n != 0 {
		t.Errorf("%d lembretes com o monitor desligado", n)
	}

	dez := 10 * time.Minute
	m.ReminderInterval = &dez
	f.engine.Upsert(m)
	novosLembretes(f.engine, relogio, 0).Tick(context.Background())
	if n := len(lembretes(f.dispatch)); n != 1 {
		t.Errorf("%d lembretes com intervalo no monitor, quer 1", n)
	}
}

// O intervalo do grupo vale para os monitores que não escolheram o seu:
// é como se liga o lembrete para o ambiente de produção inteiro.
func TestRemindersInheritTheGroupInterval(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)

	hora := time.Hour
	g := domain.Group{Name: "RPA", Slug: "rpa", ReminderInterval: &hora}
	if err := f.store.Groups().Create(context.Background(), &g); err != nil {
		t.Fatalf("Create group: %v", err)
	}
	m := f.monitor
	m.GroupID = &g.ID
	if err := f.store.Monitors().Update(context.Background(), m); err != nil {
		t.Fatalf("Update: %v", err)
	}
	f.engine.Upsert(m)
	f.derruba(t)

	relogio := clock.NewFake(quedaConfirmada.Add(59 * time.Minute))
	r := novosLembretes(f.engine, relogio, 0)
	r.Tick(context.Background())
	if n := len(lembretes(f.dispatch)); n != 0 {
		t.Fatalf("%d lembretes antes do intervalo do grupo", n)
	}
	relogio.Advance(time.Minute)
	r.Tick(context.Background())
	got := lembretes(f.dispatch)
	if len(got) != 1 || got[0].Group != "RPA" {
		t.Errorf("lembretes = %+v, quer um, com o grupo", got)
	}
}

// Um reinício no meio da madrugada não pode reenviar o lembrete que
// acabou de sair: o último lembrete fica no banco, e um laço novo, com um
// motor novo, parte dele.
func TestRemindersSurviveRestart(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)
	f.derruba(t)

	relogio := clock.NewFake(quedaConfirmada.Add(30 * time.Minute))
	novosLembretes(f.engine, relogio, 30*time.Minute).Tick(context.Background())

	novo := incident.NewEngine(f.sink, f.store, f.dispatch, nil)
	novo.Upsert(f.monitor)
	if err := novo.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	relogio.Advance(10 * time.Minute)
	novosLembretes(novo, relogio, 30*time.Minute).Tick(context.Background())

	if n := len(lembretes(f.dispatch)); n != 1 {
		t.Errorf("%d lembretes depois do reinício, quer só o de antes", n)
	}
}

// A janela de manutenção cala o lembrete da queda que segue aberta
// enquanto durar; acabada a janela, a queda volta a ser lembrada.
func TestRemindersSkipMaintenance(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)
	f.derruba(t)

	ctx := context.Background()
	w := domain.MaintenanceWindow{
		Title: "deploy", StartsAt: quedaConfirmada.Add(30 * time.Minute), EndsAt: quedaConfirmada.Add(90 * time.Minute),
		Recurrence: domain.RecurrenceNone, TimeZone: "UTC", MonitorIDs: []int64{f.monitor.ID},
	}
	if err := f.store.Maintenance().Create(ctx, &w); err != nil {
		t.Fatalf("Maintenance().Create: %v", err)
	}
	cal := maintenance.New(f.store)
	if err := cal.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	f.engine.WithMaintenance(cal)

	relogio := clock.NewFake(quedaConfirmada.Add(time.Hour))
	r := novosLembretes(f.engine, relogio, 30*time.Minute)

	r.Tick(ctx)
	if n := len(lembretes(f.dispatch)); n != 0 {
		t.Fatalf("%d lembretes durante a manutenção", n)
	}
	relogio.Advance(31 * time.Minute)
	r.Tick(ctx)
	if n := len(lembretes(f.dispatch)); n != 1 {
		t.Errorf("%d lembretes depois da manutenção, quer 1", n)
	}
}

// Run revê as quedas no ritmo do relógio, sem esperar batida nova: o
// monitor fora do ar costuma ser justamente o que parou de bater.
func TestRemindersRunOnTheClock(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)
	f.derruba(t)

	relogio := clock.NewFake(quedaConfirmada.Add(29 * time.Minute))
	r := incident.NewReminders(f.engine, incident.ReminderOptions{
		Clock: relogio, Default: 30 * time.Minute, Tick: time.Minute,
	})

	ctx, cancel := context.WithCancel(context.Background())
	feito := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(feito)
	}()

	relogio.BlockUntil(1)
	relogio.Advance(time.Minute)
	relogio.BlockUntil(1)
	cancel()
	<-feito

	if n := len(lembretes(f.dispatch)); n != 1 {
		t.Errorf("%d lembretes depois de um ciclo vencido, quer 1", n)
	}
}

// Dependentes (#36): o filho que já estava fora quando o pai caiu tem
// incidente aberto, mas a causa agora é o pai. Um lembrete por filho
// devolveria a enxurrada que a dependência existe para evitar; sai um, o
// do pai, dizendo quantos estão calados.
func TestRemindersSkipChildrenOfADownParent(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)
	for i := range threshold {
		f.bate(t, f.pai, i, domain.StatusUp)
		f.bate(t, f.filho, i, domain.StatusDown)
	}
	if f.engine.StateOf(f.filho.ID).Status != domain.StatusDown {
		t.Fatal("setup: o filho não confirmou a própria queda")
	}
	f.derrubaPai(t, threshold)

	relogio := clock.NewFake(epoch.Add(3 * time.Hour))
	novosLembretes(f.engine, relogio, 30*time.Minute).Tick(context.Background())

	got := lembretes(f.dispatch)
	if len(got) != 1 || got[0].Monitor.ID != f.pai.ID {
		t.Fatalf("lembretes = %d, quer só o do pai", len(got))
	}
	if got[0].Dependents != 1 {
		t.Errorf("Dependents = %d, quer 1", got[0].Dependents)
	}
}
