package notifier_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/notifier"
)

// teamsCapture sobe um destino https e devolve o canal apontado para ele
// e a leitura do último corpo recebido.
func teamsCapture(t *testing.T, cfg string) (notifier.Notifier, func() string) {
	t.Helper()

	var (
		mu     sync.Mutex
		ultimo string
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corpo, _ := io.ReadAll(r.Body)
		mu.Lock()
		ultimo = string(corpo)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	raw := strings.ReplaceAll(cfg, "$URL", srv.URL+"/workflows/abc/triggers/manual/paths/invoke?sig=segredo")
	n, err := notifier.NewTeamsWithClient(json.RawMessage(raw), srv.Client())
	if err != nil {
		t.Fatalf("NewTeams returned unexpected error: %v", err)
	}
	return n, func() string {
		mu.Lock()
		defer mu.Unlock()
		return ultimo
	}
}

// comURLPublica configura o endereço externo só durante o teste.
func comURLPublica(t *testing.T, base string) {
	t.Helper()
	notifier.SetPublicURL(base)
	t.Cleanup(func() { notifier.SetPublicURL("") })
}

// mensagemTeams embrulha o cartão como o Workflows o recebe.
func mensagemTeams(card string) string {
	return `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":` + card + `}]}`
}

func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("expected value is not JSON: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("body mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestTeamsSendsAnAdaptiveCardForAnOutage(t *testing.T) {
	comURLPublica(t, "https://upwatch.exemplo.com/")
	n, corpo := teamsCapture(t, `{"url":"$URL"}`)

	nota := outage()
	nota.Group = "RPA / prod"
	if err := send(t, n, nota); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	assertJSON(t, corpo(), mensagemTeams(`{
		"$schema":"http://adaptivecards.io/schemas/adaptive-card.json",
		"type":"AdaptiveCard","version":"1.4",
		"msteams":{"width":"Full"},
		"body":[
			{"type":"Container","style":"attention","bleed":true,"items":[
				{"type":"TextBlock","text":"FORA DO AR","weight":"Bolder","color":"Attention","size":"Small","wrap":true},
				{"type":"TextBlock","text":"RPA / prod · api de produção","weight":"Bolder","size":"Large","wrap":true,"spacing":"None"}
			]},
			{"type":"TextBlock","text":"api de produção (RPA / prod) está fora do ar: status 502 fora do esperado","wrap":true},
			{"type":"FactSet","facts":[
				{"title":"Causa","value":"status 502 fora do esperado"},
				{"title":"Alvo","value":"https://api.exemplo.com/health"}
			]},
			{"type":"TextBlock","text":"{{DATE(2026-07-28T12:00:00Z, SHORT)}} {{TIME(2026-07-28T12:00:00Z)}}","isSubtle":true,"size":"Small","wrap":true}
		],
		"actions":[{"type":"Action.OpenUrl","title":"Abrir no UpWatch","url":"https://upwatch.exemplo.com/monitors/7"}]
	}`))
}

func TestTeamsSendsAnAdaptiveCardForDegradation(t *testing.T) {
	n, corpo := teamsCapture(t, `{"url":"$URL"}`)

	nota := outage()
	nota.Event = incident.Event{
		Kind: domain.ChangeDegraded, From: domain.StatusUp, To: domain.StatusDegraded, At: epoch,
	}
	nota.Message = "respondeu em 2300 ms"
	if err := send(t, n, nota); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	// Sem endereço externo, nenhum botão: link relativo não abre nada no
	// Teams.
	assertJSON(t, corpo(), mensagemTeams(`{
		"$schema":"http://adaptivecards.io/schemas/adaptive-card.json",
		"type":"AdaptiveCard","version":"1.4",
		"msteams":{"width":"Full"},
		"body":[
			{"type":"Container","style":"warning","bleed":true,"items":[
				{"type":"TextBlock","text":"DEGRADADO","weight":"Bolder","color":"Warning","size":"Small","wrap":true},
				{"type":"TextBlock","text":"api de produção","weight":"Bolder","size":"Large","wrap":true,"spacing":"None"}
			]},
			{"type":"TextBlock","text":"api de produção está degradado: respondeu em 2300 ms","wrap":true},
			{"type":"FactSet","facts":[
				{"title":"Causa","value":"respondeu em 2300 ms"},
				{"title":"Alvo","value":"https://api.exemplo.com/health"}
			]},
			{"type":"TextBlock","text":"{{DATE(2026-07-28T12:00:00Z, SHORT)}} {{TIME(2026-07-28T12:00:00Z)}}","isSubtle":true,"size":"Small","wrap":true}
		]
	}`))
}

func TestTeamsSendsAnAdaptiveCardForARecovery(t *testing.T) {
	comURLPublica(t, "https://upwatch.exemplo.com")
	n, corpo := teamsCapture(t, `{"url":"$URL"}`)

	if err := send(t, n, recovery(65*time.Minute)); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	assertJSON(t, corpo(), mensagemTeams(`{
		"$schema":"http://adaptivecards.io/schemas/adaptive-card.json",
		"type":"AdaptiveCard","version":"1.4",
		"msteams":{"width":"Full"},
		"body":[
			{"type":"Container","style":"good","bleed":true,"items":[
				{"type":"TextBlock","text":"VOLTOU","weight":"Bolder","color":"Good","size":"Small","wrap":true},
				{"type":"TextBlock","text":"api de produção","weight":"Bolder","size":"Large","wrap":true,"spacing":"None"}
			]},
			{"type":"TextBlock","text":"api de produção voltou depois de 1 h 5 min","wrap":true},
			{"type":"FactSet","facts":[
				{"title":"Duração","value":"1 h 5 min"},
				{"title":"Alvo","value":"https://api.exemplo.com/health"}
			]},
			{"type":"TextBlock","text":"{{DATE(2026-07-28T13:05:00Z, SHORT)}} {{TIME(2026-07-28T13:05:00Z)}}","isSubtle":true,"size":"Small","wrap":true}
		],
		"actions":[{"type":"Action.OpenUrl","title":"Abrir no UpWatch","url":"https://upwatch.exemplo.com/monitors/7"}]
	}`))
}

// O lembrete leva a mesma urgência da queda, com rótulo próprio: quem lê
// precisa ver que é a mesma queda que segue aberta, e não uma nova.
func TestTeamsSendsAnAdaptiveCardForAReminder(t *testing.T) {
	comURLPublica(t, "https://upwatch.exemplo.com")
	n, corpo := teamsCapture(t, `{"url":"$URL"}`)

	if err := send(t, n, reminder(2*time.Hour+30*time.Minute)); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	assertJSON(t, corpo(), mensagemTeams(`{
		"$schema":"http://adaptivecards.io/schemas/adaptive-card.json",
		"type":"AdaptiveCard","version":"1.4",
		"msteams":{"width":"Full"},
		"body":[
			{"type":"Container","style":"attention","bleed":true,"items":[
				{"type":"TextBlock","text":"AINDA FORA DO AR","weight":"Bolder","color":"Attention","size":"Small","wrap":true},
				{"type":"TextBlock","text":"api de produção","weight":"Bolder","size":"Large","wrap":true,"spacing":"None"}
			]},
			{"type":"TextBlock","text":"api de produção continua fora do ar há 2 h 30 min: status 502 fora do esperado","wrap":true},
			{"type":"FactSet","facts":[
				{"title":"Causa","value":"status 502 fora do esperado"},
				{"title":"Duração","value":"2 h 30 min"},
				{"title":"Alvo","value":"https://api.exemplo.com/health"}
			]},
			{"type":"TextBlock","text":"{{DATE(2026-07-28T14:30:00Z, SHORT)}} {{TIME(2026-07-28T14:30:00Z)}}","isSubtle":true,"size":"Small","wrap":true}
		],
		"actions":[{"type":"Action.OpenUrl","title":"Abrir no UpWatch","url":"https://upwatch.exemplo.com/monitors/7"}]
	}`))
}

// O botão Testar manda um monitor sem id: o link vai para o painel, e o
// cartão sai igual ao de uma volta de verdade.
func TestTeamsTestNotificationRenders(t *testing.T) {
	comURLPublica(t, "https://upwatch.exemplo.com")
	n, corpo := teamsCapture(t, `{"url":"$URL"}`)

	teste := notifier.Notification{
		Monitor: domain.Monitor{Name: "teste do UpWatch", Target: "plantão de infra"},
		Event: domain.StateChange{
			Kind: domain.ChangeUp, From: domain.StatusDown, To: domain.StatusUp,
			At: epoch, Duration: 5 * time.Minute,
		},
	}
	if err := send(t, n, teste); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	assertJSON(t, corpo(), mensagemTeams(`{
		"$schema":"http://adaptivecards.io/schemas/adaptive-card.json",
		"type":"AdaptiveCard","version":"1.4",
		"msteams":{"width":"Full"},
		"body":[
			{"type":"Container","style":"good","bleed":true,"items":[
				{"type":"TextBlock","text":"VOLTOU","weight":"Bolder","color":"Good","size":"Small","wrap":true},
				{"type":"TextBlock","text":"teste do UpWatch","weight":"Bolder","size":"Large","wrap":true,"spacing":"None"}
			]},
			{"type":"TextBlock","text":"teste do UpWatch voltou depois de 5 min","wrap":true},
			{"type":"FactSet","facts":[
				{"title":"Duração","value":"5 min"},
				{"title":"Alvo","value":"plantão de infra"}
			]},
			{"type":"TextBlock","text":"{{DATE(2026-07-28T12:00:00Z, SHORT)}} {{TIME(2026-07-28T12:00:00Z)}}","isSubtle":true,"size":"Small","wrap":true}
		],
		"actions":[{"type":"Action.OpenUrl","title":"Abrir no UpWatch","url":"https://upwatch.exemplo.com/"}]
	}`))
}

func TestTeamsAppliesTheCustomTemplateToTheSentence(t *testing.T) {
	n, corpo := teamsCapture(t, `{"url":"$URL","template":"{{.Monitor.Name}} mudou para {{.Status}}"}`)

	if err := send(t, n, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}
	if !strings.Contains(corpo(), `"text":"api de produção mudou para down"`) {
		t.Errorf("the card omits the templated sentence: %s", corpo())
	}
}

func TestTeamsRejectsObviousMistakesInTheURL(t *testing.T) {
	casos := map[string]string{
		"vazia":    ``,
		"http":     `http://prod-00.westus.logic.azure.com/workflows/abc`,
		"slack":    `https://hooks.slack.com/services/T0/B0/tok`,
		"discord":  `https://discord.com/api/webhooks/1/tok`,
		"sem host": `https:///workflows/abc`,
	}
	for nome, url := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := notifier.NewTeams(json.RawMessage(`{"url":"` + url + `"}`)); err == nil {
				t.Errorf("NewTeams(%q) returned nil error, want an error", url)
			}
		})
	}
}

// O host não é conferido contra uma lista: a Microsoft já trocou o domínio
// do Workflows uma vez, e uma lista fechada recusaria o próximo.
func TestTeamsAcceptsWorkflowsURLsFromAnyMicrosoftHost(t *testing.T) {
	for _, url := range []string{
		"https://prod-12.westus.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?api-version=2016-06-01&sig=x",
		"https://default1234.ab.environment.api.powerplatform.com:443/powerautomate/automations/direct/workflows/abc/triggers/manual/paths/invoke?sig=x",
		"https://contoso.webhook.office.com/webhookb2/abc/IncomingWebhook/def/ghi",
	} {
		if _, err := notifier.NewTeams(json.RawMessage(`{"url":"` + url + `"}`)); err != nil {
			t.Errorf("NewTeams(%q) returned unexpected error: %v", url, err)
		}
	}
}

func TestTeamsRefusesBodyTemplate(t *testing.T) {
	_, err := notifier.NewTeams(json.RawMessage(`{"url":"https://x.logic.azure.com/w","body_template":{"a":"$text"}}`))
	if err == nil {
		t.Fatal("NewTeams with body_template returned nil error, want an error")
	}
}
