package incident_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// Canal vinculado ao grupo vale para os monitores dele sem vínculo
// direto. É o que permite "avisar o time do RPA sobre tudo do RPA" sem
// lembrar de vincular cada monitor novo.
func TestEngineNotifiesThroughTheGroupChannel(t *testing.T) {
	f := newEngine(t)
	ctx := context.Background()

	sistema := domain.Group{Name: "RPA", Slug: "rpa"}
	if err := f.store.Groups().Create(ctx, &sistema); err != nil {
		t.Fatal(err)
	}
	f.monitor.GroupID = &sistema.ID
	if err := f.store.Monitors().Update(ctx, f.monitor); err != nil {
		t.Fatal(err)
	}
	f.engine.Upsert(f.monitor)

	c := domain.Channel{
		Name: "time do rpa", Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
	}
	if err := f.store.Channels().Create(ctx, &c); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Channels().LinkGroup(ctx, sistema.ID, c.ID); err != nil {
		t.Fatal(err)
	}

	f.observe(t, repeatStatus(threshold, domain.StatusDown)...)

	if len(f.dispatch.all()) != 1 {
		t.Fatalf("sent %d notifications, want 1 through the group channel", len(f.dispatch.all()))
	}
	if len(f.dispatch.channels[0]) != 1 {
		t.Errorf("the notification went to %d channels, want the group's one", len(f.dispatch.channels[0]))
	}
}

// Cada mudança confirmada fica no histórico com a causa. Sem isso a aba
// de eventos só teria as batidas cruas, que somem em dias, e a pergunta
// "quando caiu e por quê?" ficaria sem resposta depois da poda.
func TestEngineRecordsTransitionsWithCause(t *testing.T) {
	f := newEngine(t)

	f.observe(t, append(repeatStatus(threshold, domain.StatusDown), repeatStatus(threshold, domain.StatusUp)...)...)

	got, err := f.store.Transitions().List(context.Background(), store.TransitionFilter{MonitorID: f.monitor.ID})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("recorded %d transitions, want down and up: %+v", len(got), got)
	}
	queda, volta := got[1], got[0]
	if queda.To != domain.StatusDown || queda.Cause == "" {
		t.Errorf("the outage transition = %+v, want down with a cause", queda)
	}
	if volta.From != domain.StatusDown || volta.To != domain.StatusUp {
		t.Errorf("the recovery transition = %+v, want down → up", volta)
	}
}

// O aviso leva o caminho do grupo do monitor. Monitores se chamam "api" e
// "front" dentro de grupos, e sem o grupo o aviso não diz qual caiu.
func TestEngineNotificationCarriesTheGroupPath(t *testing.T) {
	f := newEngine(t)
	ctx := context.Background()

	sistema := domain.Group{Name: "RPA", Slug: "rpa"}
	if err := f.store.Groups().Create(ctx, &sistema); err != nil {
		t.Fatal(err)
	}
	ambiente := domain.Group{Name: "prod", Slug: "prod", ParentID: &sistema.ID}
	if err := f.store.Groups().Create(ctx, &ambiente); err != nil {
		t.Fatal(err)
	}
	f.monitor.GroupID = &ambiente.ID
	if err := f.store.Monitors().Update(ctx, f.monitor); err != nil {
		t.Fatal(err)
	}
	f.engine.Upsert(f.monitor)
	f.linkChannel(t)

	f.observe(t, repeatStatus(threshold, domain.StatusDown)...)

	enviados := f.dispatch.all()
	if len(enviados) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(enviados))
	}
	if enviados[0].Group != "RPA / prod" {
		t.Errorf("Group = %q, want \"RPA / prod\"", enviados[0].Group)
	}
}

func TestEngineNotificationWithoutGroupHasEmptyPath(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)

	f.observe(t, repeatStatus(threshold, domain.StatusDown)...)

	enviados := f.dispatch.all()
	if len(enviados) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(enviados))
	}
	if enviados[0].Group != "" {
		t.Errorf("Group = %q, want vazio para monitor sem grupo", enviados[0].Group)
	}
}
