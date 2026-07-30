# UpWatch

Monitoramento de disponibilidade que cabe num binário e mostra latência
junto com o estado — porque quase todo incidente começa com o serviço
ficando lento, não caindo.

**[jbnado.github.io/upwatch](https://jbnado.github.io/upwatch)** — o
mesmo conteúdo em página, para quem chega de fora.

Um arquivo, um contêiner, um volume. A interface vem embarcada no
executável: não há nginx ao lado nem pasta de arquivos estáticos que
possa ficar defasada em relação ao servidor que os acompanha.

```
docker run -d --name upwatch -p 8080:8080 -v upwatch:/data \
  ghcr.io/jbnado/upwatch:latest
```

Abra `http://localhost:8080` e crie a conta de administração. É a única
conta criada sem autenticação; depois dela, o cadastro fecha.

## O que ele faz

**Verifica** por HTTP, TCP, ICMP, DNS, TLS e sinal do próprio serviço
(push, para tarefas agendadas e processos sem porta exposta). O intervalo
vai de cinco segundos em diante, por monitor.

**Guarda meses sem guardar meses de dados crus.** As batidas duram uma
semana; depois viram agregado horário e diário, com percentis exatos
calculados sempre a partir do dado cru — nunca percentil de percentil.
Uma instalação com cinco alvos verificando a cada minuto ocupou 2,6 MB
depois de trinta dias.

**Confirma antes de alarmar.** Uma queda só vira incidente depois de N
falhas seguidas, configurável por monitor. Oscilação de rede não acorda
ninguém.

**Sabe distinguir a queda do alvo da queda da própria rede.** Quando as
verificações começam a falhar, uma sonda independente confere se a rede
local ainda responde; se não responder, os resultados viram "sem
medição" em vez de "fora do ar". Essa sonda só ganha esse poder depois
de provar que funciona — uma sonda bloqueada por firewall silenciaria
todos os alertas para sempre.

**Avisa** por webhook, Discord e Slack, com um botão de teste em cada
canal. Descobrir que o alerta não chega durante a queda é descobrir
tarde.

**Agrupa por etiqueta.** Homolog e produção lado a lado sem virar uma
lista única de quarenta linhas: etiquete os alvos e o painel oferece
agrupar por qualquer uma delas. As etiquetas são normalizadas na entrada
— "Produção", "produção " e "PRODUÇÃO" são o mesmo grupo, e não três.

**Tem dois papéis de acesso.** Administrador cadastra e altera;
observador só lê — serve para plantão, gerência e time vizinho
acompanharem sem poder mexer. A barreira está no servidor, não em botão
escondido: um observador com token recebe 403 em qualquer escrita.

**Publica uma página de estado** no formato que Anthropic, Cloudflare e
Google consolidaram: veredito no topo, componentes agrupados, noventa
barras de histórico, incidentes anteriores com linha do tempo. Ela nunca
revela o endereço do alvo nem a causa detectada pela sonda — o que sai é
só o que você escreveu.

**Fala com quem já tem Prometheus** em `/metrics`, sem credencial, para
o alerta morar onde já mora o resto.

## Instalação

### Docker Compose

```bash
curl -O https://raw.githubusercontent.com/Jbnado/upwatch/main/compose.yaml
docker compose up -d
```

Com PostgreSQL, quando a disponibilidade do próprio monitorador importa e
você quer mais de uma instância:

```bash
curl -O https://raw.githubusercontent.com/Jbnado/upwatch/main/compose.postgres.yaml
POSTGRES_PASSWORD=$(openssl rand -base64 24) docker compose -f compose.postgres.yaml up -d
```

### Binário

Baixe o executável da página de releases. Ele é estático e não depende de
nada instalado:

```bash
UPWATCH_DB_DSN=./upwatch.db ./upwatch
```

## Configuração

Tudo por variável de ambiente ou arquivo YAML. A variável ganha do
arquivo, e o arquivo ganha do padrão — assim uma imagem sobe sem
configuração nenhuma e uma instalação séria versiona o YAML.

| Variável | Padrão | O que faz |
|---|---|---|
| `UPWATCH_LISTEN` | `:8080` | Endereço de escuta |
| `UPWATCH_DB_DRIVER` | `sqlite` | `sqlite` ou `postgres` |
| `UPWATCH_DB_DSN` | `/data/upwatch.db` | Caminho do arquivo ou string de conexão |
| `UPWATCH_RETENTION_RAW` | `168h` | Quanto tempo as batidas cruas duram |
| `UPWATCH_RETENTION_HOURLY` | `2160h` | Retenção do agregado horário |
| `UPWATCH_RETENTION_DAILY` | `17520h` | Retenção do agregado diário |
| `UPWATCH_ROLLUP_INTERVAL` | `5m` | De quanto em quanto a agregação roda |
| `UPWATCH_WORKERS` | `50` | Teto de verificações simultâneas |
| `UPWATCH_SESSION_TTL` | `168h` | Validade da sessão da interface |
| `UPWATCH_SECURE_COOKIES` | `false` | Marca o cookie como Secure; ligue ao servir por HTTPS |
| `UPWATCH_PUBLIC_URL` | vazio | Endereço externo, para o feed da página pública usar URLs absolutas |

Durações aceitam sufixo de dias: `90d` é o mesmo que `2160h`.

`UPWATCH_SECURE_COOKIES` fica desligado por padrão de propósito. Muita
instalação caseira serve em HTTP na rede local, e um cookie Secure ali
simplesmente não é enviado — o login pareceria quebrado sem explicação.

## API

A API é de primeira classe, não um acessório da interface: a tela consome
exatamente os mesmos endpoints que qualquer script. A especificação
OpenAPI é servida pela própria instalação, em `/api/v1/openapi.yaml`, e
um teste garante que ela não diverge das rotas de verdade — nos dois
sentidos.

```bash
# Token de acesso: Ajustes → Tokens de acesso
curl -H "Authorization: Bearer upw_..." http://localhost:8080/api/v1/monitors
```

## Página pública de estado

Em Ajustes → Páginas de estado. Marque uma como padrão e ela responde em
`/status`; com várias, cada uma responde no próprio `/status/<slug>`.

Duas coisas que ela faz de propósito e que valem saber antes de publicar:

**As barras são automáticas, o relato não.** A causa que a sonda detecta é
literal e interna — `dial tcp 10.0.3.7:5432: connect: connection refused`
— e entregaria endereço, porta e tecnologia de um serviço que ninguém de
fora deveria enxergar. Por isso ela nunca é publicada. Uma instalação
recém-subida mostra as barras e "nenhum incidente relatado"; o texto que
aparece em "incidentes anteriores" é o que você escrever.

**Cada componente tem um rótulo público.** O monitor pode se chamar
`api-prod-us-east-1` na operação e aparecer como "API" para quem lê, sem
obrigar você a renomeá-lo nem a entregar sua convenção de nomes.

Há também um feed Atom em `/api/v1/public/<slug>/feed.atom`, para
acompanhar sem cadastrar e-mail e para ligar num canal de chat sem que o
UpWatch precise saber falar com aquele canal.

## Prometheus

```yaml
scrape_configs:
  - job_name: upwatch
    static_configs:
      - targets: ['upwatch:8080']
```

Em `upwatch_monitor_status`, 1 é no ar, 0 fora do ar, 2 degradado e -1
sem medição. "Sem medição" não é zero de propósito: confundir os dois
faria o alerta disparar para todo monitor recém-criado.

O endereço do alvo nunca vira rótulo. Além de descrever a topologia
interna, endereço em rótulo é cardinalidade alta — é assim que se derruba
um Prometheus.

## Desenvolvimento

```bash
make test        # suíte completa, com detector de corrida
make test-fast   # pula a simulação de retenção de 30 dias
make lint        # vet, gofmt e tsc
make build       # interface + binário estático
```

A interface é embarcada por `go:embed`, então compilá-la é pré-requisito
de compilar o servidor — o `make build` cuida da ordem.

Para mexer no desenho sem recompilar o binário a cada mudança, suba os
dois lados:

```bash
go run ./cmd/upwatch      # num terminal
cd web && pnpm dev        # noutro; encaminha /api para o Go
```

### Sobre os testes

O projeto é escrito em TDD, e a suíte é o principal artefato de desenho.
Duas partes merecem destaque:

**A suíte de conformidade do armazenamento** roda a mesma bateria contra
SQLite e PostgreSQL — 124 casos idênticos nos dois, zero pulados. É o que
impede "banco plugável" de virar fachada. Para rodar contra PostgreSQL:

```bash
UPWATCH_TEST_POSTGRES_DSN='postgres://...?sslmode=disable' go test ./internal/store/sqlstore/
```

**Os testes de invasão da página pública** atacam a única superfície sem
credencial: travessia de caminho em nove formas, injeção de SQL no slug,
enumeração de páginas, texto hostil, cabeçalho `Host` forjado. Um deles
encontrou um defeito real durante o desenvolvimento.

## Como contribuir

Leia [CONTRIBUTING.md](CONTRIBUTING.md) antes de abrir um pull request —
sobretudo a parte de testes, que é onde este projeto tem opinião.

Falha de segurança não vai em issue pública: veja
[SECURITY.md](SECURITY.md).

## Licença

AGPL-3.0. Você pode usar, modificar e distribuir; se oferecer o UpWatch
modificado como serviço para terceiros, precisa disponibilizar o código
das suas modificações.
