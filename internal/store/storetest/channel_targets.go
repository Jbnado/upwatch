package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// channelTargetCases cobrem a troca dos vínculos de um canal em lote, que
// é o que o formulário do canal grava ao salvar.
func channelTargetCases() []conformanceCase {
	return []conformanceCase{
		{"ChannelTargetsReplaceTheSet", testChannelTargetsReplaceTheSet},
		{"ChannelCreateWithTargetsRollsBack", testChannelCreateWithTargetsRollsBack},
		{"ChannelUpdateWithTargetsRollsBack", testChannelUpdateWithTargetsRollsBack},
		{"ChannelSetTargetsRollsBack", testChannelSetTargetsRollsBack},
		{"ChannelTargetsFollowSelection", testChannelTargetsFollowSelection},
	}
}

func novoCanal(name string) domain.Channel {
	return domain.Channel{
		Name: name, Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
	}
}

func alvosDe(t *testing.T, s store.Store, channelID int64) store.ChannelTargets {
	t.Helper()
	todos, err := s.Channels().Targets(context.Background(), nil)
	if err != nil {
		t.Fatalf("Targets returned unexpected error: %v", err)
	}
	return todos[channelID]
}

func mesmosAlvos(got, want store.ChannelTargets) bool {
	norm := func(v []int64) []int64 {
		if len(v) == 0 {
			return nil
		}
		return v
	}
	return reflect.DeepEqual(norm(got.GroupIDs), norm(want.GroupIDs)) &&
		reflect.DeepEqual(norm(got.MonitorIDs), norm(want.MonitorIDs))
}

// Gravar o conjunto troca o anterior inteiro, e o grupo gravado vale para
// os monitores dele na hora do aviso.
func testChannelTargetsReplaceTheSet(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	vazio := mustCreateGroup(t, s, "Vazio", nil)
	api := mustCreateMonitorIn(t, s, "api", &rpa.ID)
	solto := mustCreateMonitorIn(t, s, "solto", nil)

	c := novoCanal("plantão")
	inicial := store.ChannelTargets{GroupIDs: []int64{vazio.ID}, MonitorIDs: []int64{solto}}
	if err := s.Channels().CreateWithTargets(ctx, &c, inicial); err != nil {
		t.Fatalf("CreateWithTargets returned unexpected error: %v", err)
	}
	if got := alvosDe(t, s, c.ID); !mesmosAlvos(got, inicial) {
		t.Errorf("Targets after create = %+v, want %+v", got, inicial)
	}

	troca := store.ChannelTargets{GroupIDs: []int64{rpa.ID}}
	if err := s.Channels().SetTargets(ctx, c.ID, troca); err != nil {
		t.Fatalf("SetTargets returned unexpected error: %v", err)
	}
	if got := alvosDe(t, s, c.ID); !mesmosAlvos(got, troca) {
		t.Errorf("Targets after SetTargets = %+v, want %+v", got, troca)
	}
	destinos, err := s.Channels().Destinations(ctx, api)
	if err != nil {
		t.Fatalf("Destinations returned unexpected error: %v", err)
	}
	if len(destinos) != 1 || destinos[0].ID != c.ID {
		t.Errorf("Destinations(api) = %+v, want the channel linked through the group", destinos)
	}

	c.Name = "plantão de infra"
	if err := s.Channels().UpdateWithTargets(ctx, c, store.ChannelTargets{}); err != nil {
		t.Fatalf("UpdateWithTargets returned unexpected error: %v", err)
	}
	if got := alvosDe(t, s, c.ID); !mesmosAlvos(got, store.ChannelTargets{}) {
		t.Errorf("Targets after emptying = %+v, want none", got)
	}
	if got, _ := s.Channels().Get(ctx, c.ID); got.Name != "plantão de infra" {
		t.Errorf("Name after UpdateWithTargets = %q, want the new one", got.Name)
	}
}

// Um monitor que não existe no meio do conjunto desfaz tudo: o canal não
// pode nascer sem parte dos vínculos que a pessoa pediu.
func testChannelCreateWithTargetsRollsBack(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	c := novoCanal("plantão")
	err := s.Channels().CreateWithTargets(ctx, &c,
		store.ChannelTargets{GroupIDs: []int64{rpa.ID}, MonitorIDs: []int64{9999}})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateWithTargets err = %v, want ErrNotFound", err)
	}

	canais, err := s.Channels().List(ctx)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(canais) != 0 {
		t.Errorf("List after failed create = %+v, want no channel", canais)
	}
	if todos, _ := s.Channels().Targets(ctx, nil); len(todos) != 0 {
		t.Errorf("Targets after failed create = %+v, want none", todos)
	}
}

func testChannelUpdateWithTargetsRollsBack(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	c := novoCanal("plantão")
	antes := store.ChannelTargets{GroupIDs: []int64{rpa.ID}}
	if err := s.Channels().CreateWithTargets(ctx, &c, antes); err != nil {
		t.Fatalf("CreateWithTargets returned unexpected error: %v", err)
	}

	c.Name = "outro nome"
	err := s.Channels().UpdateWithTargets(ctx, c, store.ChannelTargets{GroupIDs: []int64{9999}})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateWithTargets err = %v, want ErrNotFound", err)
	}
	if got, _ := s.Channels().Get(ctx, c.ID); got.Name != "plantão" {
		t.Errorf("Name after failed update = %q, want the old one", got.Name)
	}
	if got := alvosDe(t, s, c.ID); !mesmosAlvos(got, antes) {
		t.Errorf("Targets after failed update = %+v, want %+v", got, antes)
	}
}

func testChannelSetTargetsRollsBack(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	outro := mustCreateGroup(t, s, "Outro", nil)
	c := novoCanal("plantão")
	antes := store.ChannelTargets{GroupIDs: []int64{rpa.ID}}
	if err := s.Channels().CreateWithTargets(ctx, &c, antes); err != nil {
		t.Fatalf("CreateWithTargets returned unexpected error: %v", err)
	}

	err := s.Channels().SetTargets(ctx, c.ID,
		store.ChannelTargets{GroupIDs: []int64{outro.ID}, MonitorIDs: []int64{9999}})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetTargets err = %v, want ErrNotFound", err)
	}
	if got := alvosDe(t, s, c.ID); !mesmosAlvos(got, antes) {
		t.Errorf("Targets after failed SetTargets = %+v, want %+v", got, antes)
	}

	if err := s.Channels().SetTargets(ctx, 9999, store.ChannelTargets{}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetTargets on a missing channel err = %v, want ErrNotFound", err)
	}
}

// A seleção recorta o que a conta enxerga: grupo de fora e monitor de
// fora não aparecem, nem pela contagem.
func testChannelTargetsFollowSelection(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	ret := mustCreateGroup(t, s, "Retaguarda", nil)
	rpa := mustCreateGroup(t, s, "RPA", nil)
	dentro := mustCreateMonitorIn(t, s, "bff", &ret.ID)
	fora := mustCreateMonitorIn(t, s, "erp", &rpa.ID)
	solto := mustCreateMonitorIn(t, s, "solto", nil)

	c := novoCanal("plantão")
	tudo := store.ChannelTargets{GroupIDs: []int64{ret.ID, rpa.ID}, MonitorIDs: []int64{dentro, fora, solto}}
	if err := s.Channels().CreateWithTargets(ctx, &c, tudo); err != nil {
		t.Fatalf("CreateWithTargets returned unexpected error: %v", err)
	}
	so := mustCreateChannel(t, s, "só de fora")
	if err := s.Channels().SetTargets(ctx, so.ID, store.ChannelTargets{GroupIDs: []int64{rpa.ID}}); err != nil {
		t.Fatalf("SetTargets returned unexpected error: %v", err)
	}

	got, err := s.Channels().Targets(ctx, &store.GroupSelection{IDs: []int64{ret.ID}})
	if err != nil {
		t.Fatalf("Targets returned unexpected error: %v", err)
	}
	want := store.ChannelTargets{GroupIDs: []int64{ret.ID}, MonitorIDs: []int64{dentro}}
	if !mesmosAlvos(got[c.ID], want) {
		t.Errorf("Targets(Retaguarda)[canal] = %+v, want %+v", got[c.ID], want)
	}
	if _, ok := got[so.ID]; ok {
		t.Errorf("Targets(Retaguarda) shows a channel linked only outside: %+v", got[so.ID])
	}

	semGrupo, err := s.Channels().Targets(ctx, &store.GroupSelection{Ungrouped: true})
	if err != nil {
		t.Fatalf("Targets returned unexpected error: %v", err)
	}
	if want := (store.ChannelTargets{MonitorIDs: []int64{solto}}); !mesmosAlvos(semGrupo[c.ID], want) {
		t.Errorf("Targets(sem grupo)[canal] = %+v, want %+v", semGrupo[c.ID], want)
	}

	if todos := alvosDe(t, s, c.ID); !mesmosAlvos(todos, tudo) {
		t.Errorf("Targets(nil)[canal] = %+v, want %+v", todos, tudo)
	}
}
