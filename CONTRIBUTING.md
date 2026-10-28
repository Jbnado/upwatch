# Contribuindo

Antes de escrever código, abra uma issue descrevendo o problema. Uma
mudança que resolve algo que ninguém tinha vira discussão de desenho
depois de o trabalho estar feito, e aí já é tarde para mudar de rumo sem
desperdício.

## Rodando

```bash
make test    # suíte completa, com detector de corrida
make lint    # vet, gofmt, golangci-lint e tsc
make build   # interface + binário estático
```

Para PostgreSQL, suba um descartável e aponte a variável:

```bash
docker run -d --rm --name pg -e POSTGRES_PASSWORD=upwatch \
  -e POSTGRES_USER=upwatch -e POSTGRES_DB=upwatch -p 55432:5432 postgres:17-alpine

UPWATCH_TEST_POSTGRES_DSN='postgres://upwatch:upwatch@127.0.0.1:55432/upwatch?sslmode=disable' \
  go test ./internal/store/...
```

Os checkers de protocolo têm o mesmo esquema, com Redis e MySQL também:

```bash
docker run -d --rm --name redis -p 16379:6379 redis:7-alpine
docker run -d --rm --name mysql -e MYSQL_ROOT_PASSWORD=root   -e MYSQL_USER=upwatch -e MYSQL_PASSWORD=upwatch -e MYSQL_DATABASE=upwatch   -p 53306:3306 mysql:8.4

UPWATCH_TEST_REDIS_ADDR=127.0.0.1:16379 UPWATCH_TEST_POSTGRES_DSN='postgres://upwatch:upwatch@127.0.0.1:55432/upwatch?sslmode=disable' UPWATCH_TEST_MYSQL_DSN='upwatch:upwatch@tcp(127.0.0.1:53306)/upwatch'   go test ./internal/checker/...
```

Sem a variável o teste se anuncia como pulado, e não some em silêncio.

### Segredos

A CI varre o histórico do PR com o [gitleaks](https://github.com/gitleaks/gitleaks) e falha se achar algo que pareça credencial, inclusive URL com senha embutida (`postgres://usuario:senha@host`). Para conferir antes do push:

```bash
gitleaks git . --config .gitleaks.toml
```

Teste que precisa de um valor com cara de senha usa um valor fictício e o registra no allowlist do `.gitleaks.toml`, com o motivo. A exceção é sempre o valor exato, nunca um caminho inteiro.

## Como este projeto escreve testes

**Teste antes do código, sem exceção.** A suíte não é rede de segurança
aqui, é o artefato de desenho: quase toda decisão estrutural deste
repositório apareceu porque um teste ficou difícil de escrever.

**O teste explica por que a regra existe**, não o que a linha faz. Um
comentário dizendo "verifica que devolve 404" repete o código. Um
dizendo "distinguir página desligada de inexistente confirmaria a
existência a quem só chutou o endereço" preserva a razão — e é isso que
impede alguém de "simplificar" a regra dali a um ano.

**Teste que passa com a funcionalidade desligada não vale nada.** Se
você não viu o teste falhar antes de escrever o código, não sabe se ele
testa alguma coisa. Aconteceu mais de uma vez neste repositório: um teste
de limitador de tentativas passava com o limitador quebrado, e um teste
de vazamento na página pública só provou ter dentes quando o vazamento
foi introduzido de propósito para vê-lo falhar.

**Um backend novo de armazenamento passa em `storetest.RunConformance`
inteiro, ou não entra.** É o que impede "plugável" de virar fachada.

## O que a revisão vai olhar

- O teste falha antes da correção?
- O comentário explica a razão, ou repete o código?
- Alguma decisão foi tomada em dois lugares? Dois lugares divergem.
- A mudança toca a superfície pública? Aí o teste de vazamento é
  obrigatório.

## Estilo

Go formatado por `gofmt`; TypeScript e comentários em português, como o
resto do repositório. Mensagens de commit descrevem o problema resolvido
e a razão da escolha — não a lista de arquivos alterados, que o diff já
mostra.

## Licença

Contribuições entram sob AGPL-3.0, a mesma licença do projeto.
