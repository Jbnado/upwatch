// Package storetest é a suíte de conformidade que toda implementação de
// store precisa passar.
//
// Existe para que "storage plugável" seja verificável e não promessa: a
// mesma bateria roda contra SQLite, PostgreSQL e qualquer backend futuro.
// Um driver que não passe aqui não entra.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// Factory devolve uma Store vazia e já migrada, descartada ao fim do teste.
type Factory func(t *testing.T) store.Store

// epoch é o instante de referência das amostras. Truncado ao milissegundo
// porque é essa a granularidade gravada; usar precisão maior faria as
// comparações falharem por arredondamento, não por defeito real.
var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// conformanceCase é um caso da suíte.
type conformanceCase struct {
	name string
	run  func(*testing.T, Factory)
}

// RunConformance executa a suíte inteira contra a implementação fornecida.
func RunConformance(t *testing.T, newStore Factory) {
	t.Helper()

	cases := append(monitoringCases(), authCases()...)
	cases = append(cases, incidentCases()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.run(t, newStore) })
	}
}

// monitoringCases cobre monitores, batidas, agregados e retenção.
func monitoringCases() []conformanceCase {
	return []conformanceCase{
		{"MonitorCreateAssignsID", testMonitorCreateAssignsID},
		{"MonitorCreateSetsTimestamps", testMonitorCreateSetsTimestamps},
		{"MonitorRoundTripsAllFields", testMonitorRoundTripsAllFields},
		{"MonitorGetMissingReturnsNotFound", testMonitorGetMissingReturnsNotFound},
		{"MonitorDuplicateNameReturnsConflict", testMonitorDuplicateNameReturnsConflict},
		{"MonitorUpdatePersistsChanges", testMonitorUpdatePersistsChanges},
		{"MonitorUpdateMissingReturnsNotFound", testMonitorUpdateMissingReturnsNotFound},
		{"MonitorDeleteRemovesIt", testMonitorDeleteRemovesIt},
		{"MonitorDeleteMissingReturnsNotFound", testMonitorDeleteMissingReturnsNotFound},
		{"MonitorDeleteCascadesHeartbeats", testMonitorDeleteCascadesHeartbeats},
		{"MonitorDeleteCascadesRollups", testMonitorDeleteCascadesRollups},
		{"MonitorRoundTripsCheckerConfig", testMonitorRoundTripsCheckerConfig},
		{"MonitorDefaultsToEmptyConfig", testMonitorDefaultsToEmptyConfig},
		{"MonitorListPaginatesStably", testMonitorListPaginatesStably},
		{"MonitorListFiltersByEnabled", testMonitorListFiltersByEnabled},
		{"MonitorListCapsLimit", testMonitorListCapsLimit},

		{"HeartbeatWriteAndQuery", testHeartbeatWriteAndQuery},
		{"HeartbeatQueryIsHalfOpenRange", testHeartbeatQueryIsHalfOpenRange},
		{"HeartbeatWriteFillsDefaultProbeID", testHeartbeatWriteFillsDefaultProbeID},
		{"HeartbeatQueryFiltersByProbe", testHeartbeatQueryFiltersByProbe},
		{"HeartbeatAllStatusesRoundTrip", testHeartbeatAllStatusesRoundTrip},
		{"HeartbeatWriteEmptyBatchIsNoop", testHeartbeatWriteEmptyBatchIsNoop},
		{"HeartbeatWriteLargeBatch", testHeartbeatWriteLargeBatch},
		{"HeartbeatQueryEmptyRangeReturnsNothing", testHeartbeatQueryEmptyRangeReturnsNothing},
		{"HeartbeatQueryRespectsLimit", testHeartbeatQueryRespectsLimit},
		{"HeartbeatQueryReturnsChronologicalOrder", testHeartbeatQueryReturnsChronologicalOrder},
		{"StreamHeartbeatsIgnoresPaginationCap", testStreamHeartbeatsIgnoresPaginationCap},
		{"StreamHeartbeatsRespectsRange", testStreamHeartbeatsRespectsRange},
		{"StreamHeartbeatsIsChronological", testStreamHeartbeatsIsChronological},
		{"StreamHeartbeatsPropagatesCallbackError", testStreamHeartbeatsPropagatesCallbackError},

		{"RollupWriteAndQuery", testRollupWriteAndQuery},
		{"RollupWriteIsIdempotent", testRollupWriteIsIdempotent},
		{"RollupQueryFiltersByResolution", testRollupQueryFiltersByResolution},
		{"RollupRoundTripsPercentiles", testRollupRoundTripsPercentiles},
		{"RollupHoldsCountersBeyondSmallint", testRollupHoldsCountersBeyondSmallint},

		{"PruneHeartbeatsRemovesOlderOnly", testPruneHeartbeatsRemovesOlderOnly},
		{"PruneHeartbeatsReportsCount", testPruneHeartbeatsReportsCount},
		{"PruneRollupsOnlyAffectsGivenResolution", testPruneRollupsOnlyAffectsGivenResolution},

		{"PushStateStartsEmpty", testPushStateStartsEmpty},
		{"PushStateRoundTrips", testPushStateRoundTrips},
		{"PushStateIsPerMonitor", testPushStateIsPerMonitor},
		{"PushStateCascadesOnMonitorDelete", testPushStateCascadesOnMonitorDelete},
		{"OldestHeartbeatOnEmptyStore", testOldestHeartbeatOnEmptyStore},
		{"OldestHeartbeatFindsEarliest", testOldestHeartbeatFindsEarliest},
		{"WatermarkStartsZero", testWatermarkStartsZero},
		{"WatermarkRoundTrips", testWatermarkRoundTrips},
		{"WatermarkIsPerResolution", testWatermarkIsPerResolution},

		{"ConcurrentHeartbeatWrites", testConcurrentHeartbeatWrites},
	}
}

// ---------- helpers ----------

func newMonitor(name string) domain.Monitor {
	return domain.Monitor{
		Name:                  name,
		Type:                  domain.MonitorHTTP,
		Target:                "https://example.com/health",
		Interval:              time.Minute,
		Timeout:               10 * time.Second,
		ConfirmationThreshold: 3,
		Enabled:               true,
	}
}

// mustCreateMonitor cria um monitor e devolve seu ID.
func mustCreateMonitor(t *testing.T, s store.Store, name string) int64 {
	t.Helper()
	m := newMonitor(name)
	if err := s.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
	}
	return m.ID
}

func beat(monitorID int64, offset time.Duration, status domain.Status, latency int64) domain.Heartbeat {
	return domain.Heartbeat{
		MonitorID: monitorID,
		Timestamp: epoch.Add(offset),
		Status:    status,
		LatencyMS: latency,
	}
}

func fullRange() store.TimeRange {
	return store.TimeRange{From: epoch.Add(-24 * time.Hour), To: epoch.Add(24 * time.Hour)}
}

// ---------- monitores ----------

func testMonitorCreateAssignsID(t *testing.T, newStore Factory) {
	s := newStore(t)
	m := newMonitor("api")

	if err := s.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	if m.ID == 0 {
		t.Error("Create left ID as zero, want a generated identifier")
	}
}

func testMonitorCreateSetsTimestamps(t *testing.T, newStore Factory) {
	s := newStore(t)
	m := newMonitor("api")

	if err := s.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	if m.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero after Create")
	}
	if m.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero after Create")
	}
}

func testMonitorRoundTripsAllFields(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	parentID := mustCreateMonitor(t, s, "gateway")

	want := domain.Monitor{
		Name:                  "api de produção",
		Type:                  domain.MonitorDNS,
		Target:                "example.com",
		Interval:              90 * time.Second,
		Timeout:               7 * time.Second,
		ConfirmationThreshold: 5,
		DegradedLatency:       1500 * time.Millisecond,
		ParentID:              &parentID,
		Enabled:               false,
		Tags:                  []string{"prod", "crítico"},
	}
	if err := s.Monitors().Create(ctx, &want); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	got, err := s.Monitors().Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}

	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if got.Type != want.Type {
		t.Errorf("Type = %v, want %v", got.Type, want.Type)
	}
	if got.Target != want.Target {
		t.Errorf("Target = %q, want %q", got.Target, want.Target)
	}
	if got.Interval != want.Interval {
		t.Errorf("Interval = %v, want %v", got.Interval, want.Interval)
	}
	if got.Timeout != want.Timeout {
		t.Errorf("Timeout = %v, want %v", got.Timeout, want.Timeout)
	}
	if got.ConfirmationThreshold != want.ConfirmationThreshold {
		t.Errorf("ConfirmationThreshold = %d, want %d", got.ConfirmationThreshold, want.ConfirmationThreshold)
	}
	if got.DegradedLatency != want.DegradedLatency {
		t.Errorf("DegradedLatency = %v, want %v", got.DegradedLatency, want.DegradedLatency)
	}
	if got.Enabled != want.Enabled {
		t.Errorf("Enabled = %v, want %v", got.Enabled, want.Enabled)
	}
	if got.ParentID == nil || *got.ParentID != parentID {
		t.Errorf("ParentID = %v, want %d", got.ParentID, parentID)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "prod" || got.Tags[1] != "crítico" {
		t.Errorf("Tags = %v, want [prod crítico]", got.Tags)
	}
}

func testMonitorGetMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)

	_, err := s.Monitors().Get(context.Background(), 4242)

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get of missing monitor returned %v, want store.ErrNotFound", err)
	}
}

func testMonitorDuplicateNameReturnsConflict(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	mustCreateMonitor(t, s, "api")

	dup := newMonitor("api")
	err := s.Monitors().Create(ctx, &dup)

	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("Create with duplicate name returned %v, want store.ErrConflict", err)
	}
}

func testMonitorUpdatePersistsChanges(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	m, err := s.Monitors().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	m.Name = "api renomeada"
	m.Interval = 5 * time.Minute
	m.Enabled = false

	if err := s.Monitors().Update(ctx, m); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}

	got, err := s.Monitors().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after update returned unexpected error: %v", err)
	}
	if got.Name != "api renomeada" {
		t.Errorf("Name = %q, want %q", got.Name, "api renomeada")
	}
	if got.Interval != 5*time.Minute {
		t.Errorf("Interval = %v, want %v", got.Interval, 5*time.Minute)
	}
	if got.Enabled {
		t.Error("Enabled = true, want false")
	}
}

func testMonitorUpdateMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)
	m := newMonitor("fantasma")
	m.ID = 4242

	err := s.Monitors().Update(context.Background(), m)

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Update of missing monitor returned %v, want store.ErrNotFound", err)
	}
}

func testMonitorDeleteRemovesIt(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.Monitors().Delete(ctx, id); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	if _, err := s.Monitors().Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after delete returned %v, want store.ErrNotFound", err)
	}
}

func testMonitorDeleteMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)

	err := s.Monitors().Delete(context.Background(), 4242)

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Delete of missing monitor returned %v, want store.ErrNotFound", err)
	}
}

// Sem cascata o histórico vira lixo órfão que nada apaga — e no SQLite a
// cascata só funciona com PRAGMA foreign_keys ligado por conexão, um
// esquecimento clássico que este teste pega.
func testMonitorDeleteCascadesHeartbeats(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{
		beat(id, 0, domain.StatusUp, 100),
		beat(id, time.Minute, domain.StatusUp, 110),
	}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	if err := s.Monitors().Delete(ctx, id); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{MonitorID: id, Range: fullRange()})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("found %d heartbeats after deleting the monitor, want 0", len(got))
	}
}

func testMonitorDeleteCascadesRollups(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteRollups(ctx, []domain.Rollup{{
		MonitorID: id, ProbeID: domain.DefaultProbeID,
		Resolution: domain.ResolutionHourly, BucketStart: epoch,
		Total: 10, Up: 10,
	}}); err != nil {
		t.Fatalf("WriteRollups returned unexpected error: %v", err)
	}

	if err := s.Monitors().Delete(ctx, id); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	got, err := s.QueryRollups(ctx, store.RollupQuery{
		MonitorID: id, Resolution: domain.ResolutionHourly, Range: fullRange(),
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("found %d rollups after deleting the monitor, want 0", len(got))
	}
}

// A configuração específica de cada tipo de check viaja como JSON opaco.
// Guardar uma coluna por opção de cada tipo — o caminho do Uptime Kuma —
// faria a tabela crescer a cada checker novo; aqui o store não precisa
// conhecer nenhum deles.
func testMonitorRoundTripsCheckerConfig(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	m := newMonitor("api")
	m.Config = []byte(`{"method":"POST","expect_status":[200,201],"body_contains":"ok"}`)
	if err := s.Monitors().Create(ctx, &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	got, err := s.Monitors().Get(ctx, m.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(got.Config, &decoded); err != nil {
		t.Fatalf("stored config is not valid JSON (%q): %v", got.Config, err)
	}
	if decoded["method"] != "POST" {
		t.Errorf("config[method] = %v, want POST", decoded["method"])
	}
	if decoded["body_contains"] != "ok" {
		t.Errorf("config[body_contains] = %v, want ok", decoded["body_contains"])
	}
}

// Monitor sem config precisa voltar como JSON vazio válido, não nil: o
// checker faz Unmarshal direto e nil quebraria a decodificação.
func testMonitorDefaultsToEmptyConfig(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	got, err := s.Monitors().Get(ctx, id)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(got.Config, &decoded); err != nil {
		t.Fatalf("default config is not valid JSON (%q): %v", got.Config, err)
	}
	if len(decoded) != 0 {
		t.Errorf("default config = %v, want an empty object", decoded)
	}
}

// Paginação por keyset precisa cobrir todos os registros sem repetir
// nenhum, que é justamente onde OFFSET falha quando há escrita concorrente.
func testMonitorListPaginatesStably(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	const total = 12

	for i := 0; i < total; i++ {
		mustCreateMonitor(t, s, fmt.Sprintf("monitor-%02d", i))
	}

	seen := map[int64]bool{}
	filter := store.MonitorFilter{Page: store.PageFilter{Limit: 5}}
	pages := 0

	for {
		page, err := s.Monitors().List(ctx, filter)
		if err != nil {
			t.Fatalf("List returned unexpected error: %v", err)
		}
		pages++
		if pages > total+2 {
			t.Fatal("pagination did not terminate")
		}
		for _, m := range page.Items {
			if seen[m.ID] {
				t.Errorf("monitor %d returned on more than one page", m.ID)
			}
			seen[m.ID] = true
			filter.Page.AfterID = m.ID
		}
		if !page.HasMore {
			break
		}
		if len(page.Items) == 0 {
			t.Fatal("HasMore = true but the page was empty")
		}
	}

	if len(seen) != total {
		t.Errorf("pagination covered %d monitors, want %d", len(seen), total)
	}
}

func testMonitorListFiltersByEnabled(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	on := newMonitor("ativo")
	on.Enabled = true
	if err := s.Monitors().Create(ctx, &on); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	off := newMonitor("pausado")
	off.Enabled = false
	if err := s.Monitors().Create(ctx, &off); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	enabled := true
	page, err := s.Monitors().List(ctx, store.MonitorFilter{Enabled: &enabled})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}

	if len(page.Items) != 1 {
		t.Fatalf("List returned %d monitors, want 1", len(page.Items))
	}
	if page.Items[0].Name != "ativo" {
		t.Errorf("List returned %q, want %q", page.Items[0].Name, "ativo")
	}
}

// Limite ausente não pode significar "traga tudo".
func testMonitorListCapsLimit(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		mustCreateMonitor(t, s, fmt.Sprintf("m-%d", i))
	}

	page, err := s.Monitors().List(ctx, store.MonitorFilter{
		Page: store.PageFilter{Limit: store.MaxPageSize + 10_000},
	})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}

	if len(page.Items) > store.MaxPageSize {
		t.Errorf("List returned %d items, want at most %d", len(page.Items), store.MaxPageSize)
	}
}

// ---------- heartbeats ----------

func testHeartbeatWriteAndQuery(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	want := beat(id, 0, domain.StatusUp, 123)
	want.Message = "ok"
	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{want}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{MonitorID: id, Range: fullRange()})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("QueryHeartbeats returned %d rows, want 1", len(got))
	}
	if got[0].Status != want.Status {
		t.Errorf("Status = %v, want %v", got[0].Status, want.Status)
	}
	if got[0].LatencyMS != want.LatencyMS {
		t.Errorf("LatencyMS = %d, want %d", got[0].LatencyMS, want.LatencyMS)
	}
	if got[0].Message != want.Message {
		t.Errorf("Message = %q, want %q", got[0].Message, want.Message)
	}
	if !got[0].Timestamp.Equal(want.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", got[0].Timestamp, want.Timestamp)
	}
}

// Janela [From, To): sem o fim exclusivo, buckets adjacentes contariam a
// mesma amostra duas vezes na agregação.
func testHeartbeatQueryIsHalfOpenRange(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{
		beat(id, -time.Second, domain.StatusUp, 1), // antes da janela
		beat(id, 0, domain.StatusUp, 2),            // no início: incluída
		beat(id, time.Minute, domain.StatusUp, 3),  // no fim: excluída
	}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{
		MonitorID: id,
		Range:     store.TimeRange{From: epoch, To: epoch.Add(time.Minute)},
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("QueryHeartbeats returned %d rows, want 1", len(got))
	}
	if got[0].LatencyMS != 2 {
		t.Errorf("returned the heartbeat with latency %d, want the one with 2", got[0].LatencyMS)
	}
}

func testHeartbeatWriteFillsDefaultProbeID(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{beat(id, 0, domain.StatusUp, 10)}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{MonitorID: id, Range: fullRange()})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("QueryHeartbeats returned %d rows, want 1", len(got))
	}
	if got[0].ProbeID != domain.DefaultProbeID {
		t.Errorf("ProbeID = %q, want %q", got[0].ProbeID, domain.DefaultProbeID)
	}
}

func testHeartbeatQueryFiltersByProbe(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	local := beat(id, 0, domain.StatusUp, 10)
	remote := beat(id, time.Second, domain.StatusUp, 20)
	remote.ProbeID = "eu-west"
	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{local, remote}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{
		MonitorID: id, ProbeID: "eu-west", Range: fullRange(),
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("QueryHeartbeats returned %d rows, want 1", len(got))
	}
	if got[0].ProbeID != "eu-west" {
		t.Errorf("ProbeID = %q, want %q", got[0].ProbeID, "eu-west")
	}
}

func testHeartbeatAllStatusesRoundTrip(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	statuses := []domain.Status{
		domain.StatusUnknown, domain.StatusUp, domain.StatusDown, domain.StatusDegraded,
	}
	var batch []domain.Heartbeat
	for i, st := range statuses {
		batch = append(batch, beat(id, time.Duration(i)*time.Minute, st, 0))
	}
	if err := s.WriteHeartbeats(ctx, batch); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{MonitorID: id, Range: fullRange()})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != len(statuses) {
		t.Fatalf("QueryHeartbeats returned %d rows, want %d", len(got), len(statuses))
	}
	for i, want := range statuses {
		if got[i].Status != want {
			t.Errorf("row %d status = %v, want %v", i, got[i].Status, want)
		}
	}
}

// O batch writer faz flush por tempo mesmo sem resultado acumulado; um
// lote vazio não pode virar erro nem transação inútil.
func testHeartbeatWriteEmptyBatchIsNoop(t *testing.T, newStore Factory) {
	s := newStore(t)

	if err := s.WriteHeartbeats(context.Background(), nil); err != nil {
		t.Errorf("WriteHeartbeats(nil) returned unexpected error: %v", err)
	}
}

func testHeartbeatWriteLargeBatch(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	const n = 2000
	batch := make([]domain.Heartbeat, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, beat(id, time.Duration(i)*time.Second, domain.StatusUp, int64(i)))
	}

	if err := s.WriteHeartbeats(ctx, batch); err != nil {
		t.Fatalf("WriteHeartbeats with %d rows returned unexpected error: %v", n, err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{
		MonitorID: id,
		Range:     store.TimeRange{From: epoch, To: epoch.Add(n * time.Second)},
		Limit:     store.MaxPageSize,
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != store.MaxPageSize {
		t.Errorf("QueryHeartbeats returned %d rows, want %d (the cap)", len(got), store.MaxPageSize)
	}
}

func testHeartbeatQueryEmptyRangeReturnsNothing(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{beat(id, 0, domain.StatusUp, 1)}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{
		MonitorID: id,
		Range:     store.TimeRange{From: epoch.Add(time.Hour), To: epoch.Add(2 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("QueryHeartbeats returned %d rows for an empty window, want 0", len(got))
	}
}

func testHeartbeatQueryRespectsLimit(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	var batch []domain.Heartbeat
	for i := 0; i < 20; i++ {
		batch = append(batch, beat(id, time.Duration(i)*time.Second, domain.StatusUp, 1))
	}
	if err := s.WriteHeartbeats(ctx, batch); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{
		MonitorID: id, Range: fullRange(), Limit: 5,
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("QueryHeartbeats returned %d rows, want 5", len(got))
	}
}

// A agregação percorre a janela em ordem; sem ordenação garantida os
// percentis passariam a depender do plano de execução do banco.
func testHeartbeatQueryReturnsChronologicalOrder(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{
		beat(id, 2*time.Minute, domain.StatusUp, 3),
		beat(id, 0, domain.StatusUp, 1),
		beat(id, time.Minute, domain.StatusUp, 2),
	}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{MonitorID: id, Range: fullRange()})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("QueryHeartbeats returned %d rows, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Fatalf("row %d (%v) precedes row %d (%v): results are not chronological",
				i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
}

// A agregação precisa enxergar o bucket inteiro. Um bucket diário com
// check de um segundo tem 86.400 batidas; se o stream respeitasse o teto
// de paginação, os percentis descreveriam apenas as primeiras 500.
func testStreamHeartbeatsIgnoresPaginationCap(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	total := store.MaxPageSize * 3
	batch := make([]domain.Heartbeat, 0, total)
	for i := 0; i < total; i++ {
		batch = append(batch, beat(id, time.Duration(i)*time.Second, domain.StatusUp, int64(i)))
	}
	if err := s.WriteHeartbeats(ctx, batch); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	seen := 0
	err := s.StreamHeartbeats(ctx, id, fullRange(), func(domain.Heartbeat) error {
		seen++
		return nil
	})
	if err != nil {
		t.Fatalf("StreamHeartbeats returned unexpected error: %v", err)
	}

	if seen != total {
		t.Errorf("streamed %d heartbeats, want %d", seen, total)
	}
}

func testStreamHeartbeatsRespectsRange(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{
		beat(id, -time.Second, domain.StatusUp, 1), // antes
		beat(id, 0, domain.StatusUp, 2),            // início inclusivo
		beat(id, 30*time.Second, domain.StatusUp, 3),
		beat(id, time.Minute, domain.StatusUp, 4), // fim exclusivo
	}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	var seen []int64
	err := s.StreamHeartbeats(ctx, id,
		store.TimeRange{From: epoch, To: epoch.Add(time.Minute)},
		func(hb domain.Heartbeat) error {
			seen = append(seen, hb.LatencyMS)
			return nil
		})
	if err != nil {
		t.Fatalf("StreamHeartbeats returned unexpected error: %v", err)
	}

	if len(seen) != 2 || seen[0] != 2 || seen[1] != 3 {
		t.Errorf("streamed latencies %v, want [2 3]", seen)
	}
}

func testStreamHeartbeatsIsChronological(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{
		beat(id, 2*time.Minute, domain.StatusUp, 3),
		beat(id, 0, domain.StatusUp, 1),
		beat(id, time.Minute, domain.StatusUp, 2),
	}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	var last time.Time
	err := s.StreamHeartbeats(ctx, id, fullRange(), func(hb domain.Heartbeat) error {
		if !last.IsZero() && hb.Timestamp.Before(last) {
			t.Errorf("heartbeat at %v followed %v: stream is not chronological", hb.Timestamp, last)
		}
		last = hb.Timestamp
		return nil
	})
	if err != nil {
		t.Fatalf("StreamHeartbeats returned unexpected error: %v", err)
	}
}

// Interromper precisa abortar a varredura, não continuar lendo milhões de
// linhas depois de o consumidor já ter desistido.
func testStreamHeartbeatsPropagatesCallbackError(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	var batch []domain.Heartbeat
	for i := 0; i < 100; i++ {
		batch = append(batch, beat(id, time.Duration(i)*time.Second, domain.StatusUp, 1))
	}
	if err := s.WriteHeartbeats(ctx, batch); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	sentinel := errors.New("chega")
	seen := 0
	err := s.StreamHeartbeats(ctx, id, fullRange(), func(domain.Heartbeat) error {
		seen++
		if seen == 3 {
			return sentinel
		}
		return nil
	})

	if !errors.Is(err, sentinel) {
		t.Errorf("StreamHeartbeats returned %v, want the callback error", err)
	}
	if seen != 3 {
		t.Errorf("callback ran %d times, want the scan to stop at 3", seen)
	}
}

// ---------- rollups ----------

func sampleRollup(monitorID int64, res domain.Resolution, bucket time.Time) domain.Rollup {
	return domain.Rollup{
		MonitorID: monitorID, ProbeID: domain.DefaultProbeID,
		Resolution: res, BucketStart: bucket,
		Total: 100, Up: 90, Down: 5, Degraded: 5,
		LatencySamples: 95,
		LatencyAvgMS:   120.5, LatencyMinMS: 10, LatencyMaxMS: 900,
		LatencyP50MS: 100, LatencyP95MS: 480.25, LatencyP99MS: 850.75,
	}
}

func testRollupWriteAndQuery(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	want := sampleRollup(id, domain.ResolutionHourly, epoch)
	if err := s.WriteRollups(ctx, []domain.Rollup{want}); err != nil {
		t.Fatalf("WriteRollups returned unexpected error: %v", err)
	}

	got, err := s.QueryRollups(ctx, store.RollupQuery{
		MonitorID: id, Resolution: domain.ResolutionHourly, Range: fullRange(),
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("QueryRollups returned %d rows, want 1", len(got))
	}
	if got[0].Total != want.Total || got[0].Up != want.Up || got[0].Down != want.Down {
		t.Errorf("counters = (total %d, up %d, down %d), want (%d, %d, %d)",
			got[0].Total, got[0].Up, got[0].Down, want.Total, want.Up, want.Down)
	}
	if !got[0].BucketStart.Equal(want.BucketStart) {
		t.Errorf("BucketStart = %v, want %v", got[0].BucketStart, want.BucketStart)
	}
}

// Reprocessar um bucket após falha precisa sobrescrever, não duplicar —
// caso contrário uma reexecução inflaria as estatísticas.
func testRollupWriteIsIdempotent(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	first := sampleRollup(id, domain.ResolutionHourly, epoch)
	if err := s.WriteRollups(ctx, []domain.Rollup{first}); err != nil {
		t.Fatalf("first WriteRollups returned unexpected error: %v", err)
	}

	second := first
	second.Total = 200
	second.Up = 200
	second.Down = 0
	if err := s.WriteRollups(ctx, []domain.Rollup{second}); err != nil {
		t.Fatalf("second WriteRollups returned unexpected error: %v", err)
	}

	got, err := s.QueryRollups(ctx, store.RollupQuery{
		MonitorID: id, Resolution: domain.ResolutionHourly, Range: fullRange(),
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("QueryRollups returned %d rows after rewriting the same bucket, want 1", len(got))
	}
	if got[0].Total != 200 || got[0].Down != 0 {
		t.Errorf("counters = (total %d, down %d), want (200, 0): rewrite did not replace the row",
			got[0].Total, got[0].Down)
	}
}

func testRollupQueryFiltersByResolution(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteRollups(ctx, []domain.Rollup{
		sampleRollup(id, domain.ResolutionHourly, epoch),
		sampleRollup(id, domain.ResolutionDaily, epoch),
	}); err != nil {
		t.Fatalf("WriteRollups returned unexpected error: %v", err)
	}

	got, err := s.QueryRollups(ctx, store.RollupQuery{
		MonitorID: id, Resolution: domain.ResolutionDaily, Range: fullRange(),
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("QueryRollups returned %d rows, want 1", len(got))
	}
	if got[0].Resolution != domain.ResolutionDaily {
		t.Errorf("Resolution = %v, want %v", got[0].Resolution, domain.ResolutionDaily)
	}
}

// Percentis são a razão de o rollup existir: perder precisão na ida e
// volta ao banco esvaziaria o diferencial.
func testRollupRoundTripsPercentiles(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	want := sampleRollup(id, domain.ResolutionHourly, epoch)
	if err := s.WriteRollups(ctx, []domain.Rollup{want}); err != nil {
		t.Fatalf("WriteRollups returned unexpected error: %v", err)
	}

	got, err := s.QueryRollups(ctx, store.RollupQuery{
		MonitorID: id, Resolution: domain.ResolutionHourly, Range: fullRange(),
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("QueryRollups returned %d rows, want 1", len(got))
	}

	for _, f := range []struct {
		name      string
		got, want float64
	}{
		{"LatencyAvgMS", got[0].LatencyAvgMS, want.LatencyAvgMS},
		{"LatencyMinMS", got[0].LatencyMinMS, want.LatencyMinMS},
		{"LatencyMaxMS", got[0].LatencyMaxMS, want.LatencyMaxMS},
		{"LatencyP50MS", got[0].LatencyP50MS, want.LatencyP50MS},
		{"LatencyP95MS", got[0].LatencyP95MS, want.LatencyP95MS},
		{"LatencyP99MS", got[0].LatencyP99MS, want.LatencyP99MS},
	} {
		if f.got != f.want {
			t.Errorf("%s = %v, want %v", f.name, f.got, f.want)
		}
	}
	if got[0].LatencySamples != want.LatencySamples {
		t.Errorf("LatencySamples = %d, want %d", got[0].LatencySamples, want.LatencySamples)
	}
}

// Um bucket diário com um check por segundo passa de 86 mil amostras.
// Colunas smallint estourariam silenciosamente.
func testRollupHoldsCountersBeyondSmallint(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	const big = 86_400
	r := sampleRollup(id, domain.ResolutionDaily, epoch)
	r.Total, r.Up, r.Down, r.Degraded = big, big, 0, 0

	if err := s.WriteRollups(ctx, []domain.Rollup{r}); err != nil {
		t.Fatalf("WriteRollups returned unexpected error: %v", err)
	}

	got, err := s.QueryRollups(ctx, store.RollupQuery{
		MonitorID: id, Resolution: domain.ResolutionDaily, Range: fullRange(),
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("QueryRollups returned %d rows, want 1", len(got))
	}
	if got[0].Total != big || got[0].Up != big {
		t.Errorf("counters = (total %d, up %d), want (%d, %d)", got[0].Total, got[0].Up, big, big)
	}
}

// ---------- retenção ----------

func testPruneHeartbeatsRemovesOlderOnly(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{
		beat(id, -2*time.Hour, domain.StatusUp, 1), // antiga: sai
		beat(id, -time.Hour, domain.StatusUp, 2),   // no corte: fica (exclusivo)
		beat(id, 0, domain.StatusUp, 3),            // recente: fica
	}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	if _, err := s.PruneHeartbeats(ctx, epoch.Add(-time.Hour)); err != nil {
		t.Fatalf("PruneHeartbeats returned unexpected error: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{MonitorID: id, Range: fullRange()})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d heartbeats after prune, want 2", len(got))
	}
	for _, hb := range got {
		if hb.Timestamp.Before(epoch.Add(-time.Hour)) {
			t.Errorf("heartbeat at %v survived a prune with cutoff %v", hb.Timestamp, epoch.Add(-time.Hour))
		}
	}
}

func testPruneHeartbeatsReportsCount(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{
		beat(id, -3*time.Hour, domain.StatusUp, 1),
		beat(id, -2*time.Hour, domain.StatusUp, 2),
		beat(id, 0, domain.StatusUp, 3),
	}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	removed, err := s.PruneHeartbeats(ctx, epoch.Add(-time.Hour))
	if err != nil {
		t.Fatalf("PruneHeartbeats returned unexpected error: %v", err)
	}

	if removed != 2 {
		t.Errorf("PruneHeartbeats reported %d rows removed, want 2", removed)
	}
}

// Cada camada tem retenção própria: podar a horária não pode levar junto a
// diária, que é justamente a que sustenta o gráfico de meses.
func testPruneRollupsOnlyAffectsGivenResolution(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	old := epoch.Add(-48 * time.Hour)
	if err := s.WriteRollups(ctx, []domain.Rollup{
		sampleRollup(id, domain.ResolutionHourly, old),
		sampleRollup(id, domain.ResolutionDaily, old),
	}); err != nil {
		t.Fatalf("WriteRollups returned unexpected error: %v", err)
	}

	removed, err := s.PruneRollups(ctx, domain.ResolutionHourly, epoch)
	if err != nil {
		t.Fatalf("PruneRollups returned unexpected error: %v", err)
	}
	if removed != 1 {
		t.Errorf("PruneRollups reported %d rows removed, want 1", removed)
	}

	daily, err := s.QueryRollups(ctx, store.RollupQuery{
		MonitorID: id, Resolution: domain.ResolutionDaily,
		Range: store.TimeRange{From: old.Add(-time.Hour), To: epoch},
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(daily) != 1 {
		t.Errorf("found %d daily rollups after pruning hourly, want 1", len(daily))
	}
}

// ---------- sinal de push ----------

func testPushStateStartsEmpty(t *testing.T, newStore Factory) {
	s := newStore(t)
	id := mustCreateMonitor(t, s, "cron noturno")

	_, ok, err := s.LastPush(context.Background(), id)
	if err != nil {
		t.Fatalf("LastPush returned unexpected error: %v", err)
	}
	if ok {
		t.Error("LastPush reported a signal on a monitor that never pushed, want none")
	}
}

// Reenviar avança o instante em vez de acumular linhas: um cron que bate a
// cada minuto criaria milhares de registros por dia.
func testPushStateRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "cron noturno")

	if err := s.RecordPush(ctx, id, epoch); err != nil {
		t.Fatalf("RecordPush returned unexpected error: %v", err)
	}
	got, ok, err := s.LastPush(ctx, id)
	if err != nil {
		t.Fatalf("LastPush returned unexpected error: %v", err)
	}
	if !ok || !got.Equal(epoch) {
		t.Fatalf("LastPush = (%v, %v), want (%v, true)", got, ok, epoch)
	}

	later := epoch.Add(time.Hour)
	if err := s.RecordPush(ctx, id, later); err != nil {
		t.Fatalf("second RecordPush returned unexpected error: %v", err)
	}
	got, _, err = s.LastPush(ctx, id)
	if err != nil {
		t.Fatalf("LastPush returned unexpected error: %v", err)
	}
	if !got.Equal(later) {
		t.Errorf("LastPush = %v, want %v", got, later)
	}
}

func testPushStateIsPerMonitor(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	first := mustCreateMonitor(t, s, "cron A")
	second := mustCreateMonitor(t, s, "cron B")

	if err := s.RecordPush(ctx, first, epoch); err != nil {
		t.Fatalf("RecordPush returned unexpected error: %v", err)
	}

	if _, ok, _ := s.LastPush(ctx, second); ok {
		t.Error("a push on one monitor showed up on another")
	}
}

func testPushStateCascadesOnMonitorDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "cron noturno")

	if err := s.RecordPush(ctx, id, epoch); err != nil {
		t.Fatalf("RecordPush returned unexpected error: %v", err)
	}
	if err := s.Monitors().Delete(ctx, id); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	if _, ok, err := s.LastPush(ctx, id); err != nil {
		t.Fatalf("LastPush returned unexpected error: %v", err)
	} else if ok {
		t.Error("push state survived the monitor being deleted, want it cascaded away")
	}
}

func testOldestHeartbeatOnEmptyStore(t *testing.T, newStore Factory) {
	s := newStore(t)

	_, ok, err := s.OldestHeartbeat(context.Background())
	if err != nil {
		t.Fatalf("OldestHeartbeat returned unexpected error: %v", err)
	}
	if ok {
		t.Error("OldestHeartbeat reported a heartbeat on an empty store, want none")
	}
}

// É por onde a agregação começa quando não há marca d'água; errar aqui
// deixaria batidas serem podadas sem nunca virarem estatística.
func testOldestHeartbeatFindsEarliest(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	first := mustCreateMonitor(t, s, "primeiro")
	second := mustCreateMonitor(t, s, "segundo")

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{
		beat(first, time.Hour, domain.StatusUp, 1),
		beat(second, -3*time.Hour, domain.StatusUp, 1), // a mais antiga, de outro monitor
		beat(first, 0, domain.StatusUp, 1),
	}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}

	got, ok, err := s.OldestHeartbeat(ctx)
	if err != nil {
		t.Fatalf("OldestHeartbeat returned unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("OldestHeartbeat reported no heartbeat, want the earliest one")
	}
	if want := epoch.Add(-3 * time.Hour); !got.Equal(want) {
		t.Errorf("OldestHeartbeat = %v, want %v", got, want)
	}
}

// ---------- marca d'água ----------

func testWatermarkStartsZero(t *testing.T, newStore Factory) {
	s := newStore(t)

	got, err := s.RollupWatermark(context.Background(), domain.ResolutionHourly)
	if err != nil {
		t.Fatalf("RollupWatermark returned unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("RollupWatermark on a fresh store = %v, want the zero time", got)
	}
}

func testWatermarkRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.SetRollupWatermark(ctx, domain.ResolutionHourly, epoch); err != nil {
		t.Fatalf("SetRollupWatermark returned unexpected error: %v", err)
	}

	got, err := s.RollupWatermark(ctx, domain.ResolutionHourly)
	if err != nil {
		t.Fatalf("RollupWatermark returned unexpected error: %v", err)
	}
	if !got.Equal(epoch) {
		t.Errorf("RollupWatermark = %v, want %v", got, epoch)
	}

	// Reescrever avança a marca em vez de criar uma segunda linha.
	later := epoch.Add(time.Hour)
	if err := s.SetRollupWatermark(ctx, domain.ResolutionHourly, later); err != nil {
		t.Fatalf("second SetRollupWatermark returned unexpected error: %v", err)
	}
	got, err = s.RollupWatermark(ctx, domain.ResolutionHourly)
	if err != nil {
		t.Fatalf("RollupWatermark returned unexpected error: %v", err)
	}
	if !got.Equal(later) {
		t.Errorf("RollupWatermark = %v, want %v", got, later)
	}
}

func testWatermarkIsPerResolution(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	if err := s.SetRollupWatermark(ctx, domain.ResolutionHourly, epoch); err != nil {
		t.Fatalf("SetRollupWatermark returned unexpected error: %v", err)
	}

	got, err := s.RollupWatermark(ctx, domain.ResolutionDaily)
	if err != nil {
		t.Fatalf("RollupWatermark returned unexpected error: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("daily watermark = %v after setting only the hourly one, want the zero time", got)
	}
}

// ---------- concorrência ----------

// O batch writer grava de uma goroutine enquanto a API lê de outra. Um
// backend que serialize mal aqui trava sob carga real.
func testConcurrentHeartbeatWrites(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			batch := make([]domain.Heartbeat, 0, perWriter)
			for i := 0; i < perWriter; i++ {
				offset := time.Duration(w*perWriter+i) * time.Second
				batch = append(batch, beat(id, offset, domain.StatusUp, 1))
			}
			if err := s.WriteHeartbeats(ctx, batch); err != nil {
				errs <- fmt.Errorf("writer %d: %w", w, err)
			}
		}(w)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write failed: %v", err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{
		MonitorID: id, Range: fullRange(), Limit: store.MaxPageSize,
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != writers*perWriter {
		t.Errorf("stored %d heartbeats, want %d", len(got), writers*perWriter)
	}
}
