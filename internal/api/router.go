package api

import (
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/metrics"
	"github.com/Jbnado/upwatch/internal/store"
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

	// Version aparece em upwatch_build_info, para o painel de métricas
	// mostrar qual versão está no ar sem precisar consultar o processo.
	Version string

	// Counters acumula o que o processo viu, para a exposição de métricas
	// não precisar varrer a tabela de batidas a cada raspagem.
	Counters *metrics.Counters

	// MetricsToken, quando preenchido, passa a ser exigido em /metrics
	// como "Authorization: Bearer <token>".
	//
	// Vazio mantém a exposição aberta, que é o caso da rede local onde o
	// Prometheus raspa sem configurar nada. Numa instalação exposta na
	// internet, a exposição aberta publica o nome de todos os monitores, e
	// com eles a topologia interna.
	MetricsToken string

	// SetupToken é exigido no POST /setup enquanto a instalação não tem
	// conta. Quem sobe o processo o define (UPWATCH_SETUP_TOKEN) ou recebe
	// um gerado no log.
	//
	// Vazio fecha o assistente em vez de abri-lo: a API montada sem token
	// é um passo esquecido, e o esquecimento não pode entregar a instância
	// a quem chegar primeiro.
	SetupToken string

	// PublicURL é o endereço externo da instalação, como
	// "https://status.exemplo.com".
	//
	// Vazio faz o feed usar endereços relativos, que é o padrão seguro: a
	// alternativa seria reconstruí-los a partir do cabeçalho Host, e esse
	// cabeçalho é controlado por quem chama. Configure atrás de proxy,
	// quando quiser endereços absolutos no feed.
	PublicURL string

	// Resolver resolve o alvo de monitor cadastrado por conta restrita,
	// para recusar o que aponta para a rede interna do servidor. Nulo usa
	// o resolvedor do sistema.
	Resolver Resolver

	// AllowScopedPrivateTargets deixa conta restrita monitorar endereço
	// interno (loopback, rede privada, metadado da nuvem). Desligado por
	// padrão: o checker conecta a partir do servidor, e liberar faria de
	// cada administrador de um grupo uma sonda na rede de dentro dele.
	AllowScopedPrivateTargets bool
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
	publicURL     string
	metricsToken  string
	setupToken    string
	version       string

	resolver                  Resolver
	allowScopedPrivateTargets bool

	counters *metrics.Counters

	events *eventHub

	// pushLimit segura o /push, que não tem login.
	pushLimit *rateLimiter
	// setupLimit segura o POST /setup, onde se tenta o token de
	// configuração.
	setupLimit *rateLimiter

	// policies é a política de escopo de cada rota, declarada ao registrá-la.
	policies map[string]scopePolicy
}

// New monta o roteador da API.
func New(opts Options) http.Handler {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = auth.DefaultSessionTTL
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}

	a := &API{
		store:         opts.Store,
		auth:          opts.Auth,
		checkers:      opts.Checkers,
		clock:         opts.Clock,
		scheduler:     opts.Scheduler,
		secureCookies: opts.SecureCookies,
		sessionTTL:    opts.SessionTTL,
		publicURL:     opts.PublicURL,
		metricsToken:  opts.MetricsToken,
		setupToken:    opts.SetupToken,
		version:       opts.Version,
		counters:      opts.Counters,
		resolver:      opts.Resolver,

		allowScopedPrivateTargets: opts.AllowScopedPrivateTargets,
		events:                    newEventHub(),
		pushLimit:                 newRateLimiter(opts.Clock, pushBurst, pushPerSecond),
		setupLimit:                newRateLimiter(opts.Clock, setupBurst, setupPerSecond),
		policies:                  map[string]scopePolicy{},
	}
	return a.routes()
}

func (a *API) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(recoverPanic, securityHeaders)
	// Toda rota entra pela tabela, que exige a política de escopo junto.
	t := routeTable{r: r, policies: a.policies}

	// Liveness fora de qualquer autenticação: orquestradores a consultam
	// antes de existir credencial alguma.
	t.handle(http.MethodGet, "/healthz", policyPublic, a.handleHealth)

	// Exposição para Prometheus. Fora da sessão porque o coletor raspa sem
	// ela; o token próprio é opcional (ver handleMetrics).
	t.handle(http.MethodGet, "/metrics", policyPublic, a.handleMetrics)

	r.Route("/api/v1", func(r chi.Router) {
		t := routeTable{r: r, prefix: "/api/v1", policies: a.policies}
		// A especificação é pública: descobrir como autenticar é o que se
		// procura nela, e exigir credencial para lê-la criaria um ciclo.
		t.handle(http.MethodGet, "/openapi.yaml", policyPublic, a.handleOpenAPI)

		// Assistente de primeiro acesso; ele mesmo recusa a segunda chamada.
		// O POST exige o token de configuração e é limitado por origem,
		// para o token não ser adivinhado por tentativa.
		t.handle(http.MethodGet, "/setup", policyPublic, a.handleSetupStatus)
		t.on(r.With(a.setupLimit.limit)).handle(http.MethodPost, "/setup", policyPublic, a.handleSetup)

		t.handle(http.MethodPost, "/auth/login", policyPublic, a.handleLogin)

		// Endpoint de push: o segredo está no caminho, então a sessão não
		// se aplica. É como um cron reporta sem carregar credencial de
		// interface. GET também, porque muito agendador só sabe fazer
		// `curl URL`. Limitado por origem, justamente por não ter login.
		push := t.on(r.With(a.pushLimit.limit))
		push.handle(http.MethodGet, "/push/{token}", policyPublic, a.handlePush)
		push.handle(http.MethodPost, "/push/{token}", policyPublic, a.handlePush)

		// Página pública de estado. É a única superfície de leitura sem
		// credencial, e por isso a resposta é montada num pacote próprio —
		// nenhum handler daqui decide o que sai.
		//
		// Sem slug responde a página padrão: numa instalação com uma
		// página só, o slug repete o que o caminho já diz, e "/status" é
		// o endereço que se cola em contrato e em rodapé de e-mail.
		t.handle(http.MethodGet, "/public", policyPublic, a.handlePublicDefault)
		t.handle(http.MethodGet, "/public/feed.atom", policyPublic, a.handlePublicDefaultFeed)
		t.handle(http.MethodGet, "/public/{slug}", policyPublic, a.handlePublicStatus)
		t.handle(http.MethodGet, "/public/{slug}/feed.atom", policyPublic, a.handlePublicFeed)

		// Leitura: administrador e observador entram igual.
		//
		// O observador enxerga tudo o que o administrador enxerga dentro
		// do escopo dele, inclusive endereço de alvo e causa de falha. Ele
		// é de dentro; quem não é vê a página pública, que tem outras
		// regras.
		//
		// O escopo é aplicado pela guarda do grupo de rotas, como o papel,
		// segundo a política que cada rota declara ao ser registrada. Rota
		// sem política não sobe.
		r.Group(func(r chi.Router) {
			r.Use(a.authenticate, a.withViewer, a.scopeGuard)
			t := t.on(r)

			t.handle(http.MethodPost, "/auth/logout", policyAccount, a.handleLogout)
			t.handle(http.MethodGet, "/auth/me", policyAccount, a.handleMe)
			// Trocar a própria senha não é administrar. Exigir
			// administrador aqui obrigaria a pedir ajuda justamente
			// quando alguém suspeita da própria credencial.
			t.handle(http.MethodPost, "/auth/password", policyAccount, a.handleChangePassword)

			// Uma requisição resume a janela para todos os monitores. É o
			// que a lista consome: sem isso o painel abria uma conexão por
			// monitor, degradando justamente na instalação grande.
			t.handle(http.MethodGet, "/summaries", policyFiltered, a.handleSummaries)

			t.handle(http.MethodGet, "/monitors", policyFiltered, a.handleListMonitors)
			t.handle(http.MethodGet, "/monitors/{id}", policyMonitor, a.handleGetMonitor)
			t.handle(http.MethodGet, "/monitors/{id}/heartbeats", policyMonitor, a.handleHeartbeats)
			t.handle(http.MethodGet, "/monitors/{id}/rollups", policyMonitor, a.handleRollups)
			t.handle(http.MethodGet, "/monitors/{id}/export", policyMonitor, a.handleExport)
			t.handle(http.MethodGet, "/monitors/{id}/channels", policyMonitor, a.handleListMonitorChannels)
			// Linha do tempo de falhas e mudanças de estado, cada uma com
			// a causa.
			t.handle(http.MethodGet, "/monitors/{id}/events", policyMonitor, a.handleMonitorEvents)

			// Grupos e o que a tela inicial lê: o resumo por grupo e o que
			// está errado agora, já recortados pelo escopo.
			t.handle(http.MethodGet, "/groups", policyFiltered, a.handleListGroups)
			t.handle(http.MethodGet, "/groups/summary", policyFiltered, a.handleGroupSummary)
			t.handle(http.MethodGet, "/groups/{id}", policyGroup, a.handleGetMonitorGroup)
			t.handle(http.MethodGet, "/groups/{id}/channels", policyGroup, a.handleListGroupChannels)
			t.handle(http.MethodGet, "/problems", policyFiltered, a.handleProblems)

			t.handle(http.MethodGet, "/channel-types", policyAccount, a.handleListChannelTypes)
			t.handle(http.MethodGet, "/channels", policyFiltered, a.handleListChannels)
			t.handle(http.MethodGet, "/channels/{id}", policyChannel, a.handleGetChannel)

			t.handle(http.MethodGet, "/incidents", policyFiltered, a.handleListIncidents)

			t.handle(http.MethodGet, "/status-pages", policyFiltered, a.handleListStatusPages)
			t.handle(http.MethodGet, "/status-pages/{id}", policyFiltered, a.handleGetStatusPage)

			t.handle(http.MethodGet, "/announcements", policyFiltered, a.handleListAnnouncements)
			t.handle(http.MethodGet, "/announcements/{id}", policyFiltered, a.handleGetAnnouncement)

			t.handle(http.MethodGet, "/events", policyStream, a.handleEvents)
		})

		// Escrita: só administrador.
		//
		// A barreira é um grupo de rotas, não uma checagem repetida em
		// cada handler. Esquecer a checagem num handler novo é o modo
		// mais provável de a permissão vazar; aqui o esquecimento é
		// impossível, porque a rota nasce dentro do grupo.
		r.Group(func(r chi.Router) {
			r.Use(a.authenticate, a.requireAdmin, a.withViewer, a.scopeGuard)
			t := t.on(r)

			// Token de API faz tudo o que a interface faz, então criá-lo
			// é operação de administrador — senão o observador emitiria
			// para si mesmo uma credencial sem as suas restrições.
			t.handle(http.MethodGet, "/auth/tokens", policyAccount, a.handleListTokens)
			t.handle(http.MethodPost, "/auth/tokens", policyAccount, a.handleCreateToken)
			t.handle(http.MethodDelete, "/auth/tokens/{id}", policyAccount, a.handleRevokeToken)

			t.handle(http.MethodGet, "/users", policyFiltered, a.handleListUsers)
			t.handle(http.MethodPost, "/users", policyPlacement, a.handleCreateUser)
			t.handle(http.MethodPut, "/users/{id}", policyUser, a.handleUpdateUserRole)
			t.handle(http.MethodDelete, "/users/{id}", policyUser, a.handleDeleteUser)

			t.handle(http.MethodPost, "/monitors", policyPlacement, a.handleCreateMonitor)
			t.handle(http.MethodPut, "/monitors/{id}", policyMonitor, a.handleUpdateMonitor)
			t.handle(http.MethodDelete, "/monitors/{id}", policyMonitor, a.handleDeleteMonitor)
			// "Definir pai" para vários monitores de uma vez: o {id} é o
			// pai, e cada filho é conferido contra o escopo no handler.
			t.handle(http.MethodPost, "/monitors/{id}/children", policyMonitor, a.handleSetChildren)

			t.handle(http.MethodPut, "/monitors/{id}/channels/{channelID}", policyFullScope|policyMonitor, a.handleLinkChannel)
			t.handle(http.MethodDelete, "/monitors/{id}/channels/{channelID}", policyFullScope|policyMonitor, a.handleUnlinkChannel)

			t.handle(http.MethodPost, "/groups", policyPlacement, a.handleCreateMonitorGroup)
			t.handle(http.MethodPut, "/groups/{id}", policyGroup, a.handleUpdateMonitorGroup)
			t.handle(http.MethodDelete, "/groups/{id}", policyGroup, a.handleDeleteMonitorGroup)
			// Canal no grupo vale para todos os monitores dele, inclusive
			// os criados depois. O canal é recurso de todos os grupos, então
			// vincular e desvincular exigem escopo total, como alterá-lo:
			// pendurar o pager da diretoria no próprio grupo é mexer no
			// aviso dos outros.
			t.handle(http.MethodPut, "/groups/{id}/channels/{channelID}", policyFullScope|policyGroup, a.handleLinkGroupChannel)
			t.handle(http.MethodDelete, "/groups/{id}/channels/{channelID}", policyFullScope|policyGroup, a.handleUnlinkGroupChannel)

			t.handle(http.MethodPost, "/channels", policyFullScope, a.handleCreateChannel)
			t.handle(http.MethodPut, "/channels/{id}", policyFullScope, a.handleUpdateChannel)
			t.handle(http.MethodDelete, "/channels/{id}", policyFullScope, a.handleDeleteChannel)
			// Entrega um aviso de exemplo. Sem isto, a única forma de saber
			// que o canal funciona seria esperar uma queda de verdade.
			t.handle(http.MethodPost, "/channels/{id}/test", policyFullScope, a.handleTestChannel)

			// Administração das páginas públicas.
			t.handle(http.MethodPost, "/status-pages", policyFullScope, a.handleCreateStatusPage)
			t.handle(http.MethodPut, "/status-pages/{id}", policyFullScope, a.handleUpdateStatusPage)
			t.handle(http.MethodDelete, "/status-pages/{id}", policyFullScope, a.handleDeleteStatusPage)
			t.handle(http.MethodPut, "/status-pages/{id}/default", policyFullScope, a.handleSetDefaultStatusPage)

			t.handle(http.MethodPost, "/status-pages/{id}/groups", policyFullScope, a.handleCreateGroup)
			t.handle(http.MethodPut, "/status-pages/{id}/groups/{groupID}", policyFullScope, a.handleUpdateGroup)
			t.handle(http.MethodDelete, "/status-pages/{id}/groups/{groupID}", policyFullScope, a.handleDeleteGroup)

			// PUT e não POST: publicar duas vezes o mesmo alvo atualiza o
			// vínculo em vez de criar um segundo.
			t.handle(http.MethodPut, "/status-pages/{id}/components/{monitorID}", policyFullScope|policyMonitorParam, a.handleSetComponent)
			t.handle(http.MethodDelete, "/status-pages/{id}/components/{monitorID}", policyFullScope|policyMonitorParam, a.handleRemoveComponent)

			// Relatos: o que uma pessoa escreve, separado do incidente que a
			// sonda detecta.
			t.handle(http.MethodPost, "/announcements", policyFullScope, a.handleCreateAnnouncement)
			t.handle(http.MethodPut, "/announcements/{id}", policyFullScope, a.handleUpdateAnnouncement)
			t.handle(http.MethodDelete, "/announcements/{id}", policyFullScope, a.handleDeleteAnnouncement)
			t.handle(http.MethodPost, "/announcements/{id}/updates", policyFullScope, a.handlePublishUpdate)
		})
	})

	// Rota sem política não sobe. Pânico na montagem, e não erro em
	// produção: é defeito de quem escreveu a rota, e o primeiro teste que
	// monta a API o encontra.
	if err := checkPolicies(r, a.policies); err != nil {
		panic(err)
	}
	return r
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
