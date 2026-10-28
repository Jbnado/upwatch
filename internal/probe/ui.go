package probe

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

//go:embed ui
var uiFiles embed.FS

// UIBackend é o que a interface precisa do daemon.
type UIBackend interface {
	Config() Config
	State() Status
	// Reload aplica na hora a configuração que a interface acabou de
	// gravar, sem esperar a próxima conferência do arquivo.
	Reload()
}

// ValidateUIListen confere o endereço da interface.
//
// Fora do loopback ela só escuta com --ui-listen-any: a sonda roda na
// rede de outra pessoa, e abrir uma porta nela é uma decisão explícita,
// nunca um padrão. ":8090" sem host é todas as interfaces, e por isso
// também exige a flag.
func ValidateUIListen(addr string, listenAny bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("endereço da interface %q inválido; use algo como 127.0.0.1:8090", addr)
	}
	if listenAny {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("a interface só escuta em loopback (127.0.0.1); para %q, passe também --ui-listen-any", addr)
}

// minUITokenLength recusa um token curto o bastante para ser adivinhado.
const minUITokenLength = 16

// maxUIBody é o teto do corpo de um cadastro.
const maxUIBody = 16 << 10

type ui struct {
	path    string
	backend UIBackend
}

// NewUI monta a interface web.
//
// Toda chamada de dados exige o token no cabeçalho Authorization, mesmo
// em loopback: qualquer processo da VM, ou um contêiner na mesma rede,
// alcança a porta. Cabeçalho, e não cookie, para outro site aberto no
// navegador não conseguir agir em nome de quem está logado.
func NewUI(path string, b UIBackend) http.Handler {
	u := &ui{path: path, backend: b}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", u.static("ui/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", u.static("ui/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /api/checks", u.auth(u.list))
	mux.HandleFunc("POST /api/checks", u.auth(u.add))
	mux.HandleFunc("DELETE /api/checks/{name}", u.auth(u.remove))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (u *ui) static(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		data, err := uiFiles.ReadFile(name)
		if err != nil {
			http.Error(w, "arquivo ausente", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}

func (u *ui) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := u.backend.Config().UIToken
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Sem token configurado, fechado: "sem token" não pode virar
		// "sem senha".
		if len(want) < minUITokenLength || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeUIError(w, http.StatusUnauthorized, "token de acesso inválido")
			return
		}
		next(w, r)
	}
}

type uiCheck struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Interval string          `json:"interval"`
	PushURL  string          `json:"push_url"`
	Summary  string          `json:"summary,omitempty"`
	Result   *ResultStatus   `json:"result,omitempty"`
	Delivery *DeliveryStatus `json:"delivery,omitempty"`
}

type uiList struct {
	Probe     string    `json:"probe"`
	Heartbeat *uiCheck  `json:"heartbeat,omitempty"`
	Checks    []uiCheck `json:"checks"`
}

func (u *ui) list(w http.ResponseWriter, _ *http.Request) {
	cfg, st := u.backend.Config(), u.backend.State()
	out := uiList{Probe: cfg.Name, Checks: make([]uiCheck, 0, len(cfg.Checks))}

	attach := func(c *uiCheck, name string) {
		if s, ok := st.Checks[name]; ok {
			c.Result, c.Delivery = s.Result, s.Delivery
		}
	}
	if cfg.Heartbeat.PushURL != "" {
		hb := &uiCheck{Name: HeartbeatName, Interval: cfg.Heartbeat.Interval.String(), PushURL: MaskURL(cfg.Heartbeat.PushURL)}
		attach(hb, HeartbeatName)
		out.Heartbeat = hb
	}
	for _, ch := range cfg.Checks {
		c := uiCheck{
			Name: ch.Name, Type: ch.Type, Target: ch.Target, Interval: ch.Interval.String(),
			PushURL: MaskURL(ch.PushURL), Summary: Describe(ch),
		}
		attach(&c, ch.Name)
		out.Checks = append(out.Checks, c)
	}
	sort.Slice(out.Checks, func(i, j int) bool { return out.Checks[i].Name < out.Checks[j].Name })
	writeUIJSON(w, http.StatusOK, out)
}

type uiCheckRequest struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Target       string   `json:"target"`
	PushURL      string   `json:"push_url"`
	Interval     string   `json:"interval"`
	Timeout      string   `json:"timeout"`
	Assert       []string `json:"assert"`
	ExpectStatus []int    `json:"expect_status"`
	BodyContains string   `json:"body_contains"`
	Username     string   `json:"username"`
	Password     string   `json:"password"`
	Replace      bool     `json:"replace"`
}

func parseOptionalDuration(field, v string) (time.Duration, error) {
	if strings.TrimSpace(v) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s %q inválido; use algo como 30s ou 1m", field, v)
	}
	return d, nil
}

func (u *ui) add(w http.ResponseWriter, r *http.Request) {
	var req uiCheckRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUIBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeUIError(w, http.StatusBadRequest, "corpo JSON inválido")
		return
	}
	interval, err1 := parseOptionalDuration("intervalo", req.Interval)
	timeout, err2 := parseOptionalDuration("timeout", req.Timeout)
	if err := errors.Join(err1, err2); err != nil {
		writeUIError(w, http.StatusBadRequest, err.Error())
		return
	}

	var asserts []string
	for _, a := range req.Assert {
		if strings.TrimSpace(a) != "" {
			asserts = append(asserts, a)
		}
	}
	ch, err := CheckInput{
		Name: req.Name, Type: req.Type, Target: req.Target, PushURL: req.PushURL,
		Interval: interval, Timeout: timeout, Asserts: asserts,
		ExpectStatus: req.ExpectStatus, BodyContains: req.BodyContains,
		Username: req.Username, Password: req.Password,
	}.Check()
	if err == nil {
		_, err = Update(u.path, func(c *Config) error { return c.Add(ch, req.Replace) })
	}
	if err != nil {
		writeUIError(w, http.StatusBadRequest, err.Error())
		return
	}
	u.backend.Reload()
	w.WriteHeader(http.StatusCreated)
}

func (u *ui) remove(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := Update(u.path, func(c *Config) error { return c.Remove(name) }); err != nil {
		writeUIError(w, http.StatusNotFound, err.Error())
		return
	}
	u.backend.Reload()
	w.WriteHeader(http.StatusNoContent)
}

func writeUIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeUIError(w http.ResponseWriter, status int, msg string) {
	writeUIJSON(w, status, map[string]string{"error": msg})
}
