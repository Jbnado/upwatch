# upwatch-probe

A sonda para redes privadas. Ela roda dentro da rede que o UpWatch
central não alcança (atrás de VPN, de firewall, numa VM que só sai para a
internet), executa as verificações localmente com os mesmos checkers do
central e manda cada resultado para um monitor push dele.

```
  rede privada                                   internet
 ┌────────────────────────────────────┐        ┌──────────────────┐
 │ redis:6379   bff-api:3000          │        │ UpWatch central  │
 │     ▲             ▲                │  push  │ (VPS)            │
 │     └── upwatch-probe ─────────────┼───────►│ /api/v1/push/... │
 │         (só conexão de saída)      │ HTTPS  │                  │
 └────────────────────────────────────┘        └──────────────────┘
```

## O que sai da rede, e o que não sai

A sonda roda na máquina de outra pessoa, muitas vezes de produção. Ela
não pode virar porta de entrada:

- **Só sai, nunca escuta.** Por padrão nenhuma porta é aberta. A interface
  web é opcional (`--ui`) e escuta em `127.0.0.1`, salvo
  `--ui-listen-any`.
- **Mão única.** A sonda manda resultado ao central e nunca recebe
  comando, configuração nem código dele. Comprometer o central não dá
  controle sobre as máquinas onde as sondas rodam.
- **Pouco e previsível.** Cada envio é `{"status","msg","latency_ms"}`: o
  estado, a causa curta (limpa e cortada em 200 caracteres) e a latência.
  Nenhum corpo de resposta, cabeçalho, endereço interno ou credencial.
  `--dry-run` imprime o corpo exato antes de qualquer envio.
- **Sem `docker.sock`.** Montar o socket é dar root no host, e a sonda não
  precisa dele: o estado dos serviços sai de HTTP, TCP, Redis e banco.
- **Segredo em arquivo 600**, nunca em variável de ambiente (que aparece no
  `docker inspect`) e nunca no log. O token do push aparece mascarado
  (`.../push/abcd…`) em toda saída, porque a sessão do jump server costuma
  ser gravada. A sonda recusa um arquivo de configuração legível por
  outros usuários.
- **Push só em HTTPS.** O token viaja no caminho; em HTTP qualquer um no
  trajeto o lê. `allow_http_push` existe para o central na mesma rede
  confiável, e precisa ser pedido.
- **Não se atualiza sozinha.** Atualizar é trocar a imagem, de propósito.

## Antes de instalar: os monitores no central

Cada verificação local corresponde a um **monitor push** no central, e a
sonda tem mais um, só dela, para o sinal de vida:

| Monitor push no central | Para quê |
| --- | --- |
| `sonda vm-prod-1` | sinal de vida: cai quando a sonda some |
| `rq-workers (vm-prod-1)` | a asserção do Redis |
| `bff health (vm-prod-1)` | o `/health` do BFF |

Crie-os pela interface do central (tipo **push**) e copie o endereço de
push de cada um (`https://upwatch.exemplo.com/api/v1/push/TOKEN`).

Use o mesmo intervalo da sonda (60s) e ligue as verificações ao sinal de
vida: no formulário de cada uma, em **depende de** (o sinal de vida de um
grupo de sondas aparece sugerido no topo), ou de uma vez pelo detalhe do
sinal de vida, em **Mais ações → Ligar dependentes**: a árvore de grupos
marca um ambiente inteiro num clique. Marcar um grupo liga as
verificações que estão nele agora; uma criada depois não vira dependente
sozinha. Depois do primeiro, a seção **Dependentes** fica à vista no
detalhe, com ligar e desligar.

Ligadas, quando a sonda para, o único aviso é "sonda vm-prod-1 está fora
do ar", dizendo quantas verificações ficaram sem aviso. As verificações
aparecem como "sem sinal — sonda vm-prod-1 fora", em cinza, e o intervalo
vira lacuna no histórico delas, não queda. Quando a sonda volta, a
verificação que continuar falhando avisa normalmente. Como cada monitor
percebe o silêncio na própria janela, a falha de uma verificação espera
o sinal de vida decidir — por até a janela dele mais um intervalo — antes
de virar aviso.

## Instalação numa VM, um comando por linha

Pensado para o terminal web de um jump server: cada linha é um comando
completo, sem laço, sem variável e sem tela cheia. Troque `TOKEN_...` pelos
endereços copiados do central.

```bash
docker volume create upwatch-probe-config
docker run -d --name upwatch-probe --restart unless-stopped --read-only --cap-drop ALL --security-opt no-new-privileges --memory 64m --cpus 0.2 --pids-limit 64 --network redis-network -v upwatch-probe-config:/config ghcr.io/jbnado/upwatch-probe:main
docker exec upwatch-probe upwatch-probe init --name vm-prod-1 --heartbeat-url https://upwatch.exemplo.com/api/v1/push/TOKEN_DA_SONDA
docker exec upwatch-probe upwatch-probe add --name rq-workers --type redis --target redis:6379 --assert "SCARD rq:workers:default >= 1" --assert "LLEN rq:queue:default < 100" --push-url https://upwatch.exemplo.com/api/v1/push/TOKEN_DO_RQ --interval 60s
docker exec upwatch-probe upwatch-probe add --name bff --type http --target http://bff-api:3000/health --expect-status 200 --push-url https://upwatch.exemplo.com/api/v1/push/TOKEN_DO_BFF --interval 60s
docker exec upwatch-probe upwatch-probe add --name rpa-services --type http --target http://rpa-services-api:8000/health --expect-status 200 --push-url https://upwatch.exemplo.com/api/v1/push/TOKEN_DO_RPA --interval 60s
docker exec upwatch-probe upwatch-probe test rq-workers --dry-run
docker exec upwatch-probe upwatch-probe test bff
docker exec upwatch-probe upwatch-probe status
docker logs --tail 20 upwatch-probe
```

O contêiner sobe antes de existir configuração e espera por ela. Cada
`add` só grava o arquivo; o daemon percebe a mudança em até 2 segundos e
passa a verificar, sem reinício.

O que cada opção do `docker run` faz:

| Opção | Por quê |
| --- | --- |
| `--read-only` | a imagem não é gravável; só o volume de configuração é |
| `--cap-drop ALL` | nenhuma capability do kernel |
| `--security-opt no-new-privileges` | nada lá dentro ganha privilégio por setuid |
| `--memory 64m --cpus 0.2 --pids-limit 64` | a sonda não disputa recurso com a aplicação vigiada |
| `--network redis-network` | alcança `redis:6379` pela rede overlay do stack (que precisa ser *attachable*) |
| `-v upwatch-probe-config:/config` | o único lugar gravável: `probe.yaml` e o estado |
| `--restart unless-stopped` | volta sozinha depois de um reboot da VM |

Se o BFF e a API estão noutra rede overlay, ligue a sonda a ela também
(precisa ser *attachable*):

```bash
docker network connect nome-da-rede-do-bff upwatch-probe
```

No Docker Swarm, o nome do serviço na rede overlay costuma levar o
prefixo do stack (`meustack_bff-api`); o nome curto só resolve se o
serviço declarar esse alias. Na dúvida, `upwatch-probe test` diz se o nome
resolve.

A sonda roda **fora** do stack, como contêiner avulso: ela não depende do
deploy de ninguém, e um deploy do stack não a derruba.

### Fixar pelo digest e conferir a assinatura

A tag anda; o digest não. Para a imagem que roda na rede alheia, fixe o
digest:

```bash
docker buildx imagetools inspect ghcr.io/jbnado/upwatch-probe:main
```

e use `ghcr.io/jbnado/upwatch-probe@sha256:...` no `docker run`. A imagem
é assinada pelo workflow de release (cosign, sem chave) e leva SBOM:

```bash
cosign verify ghcr.io/jbnado/upwatch-probe@sha256:... --certificate-identity-regexp '^https://github.com/Jbnado/upwatch/.github/workflows/release.yml@' --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Atualizar

```bash
docker pull ghcr.io/jbnado/upwatch-probe@sha256:NOVO_DIGEST
docker rm -f upwatch-probe
docker run -d --name upwatch-probe ... ghcr.io/jbnado/upwatch-probe@sha256:NOVO_DIGEST
```

A configuração fica no volume e sobrevive à troca.

## O assistente

Para quem prefere responder perguntas:

```bash
docker exec -it upwatch-probe upwatch-probe setup
```

Uma pergunta por linha, o padrão entre colchetes (Enter aceita), a
resposta errada recusada na hora com o motivo. Cada verificação é testada
antes de ser gravada; a que falha só é gravada se você confirmar. No fim,
o setup mostra o token da interface web.

```
Nome desta sonda [vm-prod-1]:
Endereço de push do sinal de vida (monitor push "sonda vm-prod-1" no central; vazio para nenhum): https://...
Intervalo do sinal de vida [60s]:
Adicionar uma verificação? (s/n) [s]:
  Nome da verificação: rq-workers
  Tipo (http, tcp, redis, postgres, mysql, dns, tls, icmp) [http]: redis
  Alvo (ex.: redis:6379): redis:6379
  Asserções de leitura, uma por linha (ex.: SCARD rq:workers:default >= 1); vazio termina.
  Asserção: SCARD rq:workers:default >= 1
  Asserção:
  Intervalo [60s]:
  Endereço de push desta verificação: https://...
  Testando rq-workers...
  Resultado: up, 1ms
  Salvar esta verificação? (s/n) [s]:
Adicionar outra verificação? (s/n) [n]:
```

## Comandos

| Comando | O que faz |
| --- | --- |
| `run [--dry-run] [--ui 127.0.0.1:8090] [--ui-listen-any]` | o daemon; é o padrão do contêiner |
| `setup` | o assistente acima |
| `init --name ... --heartbeat-url ... [--heartbeat-interval 60s] [--allow-http-push]` | nome da sonda e sinal de vida |
| `add --name ... --type ... --target ... --push-url ... [opções]` | inclui uma verificação (`--replace` substitui) |
| `list [--json]` | lista, com a asserção na mesma forma que o `add` aceita |
| `test <nome> [--dry-run]` | roda uma vez, mostra o resultado e o corpo, e envia (com `--dry-run`, não envia) |
| `remove <nome>` | remove |
| `status` | último resultado e último envio de cada verificação e do sinal de vida |
| `ui-token [--rotate]` | mostra, ou troca, o token da interface web |

Todos aceitam `--config` (padrão `/config/probe.yaml`, ou
`UPWATCH_PROBE_CONFIG`). Cor só aparece num terminal de verdade e some com
`NO_COLOR`.

Opções do `add`:

| Opção | Tipo | |
| --- | --- | --- |
| `--assert "SCARD rq:workers:default >= 1"` | redis | repetível; `COMANDO [chave] [campo] OP VALOR` |
| `--expect-status 200,204` | http | padrão: qualquer 2xx |
| `--body-contains texto` | http | |
| `--username u --password-stdin` | redis, postgres, mysql, http (basic auth) | a senha vem da primeira linha da entrada |
| `--database`, `--query` | postgres, mysql | consulta só de leitura |
| `--config-json '{...}'` | todos | a configuração completa do checker, a mesma do central |
| `--interval 60s --timeout 10s --degraded-latency 2s` | todos | |

As asserções de Redis aceitam só comandos de leitura: `SCARD`, `LLEN`,
`GET`, `EXISTS`, `HGET`, `ZCARD`, `DBSIZE`, `INFO` e `WORKERS`. Os
operadores são `==`, `!=`, `<`, `<=`, `>` e `>=`. Exemplos:

```
SCARD rq:workers:default >= 1
LLEN rq:queue:default < 100
HGET rq:worker:w1 state != 0
INFO memory used_memory < 1000000000
DBSIZE > 0
WORKERS bull:excel-render >= 1
```

`WORKERS <prefixo>:<fila>` conta os workers vivos de uma fila Bull ou
BullMQ. Essas bibliotecas não registram worker em chave nenhuma (e
`EXISTS bull:<fila>:stalled-check` pisca, porque a chave expira entre uma
checagem e outra); cada worker dá à sua conexão o nome
`<prefixo>:<base64 da fila>`, com `:w:<nome>` no fim no BullMQ quando o
worker tem nome, e a asserção conta essas conexões no `CLIENT LIST`. O
push leva só a contagem (`WORKERS bull:excel-render = 0, esperado >= 1`),
nunca endereço ou nome de outras conexões. O usuário precisa de
`+client|list` na ACL (abaixo).

A senha nunca vai em flag: ficaria no histórico, na gravação do jump
server e no `ps`. Use `--password-stdin` ou, melhor, um usuário sem senha
que só leia o necessário (abaixo).

## O arquivo `probe.yaml`

```yaml
# Configuração da upwatch-probe.
name: vm-prod-1
heartbeat:
  push_url: https://upwatch.exemplo.com/api/v1/push/TOKEN_DA_SONDA
  interval: 1m0s
ui_token: 9f2c...          # gerado no init/setup; protege a interface web
# allow_http_push: true    # só com o central na mesma rede confiável
checks:
  - name: rq-workers
    type: redis
    target: redis:6379
    interval: 1m0s
    timeout: 10s
    push_url: https://upwatch.exemplo.com/api/v1/push/TOKEN_DO_RQ
    config:
      assertions:
        - {command: SCARD, key: "rq:workers:default", op: gte, value: 1}
        - {command: LLEN, key: "rq:queue:default", op: lt, value: 100}
  - name: bff
    type: http
    target: http://bff-api:3000/health
    interval: 1m0s
    timeout: 10s
    # degraded_latency: 2s
    push_url: https://upwatch.exemplo.com/api/v1/push/TOKEN_DO_BFF
    config:
      expect_status: [200]
```

`config` é a configuração do checker, a mesma que o central aceita em
JSON, aqui em YAML. Campo desconhecido é erro (um `intervall` ignorado
faria a verificação rodar com o padrão sem ninguém saber). O arquivo pode
ser editado à mão; o daemon o relê quando ele muda, ou com
`docker kill --signal HUP upwatch-probe`. Uma edição com erro não derruba
o que estava rodando: a sonda segue com a última configuração boa e diz o
motivo no log.

Ao lado dele fica `probe.state.json`, onde o daemon grava o último
resultado e o último envio. É de lá que o `status` lê.

## Envio e falhas

Cada verificação manda `POST` com o corpo JSON para o seu endereço de
push. Se o envio falha por rede, 5xx ou 429, a sonda tenta de novo depois
de 2s, 4s e 8s (respeitando o `Retry-After`) e desiste até a próxima
medição. A fila guarda um sinal só: o resultado novo substitui o que
ainda não saiu. Uma queda longa do central não acumula fila nem faz a
sonda despejar sinais velhos quando ele volta. 401 (token errado ou
monitor apagado) não é repetido; aparece no `status` e no log.

O envio respeita `HTTPS_PROXY` e não segue redirecionamento: seguir
mandaria o token a outro host.

## Credenciais mínimas

O melhor segredo é o que não abre nada. Crie um usuário só para a sonda:

```
# Redis: só os comandos que a sonda usa, só nas chaves do RQ
ACL SETUSER upwatch on >SENHA ~rq:* -@all +ping +info +scard +llen
# com WORKERS (Bull/BullMQ), acrescente; CLIENT LIST não lê chave nenhuma,
# mas mostra endereço e nome de todas as conexões do Redis
ACL SETUSER upwatch +client|list
# e ~bull:* se também vigiar as chaves da fila (LLEN bull:<fila>:wait)
```

```sql
-- PostgreSQL: entra e não lê tabela nenhuma (a consulta padrão é SELECT 1)
CREATE ROLE upwatch LOGIN PASSWORD 'SENHA';
-- MySQL: só o direito de conectar
CREATE USER 'upwatch'@'%' IDENTIFIED BY 'SENHA';
```

Os checkers recusam comando de escrita de qualquer jeito: o Redis aceita
só a lista de leitura acima, e o PostgreSQL e o MySQL rodam a consulta numa
transação somente leitura.

## Interface web (opcional)

Desligada por padrão. Para ligar, o contêiner precisa escutar fora do
próprio loopback, e a porta só é publicada no loopback da VM:

```bash
docker run -d --name upwatch-probe ... -p 127.0.0.1:8090:8090 ghcr.io/jbnado/upwatch-probe:main run --ui 0.0.0.0:8090 --ui-listen-any
docker exec upwatch-probe upwatch-probe ui-token
```

Acesse por um túnel SSH até a VM (`ssh -L 8090:127.0.0.1:8090 vm`) e
entre com o token. A interface lista as verificações com o estado e o
último envio, e permite adicionar e remover. Mesmo em loopback ela exige o
token: qualquer processo da VM alcança a porta.

## ICMP

O ping sem privilégio usa socket UDP de ICMP, permitido pela faixa
`net.ipv4.ping_group_range`, que o Docker já abre para o contêiner. Com
`--cap-drop ALL` o modo `privileged` (socket cru) não funciona, e não
deveria: ele exigiria `NET_RAW`.

## Sem Docker

O binário é estático e roda em qualquer Linux:

```bash
upwatch-probe add --config /etc/upwatch-probe/probe.yaml --name ...
upwatch-probe run --config /etc/upwatch-probe/probe.yaml
```

Os binários estão na página de releases, com `SHA256SUMS`.
