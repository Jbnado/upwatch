package incident_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

// familia é o motor com um pai (a sonda) e um filho (uma verificação dela),
// os dois com canal de aviso, para que todo aviso decidido apareça.
type familia struct {
	store    *sqlstore.Store
	sink     *recordingSink
	dispatch *recordingDispatcher
	engine   *incident.Engine
	pai      domain.Monitor
	filho    domain.Monitor
}

func novaFamilia(t *testing.T) *familia {
	t.Helper()
	ctx := context.Background()

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"), secretbox.Ephemeral())
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	novo := func(nome string, pai *int64) domain.Monitor {
		m := domain.Monitor{
			Name: nome, Type: domain.MonitorHTTP, Target: "https://exemplo.com",
			Interval: time.Minute, Timeout: 10 * time.Second,
			ConfirmationThreshold: threshold, Enabled: true, ParentID: pai,
		}
		if err := st.Monitors().Create(ctx, &m); err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		return m
	}
	pai := novo("sonda vm-01", nil)
	filho := novo("redis da vm-01", &pai.ID)

	c := domain.Channel{
		Name: "plantão", Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
	}
	if err := st.Channels().Create(ctx, &c); err != nil {
		t.Fatalf("Create channel returned unexpected error: %v", err)
	}
	for _, m := range []domain.Monitor{pai, filho} {
		if err := st.Channels().Link(ctx, m.ID, c.ID); err != nil {
			t.Fatalf("Link returned unexpected error: %v", err)
		}
	}

	sink := &recordingSink{}
	dispatch := &recordingDispatcher{}
	eng := incident.NewEngine(sink, st, dispatch, nil)
	eng.Upsert(pai)
	eng.Upsert(filho)
	return &familia{store: st, sink: sink, dispatch: dispatch, engine: eng, pai: pai, filho: filho}
}

// bate entrega uma batida ao motor no minuto indicado a partir de epoch.
func (f *familia) bate(t *testing.T, m domain.Monitor, minuto int, status domain.Status) {
	t.Helper()
	hb := domain.Heartbeat{
		MonitorID: m.ID, Timestamp: epoch.Add(time.Duration(minuto) * time.Minute),
		Status: status, Message: "connection refused",
	}
	if err := f.engine.Submit(context.Background(), hb); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}
}

// avisosDe conta os avisos de queda enviados sobre o monitor.
func (f *familia) quedasAvisadas(m domain.Monitor) int {
	n := 0
	for _, aviso := range f.dispatch.all() {
		if aviso.Monitor.ID == m.ID && aviso.Event.Kind == incident.KindDown {
			n++
		}
	}
	return n
}

// ultimaGravada é a última batida do monitor como foi para o banco.
func (f *familia) ultimaGravada(t *testing.T, m domain.Monitor) domain.Heartbeat {
	t.Helper()
	f.sink.mu.Lock()
	defer f.sink.mu.Unlock()
	for i := len(f.sink.got) - 1; i >= 0; i-- {
		if f.sink.got[i].MonitorID == m.ID {
			return f.sink.got[i]
		}
	}
	t.Fatalf("no heartbeat of %q reached the sink", m.Name)
	return domain.Heartbeat{}
}

// ambosNoAr confirma pai e filho no ar nos minutos antes de epoch.
func (f *familia) ambosNoAr(t *testing.T) {
	t.Helper()
	for i := range threshold {
		f.bate(t, f.pai, i-threshold, domain.StatusUp)
		f.bate(t, f.filho, i-threshold, domain.StatusUp)
	}
}

// derrubaPai confirma a queda do pai a partir do minuto dado.
func (f *familia) derrubaPai(t *testing.T, desde int) {
	t.Helper()
	for i := range threshold {
		f.bate(t, f.pai, desde+i, domain.StatusDown)
	}
	if f.engine.StateOf(f.pai.ID).Status != domain.StatusDown {
		t.Fatal("setup: the parent did not confirm its outage")
	}
}

// Caso real: a VM da sonda perdeu a saída para a internet. O sinal de
// vida dela e as verificações pararam juntos, e chegaram cinco avisos
// para uma causa só. Com o pai fora, a queda do filho não é dele: não
// avisa, não abre incidente, e vai para o histórico como lacuna — sem
// medição —, para não estragar a disponibilidade de um serviço que
// continuou de pé.
func TestEngineSilencesChildrenWhileTheParentIsDown(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)

	f.derrubaPai(t, 1)
	for i := range threshold + 2 {
		f.bate(t, f.filho, 4+i, domain.StatusDown)
	}

	if n := f.quedasAvisadas(f.filho); n != 0 {
		t.Errorf("child sent %d down notifications while the parent was down, want 0", n)
	}
	if n := f.quedasAvisadas(f.pai); n != 1 {
		t.Errorf("parent sent %d down notifications, want 1", n)
	}
	if _, err := f.store.Incidents().Current(context.Background(), f.filho.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("child incident = %v, want none", err)
	}

	gravada := f.ultimaGravada(t, f.filho)
	if gravada.Status != domain.StatusUnknown {
		t.Errorf("child heartbeat stored as %s, want unknown (a gap, not an outage)", gravada.Status)
	}
	if !strings.Contains(gravada.Message, "sonda vm-01") || !strings.Contains(gravada.Message, "connection refused") {
		t.Errorf("child heartbeat message = %q, want the parent's name and the original cause", gravada.Message)
	}
}

// O aviso do pai diz quantos ficaram calados por causa dele: quem recebe
// um aviso só precisa saber que ele vale por vários.
func TestEngineParentNotificationCountsSilencedDependents(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)

	f.derrubaPai(t, 1)

	for _, aviso := range f.dispatch.all() {
		if aviso.Monitor.ID == f.pai.ID && aviso.Event.Kind == incident.KindDown {
			if aviso.Dependents != 1 {
				t.Errorf("parent notification Dependents = %d, want 1", aviso.Dependents)
			}
			return
		}
	}
	t.Fatal("the parent's outage was not notified")
}

// Quando o pai volta, o filho que continua fora tem uma queda própria, e
// ela avisa — depois da confirmação, como qualquer outra.
func TestEngineChildStillDownAfterTheParentReturnsNotifies(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)
	f.derrubaPai(t, 1)
	f.bate(t, f.filho, 4, domain.StatusDown)

	for i := range threshold {
		f.bate(t, f.pai, 5+i, domain.StatusUp)
	}
	if f.engine.StateOf(f.pai.ID).Status != domain.StatusUp {
		t.Fatal("setup: the parent did not come back")
	}

	for i := range threshold {
		f.bate(t, f.filho, 8+i, domain.StatusDown)
	}
	if n := f.quedasAvisadas(f.filho); n != 1 {
		t.Errorf("child sent %d down notifications after the parent returned, want 1", n)
	}
}

// Pai sem medição não cala ninguém: um pai mal configurado, que nunca
// chega a medir, silenciaria os filhos para sempre.
func TestEngineUnknownParentDoesNotSilenceChildren(t *testing.T) {
	f := novaFamilia(t)

	for i := range threshold {
		f.bate(t, f.filho, i, domain.StatusDown)
	}
	if n := f.quedasAvisadas(f.filho); n != 1 {
		t.Errorf("child under an unmeasured parent sent %d notifications, want 1", n)
	}
}

// A corrida do caso real: as verificações e o sinal de vida param juntos,
// mas cada um tem a própria janela, e o filho costuma perceber o
// silêncio antes do pai. Sem esperar o pai, o filho avisaria primeiro e
// a supressão chegaria tarde. A falha do filho fica retida até o pai
// mostrar que está vivo depois dela — ou cair.
func TestEngineHoldsChildFailureUntilTheParentDecides(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)

	// O filho falha antes de o pai ter qualquer observação nova.
	f.bate(t, f.filho, 1, domain.StatusDown)
	f.bate(t, f.filho, 2, domain.StatusDown)
	f.bate(t, f.pai, 2, domain.StatusDown)
	f.bate(t, f.filho, 3, domain.StatusDown)
	f.bate(t, f.pai, 3, domain.StatusDown)
	f.bate(t, f.filho, 4, domain.StatusDown)
	f.bate(t, f.pai, 4, domain.StatusDown)
	f.bate(t, f.filho, 5, domain.StatusDown)

	if n := f.quedasAvisadas(f.filho); n != 0 {
		t.Errorf("child notified %d times racing ahead of its parent's outage, want 0", n)
	}
	if n := f.quedasAvisadas(f.pai); n != 1 {
		t.Errorf("parent sent %d down notifications, want 1", n)
	}
}

// A retenção acaba quando o pai prova que está vivo depois de a falha do
// filho começar: aí a queda é do filho, e avisa.
func TestEngineReleasesHeldFailureOnceTheParentProvesAlive(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)

	f.bate(t, f.filho, 1, domain.StatusDown)
	if got := f.ultimaGravada(t, f.filho); got.Status != domain.StatusUnknown {
		t.Errorf("held failure stored as %s, want unknown until the parent decides", got.Status)
	}

	f.bate(t, f.pai, 1, domain.StatusUp)
	for i := range threshold {
		f.bate(t, f.filho, 2+i, domain.StatusDown)
	}
	if n := f.quedasAvisadas(f.filho); n != 1 {
		t.Errorf("child sent %d down notifications after the parent proved alive, want 1", n)
	}
}

// A retenção tem prazo: um pai que não diz nada — sem verificação nova
// porque o agendador parou, por exemplo — não pode calar o filho para
// sempre.
func TestEngineHoldExpiresWithoutWordFromTheParent(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)

	for i := range 10 {
		f.bate(t, f.filho, 1+i, domain.StatusDown)
	}
	if n := f.quedasAvisadas(f.filho); n != 1 {
		t.Errorf("child sent %d down notifications with a silent parent, want 1", n)
	}
}

// Pai pausado parou no tempo: nem cala nem segura os filhos.
func TestEnginePausedParentNeitherSilencesNorHolds(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)
	f.derrubaPai(t, 1)
	pausado := f.pai
	pausado.Enabled = false
	f.engine.Upsert(pausado)

	for i := range threshold {
		f.bate(t, f.filho, 4+i, domain.StatusDown)
	}
	if n := f.quedasAvisadas(f.filho); n != 1 {
		t.Errorf("child under a paused parent sent %d notifications, want 1", n)
	}
}
