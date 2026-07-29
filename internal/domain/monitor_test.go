package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

func TestMonitorTypeString(t *testing.T) {
	for _, tt := range []struct {
		typ  domain.MonitorType
		want string
	}{
		{domain.MonitorHTTP, "http"},
		{domain.MonitorTCP, "tcp"},
		{domain.MonitorICMP, "icmp"},
		{domain.MonitorDNS, "dns"},
		{domain.MonitorTLS, "tls"},
		{domain.MonitorPush, "push"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.typ.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseMonitorTypeRejectsUnknown(t *testing.T) {
	if _, err := domain.ParseMonitorType("carrier-pigeon"); err == nil {
		t.Fatal("ParseMonitorType returned nil error for unknown type, want an error")
	}
}

// validMonitor devolve um monitor que passa na validação. Cada teste
// modifica só o campo sob análise, de modo que a falha aponte a causa.
func validMonitor() domain.Monitor {
	return domain.Monitor{
		Name:                  "API de produção",
		Type:                  domain.MonitorHTTP,
		Target:                "https://example.com/health",
		Interval:              60 * time.Second,
		Timeout:               10 * time.Second,
		ConfirmationThreshold: 3,
		Enabled:               true,
	}
}

func TestMonitorValidateAcceptsWellFormedMonitor(t *testing.T) {
	if err := validMonitor().Validate(); err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
}

// assertFieldError confirma que a validação falhou apontando o campo certo,
// para a API conseguir devolver 400 com a causa.
func assertFieldError(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Validate() returned nil error, want error on field %q", field)
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate() returned %T (%v), want *domain.ValidationError", err, err)
	}
	if ve.Field != field {
		t.Errorf("error on field %q, want field %q", ve.Field, field)
	}
}

func TestMonitorValidateRejectsEmptyName(t *testing.T) {
	m := validMonitor()
	m.Name = "   "
	assertFieldError(t, m.Validate(), "name")
}

func TestMonitorValidateRejectsUnknownType(t *testing.T) {
	m := validMonitor()
	m.Type = domain.MonitorType(99)
	assertFieldError(t, m.Validate(), "type")
}

func TestMonitorValidateRejectsEmptyTarget(t *testing.T) {
	m := validMonitor()
	m.Target = ""
	assertFieldError(t, m.Validate(), "target")
}

// Push é o único tipo sem alvo: quem bate é o serviço monitorado.
func TestMonitorValidateAllowsEmptyTargetForPush(t *testing.T) {
	m := validMonitor()
	m.Type = domain.MonitorPush
	m.Target = ""

	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() on push monitor returned unexpected error: %v", err)
	}
}

func TestMonitorValidateRejectsIntervalBelowMinimum(t *testing.T) {
	m := validMonitor()
	m.Interval = domain.MinInterval - time.Nanosecond
	assertFieldError(t, m.Validate(), "interval")
}

func TestMonitorValidateAcceptsIntervalAtMinimum(t *testing.T) {
	m := validMonitor()
	m.Interval = domain.MinInterval
	m.Timeout = domain.MinInterval / 2

	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() at minimum interval returned unexpected error: %v", err)
	}
}

func TestMonitorValidateRejectsZeroTimeout(t *testing.T) {
	m := validMonitor()
	m.Timeout = 0
	assertFieldError(t, m.Validate(), "timeout")
}

// Timeout >= intervalo faz os checks empilharem: o seguinte começa antes
// de o anterior desistir, e o pool de workers satura.
func TestMonitorValidateRejectsTimeoutNotSmallerThanInterval(t *testing.T) {
	for _, tt := range []struct {
		name    string
		timeout time.Duration
	}{
		{"igual ao intervalo", 60 * time.Second},
		{"maior que o intervalo", 90 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := validMonitor()
			m.Interval = 60 * time.Second
			m.Timeout = tt.timeout
			assertFieldError(t, m.Validate(), "timeout")
		})
	}
}

func TestMonitorValidateRejectsConfirmationThresholdBelowOne(t *testing.T) {
	m := validMonitor()
	m.ConfirmationThreshold = 0
	assertFieldError(t, m.Validate(), "confirmation_threshold")
}

func TestMonitorValidateRejectsNegativeDegradedLatency(t *testing.T) {
	m := validMonitor()
	m.DegradedLatency = -time.Millisecond
	assertFieldError(t, m.Validate(), "degraded_latency")
}

// Zero desliga a detecção de lentidão em vez de marcar tudo como degradado.
func TestMonitorValidateAllowsZeroDegradedLatency(t *testing.T) {
	m := validMonitor()
	m.DegradedLatency = 0

	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
}

// Um monitor não pode ser pai de si mesmo — o ciclo travaria a resolução
// de hierarquia quando ela chegar.
func TestMonitorValidateRejectsSelfAsParent(t *testing.T) {
	m := validMonitor()
	m.ID = 7
	parent := int64(7)
	m.ParentID = &parent
	assertFieldError(t, m.Validate(), "parent_id")
}

func TestMonitorValidateTrimsNameBeforeChecking(t *testing.T) {
	m := validMonitor()
	m.Name = "  API  "

	if err := m.Validate(); err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
}

func TestValidationErrorMentionsFieldAndReason(t *testing.T) {
	m := validMonitor()
	m.Name = ""

	err := m.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil error, want an error")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error message %q does not mention the field name", err.Error())
	}
}
