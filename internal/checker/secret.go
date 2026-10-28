package checker

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/Jbnado/upwatch/internal/domain"
)

// secretMask substitui a credencial onde quer que ela apareça.
const secretMask = "***"

// scrubSecret remove a credencial de uma mensagem de causa.
//
// A causa vai para o painel, para o aviso no canal e para o histórico
// exportado, lidos por quem não tem acesso à senha. Drivers e servidores
// às vezes repetem o que receberam — a cadeia de conexão inteira num erro
// de parse, a senha ecoada por um proxy —, então a limpeza acontece na
// saída, sem depender de cada fonte de erro se comportar.
//
// As formas codificadas também saem: numa URL de conexão a senha aparece
// escapada, e "p@ss" vira "p%40ss".
func scrubSecret(msg, secret string) string {
	if secret == "" {
		return msg
	}
	for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
		msg = strings.ReplaceAll(msg, form, secretMask)
	}
	return msg
}

// passwordField é o campo de Config que guarda a credencial nos tipos
// que a têm, e passwordSetField o marcador que a substitui na saída.
const (
	passwordField    = "password"
	passwordSetField = "password_set"
)

// secretTypes são os tipos cuja configuração carrega credencial. A lista
// mora aqui, e não em cada handler, para a omissão no GET e a preservação
// na edição nunca divergirem.
var secretTypes = map[domain.MonitorType]bool{
	domain.MonitorRedis:    true,
	domain.MonitorPostgres: true,
	domain.MonitorMySQL:    true,
}

// RedactConfig devolve a configuração sem a credencial, para sair pela
// API.
//
// A senha é trocada por password_set: a interface precisa saber que
// existe uma para dizer "deixe em branco para manter", sem saber qual é.
// Uma configuração que não se consegue interpretar sai vazia, nunca
// crua: na dúvida, não se publica o que pode conter segredo.
func RedactConfig(t domain.MonitorType, raw json.RawMessage) json.RawMessage {
	if !secretTypes[t] || len(raw) == 0 {
		return raw
	}
	fields, err := configFields(raw)
	if err != nil {
		return nil
	}
	if hasPassword(fields) {
		fields[passwordSetField] = json.RawMessage("true")
	}
	delete(fields, passwordField)
	out, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return out
}

// CarrySecrets prepara a configuração de uma edição, mantendo a senha
// guardada quando a requisição não a traz.
//
// Como a senha não volta no GET, editar qualquer outro campo chega sem
// ela; tratar a ausência como "sem senha" apagaria a credencial a cada
// salvamento. Senha presente, mesmo vazia, é a nova.
//
// A senha só acompanha o monitor no mesmo endereço. Do contrário, quem
// pode editar monitores mas não ler a senha a obteria trocando o alvo por
// um servidor seu: o UpWatch a entregaria lá na verificação seguinte.
func CarrySecrets(next, prev domain.Monitor) (json.RawMessage, error) {
	if !secretTypes[next.Type] || len(next.Config) == 0 {
		return next.Config, nil
	}
	fields, err := configFields(next.Config)
	if err != nil {
		// A validação do checker devolve o erro com a mensagem certa.
		return next.Config, nil
	}
	delete(fields, passwordSetField)

	if _, sent := fields[passwordField]; !sent && prev.Type == next.Type {
		prevFields, err := configFields(prev.Config)
		if err == nil && hasPassword(prevFields) {
			if prev.Target != next.Target {
				return nil, errors.New("checker: informe a senha de novo ao trocar o endereço; a guardada não acompanha o monitor para outro servidor")
			}
			fields[passwordField] = prevFields[passwordField]
		}
	}
	return json.Marshal(fields)
}

func configFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return fields, nil
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	return fields, nil
}

func hasPassword(fields map[string]json.RawMessage) bool {
	var p string
	raw, ok := fields[passwordField]
	return ok && json.Unmarshal(raw, &p) == nil && p != ""
}
