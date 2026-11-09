package auth

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
)

func reserveOrigin(t *testing.T, a *attempts, user, origin string) *attempt {
	t.Helper()
	att, err := a.reserve(user, origin)
	if err != nil {
		t.Fatalf("reserve(%q, %q) = %v", user, origin, err)
	}
	return att
}

func failOrigin(t *testing.T, a *attempts, user, origin string, count int) {
	t.Helper()
	for range count {
		reserveOrigin(t, a, user, origin).fail()
	}
}

func requireLimited(t *testing.T, a *attempts, user, origin string) {
	t.Helper()
	att, err := a.reserve(user, origin)
	if att != nil {
		att.release()
	}
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("reserve(%q, %q) = %v; queria limite", user, origin, err)
	}
}

// O limite de um atacante não acompanha a vítima em outra rede nem fecha o NAT.
func TestOriginAttemptsPairIsolation(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	failOrigin(t, a, "vitima", "atacante", 5)
	requireLimited(t, a, "vitima", "atacante")
	reserveOrigin(t, a, "vitima", "dono").succeed()
	reserveOrigin(t, a, "colega", "atacante").succeed()
}

// Um sucesso isolado não apaga os nomes de terceiros varridos pela origem.
func TestOriginAttemptsDistinctAccounts(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	for i := range 19 {
		failOrigin(t, a, fmt.Sprint("alvo-", i), "atacante", 1)
	}
	reserveOrigin(t, a, "propria", "atacante").succeed()
	failOrigin(t, a, "vigesima", "atacante", 1)
	requireLimited(t, a, "nova", "atacante")
	reserveOrigin(t, a, "propria", "atacante").succeed()
}

// Só pares esgotados alimentam o agregado da conta; conhecidos conservam orçamento próprio.
func TestOriginAttemptsDistributedAndKnown(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	reserveOrigin(t, a, "vitima", "dono").succeed()
	for i := range 3 {
		failOrigin(t, a, "vitima", fmt.Sprint("bot-", i), 5)
	}
	requireLimited(t, a, "vitima", "bot-4")
	reserveOrigin(t, a, "vitima", "dono").succeed()
	requireLimited(t, a, "vitima", "bot-4")
	failOrigin(t, a, "vitima", "dono", 5)
	requireLimited(t, a, "vitima", "dono")
}

// Cinco erros conhecidos não viram ataque contra outras contas ou outras redes.
func TestOriginAttemptsKnownFailuresStayInPair(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	for i := range 20 {
		user := fmt.Sprint("pessoa-", i)
		reserveOrigin(t, a, user, "escritorio").succeed()
		failOrigin(t, a, user, "escritorio", 5)
	}
	reserveOrigin(t, a, "outra", "escritorio").release()
}

// O sucesso retira só o próprio membro, tanto na origem quanto na conta.
func TestOriginAttemptsSuccessRemovesOnlyOwnMembership(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	for i := range 19 {
		failOrigin(t, a, fmt.Sprint("alvo-", i), "origem", 1)
	}
	reserveOrigin(t, a, "alvo-0", "origem").succeed()
	failOrigin(t, a, "mais-um", "origem", 1)
	reserveOrigin(t, a, "ainda-cabe", "origem").fail()
	requireLimited(t, a, "agora-nao", "origem")
}

// Reservas contam antes de conhecer o resultado e não somem ao sucesso concorrente.
func TestOriginAttemptsConcurrentReservations(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var accepted []*attempt
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			att, err := a.reserve("conta", "rede")
			if err == nil {
				mu.Lock()
				accepted = append(accepted, att)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(accepted) != 5 {
		t.Fatalf("reservas = %d; queria 5", len(accepted))
	}
	accepted[0].succeed()
	one := reserveOrigin(t, a, "conta", "rede")
	requireLimited(t, a, "conta", "rede")
	one.release()
	for _, att := range accepted[1:] {
		att.fail()
	}
	reserveOrigin(t, a, "conta", "rede").release()
}

// Cada membro expira pela própria falha; atividade de outro membro não o renova.
func TestOriginAttemptsAggregateExpiry(t *testing.T) {
	fake := clock.NewFake(internalEpoch)
	a := newAttempts(fake)
	for i := range 19 {
		failOrigin(t, a, fmt.Sprint("antiga-", i), "rede", 1)
	}
	fake.Advance(14 * time.Minute)
	failOrigin(t, a, "recente", "rede", 1)
	requireLimited(t, a, "nova", "rede")
	fake.Advance(time.Minute)
	reserveOrigin(t, a, "nova", "rede").release()
}

// A confiança vence em trinta dias mesmo quando o par segue registrado.
func TestOriginAttemptsKnownExpiry(t *testing.T) {
	fake := clock.NewFake(internalEpoch)
	a := newAttempts(fake)
	reserveOrigin(t, a, "vitima", "dono").succeed()
	fake.Advance(30 * 24 * time.Hour)
	for i := range 3 {
		failOrigin(t, a, "vitima", fmt.Sprint("bot-", i), 5)
	}
	requireLimited(t, a, "vitima", "dono")
}

// Três redes com palpites avulsos não esgotam o orçamento distribuído.
func TestOriginAttemptsAccountRequiresExhaustedPairs(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	for i := range 3 {
		failOrigin(t, a, "vitima", fmt.Sprint("rede-", i), 4)
	}
	reserveOrigin(t, a, "vitima", "quarta").release()
}

// Quando a janela vence, nem o par nem os agregados continuam bloqueados.
func TestOriginAttemptsPairAndAccountExpiry(t *testing.T) {
	fake := clock.NewFake(internalEpoch)
	a := newAttempts(fake)
	for i := range 3 {
		failOrigin(t, a, "vitima", fmt.Sprint("rede-", i), 5)
	}
	fake.Advance(15 * time.Minute)
	reserveOrigin(t, a, "vitima", "rede-0").release()
	reserveOrigin(t, a, "vitima", "quarta").release()
}

// A janela pode vencer com pedidos pendentes sem devolver seu orçamento.
func TestOriginAttemptsExpiryPreservesInFlight(t *testing.T) {
	fake := clock.NewFake(internalEpoch)
	a := newAttempts(fake)
	failOrigin(t, a, "conta", "rede", 3)
	pending := reserveOrigin(t, a, "conta", "rede")
	fake.Advance(15 * time.Minute)
	var others []*attempt
	for range 4 {
		others = append(others, reserveOrigin(t, a, "conta", "rede"))
	}
	requireLimited(t, a, "conta", "rede")
	pending.release()
	for _, att := range others {
		att.release()
	}
}

// Encerrar a mesma reserva de goroutines diferentes continua sendo idempotente.
func TestOriginAttemptsEndIsConcurrentSafe(t *testing.T) {
	fake := clock.NewFake(internalEpoch)
	a := newAttempts(fake)
	att := reserveOrigin(t, a, "conta", "rede")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); att.fail() }()
	}
	wg.Wait()
	failOrigin(t, a, "conta", "rede", 4)
	requireLimited(t, a, "conta", "rede")
	fake.Advance(15 * time.Minute)
	for range 5 {
		reserveOrigin(t, a, "conta", "rede")
	}
	requireLimited(t, a, "conta", "rede")
}

// Capacidade recusa quando não há registro descartável.
func TestOriginAttemptsCapacityPreservesInFlight(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	var pending []*attempt
	for i := range maxTrackedPairs {
		pending = append(pending, reserveOrigin(t, a, fmt.Sprint("conta-", i), "rede"))
	}
	requireLimited(t, a, "nova", "rede")
	for range 4 {
		pending = append(pending, reserveOrigin(t, a, "conta-0", "rede"))
	}
	requireLimited(t, a, "conta-0", "rede")
	pending[1].fail()
	reserveOrigin(t, a, "nova", "rede").release()
	requireLimited(t, a, "conta-0", "rede")
	for _, att := range pending {
		att.release()
	}
}

func TestOriginAttemptsCapacityPreservesBlockedPair(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	reserveOrigin(t, a, "admin", "conhecida").succeed()
	failOrigin(t, a, "admin", "conhecida", MaxLoginAttempts)
	for i := range maxTrackedPairs - 1 {
		failOrigin(t, a, fmt.Sprint("conta-", i), fmt.Sprint("rede-", i), 1)
	}
	reserveOrigin(t, a, "nova", "nova-rede").release()
	requireLimited(t, a, "admin", "conhecida")
}

func TestOriginAttemptsCapacityPreservesKnownPair(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	reserveOrigin(t, a, "admin", "conhecida").succeed()
	for i := range maxOriginAccounts {
		failOrigin(t, a, fmt.Sprint("scanner-", i), "conhecida", 1)
	}
	for i := range maxTrackedPairs - maxOriginAccounts - 1 {
		failOrigin(t, a, fmt.Sprint("conta-", i), fmt.Sprint("rede-", i), 1)
	}
	reserveOrigin(t, a, "nova", "nova-rede").release()
	reserveOrigin(t, a, "admin", "conhecida").release()
}

func TestAggregateCapacityPreservesBlockedMembers(t *testing.T) {
	now := internalEpoch
	a := newAggregateRecords(2, 2)
	blocked, evictable, arriving := keyFor("bloqueada"), keyFor("descartavel"), keyFor("nova")
	a.add(blocked, keyFor("um"), now)
	a.add(blocked, keyFor("dois"), now)
	a.add(evictable, keyFor("um"), now)
	a.add(arriving, keyFor("um"), now)
	if !a.blocked(blocked, now) || a.get(evictable, now) != nil {
		t.Fatal("pressão removeu escopo bloqueado em vez do incompleto")
	}
	a.add(arriving, keyFor("dois"), now)
	if !a.blocked(keyFor("quarta"), now) {
		t.Fatal("capacidade só de bloqueios deveria barrar escopo novo")
	}
}

// O erro mantém o mesmo sentinel público e oferece o escopo à auditoria.
func TestOriginAttemptsLimitScopes(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))
	failOrigin(t, a, "conta", "par", 5)
	assertScope := func(user, origin, want string) {
		t.Helper()
		_, err := a.reserve(user, origin)
		var limit *AttemptLimitError
		if !errors.Is(err, ErrTooManyAttempts) || !errors.As(err, &limit) || limit.Scope != want {
			t.Fatalf("erro = %#v; queria escopo %s", err, want)
		}
	}
	assertScope("conta", "par", "par")
	for i := range 20 {
		failOrigin(t, a, fmt.Sprint("alvo-", i), "scanner", 1)
	}
	assertScope("outro", "scanner", "origem")
	for i := range 3 {
		failOrigin(t, a, "botnet", fmt.Sprint("bot-", i), 5)
	}
	assertScope("botnet", "quarta", "conta")
}
