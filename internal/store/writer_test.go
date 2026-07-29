package store_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

var writerEpoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// fakeTS registra os lotes recebidos e sinaliza cada escrita, permitindo
// que os testes sincronizem sem sleep.
//
// A interface embutida é nil de propósito: qualquer método inesperado
// entra em pânico ruidoso em vez de devolver zero silenciosamente.
type fakeTS struct {
	store.TimeseriesStore

	mu      sync.Mutex
	batches [][]domain.Heartbeat

	written  chan int // tamanho de cada lote gravado com sucesso
	failNext atomic.Int32
}

func newFakeTS() *fakeTS {
	return &fakeTS{written: make(chan int, 64)}
}

func (f *fakeTS) WriteHeartbeats(_ context.Context, hbs []domain.Heartbeat) error {
	if f.failNext.Load() > 0 {
		f.failNext.Add(-1)
		return errors.New("banco indisponível")
	}

	f.mu.Lock()
	batch := append([]domain.Heartbeat(nil), hbs...)
	f.batches = append(f.batches, batch)
	f.mu.Unlock()

	f.written <- len(batch)
	return nil
}

// total conta todas as batidas já persistidas.
func (f *fakeTS) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func (f *fakeTS) batchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

// awaitWrite espera um lote ser gravado, falhando o teste se travar.
func awaitWrite(t *testing.T, f *fakeTS) int {
	t.Helper()
	select {
	case n := <-f.written:
		return n
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a batch to be written")
		return 0
	}
}

func beatAt(offset time.Duration) domain.Heartbeat {
	return domain.Heartbeat{
		MonitorID: 1,
		Timestamp: writerEpoch.Add(offset),
		Status:    domain.StatusUp,
		LatencyMS: 10,
	}
}

// runWriter sobe o writer em background e devolve uma função de parada.
func runWriter(t *testing.T, w *store.BatchWriter) (context.CancelFunc, <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})
	return cancel, done
}

// Um lote cheio precisa ir ao banco na hora, sem esperar o tique: é assim
// que uma rajada de checks não fica represada.
func TestBatchWriterFlushesWhenBatchIsFull(t *testing.T) {
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 3,
		Interval: time.Hour, // longe o bastante para não interferir
		Clock:    clock.NewFake(writerEpoch),
	})
	runWriter(t, w)

	for i := 0; i < 3; i++ {
		if err := w.Submit(context.Background(), beatAt(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Submit returned unexpected error: %v", err)
		}
	}

	if n := awaitWrite(t, ts); n != 3 {
		t.Errorf("wrote a batch of %d, want 3", n)
	}
}

// Sem flush por tempo, um monitor solitário teria suas batidas paradas em
// memória até o lote encher — o que poderia levar horas.
func TestBatchWriterFlushesOnInterval(t *testing.T) {
	fake := clock.NewFake(writerEpoch)
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 500,
		Interval: time.Second,
		Clock:    fake,
	})
	runWriter(t, w)

	if err := w.Submit(context.Background(), beatAt(0)); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	// Espera o laço registrar o timer antes de mexer no relógio, senão o
	// teste passaria a depender de escalonamento.
	fake.BlockUntil(1)
	fake.Advance(time.Second)

	if n := awaitWrite(t, ts); n != 1 {
		t.Errorf("wrote a batch of %d, want 1", n)
	}
}

// Tique com o buffer vazio não pode gerar transação inútil.
func TestBatchWriterDoesNotWriteEmptyBatches(t *testing.T) {
	fake := clock.NewFake(writerEpoch)
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 500, Interval: time.Second, Clock: fake,
	})
	runWriter(t, w)

	fake.BlockUntil(1)
	fake.Advance(3 * time.Second)

	select {
	case n := <-ts.written:
		t.Errorf("wrote a batch of %d on an empty buffer, want no write at all", n)
	case <-time.After(200 * time.Millisecond):
	}
}

// Perder as batidas acumuladas no desligamento apagaria justamente a
// janela em que o serviço pode ter caído.
func TestBatchWriterFlushesPendingWorkOnShutdown(t *testing.T) {
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 500, Interval: time.Hour, Clock: clock.NewFake(writerEpoch),
	})
	cancel, done := runWriter(t, w)

	for i := 0; i < 4; i++ {
		if err := w.Submit(context.Background(), beatAt(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Submit returned unexpected error: %v", err)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	if got := ts.total(); got != 4 {
		t.Errorf("persisted %d heartbeats after shutdown, want 4", got)
	}
}

// O contexto de desligamento já está cancelado; se o flush final o
// repassasse ao banco, a gravação falharia exatamente quando mais importa.
func TestBatchWriterShutdownFlushUsesUncancelledContext(t *testing.T) {
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 500, Interval: time.Hour, Clock: clock.NewFake(writerEpoch),
	})
	cancel, done := runWriter(t, w)

	if err := w.Submit(context.Background(), beatAt(0)); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}
	cancel()
	<-done

	if got := ts.total(); got != 1 {
		t.Errorf("persisted %d heartbeats, want 1", got)
	}
}

// Falha do banco não pode descartar o que já foi medido: a próxima
// tentativa precisa levar as batidas retidas junto.
func TestBatchWriterRetainsBatchAfterWriteFailure(t *testing.T) {
	fake := clock.NewFake(writerEpoch)
	ts := newFakeTS()
	ts.failNext.Store(1)

	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 500, Interval: time.Second, Clock: fake,
	})
	runWriter(t, w)

	if err := w.Submit(context.Background(), beatAt(0)); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}
	fake.BlockUntil(1)
	fake.Advance(time.Second) // este flush falha

	if err := w.Submit(context.Background(), beatAt(time.Second)); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}
	fake.BlockUntil(1)
	fake.Advance(time.Second) // este deve levar as duas

	if n := awaitWrite(t, ts); n != 2 {
		t.Errorf("wrote a batch of %d after a failure, want 2 (the retained one plus the new)", n)
	}
}

// Retenção sem teto viraria vazamento de memória com o banco fora do ar
// por horas. O writer descarta o mais antigo e conta o que perdeu.
func TestBatchWriterCapsRetainedBacklog(t *testing.T) {
	fake := clock.NewFake(writerEpoch)
	ts := newFakeTS()
	ts.failNext.Store(1000) // banco permanentemente indisponível

	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 2, MaxBacklog: 4, Interval: time.Hour, Clock: fake,
	})
	runWriter(t, w)

	for i := 0; i < 20; i++ {
		if err := w.Submit(context.Background(), beatAt(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Submit returned unexpected error: %v", err)
		}
	}

	// Dá tempo de o laço consumir tudo o que foi enviado.
	deadline := time.Now().Add(3 * time.Second)
	for w.Dropped() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if w.Dropped() == 0 {
		t.Error("Dropped() = 0 with a permanently failing store, want the overflow to be counted")
	}
	if got := w.Pending(); got > 4 {
		t.Errorf("Pending() = %d, want at most the MaxBacklog of 4", got)
	}
}

// Submit é chamado pelos workers do agendador; uma corrida aqui
// corromperia o buffer sob carga real.
func TestBatchWriterAcceptsConcurrentSubmits(t *testing.T) {
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 10, Interval: time.Millisecond, Clock: clock.Real(),
	})
	cancel, done := runWriter(t, w)

	const writers, each = 8, 25
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if err := w.Submit(context.Background(), beatAt(time.Duration(i*each+j)*time.Second)); err != nil {
					t.Errorf("Submit returned unexpected error: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	cancel()
	<-done

	if got := ts.total(); got != writers*each {
		t.Errorf("persisted %d heartbeats, want %d", got, writers*each)
	}
}

// Submit não pode travar para sempre quando a fila enche: o worker do
// agendador ficaria preso e o monitor pararia de ser verificado.
func TestBatchWriterSubmitRespectsContext(t *testing.T) {
	ts := newFakeTS()
	ts.failNext.Store(1000)
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 1, QueueSize: 1, Interval: time.Hour, Clock: clock.NewFake(writerEpoch),
	})
	// Sem Run: nada consome a fila, então ela satura.

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var err error
	for i := 0; i < 100 && err == nil; i++ {
		err = w.Submit(ctx, beatAt(time.Duration(i)*time.Second))
	}

	if err == nil {
		t.Fatal("Submit never returned an error on a saturated queue, want it to give up with the context")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Submit returned %v, want it to wrap context.DeadlineExceeded", err)
	}
}

func TestBatchWriterAppliesDefaults(t *testing.T) {
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{})

	if w.MaxBatch() != store.DefaultMaxBatch {
		t.Errorf("MaxBatch() = %d, want %d", w.MaxBatch(), store.DefaultMaxBatch)
	}
	if w.Interval() != store.DefaultFlushInterval {
		t.Errorf("Interval() = %v, want %v", w.Interval(), store.DefaultFlushInterval)
	}
}

// Batidas são normalizadas na entrada para que o probe padrão e o UTC
// valham qualquer que seja a origem do resultado.
func TestBatchWriterNormalizesHeartbeats(t *testing.T) {
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 1, Interval: time.Hour, Clock: clock.NewFake(writerEpoch),
	})
	runWriter(t, w)

	saoPaulo := time.FixedZone("America/Sao_Paulo", -3*60*60)
	if err := w.Submit(context.Background(), domain.Heartbeat{
		MonitorID: 1,
		Timestamp: time.Date(2026, 7, 28, 22, 30, 0, 0, saoPaulo),
		Status:    domain.StatusDown,
		LatencyMS: 5000,
	}); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}
	awaitWrite(t, ts)

	ts.mu.Lock()
	got := ts.batches[0][0]
	ts.mu.Unlock()

	if got.ProbeID != domain.DefaultProbeID {
		t.Errorf("ProbeID = %q, want %q", got.ProbeID, domain.DefaultProbeID)
	}
	if got.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp location = %v, want UTC", got.Timestamp.Location())
	}
	if got.LatencyMS != 0 {
		t.Errorf("LatencyMS = %d, want 0 for a heartbeat that got no response", got.LatencyMS)
	}
}

// Um lote grande precisa sair inteiro, sem sobrar resto no buffer.
func TestBatchWriterWritesEverythingSubmitted(t *testing.T) {
	ts := newFakeTS()
	w := store.NewBatchWriter(ts, store.BatchWriterOptions{
		MaxBatch: 7, Interval: time.Millisecond, Clock: clock.Real(),
	})
	cancel, done := runWriter(t, w)

	const n = 50
	for i := 0; i < n; i++ {
		if err := w.Submit(context.Background(), beatAt(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Submit returned unexpected error: %v", err)
		}
	}

	cancel()
	<-done

	if got := ts.total(); got != n {
		t.Errorf("persisted %d heartbeats, want %d", got, n)
	}
	if ts.batchCount() < 2 {
		t.Errorf("wrote %d batches, want the work to be split across several", ts.batchCount())
	}
}
