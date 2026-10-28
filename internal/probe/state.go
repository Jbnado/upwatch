package probe

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Status é o que o daemon sabe de cada verificação: o último resultado e
// o último envio.
type Status struct {
	UpdatedAt time.Time               `json:"updated_at"`
	Checks    map[string]*CheckStatus `json:"checks"`
}

// CheckStatus é o estado de uma verificação (ou do sinal de vida).
type CheckStatus struct {
	Result   *ResultStatus   `json:"result,omitempty"`
	Delivery *DeliveryStatus `json:"delivery,omitempty"`
}

// ResultStatus é a última medição.
type ResultStatus struct {
	At        time.Time `json:"at"`
	Status    string    `json:"status"`
	Msg       string    `json:"msg,omitempty"`
	LatencyMS int64     `json:"latency_ms"`
}

// DeliveryStatus é a última tentativa de envio.
type DeliveryStatus struct {
	At         time.Time `json:"at"`
	OK         bool      `json:"ok"`
	Attempts   int       `json:"attempts"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Error      string    `json:"error,omitempty"`
	GaveUp     bool      `json:"gave_up,omitempty"`
}

// StatePath é onde o daemon grava o estado: ao lado da configuração,
// porque o volume dela é o único lugar gravável do contêiner em
// --read-only.
func StatePath(configPath string) string {
	base := strings.TrimSuffix(configPath, filepath.Ext(configPath))
	return base + ".state.json"
}

// StateFile guarda o estado em memória e o espelha num arquivo, que é
// como o "upwatch-probe status", rodando por docker exec noutro
// processo, enxerga o daemon.
//
// Nada nele é segredo: o endereço de push não entra, e a causa é a mesma
// que vai ao central.
type StateFile struct {
	path   string
	mu     sync.Mutex
	st     Status
	warned bool
}

// NewStateFile cria o estado vazio. Caminho vazio mantém só em memória.
func NewStateFile(path string) *StateFile {
	return &StateFile{path: path, st: Status{Checks: map[string]*CheckStatus{}}}
}

func (s *StateFile) entry(name string) *CheckStatus {
	c, ok := s.st.Checks[name]
	if !ok {
		c = &CheckStatus{}
		s.st.Checks[name] = c
	}
	return c
}

// RecordResult registra uma medição.
func (s *StateFile) RecordResult(name string, at time.Time, sig Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(name).Result = &ResultStatus{At: at, Status: sig.Status, Msg: sig.Msg, LatencyMS: sig.LatencyMS}
	s.st.UpdatedAt = at
	s.flushLocked()
}

// RecordDelivery registra uma tentativa de envio.
func (s *StateFile) RecordDelivery(name string, d Delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entry(name).Delivery = &DeliveryStatus{
		At: d.At, OK: d.OK, Attempts: d.Attempts, HTTPStatus: d.HTTPStatus, Error: d.Err, GaveUp: d.GaveUp,
	}
	s.st.UpdatedAt = d.At
	s.flushLocked()
}

// Keep esquece as verificações fora da lista, para o status não mostrar
// para sempre uma que foi removida.
func (s *StateFile) Keep(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	for n := range s.st.Checks {
		if !keep[n] {
			delete(s.st.Checks, n)
		}
	}
	s.flushLocked()
}

// Snapshot devolve uma cópia do estado.
func (s *StateFile) Snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Status{UpdatedAt: s.st.UpdatedAt, Checks: make(map[string]*CheckStatus, len(s.st.Checks))}
	for n, c := range s.st.Checks {
		cp := *c
		out.Checks[n] = &cp
	}
	return out
}

func (s *StateFile) flushLocked() {
	if s.path == "" {
		return
	}
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err == nil {
		err = writeAtomic(s.path, data)
	}
	// Um aviso só: com o disco cheio, repetir a cada medição afogaria o
	// log que diz por que o disco encheu.
	if err != nil && !s.warned {
		s.warned = true
		slog.Warn("não foi possível gravar o estado da sonda; o status ficará defasado", "erro", err)
	}
	if err == nil {
		s.warned = false
	}
}

// ReadState lê o estado gravado pelo daemon.
func ReadState(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return Status{}, err
	}
	if st.Checks == nil {
		st.Checks = map[string]*CheckStatus{}
	}
	return st, nil
}
