package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/rollup"
	"github.com/bernardojoao/upwatch/internal/store"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
)

// retentionStart é o começo da simulação; o relógio falso avança a partir
// daqui um dia por vez.
var retentionStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// dbBytes soma o arquivo principal e o diário WAL.
func dbBytes(t *testing.T, path string) int64 {
	t.Helper()

	var total int64
	for _, suffix := range []string{"", "-wal"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("Stat returned unexpected error: %v", err)
		}
		total += info.Size()
	}
	return total
}

// A promessa central do projeto, medida em vez de afirmada: com a cascata
// de retenção o banco estabiliza num tamanho em vez de crescer com o
// tempo. É a diferença para o Uptime Kuma, cujos mantenedores declaram um
// teto prático de ~500 monitores ou ~1,5 GB e recomendam ao usuário
// monitorar menos.
func TestRetentionKeepsDatabaseFromGrowingWithTime(t *testing.T) {
	if testing.Short() {
		t.Skip("simulação longa; pulada em -short")
	}

	const (
		monitors      = 5
		checksPerHour = 60 // um check por minuto
		days          = 30
	)

	path := filepath.Join(t.TempDir(), "upwatch.db")
	st, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	ids := make([]int64, 0, monitors)
	for i := 0; i < monitors; i++ {
		m := domain.Monitor{
			Name: fmt.Sprintf("serviço-%02d", i), Type: domain.MonitorHTTP,
			Target: "https://example.com/health", Interval: time.Minute,
			Timeout: 10 * time.Second, ConfirmationThreshold: 3, Enabled: true,
		}
		if err := st.Monitors().Create(ctx, &m); err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		ids = append(ids, m.ID)
	}

	fake := clock.NewFake(retentionStart)
	worker := rollup.NewWorker(st, rollup.Options{
		Clock: fake,
		Retention: rollup.Retention{
			Raw:    3 * 24 * time.Hour,
			Hourly: 30 * 24 * time.Hour,
			Daily:  365 * 24 * time.Hour,
		},
	})

	var sizeAfterWarmup, sizeAtEnd int64

	for day := 0; day < days; day++ {
		dayStart := retentionStart.Add(time.Duration(day) * 24 * time.Hour)

		for hour := 0; hour < 24; hour++ {
			batch := make([]domain.Heartbeat, 0, monitors*checksPerHour)
			for _, id := range ids {
				for c := 0; c < checksPerHour; c++ {
					ts := dayStart.
						Add(time.Duration(hour) * time.Hour).
						Add(time.Duration(c) * time.Minute)
					status := domain.StatusUp
					if c%97 == 0 { // uma falha esporádica, para o uptime não ser trivial
						status = domain.StatusDown
					}
					batch = append(batch, domain.Heartbeat{
						MonitorID: id, Timestamp: ts, Status: status, LatencyMS: int64(80 + c%120),
					})
				}
			}
			if err := st.WriteHeartbeats(ctx, batch); err != nil {
				t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
			}
		}

		// Avança o relógio para o fim do dia e roda o ciclo de manutenção.
		fake.Advance(24 * time.Hour)
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce on day %d returned unexpected error: %v", day, err)
		}

		// O décimo dia já passou da janela de dado cru, então o regime
		// permanente começou: daí em diante o tamanho não deve subir.
		if day == 9 {
			sizeAfterWarmup = dbBytes(t, path)
		}
	}
	sizeAtEnd = dbBytes(t, path)

	t.Logf("%d monitores, %d dias, check por minuto", monitors, days)
	t.Logf("banco no dia 10: %.2f MB", float64(sizeAfterWarmup)/(1<<20))
	t.Logf("banco no dia %d: %.2f MB", days, float64(sizeAtEnd)/(1<<20))

	// Sem a cascata, vinte dias adicionais de dado cru dobrariam o banco.
	// Toleramos crescimento pequeno: os agregados horários e diários de
	// fato acumulam, só que numa fração do volume.
	maxGrowth := float64(sizeAfterWarmup) * 1.5
	if float64(sizeAtEnd) > maxGrowth {
		t.Errorf("database grew from %d to %d bytes over %d extra days, want it to stabilise",
			sizeAfterWarmup, sizeAtEnd, days-10)
	}

	// E o histórico longo continua consultável, que é o ponto de tudo isso.
	daily, err := st.QueryRollups(ctx, store.RollupQuery{
		MonitorID:  ids[0],
		Resolution: domain.ResolutionDaily,
		Range:      store.TimeRange{From: retentionStart, To: retentionStart.Add(days * 24 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(daily) < days-2 { // o último dia pode ainda estar aberto
		t.Errorf("kept %d daily rollups over %d days, want the long history to survive", len(daily), days)
	}
	for _, r := range daily {
		if r.Total == 0 {
			t.Errorf("daily rollup at %v has no samples", r.BucketStart)
		}
		if r.LatencyP95MS == 0 {
			t.Errorf("daily rollup at %v lost its percentiles", r.BucketStart)
		}
	}

	// O dado cru antigo saiu: é ele que faria o banco crescer sem limite.
	oldest, ok, err := st.OldestHeartbeat(ctx)
	if err != nil {
		t.Fatalf("OldestHeartbeat returned unexpected error: %v", err)
	}
	if ok {
		cutoff := retentionStart.Add(days * 24 * time.Hour).Add(-4 * 24 * time.Hour)
		if oldest.Before(cutoff) {
			t.Errorf("oldest raw heartbeat is %v, want nothing older than %v", oldest, cutoff)
		}
	}
}

// Com a janela de três meses que o operador tende a pedir, o gráfico longo
// precisa continuar existindo mesmo depois de o dado cru ser descartado.
func TestRetentionKeepsThreeMonthHistoryWithoutRawData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upwatch.db")
	st, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	m := domain.Monitor{
		Name: "api", Type: domain.MonitorHTTP, Target: "https://example.com",
		Interval: time.Minute, Timeout: 10 * time.Second,
		ConfirmationThreshold: 3, Enabled: true,
	}
	if err := st.Monitors().Create(ctx, &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	// Dois dias de batidas, encerrados.
	for h := 0; h < 48; h++ {
		batch := make([]domain.Heartbeat, 0, 60)
		for c := 0; c < 60; c++ {
			batch = append(batch, domain.Heartbeat{
				MonitorID: m.ID,
				Timestamp: retentionStart.Add(time.Duration(h)*time.Hour + time.Duration(c)*time.Minute),
				Status:    domain.StatusUp,
				LatencyMS: int64(100 + c),
			})
		}
		if err := st.WriteHeartbeats(ctx, batch); err != nil {
			t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
		}
	}

	fake := clock.NewFake(retentionStart.Add(72 * time.Hour))
	worker := rollup.NewWorker(st, rollup.Options{
		Clock: fake,
		Retention: rollup.Retention{
			Raw:    time.Hour, // agressiva: tudo o que foi semeado já expirou
			Hourly: 90 * 24 * time.Hour,
			Daily:  90 * 24 * time.Hour,
		},
	})
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}

	if _, ok, _ := st.OldestHeartbeat(ctx); ok {
		t.Error("raw heartbeats survived a one-hour retention window, want them all pruned")
	}

	hourly, err := st.QueryRollups(ctx, store.RollupQuery{
		MonitorID:  m.ID,
		Resolution: domain.ResolutionHourly,
		Range:      store.TimeRange{From: retentionStart, To: retentionStart.Add(72 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	if len(hourly) != 48 {
		t.Fatalf("kept %d hourly rollups, want 48: the raw data is gone but the history must remain", len(hourly))
	}

	// Percentis exatos sobreviveram à perda do dado cru.
	for _, r := range hourly {
		if r.Total != 60 {
			t.Errorf("hourly rollup at %v has Total %d, want 60", r.BucketStart, r.Total)
		}
		// Latências de 100 a 159 em 60 amostras: posto mais próximo dá
		// ceil(0,95 × 60) = 57, ou seja o 57º valor ordenado, que é 156.
		if r.LatencyP95MS != 156 {
			t.Errorf("hourly rollup at %v has p95 %v, want 156", r.BucketStart, r.LatencyP95MS)
		}
	}
}
