.PHONY: test test-fast test-web cover lint fmt web build dev clean

# ---------- testes ----------

# Detector de corrida por padrão: scheduler, worker pool e batch writer são
# concorrentes, e uma corrida ali só apareceria em produção.
test: test-web
	go test ./... -race -count=1

# Iteração local: pula a simulação de retenção de 30 dias, que domina o
# tempo da suíte. O CI roda a suíte completa.
test-fast:
	go test ./... -short -count=1

test-web:
	cd web && pnpm vitest run

cover:
	go test ./... -race -count=1 -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -n 1

cover-html: cover
	go tool cover -html=coverage.out

lint:
	go vet ./...
	gofmt -l .
	cd web && pnpm exec tsc -b

fmt:
	gofmt -w .

# ---------- build ----------

# A interface é embarcada no binário pelo go:embed, então compilá-la é
# pré-requisito de compilar o servidor. Sem esta dependência é fácil
# recompilar o Go e continuar servindo a interface da build anterior —
# um engano que custa tempo porque nada indica que aconteceu.
web:
	cd web && pnpm install --frozen-lockfile && pnpm vite build

# CGO desligado: driver SQLite pure Go, binário estático, imagem mínima.
build: web
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o upwatch ./cmd/upwatch

# Desenvolvimento: o servidor Go serve a API e o Vite serve a interface
# com recarga instantânea, encaminhando /api para o Go. Assim mexer no
# desenho não custa uma recompilação do binário.
dev:
	@echo "Suba o servidor em um terminal:  go run ./cmd/upwatch"
	@echo "E a interface em outro:          cd web && pnpm dev"

clean:
	rm -f upwatch upwatch.exe coverage.out
	rm -rf internal/web/dist/assets internal/web/dist/index.html
