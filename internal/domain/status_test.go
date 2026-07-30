package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

func TestStatusString(t *testing.T) {
	tests := []struct {
		status domain.Status
		want   string
	}{
		{domain.StatusUnknown, "unknown"},
		{domain.StatusUp, "up"},
		{domain.StatusDown, "down"},
		{domain.StatusDegraded, "degraded"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.status.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusStringForUndefinedValue(t *testing.T) {
	got := domain.Status(99).String()
	if got != "unknown" {
		t.Errorf("String() of undefined status = %q, want %q", got, "unknown")
	}
}

func TestParseStatusAcceptsKnownNames(t *testing.T) {
	tests := []struct {
		in   string
		want domain.Status
	}{
		{"up", domain.StatusUp},
		{"down", domain.StatusDown},
		{"degraded", domain.StatusDegraded},
		{"unknown", domain.StatusUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := domain.ParseStatus(tt.in)
			if err != nil {
				t.Fatalf("ParseStatus(%q) returned unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseStatus(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseStatusRejectsUnknownName(t *testing.T) {
	_, err := domain.ParseStatus("banana")
	if err == nil {
		t.Fatal("ParseStatus(\"banana\") returned nil error, want an error")
	}
}

// A API expõe status como string; o zero value do Go não pode virar `0` no JSON.
func TestStatusMarshalsAsJSONString(t *testing.T) {
	b, err := json.Marshal(domain.StatusDown)
	if err != nil {
		t.Fatalf("Marshal returned unexpected error: %v", err)
	}
	if string(b) != `"down"` {
		t.Errorf("Marshal(StatusDown) = %s, want %s", b, `"down"`)
	}
}

func TestStatusUnmarshalsFromJSONString(t *testing.T) {
	var s domain.Status
	if err := json.Unmarshal([]byte(`"degraded"`), &s); err != nil {
		t.Fatalf("Unmarshal returned unexpected error: %v", err)
	}
	if s != domain.StatusDegraded {
		t.Errorf("Unmarshal(%q) = %v, want %v", `"degraded"`, s, domain.StatusDegraded)
	}
}

func TestStatusUnmarshalRejectsInvalidString(t *testing.T) {
	var s domain.Status
	err := json.Unmarshal([]byte(`"banana"`), &s)
	if err == nil {
		t.Fatal("Unmarshal of invalid status returned nil error, want an error")
	}
}

// Latência só é significativa quando o alvo respondeu.
func TestStatusCountsAsResponsive(t *testing.T) {
	tests := []struct {
		status domain.Status
		want   bool
	}{
		{domain.StatusUp, true},
		{domain.StatusDegraded, true},
		{domain.StatusDown, false},
		{domain.StatusUnknown, false},
	}

	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			if got := tt.status.Responsive(); got != tt.want {
				t.Errorf("%v.Responsive() = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}
