# Capacidade do agendador na v0.9 — desenho

## Intenção

Permitir que o operador reconheça quando os checks estão começando tarde por saturação do UpWatch, antes de alterar o número de workers ou atribuir a demora ao serviço monitorado. A primeira entrega da [issue #129](https://github.com/Jbnado/upwatch/issues/129) mede o comportamento atual; uma troca na política da fila depende dos resultados da avaliação de carga.

## Entrega

1. Expor em `/metrics` a quantidade de checks vencidos aguardando início, a quantidade em execução, o atraso entre vencimento e início e a duração da verificação. São séries globais do processo, sem rótulos de monitor, conta ou grupo. Os histogramas usam faixas fixas e incluem contagem, soma e bucket infinito; os gauges publicam zero quando a fila está vazia. O acesso a `/metrics` segue a política de token atual.
2. Medir atraso no instante em que um worker realmente inicia o check, usando o vencimento guardado na entrada da fila. Atraso nunca é negativo. A duração mede somente a chamada ao checker; a entrega da batida ao sink continua fora dela. Um check que começa tarde e encontra o alvo disponível registra atraso, sem inventar um incidente.
3. Contar como aguardando os checks já retirados do heap por `takeDue` que ainda esperam por um slot. Ao cancelar o contexto, nenhum desses checks pode ficar marcado como `inFlight` sem execução nem reagendamento. Monitores removidos ou desabilitados durante a espera não devem ser executados.
4. Acrescentar um cenário de carga controlada e reproduzível com checks rápidos, lentos e timeout, intervalos diferentes e grupos desbalanceados. Registrar atraso, crescimento da fila e uso de recursos. Usar o resultado para decidir se a política de fila atual é suficiente. Uma eventual mudança de política será uma segunda entrega com desenho e testes de concorrência próprios.
5. Documentar como interpretar as métricas, quando aumentar `UPWATCH_WORKERS`, e os custos de mais concorrência para memória, conexões, banco e rede. Não impor teto de monitores, quota por grupo ou descarte silencioso.

## Compatibilidade

As séries novas são aditivas. A configuração `UPWATCH_WORKERS`, os intervalos dos monitores, as séries existentes e a semântica de incidentes permanecem. Não há migração de banco nem mudança na API JSON. Contadores e histogramas reiniciam com o processo, como `upwatch_checks_total`.

## Abordagem

O `scheduler` envia amostras e gauges para o mesmo objeto `metrics.Counters` já compartilhado pelo servidor e por `/metrics`. A fila e os workers atualizam as medidas nos pontos de transição, com operações atômicas para permitir raspagem concorrente. A exposição Prometheus é escrita junto às famílias atuais, com unidades em segundos e cardinalidade constante.

Os testes usam o relógio falso do agendador e checkers controlados para demonstrar que cada medida representa o evento certo. A suíte Go roda com `-race`. Cada teste novo é executado primeiro contra o código sem correção e novamente após a remoção temporária da correção para provar que detecta a regressão.

## Limites e decisões

- A primeira entrega não reorganiza a fila por grupo; a avaliação deve demonstrar um problema real antes de escolher uma política de justiça.
- Como critério proposto para essa avaliação, comparar grupos com o mesmo intervalo e perfil de check sob carga equivalente. Crescimento persistente do atraso para um grupo enquanto outro continua sendo atendido, ou ausência de início de checks elegíveis, indica problema de justiça. Atraso de todos os grupos sob sobrecarga deliberada indica falta de capacidade, a ser documentada separadamente. Não há SLO numérico universal nesta entrega: o relatório apresenta percentis e máximos por cenário para o operador escolher workers conforme sua instalação.
- O atraso mede o começo do check, não o tempo até a entrega do resultado. A duração do checker e a latência reportada pelo alvo são conceitos diferentes.
- Não adicionar rótulos por alvo ou grupo para manter custo de raspagem e cardinalidade constantes.
- As faixas exatas dos histogramas e a apresentação do experimento de carga são escolhas de implementação; devem cobrir checks subsegundo, timeout e atraso de minutos.

## Referências

- [Issue #129](https://github.com/Jbnado/upwatch/issues/129).
- [Configuração do Prometheus](https://prometheus.io/docs/prometheus/latest/configuration/configuration/): intervalo e timeout são controles separados, e métricas de scrape ajudam a reconhecer proximidade de limites.
- [Métricas por instância do Prometheus](https://prometheus.io/docs/concepts/jobs_instances/): `scrape_duration_seconds` distingue custo da coleta do estado do alvo.
