.PHONY: test test-race cover lint fmt build clean

# Detector de corrida por padrão: scheduler, worker pool e batch writer são
# concorrentes, e uma corrida ali só apareceria em produção.
test:
	go test ./... -race -count=1

# Iteração local: pula a simulação de retenção de 30 dias, que domina o
# tempo da suíte. O CI roda a suíte completa.
test-fast:
	go test ./... -short -count=1

cover:
	go test ./... -race -count=1 -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -n 1

cover-html: cover
	go tool cover -html=coverage.out

lint:
	go vet ./...
	gofmt -l .

fmt:
	gofmt -w .

# CGO desligado: driver SQLite pure Go, binário estático, imagem mínima.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o upwatch ./cmd/upwatch

clean:
	rm -f upwatch upwatch.exe coverage.out
