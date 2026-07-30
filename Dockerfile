# Imagem do UpWatch: um binário, nada mais.
#
# A interface é embarcada no próprio executável, então não há nginx ao
# lado nem volume de arquivos estáticos que possa ficar defasado em
# relação ao servidor que os acompanha. O resultado é uma imagem que se
# sobe e se atualiza como uma unidade só.

# ---------- interface ----------
FROM node:22-alpine AS web

WORKDIR /app/web

# pnpm instalado explicitamente, e não por corepack.
#
# O corepack saiu da distribuição do Node a partir da linha 25, então
# "corepack enable" quebra o build inteiro na primeira atualização da
# imagem base — foi assim que a subida para node:26-alpine falhou.
#
# A versão fica fixa e igual à do CI. Com corepack ela era a que viesse
# embutida na imagem base, o que fazia a versão do pnpm mudar junto com o
# Node sem ninguém decidir isso.
RUN npm install -g pnpm@10

# Só o manifesto primeiro: a instalação de dependências é a camada cara,
# e mexer no código-fonte não deveria invalidá-la.
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY web/ ./
# O build escreve em internal/web/dist, fora de web/, porque é de lá que
# o go:embed lê.
RUN pnpm vite build


# ---------- servidor ----------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Mesma ideia: os módulos mudam bem menos que o código.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web /app/internal/web/dist ./internal/web/dist

ARG VERSION=dev

# CGO desligado. O driver SQLite é pure Go, então o binário é estático e
# a imagem final não precisa de libc — o que também elimina uma fonte
# inteira de CVEs de sistema operacional.
#
# trimpath tira o caminho de compilação do binário, e -s -w tiram a
# tabela de símbolos: menos bytes, e nenhum caminho da máquina de build
# vazando num stack trace.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /upwatch ./cmd/upwatch


# ---------- imagem final ----------
FROM alpine:3.24

# Certificados raiz: sem eles todo monitor HTTPS falharia na validação do
# certificado do alvo, e o erro pareceria queda do serviço monitorado.
#
# tzdata: as páginas públicas exibem horário no fuso configurado, e sem a
# base de fusos qualquer configuração cairia em UTC em silêncio.
RUN apk add --no-cache ca-certificates tzdata

# Usuário sem privilégio, criado explicitamente com UID fixo.
#
# UID fixo importa para volume montado: com um UID atribuído pelo sistema
# a cada build, o banco gravado por uma versão poderia ficar ilegível
# para a seguinte.
RUN addgroup -g 10001 -S upwatch \
 && adduser -u 10001 -S -G upwatch -h /data upwatch

COPY --from=build /upwatch /usr/local/bin/upwatch

# O diretório do banco pertence ao usuário sem privilégio, senão a
# primeira gravação falharia com permissão negada.
RUN mkdir -p /data && chown upwatch:upwatch /data

USER upwatch:upwatch
WORKDIR /data
VOLUME ["/data"]

ENV UPWATCH_DB_DSN=/data/upwatch.db \
    UPWATCH_LISTEN=0.0.0.0:8080

EXPOSE 8080

# O próprio /healthz responde antes de existir qualquer conta, então
# serve de sonda desde o primeiro segundo. wget vem no BusyBox do Alpine,
# o que evita instalar curl só para isto.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["upwatch"]
