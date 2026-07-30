// Package api expõe a interface HTTP do UpWatch.
//
// A API é de primeira classe, não um acessório da interface gráfica: é o
// pedido mais votado do Uptime Kuma, cuja comunicação depende de Socket.IO
// e não oferece caminho programático. Aqui a interface consome exatamente
// os mesmos endpoints que qualquer script.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// maxRequestBytes limita o corpo aceito. Sem teto, um corpo enorme
// consumiria memória do processo que precisa continuar monitorando.
const maxRequestBytes = 1 << 20 // 1 MiB

// errorCode identifica a classe do erro para o cliente reagir sem
// interpretar mensagem em português.
type errorCode string

const (
	codeInvalidRequest errorCode = "invalid_request"
	codeUnauthorized   errorCode = "unauthorized"
	codeForbidden      errorCode = "forbidden"
	codeNotFound       errorCode = "not_found"
	codeConflict       errorCode = "conflict"
	codeTooManyReqs    errorCode = "too_many_requests"
	codeInternal       errorCode = "internal_error"
)

// errorBody é o formato único de erro da API.
type errorBody struct {
	Error struct {
		Code    errorCode `json:"code"`
		Message string    `json:"message"`
		// Field aponta o campo reprovado, quando houver, para a interface
		// destacá-lo em vez de exibir só um texto genérico.
		Field string `json:"field,omitempty"`
	} `json:"error"`
}

// writeJSON serializa a resposta.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// O cabeçalho já foi enviado; só resta registrar.
		slog.Error("api: falha ao escrever resposta", "erro", err)
	}
}

// writeError devolve um erro no formato padrão.
func writeError(w http.ResponseWriter, status int, code errorCode, format string, args ...any) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = fmt.Sprintf(format, args...)
	writeJSON(w, status, body)
}

// writeFieldError devolve um erro de validação apontando o campo.
func writeFieldError(w http.ResponseWriter, field, message string) {
	var body errorBody
	body.Error.Code = codeInvalidRequest
	body.Error.Message = message
	body.Error.Field = field
	writeJSON(w, http.StatusBadRequest, body)
}

// writeStoreError traduz erros conhecidos das camadas de baixo no status
// HTTP correspondente.
//
// Centralizar a tradução evita que cada handler invente o próprio mapa e
// devolva 500 onde caberia 404.
func writeStoreError(w http.ResponseWriter, err error) {
	var validation *domain.ValidationError

	switch {
	case errors.As(err, &validation):
		writeFieldError(w, validation.Field, validation.Msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "registro não encontrado")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, codeConflict, "já existe um registro com esses dados")
	case errors.Is(err, auth.ErrSetupComplete):
		writeError(w, http.StatusConflict, codeConflict, "a instalação já possui uma conta")
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrUnauthenticated):
		// Sem detalhar: distinguir os casos ajudaria quem está tentando
		// descobrir contas válidas.
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "credenciais inválidas")
	case errors.Is(err, auth.ErrLastAdmin):
		// Conflito, não erro de validação: o pedido está bem formado, o
		// estado atual é que não permite atendê-lo.
		writeError(w, http.StatusConflict, codeConflict,
			"é preciso manter ao menos um administrador")
	case errors.Is(err, auth.ErrTooManyAttempts):
		writeError(w, http.StatusTooManyRequests, codeTooManyReqs,
			"tentativas demais; aguarde antes de tentar novamente")
	default:
		// A causa vai para o log, não para o cliente: mensagens internas
		// costumam revelar estrutura de banco e caminhos de arquivo.
		slog.Error("api: erro não tratado", "erro", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "erro interno")
	}
}

// decodeJSON lê o corpo da requisição com limite de tamanho e rejeita
// campos desconhecidos.
//
// Recusar campo desconhecido transforma erro de digitação em resposta
// explícita, em vez de o valor ser silenciosamente ignorado e o operador
// achar que configurou algo que não configurou.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "corpo inválido: %v", err)
		return false
	}
	// Um segundo objeto no corpo indica requisição malformada.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, codeInvalidRequest,
			"o corpo deve conter um único objeto JSON")
		return false
	}
	return true
}
