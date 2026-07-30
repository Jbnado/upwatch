package domain_test

import (
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Heartbeats gravados sem probe explícito pertencem à instância local.
// Preencher o default na escrita evita string vazia no banco, que
// quebraria o agrupamento quando probes remotos existirem.
func TestHeartbeatNormalizeFillsDefaultProbeID(t *testing.T) {
	hb := domain.Heartbeat{MonitorID: 1, Timestamp: time.Now(), Status: domain.StatusUp}

	hb = hb.Normalize()

	if hb.ProbeID != domain.DefaultProbeID {
		t.Errorf("ProbeID = %q, want %q", hb.ProbeID, domain.DefaultProbeID)
	}
}

func TestHeartbeatNormalizeKeepsExplicitProbeID(t *testing.T) {
	hb := domain.Heartbeat{MonitorID: 1, ProbeID: "eu-west", Timestamp: time.Now()}

	hb = hb.Normalize()

	if hb.ProbeID != "eu-west" {
		t.Errorf("ProbeID = %q, want %q", hb.ProbeID, "eu-west")
	}
}

// Todo timestamp é gravado em UTC; sem isso os buckets de rollup ficariam
// inconsistentes entre instâncias em fusos diferentes.
func TestHeartbeatNormalizeConvertsTimestampToUTC(t *testing.T) {
	saoPaulo := time.FixedZone("America/Sao_Paulo", -3*60*60)
	hb := domain.Heartbeat{MonitorID: 1, Timestamp: time.Date(2026, 7, 28, 22, 30, 0, 0, saoPaulo)}

	hb = hb.Normalize()

	if hb.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp location = %v, want UTC", hb.Timestamp.Location())
	}
	if want := time.Date(2026, 7, 29, 1, 30, 0, 0, time.UTC); !hb.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", hb.Timestamp, want)
	}
}

// Latência de uma batida sem resposta é ruído: o valor não corresponde a
// nenhum tempo de serviço observado e distorceria os percentis.
func TestHeartbeatNormalizeZeroesLatencyWhenNotResponsive(t *testing.T) {
	hb := domain.Heartbeat{MonitorID: 1, Timestamp: time.Now(), Status: domain.StatusDown, LatencyMS: 5000}

	hb = hb.Normalize()

	if hb.LatencyMS != 0 {
		t.Errorf("LatencyMS = %d, want 0 for a non-responsive heartbeat", hb.LatencyMS)
	}
}

func TestHeartbeatNormalizeKeepsLatencyWhenResponsive(t *testing.T) {
	for _, status := range []domain.Status{domain.StatusUp, domain.StatusDegraded} {
		t.Run(status.String(), func(t *testing.T) {
			hb := domain.Heartbeat{MonitorID: 1, Timestamp: time.Now(), Status: status, LatencyMS: 120}

			hb = hb.Normalize()

			if hb.LatencyMS != 120 {
				t.Errorf("LatencyMS = %d, want 120", hb.LatencyMS)
			}
		})
	}
}
