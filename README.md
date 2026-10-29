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

**Verifica** por HTTP, TCP, ICMP, DNS, TLS, sinal do próprio serviço
(push, para tarefas agendadas e processos sem porta exposta) e pelo
protocolo de Redis, PostgreSQL e MySQL. O intervalo vai de cinco segundos
em diante, por monitor. No HTTP, além do código de resposta, confere
valores do corpo JSON — veja
[Conferindo o corpo JSON](#conferindo-o-corpo-json).

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

**Avisa** por webhook, Discord, Slack, [e-mail](#e-mail) e [ntfy](#ntfy),
com um botão de teste em cada canal. Descobrir que o alerta não chega
durante a queda é descobrir tarde. O webhook genérico aceita cabeçalhos
próprios e deixa você escolher a forma do corpo — veja
[Webhook](#webhook). Canal vinculado a um grupo avisa sobre todos os
monitores dele, inclusive os criados depois; o vínculo direto com um
monitor continua existindo, e cada canal recebe o aviso uma vez só.

Uma entrega que falha é repetida até quatro vezes, com espera crescente,
quando a falha pode passar: erro de rede, timeout, HTTP 429 ou 5xx, SMTP
4xx. Quando o destino diz que aquela entrega nunca vai passar — HTTP 400,
401, 403, 404, 413 ou 422, SMTP 5xx, inclusive a senha recusada (535) —, o
aviso é dado como não entregue na primeira, e o motivo vai para o log.

**Organiza por grupo.** Grupo tem dois níveis no máximo — Sistema →
Ambiente, como `RPA` → `prod` — e todo monitor fica num ambiente (ou em
"Sem grupo", onde cai tudo de uma instalação anterior aos grupos). Dentro
do grupo o nome é curto: o `bff` de produção e o de homologação convivem
sem repetir o grupo no nome. Etiquetas continuam existindo como recorte
transversal (`banco`, `crítico`) e são normalizadas na entrada —
"Produção", "produção " e "PRODUÇÃO" são uma etiqueta só —, mas nunca
controlam acesso.

**Tem dois papéis de acesso, com escopo por grupo.** Administrador
cadastra e altera; observador só lê — serve para plantão, gerência e
time vizinho acompanharem sem poder mexer. Cada conta alcança todos os
grupos ou uma lista deles, e dar acesso a um sistema dá acesso aos
ambientes dele, inclusive os criados depois. A barreira está no
servidor, não em botão escondido: um observador com token recebe 403 em
qualquer escrita, e um monitor fora do escopo responde 404 em toda rota
— lista, detalhe, batidas, agregados, incidentes, eventos ao vivo,
exportação e resumos —, igual a um que não existe. Administrador com
escopo só cria e altera dentro dos próprios grupos, não concede escopo
maior que o dele e não mexe em canal (nem no vínculo de canal com
monitor ou grupo), página pública ou relato, que afetam todos os
grupos. Ele também só enxerga os canais ligados ao que é dele, e não
aponta monitor para a rede interna do servidor (ver
`UPWATCH_RESTRICT_PRIVATE_TARGETS`).

**Publica uma página de estado** no formato que Anthropic, Cloudflare e
Google consolidaram: veredito no topo, componentes agrupados, noventa
barras de histórico, incidentes anteriores com linha do tempo. Ela nunca
revela o endereço do alvo nem a causa detectada pela sonda — o que sai é
só o que você escreveu.

**Fala com quem já tem Prometheus** em `/metrics`, para o alerta morar
onde já mora o resto — aberto por padrão, ou atrás de um token próprio.

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
mkdir -p secrets
openssl rand -hex 24 > secrets/postgres_password
printf 'postgres://upwatch:%s@postgres:5432/upwatch?sslmode=disable\n' \
  "$(cat secrets/postgres_password)" > secrets/upwatch_db_dsn
openssl rand -base64 32 > secrets/upwatch_secret_key
docker compose -f compose.postgres.yaml up -d
```

A senha do banco, o DSN e a chave dos segredos vão por arquivo (ver
[Segredos](#segredos)).

### Primeiro acesso

Abra a interface e crie a conta de administração. A tela pede o **token
de configuração**: sem ele, uma instância recém-publicada na internet
seria de quem chegasse antes de você. O token está no log do processo,
numa linha só, escrita uma vez na subida:

```bash
docker compose logs upwatch | grep token_de_configuracao
```

Para escolher o valor em vez de ler do log — num deploy automatizado,
por exemplo —, defina `UPWATCH_SETUP_TOKEN` (ou
`UPWATCH_SETUP_TOKEN_FILE`); aí ele não vai para o log. O gerado muda a
cada reinício, e qualquer um deles deixa de valer quando a conta passa a
existir: instalação já configurada não gera token nem o menciona. O
`POST /api/v1/setup` compara o token em tempo constante e limita as
tentativas por origem.

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
| `UPWATCH_DB_DSN` | `/data/upwatch.db` | Caminho do arquivo ou string de conexão; aceita `_FILE` |
| `UPWATCH_RETENTION_RAW` | `168h` | Quanto tempo as batidas cruas duram |
| `UPWATCH_RETENTION_HOURLY` | `2160h` | Retenção do agregado horário |
| `UPWATCH_RETENTION_DAILY` | `17520h` | Retenção do agregado diário |
| `UPWATCH_ROLLUP_INTERVAL` | `5m` | De quanto em quanto a agregação roda |
| `UPWATCH_WORKERS` | `50` | Teto de verificações simultâneas |
| `UPWATCH_SESSION_TTL` | `168h` | Validade da sessão da interface |
| `UPWATCH_SECURE_COOKIES` | `false` | Marca o cookie como Secure; ligue ao servir por HTTPS |
| `UPWATCH_PUBLIC_URL` | vazio | Endereço externo, para o feed da página pública usar URLs absolutas |
| `UPWATCH_METRICS_TOKEN` | vazio | Exige `Authorization: Bearer <token>` em `/metrics`; vazio deixa aberto enquanto não houver conta restrita; aceita `_FILE` |
| `UPWATCH_SETUP_TOKEN` | vazio | Token exigido na tela de primeiro acesso; vazio, um aleatório é gerado na subida e escrito só no log; sem efeito depois que a conta existe; aceita `_FILE` (ver [Primeiro acesso](#primeiro-acesso)) |
| `UPWATCH_SECRET_KEY` | vazio | Chave (base64 de 32 bytes) que cifra as credenciais no banco; vazia, o SQLite usa a gerada ao lado do banco; aceita `_FILE` (ver [Segredos](#segredos)) |
| `UPWATCH_RESTRICT_PRIVATE_TARGETS` | `true` | Recusa, para contas com acesso restrito a grupos, monitor cujo alvo resolva para a rede interna do servidor |

Durações aceitam sufixo de dias: `90d` é o mesmo que `2160h`.

`UPWATCH_SECURE_COOKIES` fica desligado por padrão de propósito. Muita
instalação caseira serve em HTTP na rede local, e um cookie Secure ali
simplesmente não é enviado — o login pareceria quebrado sem explicação.

`UPWATCH_RESTRICT_PRIVATE_TARGETS` fica ligado por padrão. O checker
conecta a partir do servidor, e uma conta restrita a um grupo que
cadastra monitor para `127.0.0.1`, `10.0.0.0/8` ou `169.254.169.254`
usaria o servidor como sonda na rede de dentro dele — varrendo portas
pela causa de cada falha e, no metadado da nuvem, saindo com
credencial. Com a regra ligada, criar ou editar monitor com conta
restrita recusa destino que resolva para loopback, rede privada (RFC
1918 e ULA), enlace local ou espaço compartilhado de provedor, e também
nome que não resolve — em todo tipo que conecta, inclusive Redis,
PostgreSQL e MySQL. Contas que alcançam todos os grupos não são
afetadas, e editar sem mudar o destino escolhido por outra pessoa
continua permitido. Desligue se a instalação existe justamente para os
times monitorarem a rede interna. A conferência ainda é só no cadastro:
o checker não confere na hora de conectar, e um nome que troca de
endereço depois (DNS rebinding) ou um redirecionamento HTTP para dentro
passam (issue #59).

## Segredos

O UpWatch guarda credenciais que precisa usar de volta: a senha do
Redis, do PostgreSQL e do MySQL monitorados, o basic auth e os
cabeçalhos de credencial do HTTP, o token do push, a senha SMTP, o token
do ntfy e as URLs de webhook (a do Discord e a do Slack levam o token no
caminho). Elas não podem virar hash, como as senhas de quem entra na
interface, então ficam **cifradas no banco** com AES-256-GCM. Um backup,
uma cópia do volume ou um `pg_dump` não as entrega em texto. Na
memória e pela API nada muda: quem lê continua sem vê-las, e o
administrador continua vendo o token do push para copiar o endereço.

A coluna que a busca do `/push` usa guarda só o SHA-256 do token.

### Onde fica a chave

- `UPWATCH_SECRET_KEY` (o valor) ou `UPWATCH_SECRET_KEY_FILE` (o caminho
  de um arquivo com ele). A chave é base64 de 32 bytes:
  `openssl rand -base64 32`.
- **SQLite sem chave configurada:** na primeira subida o UpWatch gera uma
  em `secret.key`, ao lado do banco (`/data/secret.key` na imagem), com
  permissão 600, e avisa no log. Assim um `docker run` sem configuração
  continua subindo. Mas a chave ao lado do banco protege pouco: quem
  copia o volume leva os dois. Copie-a para fora do volume, aponte
  `UPWATCH_SECRET_KEY_FILE` para a cópia e apague a gerada.
- **PostgreSQL:** a chave precisa ser configurada. Ela não é gerada
  sozinha porque cada instância sortearia a sua, e uma não leria o que
  a outra gravou. Todas as instâncias usam a mesma.

Na partida, o UpWatch confere a chave contra um registro-sentinela
cifrado. **Chave errada**, ou **chave ausente num banco que já tem
credencial cifrada**, recusa a subida com uma mensagem dizendo o que
configurar, em vez de rodar sem conseguir verificar nada.

Ao atualizar de uma versão anterior, as credenciais em texto puro são
cifradas na primeira subida com chave, sem perda. Voltar o binário para
uma versão anterior depois disso não é suportado: ela leria a cifra como
se fosse a senha.

### Se a chave se perder

As credenciais guardadas se perdem com ela: não há como decifrá-las, e
nenhuma porta dos fundos existe para isso. O resto do banco continua
íntegro — monitores, histórico, incidentes, páginas públicas, contas.

Enquanto restar credencial cifrada, o UpWatch recusa subir sem a chave.
Para voltar, com ele parado, apague as credenciais cifradas e suba com
uma chave nova:

```bash
upwatch forget-secrets --yes
```

O comando troca cada credencial cifrada por vazio e apaga o
registro-sentinela; nada mais é tocado. Depois de subir, digite de novo
pela interface as senhas dos monitores, os tokens e as URLs dos canais
(o monitor push precisa de um token novo). No SQLite sem chave
configurada, apague também a `secret.key` antiga, para a subida gerar
outra.

Por isso a chave entra no backup, **guardada em outro lugar** que o
banco.

### Trocando a chave

Com o UpWatch parado (uma instância no ar continuaria gravando com a
chave antiga):

```bash
openssl rand -base64 32 > nova.key
upwatch rotate-secret-key --old-key-file atual.key --new-key-file nova.key
```

Sem as opções, a chave atual é a da partida (`UPWATCH_SECRET_KEY`,
`_FILE` ou a gerada ao lado do banco) e a nova vem de
`UPWATCH_NEW_SECRET_KEY` ou `UPWATCH_NEW_SECRET_KEY_FILE`. Na imagem:
`docker compose run --rm upwatch rotate-secret-key ...`. Tudo é recifrado
numa transação só: ou passa inteiro para a nova, ou nada muda. Depois,
aponte `UPWATCH_SECRET_KEY_FILE` para a nova antes de subir.

### Opções sensíveis por arquivo (`_FILE`)

`UPWATCH_DB_DSN`, `UPWATCH_METRICS_TOKEN`, `UPWATCH_SECRET_KEY` e
`UPWATCH_SETUP_TOKEN` aceitam
também `<NOME>_FILE`, com o caminho de um arquivo cujo conteúdo é o
valor — a convenção das imagens oficiais, e o que o Docker Swarm, o
Kubernetes e o Compose montam como secret. Variável de ambiente aparece
em `docker inspect`, em `/proc/<pid>/environ` e em dump de diagnóstico;
arquivo, não. A quebra de linha final do arquivo é ignorada. Definir a
variável e a `_FILE` juntas para a partida com erro, assim como uma
`_FILE` que aponta para arquivo inexistente ou vazio. O
[`compose.postgres.yaml`](compose.postgres.yaml) mostra o uso com
`secrets:`.

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

## Conferindo o corpo JSON

Um health que responde 200 com `{"db":"down"}` está fora do ar, e o código
de resposta não diz isso. Em `json_assertions` você declara o que precisa
valer no corpo:

```json
{
  "json_assertions": [
    { "path": "$.db", "op": "eq", "value": "up" },
    { "path": "$.queue_lag", "op": "lt", "value": 100 },
    { "path": "$.workers[0].alive", "op": "exists" }
  ]
}
```

Todas precisam passar. Quando uma falha, a causa no aviso diz qual e o que
foi encontrado: `$.queue_lag = 340, esperado < 100`. Como a causa vai
para chat e webhook, texto encontrado — que pode ser um token ou um
e-mail — aparece só como tamanho (`$.status = texto de 12 caracteres,
esperado contém "ok"`), e lista e objeto só como contagem; em `eq` e `ne`
contra um texto curto, a causa diz só `$.db diferente do esperado,
esperado = "up"`. Números, booleanos e `null` são mostrados. O valor
esperado é cortado em 80 caracteres.

| Operador | Passa quando |
|---|---|
| `eq`, `ne` | o valor é (ou não é) igual, **com o mesmo tipo**: `"100"` não é `100` |
| `lt`, `lte`, `gt`, `gte` | a comparação vale; só entre números |
| `contains` | o texto contém o trecho, ou a lista contém o elemento |
| `exists` | o caminho existe, mesmo com valor `null`; não leva `value` |
| `absent` | o caminho não existe; não leva `value` |

Caminho ausente é diferente de qualquer valor, `null` incluído: `ne` passa
quando o campo não está lá, e `eq` com `null` reprova. Para exigir que um
campo não exista, use `absent`; para exigir que exista com valor `null`,
`eq` com `null`.

Números comparam pelo valor e sem passar por ponto flutuante: `340` é igual
a `340.0`, e um id acima de 2^53 encontrado no corpo não se confunde com o
vizinho. O valor esperado, porém, precisa caber exato num float64 — é como
a interface o lê, e `9007199254740993` voltaria como `…992` na próxima
gravação pela tela. Números com mais de 100 caracteres ou expoente fora de
±10000 não entram em comparação: no cadastro são recusados, e no corpo
derrubam o monitor com a causa "número fora da faixa comparável".

O caminho é um subconjunto pequeno de JSONPath — `$`, `$.chave`,
`$.lista[0].chave` e `$["chave com espaço"]` — sem filtros, wildcards nem
fatias. Cada caminho aponta para no máximo um valor, então `eq` nunca
precisa decidir entre "todos" e "algum".

Caminho mal formado, operador desconhecido e `lt` contra texto são
recusados no cadastro, não na execução. Um corpo que não é JSON — a página
de erro HTML do proxy na frente da API, o caso mais comum — derruba o
monitor com essa causa. O corpo é lido até 1 MiB; um JSON maior que isso
chega cortado, e a causa diz que o motivo é o limite, não o alvo.

Na interface, fica em "conferir valores do corpo JSON", no cadastro do
monitor HTTP.

## Webhook

O canal `webhook` entrega um POST com `Content-Type: application/json`.
Sem configuração extra, o corpo é este envelope:

```json
{
  "text": "api-de-producao está fora do ar: connection refused",
  "monitor": "api-de-producao",
  "monitor_id": 7,
  "target": "https://api.exemplo.com",
  "status": "down",
  "previous_status": "up",
  "message": "connection refused",
  "at": "2026-07-30T18:42:11Z",
  "duration_seconds": 0
}
```

`status` e `previous_status` são `up`, `down`, `degraded` ou `unknown`.
`duration_seconds` é quanto durou o estado **anterior**: na queda vem 0, na
volta vem o tempo que o alvo ficou fora. `message` é a causa observada e
pode vir vazia.

### Escolhendo a forma do corpo

Um destino que já existe espera os campos com os nomes dele, e nem sempre
dá para mudar quem recebe. Em `body_template` você declara a forma, e os
marcadores são substituídos:

```json
{
  "url": "https://automacao.exemplo/alertas",
  "headers": { "X-Chave": "…" },
  "body_template": {
    "event": "$status",
    "service": { "name": "$monitor", "id": "$monitor_id" },
    "outage_seconds": "$duration_seconds",
    "summary": "[$status] $monitor"
  }
}
```

| Marcador | Entrega |
|---|---|
| `$monitor` | Nome do alvo |
| `$monitor_id` | Identificador, como número |
| `$target` | Endereço verificado |
| `$status` | `up`, `down`, `degraded` ou `unknown` |
| `$previous_status` | O estado anterior |
| `$message` | Causa observada, quando houver |
| `$at` | Instante da mudança |
| `$duration_seconds` | Duração do estado anterior, como número |
| `$text` | A frase pronta do aviso |

Também vale `${nome}`, para quando o marcador encosta em texto que
continuaria o identificador, e `$$` escreve um cifrão literal.

**Sozinho, o marcador entrega o valor com o tipo dele** — `"$duration_seconds"`
chega como `300`, não `"300"`. Dentro de um texto, compõe a frase. A
diferença importa para quem valida esquema do outro lado.

A substituição acontece sobre a estrutura JSON já decodificada, e o
resultado é serializado de volta — nunca por concatenação de texto. É o
que garante que uma aspa no nome do monitor ou uma quebra de linha na
causa não produzam um corpo que o destino recusa, perdendo o aviso da
queda por causa da própria queda.

Marcador desconhecido é recusado no cadastro do canal, não na entrega:
descobrir o erro de digitação durante o incidente é descobrir tarde
demais. Discord e Slack recusam `body_template` pelo mesmo motivo — o
corpo deles é ditado pelo destino.

### Modelo da frase

Em webhook, Discord e Slack, `template` troca a frase padrão por um modelo
[text/template](https://pkg.go.dev/text/template) do Go, como
`"{{.Monitor.Name}} => {{.Status}}"`. O modelo enxerga `Monitor` (`ID`,
`Name`, `Target`, `Type` e `Tags`), `Status`, `Message`, `Duration`, `At` e
`Text`, a frase pronta. A configuração do monitor fica de fora: ela guarda
a senha do check HTTP e o token do push, e quem cadastra canal não precisa
deles para escrever uma frase. Um modelo que tenta ler campo que não está
na lista, como `.Monitor.Config`, é recusado no cadastro. Os marcadores de
`body_template` seguem a mesma regra.

Nome, alvo e causa vêm de fora — a causa de um monitor push é escrita por
quem bate no UpWatch —, então não viram menção em massa. No Discord, o
aviso vai com `allowed_mentions` vazio, e um `@everyone` na causa chega
como texto, sem pingar ninguém. No Slack, `<`, `>` e `&` desses campos são
escapados, e `<!channel>` ou um link disfarçado chegam como texto; o que
você escreve no próprio `template`, como um link para o runbook, passa
intacto.

Os campos avançados também estão na interface, em Ajustes → Canais de
aviso, atrás de "cabeçalhos e formato do corpo".

## Redis, PostgreSQL e MySQL

Um Redis sem memória ou um PostgreSQL sem conexões livres aceitam TCP e,
na prática, estão fora do ar. Os monitores desses tipos entram de verdade:

- **Redis** envia `PING` (com usuário de ACL, senha e TLS opcionais) e,
  se configurado, acusa quando `used_memory` passa de um limite. Aceita
  também condições sobre comandos de leitura — só `SCARD`, `LLEN`, `GET`,
  `EXISTS`, `HGET`, `ZCARD`, `DBSIZE` e `INFO`, nunca comando livre. Para
  vigiar workers do RQ, por exemplo:

  ```json
  {"assertions": [
    {"command": "SCARD", "key": "rq:workers:default", "op": "gte", "value": 1},
    {"command": "LLEN",  "key": "rq:queue:default",   "op": "lt",  "value": 500}
  ]}
  ```

  Sem worker, a causa do alerta diz `SCARD rq:workers:default = 0,
  esperado >= 1`.

- **PostgreSQL e MySQL** abrem sessão e executam uma consulta (padrão
  `SELECT 1`) numa transação somente leitura; a latência é a duração da
  consulta. A consulta começa com `SELECT`, `WITH`, `SHOW`, `VALUES` ou
  `EXPLAIN`, só as 100 primeiras linhas são lidas, e o servidor recebe um
  limite de tempo um pouco abaixo do timeout do monitor, para nada dela
  seguir rodando lá depois. A causa de uma falha diz a etapa e o código
  (`consulta: erro do servidor (SQLSTATE 22P02)`), nunca o texto do
  servidor, que pode repetir dado lido; o texto vai para o log.

**Use um usuário com o mínimo de acesso.** O UpWatch guarda a senha para
verificar a cada minuto; ela não volta pela API nem aparece na causa de
uma falha, mas o melhor segredo é o que não abre nada:

```sql
-- PostgreSQL: entra e não lê tabela nenhuma
CREATE ROLE upwatch LOGIN PASSWORD '...';
-- MySQL: só o direito de conectar
CREATE USER 'upwatch'@'%' IDENTIFIED BY '...';
```

```
# Redis: só os comandos que o monitor usa
ACL SETUSER upwatch on >... ~rq:* -@all +ping +info +scard +llen
```

Ao editar o monitor, deixar a senha em branco mantém a guardada — exceto
quando o endereço muda ou o TLS fica mais fraco: aí ela precisa ser
informada de novo, para que a edição não sirva para levá-la a outro
servidor ou a quem estiver no caminho. O mesmo vale para a senha do basic
auth e para os cabeçalhos de credencial do monitor HTTP, que voltam pela
API com o valor `***`.

No PostgreSQL, um superusuário só roda a consulta padrão (`SELECT 1`);
com consulta personalizada, o monitor cai com a causa "o usuário do
monitor é superusuário". A transação somente leitura não o segura: ele
grava arquivo no servidor com `SELECT lo_export(...)` mesmo dentro dela.
O usuário acima, só com `LOGIN`, roda a consulta personalizada
normalmente.

## E-mail

O canal `email` envia por SMTP. Na API, a configuração é:

```json
{
  "host": "smtp.exemplo.com",
  "port": 587,
  "security": "starttls",
  "username": "alertas@exemplo.com",
  "password": "…",
  "from": "UpWatch <alertas@exemplo.com>",
  "to": ["plantao@exemplo.com", "infra@exemplo.com"]
}
```

`security` é `starttls` (padrão), `tls` — o TLS implícito da porta 465 —
ou `none`. Sem `port`, vale 587, 465 ou 25, conforme o modo. Com
`starttls`, um servidor que não oferece STARTTLS faz a entrega falhar em
vez de seguir em claro, e `none` só é aceito sem usuário e senha: serve
para o relé local, e não deixa a senha atravessar a rede aberta. O
certificado do servidor é sempre validado. Quando o servidor anuncia só
AUTH LOGIN, como o Microsoft 365, ele é usado no lugar de PLAIN.

O assunto é sempre `[UpWatch] <monitor>: <estado>` — `fora do ar`,
`no ar`, `degradado` ou `sem medição` —, para o cliente de e-mail agrupar
e uma regra de caixa de entrada reconhecer o alerta. Nome de monitor com
mais de 100 caracteres é cortado com reticências, para o estado não sumir
no fim de um assunto longo demais. O corpo vai em texto
e em HTML, com a causa, o alvo, o horário e, na volta, quanto tempo a
queda durou. Nome e causa são escapados no HTML: o que o alvo devolve
não vira marcação no e-mail.

Um destinatário recusado pelo servidor — a pessoa saiu da empresa, a
lista foi apagada — não cala o aviso para os outros: basta um endereço
aceito para a mensagem seguir, e os recusados aparecem no log.

Endereço com espaço, `<` ou `>` na parte local é recusado no cadastro,
mesmo entre aspas: o endereço vai cru no envelope SMTP, e um `>` ali
encerraria o endereço no meio do comando.

O botão de teste diz por extenso o que deu errado — usuário e senha
recusados, certificado não confiável, porta que esperava outro modo de
TLS.

## ntfy

O canal `ntfy` publica no [ntfy](https://ntfy.sh), que leva o alerta ao
celular sem conta: o app assina um tópico e recebe a notificação push.

```json
{
  "server": "https://ntfy.sh",
  "topic": "upwatch-alertas-x7k2",
  "token": "tk_…"
}
```

Sem `server`, vale o público. `token` é opcional, para servidor próprio
com controle de acesso, e vai como `Authorization: Bearer` — por isso,
com token, o servidor precisa ser `https://`; só o loopback (`localhost`,
`127.0.0.1`, `::1`) é aceito por `http://`, porque não sai da máquina. O
servidor é só o endereço: query, fragmento e usuário e senha na URL são
recusados no cadastro.

O servidor público é aberto: qualquer um que souber o tópico lê os
alertas. Ali o tópico é a única credencial, e o cadastro exige ao menos 12
caracteres — escolha um difícil de adivinhar, ou use servidor próprio com
token.

A queda chega com prioridade 5, a máxima; a volta e o degradado, com
prioridade 3. A mesma urgência para tudo ensinaria a ignorar tudo. O título é
`<monitor>: <estado>`, e cada estado tem seu ícone. A mensagem é cortada em
4096 bytes, o limite acima do qual o ntfy a entrega como anexo; o corte cai
na causa, e o alvo continua no fim.

A senha do SMTP e o token do ntfy, como a URL dos webhooks, nunca voltam
nas leituras da API. Também não aparecem no log nem no erro do botão de
teste: quando a entrega falha, o erro mostra só esquema e host do destino,
porque o caminho é onde ficam o token do webhook do Discord e do Slack e o
tópico do ntfy. Para trocá-los, o `PUT /api/v1/channels/{id}` leva
a configuração inteira de novo.

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
    # Só se UPWATCH_METRICS_TOKEN estiver definido.
    authorization:
      credentials: o-mesmo-token
```

**Instalação exposta na internet: defina `UPWATCH_METRICS_TOKEN`.** Sem
ele, `/metrics` responde a qualquer um com o nome de todos os monitores e
grupos, e isso descreve a topologia interna. O padrão continua aberto
porque o caso comum é o Prometheus raspando na rede local, sem nada a
configurar. O token é uma credencial à parte: vai para a configuração do
Prometheus sem dar acesso à API.

**Com contas de acesso restrito, o token é obrigatório.** A partir da
primeira conta restrita a grupos, `/metrics` sem token responde 403 —
aberto, seria o atalho dessa conta para ver os nomes que o escopo
esconde —, e o servidor avisa no início. Sem contas restritas, nada
muda.

Monitor dentro de um grupo leva também o rótulo `group`, com o caminho
(`group="RPA / prod"`). O nome só é único dentro do grupo, e sem o rótulo
o `bff` de produção e o de homologação seriam a mesma série. Monitor sem
grupo sai com os rótulos de sempre.

Em `upwatch_monitor_status`, 1 é no ar, 0 fora do ar, 2 degradado e -1
sem medição. "Sem medição" não é zero de propósito: confundir os dois
faria o alerta disparar para todo monitor recém-criado.

O endereço do alvo nunca vira rótulo. Além de descrever a topologia
interna, endereço em rótulo é cardinalidade alta — é assim que se derruba
um Prometheus.

## Push

No monitor push quem bate é o serviço: um cron, um worker, um agente
atrás de VPN. O sinal vai para `/api/v1/push/{token}`, por `GET` ou
`POST`, sem sessão — o token no caminho é a credencial. Cada token
pertence a um monitor só, e o endpoint limita a taxa por origem (300 de
rajada, 5 por segundo depois disso; acima, 429 com `Retry-After`). Atrás de
proxy reverso, todos os crons dividem o limite do proxy.

```bash
# Estou vivo (o mesmo que status=up)
curl -fsS https://upwatch.exemplo.com/api/v1/push/SEU_TOKEN

# Sei o que deu errado, e digo
curl -fsS "https://upwatch.exemplo.com/api/v1/push/SEU_TOKEN?status=down&msg=fila+sem+worker"

# Ou em JSON, com a duração medida pela própria tarefa
curl -fsS -H "Content-Type: application/json"   -d '{"status":"degraded","msg":"fila com 5 mil itens","latency_ms":1840}'   https://upwatch.exemplo.com/api/v1/push/SEU_TOKEN
```

`status` aceita `up`, `down` e `degraded`. Um `down` não abre incidente na
hora: cada sinal conta uma vez na confirmação de N falhas. As verificações
seguintes, até o próximo sinal, repetem o estado informado sem contar de
novo — com N=2 são precisos dois `down`, e um `down` seguido de silêncio
cai pela regra do silêncio. A latência também entra uma vez por sinal.
`msg` é cortada em 200 caracteres, perde caracteres de controle e de
formatação invisível, vai para o incidente e para o aviso, e nunca aparece
na página pública. `latency_ms` vai até um dia, e o corpo JSON até 16 KiB.

Por padrão o sinal é esperado a cada intervalo, mais a folga. Para rotinas
de horário fixo, informe uma agenda cron de 5 campos e o fuso:

```json
{ "token": "...", "schedule": "2 9,18 * * *", "timezone": "America/Sao_Paulo", "grace_period_seconds": 600 }
```

Com agenda, o monitor só cai quando falta o sinal de uma execução prevista
até o fim da folga — o intervalo de 15h entre 18:02 e 09:02 não vira
alarme. O fuso é obrigatório porque rotinas são agendadas no horário local
e o horário de verão desloca a execução. Sem `grace_period_seconds`, a
folga com agenda é de pelo menos 5 minutos. No horário de verão, a
execução que cai na hora que some não é cobrada naquele dia, e a que cai
na hora que se repete é esperada uma vez só, na primeira passagem — como
no cron clássico. O detalhe do monitor mostra a
próxima execução esperada.

Um monitor que nunca recebeu sinal fica "aguardando" só durante a primeira
janela depois de criado (com agenda, até a primeira execução prevista mais
a folga). Depois disso cai: uma sonda que nunca subiu é justamente a falha
que ninguém perceberia sozinho.

Mudar a configuração ou o intervalo, ou religar o monitor, recomeça a
espera: a janela conta do último sinal ou da mudança, o que vier depois.
Sem isso, trocar a agenda de semanal para diária derrubaria o monitor na
hora, cobrando execuções que ninguém estava vigiando.

## Sonda para redes privadas

O que está atrás de VPN ou firewall o UpWatch não alcança de fora. A
`upwatch-probe` roda dentro dessa rede, executa as verificações com os
mesmos checkers do servidor e manda cada resultado para um monitor push,
com estado, causa e latência. Ela só abre conexão de saída, nunca recebe
comando nem configuração do central, e manda um sinal de vida próprio:
quando ela some, o aviso diz que a sonda sumiu.

```bash
docker run -d --name upwatch-probe --restart unless-stopped --read-only --cap-drop ALL --security-opt no-new-privileges --memory 64m --cpus 0.2 -v upwatch-probe-config:/config ghcr.io/jbnado/upwatch-probe:main
docker exec upwatch-probe upwatch-probe add --name rq-workers --type redis --target redis:6379 --assert "SCARD rq:workers:default >= 1" --push-url https://upwatch.exemplo.com/api/v1/push/TOKEN
```

A configuração cabe em comandos de uma linha, pensados para o terminal
web de um jump server. Instalação completa, segurança e referência em
[cmd/upwatch-probe/README.md](cmd/upwatch-probe/README.md).

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

Os checkers de Redis, PostgreSQL e MySQL têm testes contra servidor real,
ligados por `UPWATCH_TEST_REDIS_ADDR`, `UPWATCH_TEST_POSTGRES_DSN` e
`UPWATCH_TEST_MYSQL_DSN` (ver [CONTRIBUTING.md](CONTRIBUTING.md)).

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
