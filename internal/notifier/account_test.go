package notifier_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/notifier/smtptest"
)

// tokenExpiring monta o aviso de um token prestes a vencer.
func tokenExpiring() notifier.Notification {
	return notifier.Notification{
		Account: &notifier.AccountNotice{
			Event:       domain.EventAccountTokenExpiring,
			Username:    "joao",
			TokenName:   "deploy",
			TokenPrefix: "upw_abc123",
			ExpiresAt:   time.Date(2026, 12, 18, 0, 0, 0, 0, time.UTC),
			DaysLeft:    7,
		},
	}
}

// O aviso de conta não é sobre um monitor: a frase diz o token, o dono e
// quando vence; Kind leva o tipo assinável, e Status fica vazio porque não
// há estado de monitor.
func TestAccountRenderKindStatus(t *testing.T) {
	n := tokenExpiring()
	frase := notifier.Render(n)
	for _, esperado := range []string{`"deploy"`, "joao", "vence em 7 dias", "18/12/2026"} {
		if !strings.Contains(frase, esperado) {
			t.Errorf("Render omitiu %q:\n%s", esperado, frase)
		}
	}
	if !n.IsAccount() {
		t.Error("IsAccount = false num aviso de conta")
	}
	if got := n.Kind(); got != "account.token_expiring" {
		t.Errorf("Kind = %q, want account.token_expiring", got)
	}
	if got := n.Status(); got != "" {
		t.Errorf("Status = %q, want vazio no aviso de conta", got)
	}

	// Vencimento iminente e revogação se leem de formas distintas.
	iminente := tokenExpiring()
	iminente.Account.DaysLeft = 1
	if got := notifier.Render(iminente); !strings.Contains(got, "vence amanhã") {
		t.Errorf("Render com 1 dia = %q, want 'vence amanhã'", got)
	}
	revogado := tokenExpiring()
	revogado.Account.Event = domain.EventAccountTokenRevoked
	if got := notifier.Render(revogado); !strings.Contains(got, "revogado") {
		t.Errorf("Render de revogação = %q, want falar em revogado", got)
	}
}

// O webhook do aviso de conta leva os campos do token, e não os de monitor
// zerados: event distingue o tipo, e quem integra recebe o dado.
func TestWebhookAccountBody(t *testing.T) {
	cap, url := newCapture(t)
	w, err := notifier.NewWebhook(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := send(t, w, tokenExpiring()); err != nil {
		t.Fatal(err)
	}
	var corpo map[string]any
	if err := json.Unmarshal([]byte(cap.body(0)), &corpo); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	if corpo["event"] != "account.token_expiring" {
		t.Errorf("event = %v, want account.token_expiring", corpo["event"])
	}
	if corpo["token_name"] != "deploy" {
		t.Errorf("token_name = %v, want deploy", corpo["token_name"])
	}
	if corpo["username"] != "joao" {
		t.Errorf("username = %v, want joao", corpo["username"])
	}
	if corpo["days_left"] != float64(7) {
		t.Errorf("days_left = %v, want 7", corpo["days_left"])
	}
	// Nada de campos de monitor: um monitor_id 0 confundiria quem integra.
	if _, tem := corpo["monitor_id"]; tem {
		t.Errorf("o corpo de conta trouxe monitor_id, não deveria: %v", corpo)
	}
}

// O ntfy do aviso de conta tem título próprio e prioridade normal: não é
// uma queda que precise furar o silêncio do celular.
func TestNtfyAccountTitleAndPriority(t *testing.T) {
	cap, url := newCapture(t)
	n := newNtfy(t, `{"server":"`+url+`","topic":"upwatch-alertas"}`)
	if err := send(t, n, tokenExpiring()); err != nil {
		t.Fatal(err)
	}
	r := cap.request(0)
	if got := titulo(t, r); got != "Token de API vencendo" {
		t.Errorf("Title = %q, want 'Token de API vencendo'", got)
	}
	if p := r.Header.Get("Priority"); p != "3" {
		t.Errorf("Priority = %q, want 3 (normal)", p)
	}
	if corpo := cap.body(0); !strings.Contains(corpo, "deploy") {
		t.Errorf("o corpo omite o token: %q", corpo)
	}
}

// O e-mail do aviso de conta tem assunto próprio e resume o token, o dono e
// o vencimento — sem nome de monitor.
func TestEmailAccountSubjectAndBody(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{Mode: smtptest.StartTLS})
	e := newEmail(t, emailConfig(srv, "starttls", ""), srv.RootCAs)
	if err := send(t, e, tokenExpiring()); err != nil {
		t.Fatal(err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("recebidas %d mensagens, want 1", len(msgs))
	}
	msg, texto, html := partes(t, msgs[0].Data)
	if got := assunto(t, msg); got != "[UpWatch] Token de API vencendo" {
		t.Errorf("Subject = %q, want o assunto do aviso de conta", got)
	}
	for _, esperado := range []string{"deploy", "joao", "18/12/2026"} {
		if !strings.Contains(texto, esperado) {
			t.Errorf("o texto omite %q:\n%s", esperado, texto)
		}
		if !strings.Contains(html, esperado) {
			t.Errorf("o HTML omite %q:\n%s", esperado, html)
		}
	}
}

// O cartão do Teams do aviso de conta tem cabeçalho próprio, fato de
// vencimento e nenhum botão de página de monitor.
func TestTeamsAccountCard(t *testing.T) {
	comURLPublica(t, "https://upwatch.exemplo.com/")
	n, corpo := teamsCapture(t, `{"url":"$URL"}`)
	if err := send(t, n, tokenExpiring()); err != nil {
		t.Fatal(err)
	}
	body := corpo()
	for _, esperado := range []string{"TOKEN VENCENDO", "deploy", "Vence em"} {
		if !strings.Contains(body, esperado) {
			t.Errorf("o cartão omite %q:\n%s", esperado, body)
		}
	}
	if strings.Contains(body, "Abrir no UpWatch") {
		t.Errorf("o cartão de conta trouxe o botão de monitor:\n%s", body)
	}
}

// Discord escapa o nome do token vindo de fora, como faz com os campos de
// monitor, sem corromper a Notification compartilhada entre canais.
func TestDiscordEscapesAccountFields(t *testing.T) {
	cap, url := newCapture(t)
	d, err := notifier.NewDiscord(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	nota := tokenExpiring()
	nota.Account.TokenName = "deploy*[x]"
	if err := send(t, d, nota); err != nil {
		t.Fatal(err)
	}
	var corpo map[string]any
	if err := json.Unmarshal([]byte(cap.body(0)), &corpo); err != nil {
		t.Fatalf("payload inválido: %v", err)
	}
	conteudo, _ := corpo["content"].(string)
	if strings.Contains(conteudo, "deploy*[x]") {
		t.Errorf("o nome do token não foi escapado:\n%s", conteudo)
	}
	if !strings.Contains(conteudo, `\*`) {
		t.Errorf("o markdown do nome do token não foi escapado:\n%s", conteudo)
	}
	// O ponteiro de Account é compartilhado entre canais: o escape de um não
	// pode mutar a Notification que os outros ainda vão renderizar.
	if nota.Account.TokenName != "deploy*[x]" {
		t.Errorf("o escape mutou a Notification compartilhada: %q", nota.Account.TokenName)
	}
}
