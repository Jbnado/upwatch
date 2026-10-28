package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Jbnado/upwatch/internal/domain"
)

// ntfyServidorPublico é o servidor usado quando nenhum é informado.
const ntfyServidorPublico = "https://ntfy.sh"

// ntfyTopicoPublicoMinimo é o menor tópico aceito no servidor público.
//
// Lá o tópico é a única credencial: quem souber o nome lê os alertas, que
// dizem o que caiu e por quê. "alertas" ou "upwatch" são adivinhados por
// qualquer um que tente; doze caracteres já pedem um nome inventado.
const ntfyTopicoPublicoMinimo = 12

// ntfyTopico é o formato que o ntfy aceita para tópico.
//
// Barra ou espaço no tópico publicariam noutro caminho do servidor, e o
// celular inscrito no tópico certo não receberia nada.
var ntfyTopico = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ntfyConfig é a configuração do canal ntfy.
type ntfyConfig struct {
	// Server vazio vale o servidor público.
	Server string `json:"server,omitempty"`
	Topic  string `json:"topic"`
	// Token é segredo, para servidor próprio com controle de acesso. Como
	// o resto da configuração, nunca sai pela API.
	Token string `json:"token,omitempty"`
}

type ntfy struct {
	destino string
	token   string
}

// NewNtfy cria o canal do ntfy.
//
// É o caminho mais curto até o alerta no celular sem conta em lugar
// nenhum: o app assina o tópico e recebe a notificação push.
func NewNtfy(raw json.RawMessage) (Notifier, error) {
	var cfg ntfyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("notifier: configuração de ntfy inválida: %w", err)
	}

	servidor := strings.TrimRight(strings.TrimSpace(cfg.Server), "/")
	if servidor == "" {
		servidor = ntfyServidorPublico
	}
	u, err := url.Parse(servidor)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("notifier: servidor do ntfy inválido %q; use http:// ou https://", cfg.Server)
	}
	// O tópico é concatenado ao servidor. Query ou fragmento o engoliriam
	// e a publicação iria para a raiz; usuário e senha na URL seriam
	// credencial fora do campo de token, onde a regra do https não alcança.
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(servidor, "#") {
		return nil, fmt.Errorf("notifier: servidor do ntfy inválido %q; informe só o endereço, sem ? nem #", cfg.Server)
	}
	if u.User != nil {
		return nil, fmt.Errorf("notifier: servidor do ntfy não aceita usuário e senha na URL; use o campo token")
	}

	topico := strings.TrimSpace(cfg.Topic)
	if !ntfyTopico.MatchString(topico) {
		return nil, fmt.Errorf("notifier: tópico do ntfy inválido %q; use letras, números, - e _ (até 64)", cfg.Topic)
	}
	if strings.EqualFold(u.Hostname(), "ntfy.sh") && len(topico) < ntfyTopicoPublicoMinimo {
		return nil, fmt.Errorf("notifier: no servidor público, quem souber o tópico lê os alertas; "+
			"use um tópico com ao menos %d caracteres, difícil de adivinhar", ntfyTopicoPublicoMinimo)
	}

	token := strings.TrimSpace(cfg.Token)
	if strings.ContainsFunc(token, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, fmt.Errorf("notifier: o token do ntfy tem caractere de controle")
	}
	// O token vai como Bearer no cabeçalho: por http ele atravessa a rede
	// em claro, e quem estiver no caminho passa a publicar no tópico. O
	// loopback fica de fora porque não sai da máquina.
	if token != "" && u.Scheme != "https" && !loopback(u.Hostname()) {
		return nil, fmt.Errorf("notifier: com token, o servidor do ntfy precisa de https; " +
			"por http o token atravessaria a rede em claro")
	}

	return &ntfy{destino: servidor + "/" + topico, token: token}, nil
}

// loopback diz se o host é a própria máquina.
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (n *ntfy) Send(ctx context.Context, note Notification) error {
	prioridade, tags := ntfyUrgencia(note.Event.To)

	texto := Render(note)
	if note.Monitor.Target != "" {
		texto += "\n" + note.Monitor.Target
	}

	headers := map[string]string{
		// RFC 2047 porque nome de monitor tem acento, e byte fora do ASCII
		// em cabeçalho HTTP não tem garantia de chegar inteiro. O ntfy
		// decodifica.
		"Title":    mime.QEncoding.Encode("utf-8", note.Monitor.Name+": "+statusLabel(note.Event.To)),
		"Priority": prioridade,
		"Tags":     tags,
	}
	if n.token != "" {
		headers["Authorization"] = "Bearer " + n.token
	}

	err := postRaw(ctx, n.destino, headers, "text/plain; charset=utf-8", []byte(texto))

	var recusa *statusError
	if errors.As(err, &recusa) && (recusa.Code == http.StatusUnauthorized || recusa.Code == http.StatusForbidden) {
		return Permanent(fmt.Errorf("notifier: o ntfy recusou publicar no tópico (%d); confira o token "+
			"e se ele tem permissão de escrita", recusa.Code))
	}
	return err
}

// ntfyUrgencia decide prioridade e ícone pelo estado novo.
//
// A queda precisa furar o modo silencioso do celular, que é para isso que
// se instala o ntfy. A volta e o degradado chegam com prioridade normal:
// a mesma urgência para tudo ensinaria a ignorar tudo.
func ntfyUrgencia(s domain.Status) (prioridade, tags string) {
	switch s {
	case domain.StatusDown:
		return "5", "rotating_light"
	case domain.StatusUp:
		return "3", "white_check_mark"
	case domain.StatusDegraded:
		return "3", "warning"
	default:
		return "3", "grey_question"
	}
}
