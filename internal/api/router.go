package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bernardojoao/upwatch/internal/auth"
	"github.com/bernardojoao/upwatch/internal/checker"
	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// Options reúne as dependências da API.
type Options struct {
	Store    store.Store
	Auth     *auth.Service
	Checkers *checker.Registry
	Clock    clock.Clock

	// Scheduler recebe as mudanças de monitor feitas pela API, para um
	// cadastro passar a ser verificado sem esperar reinício.
	Scheduler MonitorSink

	// SecureCookies marca o cookie de sessão como Secure. Fica desligado
	// por padrão porque muitas instalações caseiras servem em HTTP puro na
	// rede local, e um cookie Secure ali simplesmente não seria enviado —
	// o login pareceria quebrado sem explicação.
	SecureCookies bool

	// SessionTTL espelha a validade usada pelo serviço de autenticação,
	// para o cookie expirar junto com a sessão no servidor.
	SessionTTL time.Duration
}

// MonitorSink recebe as alterações de monitor.
//
// Interface estreita em vez do agendador inteiro: a API não precisa saber
// agendar, só avisar que algo mudou. É o que faz um monitor recém-criado
// começar a ser verificado sem esperar reinício.
type MonitorSink interface {
	Upsert(m domain.Monitor)
	Remove(id int64)
}

// API é o conjunto de handlers HTTP.
type API struct {
	store     store.Store
	auth      *auth.Service
	checkers  *checker.Registry
	clock     clock.Clock
	scheduler MonitorSink

	secureCookies bool
	sessionTTL    time.Duration

	events *eventHub
}

// New monta o roteador da API.
func New(opts Options) http.Handler {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = auth.DefaultSessionTTL
	}

	a := &API{
		store:         opts.Store,
		auth:          opts.Auth,
		checkers:      opts.Checkers,
		clock:         opts.Clock,
		scheduler:     opts.Scheduler,
		secureCookies: opts.SecureCookies,
		sessionTTL:    opts.SessionTTL,
		events:        newEventHub(),
	}
	return a.routes()
}

func (a *API) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(recoverPanic, securityHeaders)

	// Liveness fora de qualquer autenticação: orquestradores a consultam
	// antes de existir credencial alguma.
	r.Get("/healthz", a.handleHealth)

	r.Route("/api/v1", func(r chi.Router) {
		// A especificação é pública: descobrir como autenticar é o que se
		// procura nela, e exigir credencial para lê-la criaria um ciclo.
		r.Get("/openapi.yaml", a.handleOpenAPI)

		// Assistente de primeiro acesso; ele mesmo recusa a segunda chamada.
		r.Get("/setup", a.handleSetupStatus)
		r.Post("/setup", a.handleSetup)

		r.Post("/auth/login", a.handleLogin)

		// Endpoint de push: o segredo está no caminho, então a sessão não
		// se aplica. É como um cron reporta sem carregar credencial de
		// interface.
		r.Post("/push/{token}", a.handlePush)

		// Página pública de estado. É a única superfície de leitura sem
		// credencial, e por isso a resposta é montada num pacote próprio —
		// nenhum handler daqui decide o que sai.
		r.Get("/public/{slug}", a.handlePublicStatus)
		r.Get("/public/{slug}/feed.atom", a.handlePublicFeed)

		r.Group(func(r chi.Router) {
			r.Use(a.authenticate)

			r.Post("/auth/logout", a.handleLogout)
			r.Get("/auth/me", a.handleMe)
			r.Post("/auth/password", a.handleChangePassword)
			r.Get("/auth/tokens", a.handleListTokens)
			r.Post("/auth/tokens", a.handleCreateToken)
			r.Delete("/auth/tokens/{id}", a.handleRevokeToken)

			r.Get("/monitors", a.handleListMonitors)
			r.Post("/monitors", a.handleCreateMonitor)
			r.Get("/monitors/{id}", a.handleGetMonitor)
			r.Put("/monitors/{id}", a.handleUpdateMonitor)
			r.Delete("/monitors/{id}", a.handleDeleteMonitor)

			r.Get("/monitors/{id}/heartbeats", a.handleHeartbeats)
			r.Get("/monitors/{id}/rollups", a.handleRollups)
			r.Get("/monitors/{id}/export", a.handleExport)

			r.Get("/monitors/{id}/channels", a.handleListMonitorChannels)
			r.Put("/monitors/{id}/channels/{channelID}", a.handleLinkChannel)
			r.Delete("/monitors/{id}/channels/{channelID}", a.handleUnlinkChannel)

			r.Get("/channel-types", a.handleListChannelTypes)
			r.Get("/channels", a.handleListChannels)
			r.Post("/channels", a.handleCreateChannel)
			r.Get("/channels/{id}", a.handleGetChannel)
			r.Put("/channels/{id}", a.handleUpdateChannel)
			r.Delete("/channels/{id}", a.handleDeleteChannel)
			// Entrega um aviso de exemplo. Sem isto, a única forma de saber
			// que o canal funciona seria esperar uma queda de verdade.
			r.Post("/channels/{id}/test", a.handleTestChannel)

			r.Get("/incidents", a.handleListIncidents)

			// Administração das páginas públicas.
			r.Get("/status-pages", a.handleListStatusPages)
			r.Post("/status-pages", a.handleCreateStatusPage)
			r.Get("/status-pages/{id}", a.handleGetStatusPage)
			r.Put("/status-pages/{id}", a.handleUpdateStatusPage)
			r.Delete("/status-pages/{id}", a.handleDeleteStatusPage)

			r.Post("/status-pages/{id}/groups", a.handleCreateGroup)
			r.Put("/status-pages/{id}/groups/{groupID}", a.handleUpdateGroup)
			r.Delete("/status-pages/{id}/groups/{groupID}", a.handleDeleteGroup)

			// PUT e não POST: publicar duas vezes o mesmo alvo atualiza o
			// vínculo em vez de criar um segundo.
			r.Put("/status-pages/{id}/components/{monitorID}", a.handleSetComponent)
			r.Delete("/status-pages/{id}/components/{monitorID}", a.handleRemoveComponent)

			// Relatos: o que uma pessoa escreve, separado do incidente que a
			// sonda detecta.
			r.Get("/announcements", a.handleListAnnouncements)
			r.Post("/announcements", a.handleCreateAnnouncement)
			r.Get("/announcements/{id}", a.handleGetAnnouncement)
			r.Put("/announcements/{id}", a.handleUpdateAnnouncement)
			r.Delete("/announcements/{id}", a.handleDeleteAnnouncement)
			r.Post("/announcements/{id}/updates", a.handlePublishUpdate)

			r.Get("/events", a.handleEvents)
		})
	})

	return r
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
