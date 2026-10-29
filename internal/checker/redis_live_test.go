package checker_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Roda quando UPWATCH_TEST_REDIS_ADDR aponta para um Redis descartável
// (host:porta, sem senha). O teste cria chaves e um usuário de ACL.
//
//	docker run -d --rm --name redis -p 16379:6379 redis:7-alpine
//	UPWATCH_TEST_REDIS_ADDR=127.0.0.1:16379 go test ./internal/checker/...
//
// O servidor falso de redis_test.go cobre o protocolo; este cobre o que
// só um Redis de verdade diz — ACL, o formato real do INFO, o que o
// go-redis manda ao conectar.
const redisAddrEnv = "UPWATCH_TEST_REDIS_ADDR"

func liveRedis(t *testing.T) (string, *redis.Client) {
	t.Helper()

	addr := os.Getenv(redisAddrEnv)
	if addr == "" {
		t.Skipf("%s não definida; pulando teste contra Redis real", redisAddrEnv)
	}
	admin := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { admin.Close() })
	if err := admin.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("%s=%s não responde: %v", redisAddrEnv, addr, err)
	}
	return addr, admin
}

// O usuário recomendado na documentação: só os comandos de leitura que
// o checker usa. Se a verificação exigisse qualquer permissão além dessas,
// a recomendação seria mentira e o operador acabaria dando acesso amplo
// para o monitor funcionar.
func TestRedisLiveWithReadOnlyACLUser(t *testing.T) {
	addr, admin := liveRedis(t)
	ctx := context.Background()

	const user, pass = "upwatch-teste", "senha-do-monitor-redis"
	if err := admin.Do(ctx, "ACL", "SETUSER", user, "reset", "on", ">"+pass,
		"~*", "-@all", "+ping", "+scard", "+llen", "+get", "+exists",
		"+hget", "+zcard", "+dbsize", "+info").Err(); err != nil {
		t.Fatalf("ACL SETUSER returned unexpected error: %v", err)
	}
	t.Cleanup(func() { admin.Do(context.Background(), "ACL", "DELUSER", user) })

	admin.Del(ctx, "rq:workers:default", "rq:queue:default")
	admin.SAdd(ctx, "rq:workers:default", "w1", "w2")
	admin.RPush(ctx, "rq:queue:default", "j1", "j2", "j3")
	t.Cleanup(func() { admin.Del(context.Background(), "rq:workers:default", "rq:queue:default") })

	cfg := `{"username":"` + user + `","password":"` + pass + `","assertions":[
		{"command":"SCARD","key":"rq:workers:default","op":"gte","value":1},
		{"command":"LLEN","key":"rq:queue:default","op":"lt","value":10},
		{"command":"INFO","section":"memory","field":"used_memory","op":"gt","value":0}]}`

	res := checker.NewRedis().Check(ctx, redisMonitor(addr, cfg))
	if res.Status != domain.StatusUp {
		t.Fatalf("Status = %v (%s), want up", res.Status, res.Message)
	}

	// Sem workers, o PING continua verde; só a asserção acusa.
	admin.Del(ctx, "rq:workers:default")
	res = checker.NewRedis().Check(ctx, redisMonitor(addr, cfg))
	if res.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want down", res.Status)
	}
	if want := "SCARD rq:workers:default = 0, esperado >= 1"; res.Message != want {
		t.Errorf("Message = %q, want %q", res.Message, want)
	}
}

func TestRedisLiveWrongPasswordIsDownWithoutLeaking(t *testing.T) {
	addr, admin := liveRedis(t)
	ctx := context.Background()

	const user = "upwatch-teste-senha"
	if err := admin.Do(ctx, "ACL", "SETUSER", user, "reset", "on", ">certa", "+ping").Err(); err != nil {
		t.Fatalf("ACL SETUSER returned unexpected error: %v", err)
	}
	t.Cleanup(func() { admin.Do(context.Background(), "ACL", "DELUSER", user) })

	const wrong = "senha-errada-que-nao-pode-vazar"
	res := checker.NewRedis().Check(ctx,
		redisMonitor(addr, `{"username":"`+user+`","password":"`+wrong+`"}`))

	if res.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want down", res.Status)
	}
	if strings.Contains(res.Message, wrong) {
		t.Errorf("Message leaks the password: %q", res.Message)
	}
}

func TestRedisLiveMemoryLimit(t *testing.T) {
	addr, _ := liveRedis(t)

	res := checker.NewRedis().Check(context.Background(),
		redisMonitor(addr, `{"max_used_memory_bytes":1}`))

	if res.Status != domain.StatusDown || !strings.HasPrefix(res.Message, "INFO memory used_memory = ") {
		t.Fatalf("got %v %q, want down citing used_memory", res.Status, res.Message)
	}
}

// fakeWorker abre uma conexão com o nome que o Bull ou o BullMQ dão ao
// worker, que é tudo o que WORKERS enxerga dele.
func fakeWorker(t *testing.T, addr, name string) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: addr, ClientName: name, PoolSize: 1})
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("worker %q: Ping returned unexpected error: %v", name, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// O formato do nome vem do código das bibliotecas, e só um Redis de
// verdade confirma que CLIENT SETNAME aceita esses nomes e que o CLIENT
// LIST os devolve como o checker espera. Também confere a permissão que
// a documentação manda dar: se +client|list não bastasse, o operador
// abriria a ACL inteira para o monitor funcionar.
func TestRedisLiveWorkersCountsBullAndBullMQ(t *testing.T) {
	addr, admin := liveRedis(t)
	ctx := context.Background()

	const user, pass = "upwatch-teste-workers", "senha-do-monitor-workers"
	if err := admin.Do(ctx, "ACL", "SETUSER", user, "reset", "on", ">"+pass,
		"-@all", "+ping", "+client|list").Err(); err != nil {
		t.Fatalf("ACL SETUSER returned unexpected error: %v", err)
	}
	t.Cleanup(func() { admin.Do(context.Background(), "ACL", "DELUSER", user) })

	// base64("live-render") = bGl2ZS1yZW5kZXI=
	bull := fakeWorker(t, addr, "bull:bGl2ZS1yZW5kZXI=")              // Bull v3
	bullmq := fakeWorker(t, addr, "bull:bGl2ZS1yZW5kZXI=:w:render-1") // BullMQ com nome
	fakeWorker(t, addr, "bull:bGl2ZS1yZW5kZXI=:qe")                   // QueueEvents, não conta
	fakeWorker(t, addr, "outro:bGl2ZS1yZW5kZXI=")                     // outro prefixo, não conta

	cfg := `{"username":"` + user + `","password":"` + pass + `","assertions":[
		{"command":"WORKERS","key":"bull:live-render","op":"gte","value":2}]}`

	res := checker.NewRedis().Check(ctx, redisMonitor(addr, cfg))
	if res.Status != domain.StatusUp {
		t.Fatalf("Status = %v (%s), want up with two workers", res.Status, res.Message)
	}

	// Os workers caem; o Redis continua respondendo PING.
	bull.Close()
	bullmq.Close()
	want := "WORKERS bull:live-render = 0, esperado >= 2"
	deadline := time.Now().Add(5 * time.Second)
	for {
		// O Redis tira a conexão da lista quando percebe o fechamento,
		// o que pode levar um instante depois do Close.
		res = checker.NewRedis().Check(ctx, redisMonitor(addr, cfg))
		if res.Message == want || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res.Status != domain.StatusDown || res.Message != want {
		t.Errorf("got %v %q, want down %q", res.Status, res.Message, want)
	}
}

func TestRedisLiveWorkersWithoutClientListPermission(t *testing.T) {
	addr, admin := liveRedis(t)
	ctx := context.Background()

	const user, pass = "upwatch-teste-sem-client", "senha-do-monitor-sem-client"
	if err := admin.Do(ctx, "ACL", "SETUSER", user, "reset", "on", ">"+pass,
		"~*", "-@all", "+ping", "+scard").Err(); err != nil {
		t.Fatalf("ACL SETUSER returned unexpected error: %v", err)
	}
	t.Cleanup(func() { admin.Do(context.Background(), "ACL", "DELUSER", user) })

	res := checker.NewRedis().Check(ctx, redisMonitor(addr, `{"username":"`+user+`","password":"`+pass+`","assertions":[
		{"command":"WORKERS","key":"bull:live-render","op":"gte","value":1}]}`))

	if res.Status != domain.StatusDown || !strings.Contains(res.Message, "+client|list") {
		t.Errorf("got %v %q, want down explaining the missing +client|list", res.Status, res.Message)
	}
}
