package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// defaultWindow é a janela usada quando o cliente não informa período.
const defaultWindow = 24 * time.Hour

// timeRange lê o período da URL, aplicando um padrão sensato.
//
// Sem padrão, um cliente que esquecesse os parâmetros pediria o histórico
// inteiro — exatamente a consulta que derruba a interface quando a tabela
// cresce.
func (a *API) timeRange(r *http.Request) (store.TimeRange, error) {
	now := a.clock.Now().UTC()
	rng := store.TimeRange{From: now.Add(-defaultWindow), To: now}

	if raw := r.URL.Query().Get("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return rng, &domain.ValidationError{Field: "from", Msg: "data inválida; use RFC3339"}
		}
		rng.From = t
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return rng, &domain.ValidationError{Field: "to", Msg: "data inválida; use RFC3339"}
		}
		rng.To = t
	}

	rng = rng.Normalize()
	if !rng.Valid() {
		return rng, &domain.ValidationError{Field: "from", Msg: "o início precisa ser anterior ao fim"}
	}
	return rng, nil
}

type heartbeatResponse struct {
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"`
	LatencyMS int64     `json:"latency_ms"`
	Message   string    `json:"message,omitempty"`
	ProbeID   string    `json:"probe_id"`
}

// handleHeartbeats devolve as batidas cruas do período.
func (a *API) handleHeartbeats(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rng, err := a.timeRange(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	beats, err := a.store.QueryHeartbeats(r.Context(), store.HeartbeatQuery{
		MonitorID: id,
		ProbeID:   r.URL.Query().Get("probe_id"),
		Range:     rng,
		Limit:     int(queryInt64(r, "limit", 0)),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	items := make([]heartbeatResponse, 0, len(beats))
	for _, hb := range beats {
		items = append(items, heartbeatResponse{
			Timestamp: hb.Timestamp, Status: hb.Status.String(),
			LatencyMS: hb.LatencyMS, Message: hb.Message, ProbeID: hb.ProbeID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type rollupResponse struct {
	BucketStart   time.Time `json:"bucket_start"`
	Resolution    string    `json:"resolution"`
	Total         int       `json:"total"`
	Up            int       `json:"up"`
	Down          int       `json:"down"`
	Degraded      int       `json:"degraded"`
	Unknown       int       `json:"unknown"`
	UptimePercent float64   `json:"uptime_percent"`
	LatencyAvgMS  float64   `json:"latency_avg_ms"`
	LatencyMinMS  float64   `json:"latency_min_ms"`
	LatencyMaxMS  float64   `json:"latency_max_ms"`
	LatencyP50MS  float64   `json:"latency_p50_ms"`
	LatencyP95MS  float64   `json:"latency_p95_ms"`
	LatencyP99MS  float64   `json:"latency_p99_ms"`
}

func toRollupResponse(r domain.Rollup) rollupResponse {
	return rollupResponse{
		BucketStart: r.BucketStart, Resolution: r.Resolution.String(),
		Total: r.Total, Up: r.Up, Down: r.Down, Degraded: r.Degraded, Unknown: r.Unknown,
		UptimePercent: r.UptimePercent(),
		LatencyAvgMS:  r.LatencyAvgMS, LatencyMinMS: r.LatencyMinMS, LatencyMaxMS: r.LatencyMaxMS,
		LatencyP50MS: r.LatencyP50MS, LatencyP95MS: r.LatencyP95MS, LatencyP99MS: r.LatencyP99MS,
	}
}

// handleRollups devolve as estatísticas agregadas do período.
//
// É o que sustenta o gráfico de meses: o dado cru vive poucos dias, mas o
// agregado responde por janelas longas sem custo proporcional.
func (a *API) handleRollups(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rng, err := a.timeRange(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	resolution := domain.ResolutionHourly
	if raw := r.URL.Query().Get("resolution"); raw != "" {
		parsed, err := domain.ParseResolution(raw)
		if err != nil {
			writeFieldError(w, "resolution", "resolução inválida; use hourly ou daily")
			return
		}
		resolution = parsed
	}

	rollups, err := a.store.QueryRollups(r.Context(), store.RollupQuery{
		MonitorID: id, ProbeID: r.URL.Query().Get("probe_id"),
		Resolution: resolution, Range: rng,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	items := make([]rollupResponse, 0, len(rollups))
	for _, rl := range rollups {
		items = append(items, toRollupResponse(rl))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "resolution": resolution.String(),
	})
}

// handleExport entrega o histórico bruto em CSV ou JSON.
//
// Exportar é o que impede o UpWatch de virar um depósito do qual não se
// tira nada: quem instala precisa poder levar seus dados embora.
func (a *API) handleExport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rng, err := a.timeRange(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		writeFieldError(w, "format", "formato inválido; use csv ou json")
		return
	}

	if format == "json" {
		a.exportJSON(w, r, id, rng)
		return
	}
	a.exportCSV(w, r, id, rng)
}

func (a *API) exportCSV(w http.ResponseWriter, r *http.Request, id int64, rng store.TimeRange) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="upwatch-monitor-%d.csv"`, id))

	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write([]string{"timestamp", "status", "latency_ms", "probe_id", "message"}); err != nil {
		return
	}

	// Percorre em fluxo, sem materializar tudo: um período longo pode ter
	// milhões de linhas, e carregá-las na memória para exportar derrubaria
	// o processo que precisa continuar monitorando.
	err := a.store.StreamHeartbeats(r.Context(), id, rng, func(hb domain.Heartbeat) error {
		return cw.Write([]string{
			hb.Timestamp.Format(time.RFC3339),
			hb.Status.String(),
			strconv.FormatInt(hb.LatencyMS, 10),
			csvText(hb.ProbeID),
			csvText(hb.Message),
		})
	})
	if err != nil {
		// O corpo já começou a ser enviado, então não há como trocar o
		// status; o registro fica no log.
		writeStreamFailure(w, err)
	}
}

// csvText neutraliza o texto livre que vai para uma célula do CSV.
//
// O Excel e o LibreOffice leem como fórmula a célula que começa com =, +,
// -, @, tabulação ou retorno de carro, e a causa de um monitor push é
// escrita por quem bate no UpWatch: "=HYPERLINK(...)" viraria um link
// clicável, ou coisa pior, na planilha de quem exportou. O apóstrofo na
// frente é o que a orientação da OWASP recomenda: a planilha mostra o
// texto como texto. Só nas colunas de texto — a latência negativa, se
// existisse, continuaria número.
func csvText(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func (a *API) exportJSON(w http.ResponseWriter, r *http.Request, id int64, rng store.TimeRange) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="upwatch-monitor-%d.json"`, id))

	if _, err := w.Write([]byte(`{"items":[`)); err != nil {
		return
	}

	enc := json.NewEncoder(w)
	first := true
	err := a.store.StreamHeartbeats(r.Context(), id, rng, func(hb domain.Heartbeat) error {
		if !first {
			if _, err := w.Write([]byte(",")); err != nil {
				return err
			}
		}
		first = false
		return enc.Encode(heartbeatResponse{
			Timestamp: hb.Timestamp, Status: hb.Status.String(),
			LatencyMS: hb.LatencyMS, Message: hb.Message, ProbeID: hb.ProbeID,
		})
	})
	if err != nil {
		writeStreamFailure(w, err)
		return
	}
	_, _ = w.Write([]byte(`]}`))
}

// handlePush recebe o sinal de um monitor push.
//
// O segredo viaja no caminho porque quem reporta é um cron ou um worker,
// que não tem sessão de interface nem deve precisar de uma. Pelo mesmo
// motivo aceita GET: muito agendador e muito healthcheck só sabem fazer
// `curl URL`.
func (a *API) handlePush(w http.ResponseWriter, r *http.Request) {
	secret := chi.URLParam(r, "token")
	if secret == "" {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "token ausente")
		return
	}

	// O token é conferido antes dos parâmetros: validar primeiro deixaria
	// quem não tem token sondar o contrato.
	monitorID, ok := a.findPushMonitor(r, secret)
	if !ok {
		// Mesma resposta para token errado e monitor inexistente: separar
		// os casos permitiria descobrir quais tokens existem.
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "token inválido")
		return
	}

	sig, err := readPushSignal(w, r)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, codeInvalidRequest,
			"corpo acima de %d bytes; o sinal cabe em poucas centenas", maxPushBodyBytes)
		return
	case err != nil:
		writeStoreError(w, err)
		return
	}
	sig.At = a.clock.Now()

	if err := a.store.RecordPush(r.Context(), monitorID, sig); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// maxPushBodyBytes é o teto do corpo do sinal.
//
// O sinal cabe em poucas centenas de bytes. O teto geral da API, de 1 MiB,
// deixaria qualquer um sem login fazer o servidor ler e decodificar um
// megabyte por requisição; 16 KiB sobra para o sinal e para os campos
// extras que um agente mande junto.
const maxPushBodyBytes = 16 << 10

// pushParams são os campos opcionais do sinal, como chegam no corpo.
type pushParams struct {
	Status    string      `json:"status"`
	Msg       string      `json:"msg"`
	LatencyMS json.Number `json:"latency_ms"`
}

// readPushSignal lê status, causa e latência da query string ou do corpo
// JSON. Quando o mesmo campo vem nos dois, vale a query string.
//
// O corpo só é lido com Content-Type JSON: um cron que já fazia POST com
// qualquer coisa no corpo não pode passar a ser recusado. Campos
// desconhecidos são tolerados pelo mesmo motivo — um agente que mande a
// própria versão junto não pode ter o sinal perdido por isso.
func readPushSignal(w http.ResponseWriter, r *http.Request) (domain.PushSignal, error) {
	var params pushParams
	if isJSON(r.Header.Get("Content-Type")) {
		r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		err := dec.Decode(&params)
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			return domain.PushSignal{}, err
		case err != nil && !errors.Is(err, io.EOF):
			return domain.PushSignal{}, &domain.ValidationError{Field: "body", Msg: "corpo JSON inválido"}
		}
	}

	q := r.URL.Query()
	if q.Has("status") {
		params.Status = q.Get("status")
	}
	if q.Has("msg") {
		params.Msg = q.Get("msg")
	}
	if q.Has("latency_ms") {
		params.LatencyMS = json.Number(q.Get("latency_ms"))
	}

	status, err := domain.ParsePushStatus(params.Status)
	if err != nil {
		return domain.PushSignal{}, err
	}
	latency, err := domain.ParsePushLatency(params.LatencyMS.String())
	if err != nil {
		return domain.PushSignal{}, err
	}
	return domain.PushSignal{
		Status: status, Message: domain.CleanPushMessage(params.Msg), LatencyMS: latency,
	}, nil
}

// isJSON informa se o tipo de conteúdo declarado é JSON.
func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

// findPushMonitor localiza o monitor dono do segredo apresentado.
//
// A busca é pela coluna indexada do token: o endpoint não tem login, e
// percorrer os monitores a cada requisição deixaria qualquer um provocar
// uma varredura da tabela à vontade.
func (a *API) findPushMonitor(r *http.Request, secret string) (int64, bool) {
	m, err := a.store.Monitors().FindByPushToken(r.Context(), secret)
	if err != nil || m.Type != domain.MonitorPush {
		return 0, false
	}
	return m.ID, true
}
