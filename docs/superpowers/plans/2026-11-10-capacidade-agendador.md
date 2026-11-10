# Capacidade do agendador — plano de implementação

> **Para agentes:** usar `superpowers:executing-plans` e `superpowers:test-driven-development`; marcar cada passo após verificar o resultado.

**Objetivo:** medir saturação do agendador sem alterar a política da fila ou criar incidentes artificiais.

**Arquitetura:** `metrics.Counters` recebe gauges e histogramas globais; `scheduler` observa as transições da fila e do checker; `/metrics` publica as séries em Prometheus. Um cenário reproduzível avalia capacidade e justiça antes de qualquer política nova.

**Tecnologia:** Go 1.27, suíte com `-race`, formato de exposição Prometheus escrito pelo projeto.

**Especificação:** `docs/superpowers/specs/2026-11-10-capacidade-agendador-design.md`.

## Restrições globais

- Sem labels de monitor, grupo ou conta nas métricas novas; cardinalidade constante.
- Sem limite de monitores, quota ou descarte silencioso.
- Atraso = início real menos vencimento; duração = chamada do checker, sem sink.
- Código, comentários, erros, commits e PRs em português; identificadores em inglês.
- Teste vermelho antes do código e sabotagem de cada teste novo; suíte completa com `go test -race ./...`.

## Foco da revisão

- Check cancelado enquanto espera slot: nenhum item fica preso como `inFlight`, e o gauge de espera volta a zero.
- Monitor removido ou pausado enquanto espera: não entra no checker.
- Avanço do relógio falso e atraso negativo: só valores não negativos chegam ao histograma.
- Checker lento ou que atinge timeout: duração não inclui espera na fila nem gravação da batida.
- Raspagem concorrente: snapshot coerente, sem corrida, gauges não negativos após encerramento.

---

### Tarefa 1: Medidas do agendador

**Arquivos:** `internal/metrics/metrics.go`, `internal/metrics/metrics_test.go`.

**Interface produzida:** `QueueSchedulerChecks(n int)`, `UnqueueSchedulerChecks(n int)`, `StartSchedulerCheck(delay time.Duration)`, `FinishSchedulerCheck(duration time.Duration)`, `SchedulerSnapshot() SchedulerSnapshot`; snapshot contém `Waiting`, `Running` e histogramas de atraso/duração com contagem, soma em segundos e buckets cumulativos de 0,1; 0,5; 1; 5; 10; 30; 60; +Inf segundos. Métodos toleram receptor nulo como os contadores atuais.

- [ ] Escrever testes para transições 2 aguardando → 1 em execução → 0 e para amostras de 250 ms/2 s nos buckets e na soma; um teste concorrente lê snapshots enquanto workers atualizam.
- [ ] Rodar `go test ./internal/metrics -run Scheduler -count=1`; esperado: falha de compilação pelas interfaces ausentes.
- [ ] Implementar as estruturas e operações com mutex curto; descartar durações negativas como zero.
- [ ] Rodar os testes dirigidos e `go test -race ./internal/metrics`; esperado: todos passam.
- [ ] Sabotar temporariamente a contagem de bucket e verificar falha dos testes novos; restaurar e confirmar verde.
- [ ] Commit `feat: medir fila e duração do agendador`.

### Tarefa 2: Observar as transições reais

**Arquivos:** `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`, `cmd/upwatch/main.go`.

**Consome:** métodos de `metrics.Counters` da tarefa 1. **Produz:** `scheduler.Options.Counters *metrics.Counters`, ligado ao mesmo objeto da API no comando principal.

- [ ] Escrever testes com relógio falso e checker bloqueado: dois checks vencem para um worker, um espera, atraso é registrado quando de fato começa; cancelamento esvazia a espera; remoção/pausa impede execução do item que esperava; duração exclui sink.
- [ ] Rodar `go test ./internal/scheduler -run 'Scheduler.*(Metrics|Queued|Waiting)' -count=1`; esperado: falha pelas medidas ou opção ausentes.
- [ ] Instrumentar `takeDue`, começo/fim do checker e cancelamento. Antes de executar, validar que a entrada ainda está ativa; ao cancelar, limpar todos os itens retirados do heap que esperavam.
- [ ] Rodar testes dirigidos e `go test -race ./internal/scheduler ./cmd/upwatch`; esperado: todos passam.
- [ ] Sabotar temporariamente cada observação e guarda nova, verificar falha dos testes correspondentes, restaurar e confirmar verde.
- [ ] Commit `feat: observar saturação no agendador`.

### Tarefa 3: Exposição e operação

**Arquivos:** `internal/api/metrics.go`, `internal/api/metrics_test.go`, `README.md`, cenário de carga em `internal/scheduler/scheduler_bench_test.go`.

**Consome:** `SchedulerSnapshot()` da tarefa 1 e a instrumentação da tarefa 2. **Produz:** gauges `upwatch_scheduler_waiting_checks` e `upwatch_scheduler_running_checks`; histogramas `upwatch_scheduler_start_delay_seconds` e `upwatch_scheduler_check_duration_seconds`.

- [ ] Escrever teste da resposta `/metrics` com valores e famílias Prometheus, inclusive zero e buckets contíguos; adaptar a verificação de contiguidade para sufixos `_bucket`, `_sum`, `_count`.
- [ ] Rodar `go test ./internal/api -run Metrics -count=1`; esperado: falha pelas famílias ausentes.
- [ ] Publicar métricas sem labels variáveis; preservar autenticação existente.
- [ ] Rodar teste dirigido; sabotar cada família nova e confirmar falha; restaurar.
- [ ] Acrescentar benchmark reproduzível com perfis rápido, lento e timeout, intervalos e grupos diferentes; documentar comandos, interpretação de fila/atraso e custos de `UPWATCH_WORKERS` no README. Registrar resultados medidos em Windows ou Linux sem afirmar capacidade universal.
- [ ] Rodar `go test -race ./...`, `go test ./internal/scheduler -run '^$' -bench SchedulerCapacity -benchtime=2s`, `git diff --check`; esperado: suíte e benchmark concluídos, sem erro de diff.
- [ ] Commit `docs: explicar capacidade e métricas do agendador` (incluindo exposição e benchmark).

## Revisão final

- [ ] Revisar o diff completo contra a especificação, incluindo os cinco casos do foco da revisão.
- [ ] Rodar novamente `go test -race ./...` e conferir o estado limpo da branch.
- [ ] Abrir PR para `next` com compatibilidade descrita; não fazer merge de release.
