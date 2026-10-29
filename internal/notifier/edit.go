package notifier

import (
	"encoding/json"
	"fmt"
	"strings"
)

// mascara ocupa o valor de um cabeçalho guardado na configuração que vai
// ao formulário, e de volta na edição quer dizer "mantenha o guardado".
const mascara = "***"

// edicao é a ida e a volta da configuração de um tipo de canal pelo
// formulário de edição.
type edicao struct {
	// ocultar devolve a configuração sem credencial, com a marca de onde
	// havia uma.
	ocultar func(raw json.RawMessage) (any, error)
	// manter completa a configuração editada com a credencial guardada
	// que não voltou. prev vazio é canal sem nada para preservar.
	manter func(next, prev json.RawMessage) (json.RawMessage, error)
}

// RedactConfig devolve a configuração do canal como o formulário de
// edição a recebe: o que não é credencial volta como está, e no lugar de
// cada credencial fica a marca de que existe uma (url_set, password_set,
// token_set) ou, nos cabeçalhos, o nome com o valor mascarado. É o que
// permite dizer "em branco, mantém" sem entregar o segredo a ninguém.
//
// Configuração que não se consegue ler sai vazia, nunca crua: na dúvida,
// não se publica o que pode conter segredo.
func RedactConfig(kind string, raw json.RawMessage) json.RawMessage {
	e, ok := edicaoDe(kind)
	if !ok || len(raw) == 0 {
		return nil
	}
	view, err := e.ocultar(raw)
	if err != nil {
		return nil
	}
	out, err := json.Marshal(view)
	if err != nil {
		return nil
	}
	return out
}

// CarrySecrets prepara a configuração de uma edição, recuperando do
// canal guardado a credencial que a requisição não trouxe.
//
// Como a credencial não volta no GET, salvar qualquer outro campo chega
// sem ela; tratar a ausência como "sem senha" a apagaria a cada edição.
// Campo presente, mesmo vazio, é o novo valor. Cabeçalho com a máscara
// retoma o valor guardado.
//
// A credencial só acompanha o canal para o mesmo destino. Do contrário,
// quem pode editar canais, mas não ler os segredos, os obteria apontando
// o canal para um servidor seu e apertando Testar.
//
// Troca de tipo não preserva nada: a configuração é outra.
func CarrySecrets(kind string, next json.RawMessage, prevKind string, prev json.RawMessage) (json.RawMessage, error) {
	e, ok := edicaoDe(kind)
	if !ok || len(next) == 0 {
		return next, nil
	}
	if normalize(prevKind) != normalize(kind) {
		prev = nil
	}
	return e.manter(next, prev)
}

func edicaoDe(kind string) (edicao, bool) {
	tipo := normalize(kind)
	for _, c := range canais {
		if c.tipo == tipo {
			return c.edicao, true
		}
	}
	return edicao{}, false
}

// ---------- webhook, Discord e Slack ----------

var edicaoWebhook = edicao{
	ocultar: func(raw json.RawMessage) (any, error) {
		var cfg webhookConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
		headers := make(map[string]string, len(cfg.Headers))
		for nome, valor := range cfg.Headers {
			if valor != "" {
				valor = mascara
			}
			headers[nome] = valor
		}
		return struct {
			URLSet       bool              `json:"url_set"`
			Headers      map[string]string `json:"headers,omitempty"`
			Template     string            `json:"template,omitempty"`
			BodyTemplate json.RawMessage   `json:"body_template,omitempty"`
		}{cfg.URL != "", headers, cfg.Template, cfg.BodyTemplate}, nil
	},

	manter: func(next, prev json.RawMessage) (json.RawMessage, error) {
		var cfg, old webhookConfig
		if err := json.Unmarshal(next, &cfg); err != nil {
			// A validação devolve o erro com a mensagem certa.
			return next, nil
		}
		lerGuardado(prev, &old)

		mesmoDestino := true
		if campoEnviado(next, "url") {
			mesmoDestino = cfg.URL == old.URL
		} else {
			cfg.URL = old.URL
		}

		for nome, valor := range cfg.Headers {
			if valor != mascara {
				continue
			}
			guardado, ok := cabecalhoGuardado(old.Headers, nome)
			if !ok {
				return nil, fmt.Errorf("notifier: o cabeçalho %s chegou mascarado e não há valor guardado; informe o valor", nome)
			}
			if !mesmoDestino {
				return nil, fmt.Errorf("notifier: informe de novo o valor do cabeçalho %s ao trocar a URL; "+
					"o valor guardado não acompanha o canal para outro destino", nome)
			}
			cfg.Headers[nome] = guardado
		}
		return json.Marshal(cfg)
	},
}

// cabecalhoGuardado procura o valor guardado pelo nome exato e, na falta,
// sem diferenciar caixa, como o HTTP trata nomes.
func cabecalhoGuardado(guardados map[string]string, nome string) (string, bool) {
	if v, ok := guardados[nome]; ok {
		return v, true
	}
	for k, v := range guardados {
		if strings.EqualFold(k, nome) {
			return v, true
		}
	}
	return "", false
}

// ---------- e-mail ----------

var edicaoEmail = edicao{
	ocultar: func(raw json.RawMessage) (any, error) {
		var cfg emailConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
		return struct {
			Host        string   `json:"host"`
			Port        int      `json:"port,omitempty"`
			Security    string   `json:"security,omitempty"`
			Username    string   `json:"username,omitempty"`
			PasswordSet bool     `json:"password_set"`
			From        string   `json:"from"`
			To          []string `json:"to"`
		}{cfg.Host, cfg.Port, cfg.Security, cfg.Username, cfg.Password != "", cfg.From, cfg.To}, nil
	},

	manter: func(next, prev json.RawMessage) (json.RawMessage, error) {
		var cfg, old emailConfig
		if err := json.Unmarshal(next, &cfg); err != nil {
			return next, nil
		}
		lerGuardado(prev, &old)

		// Sem criptografia o servidor recusa senha; carregá-la faria a
		// edição falhar por um campo que a tela mostra vazio.
		semCripto := strings.EqualFold(strings.TrimSpace(cfg.Security), smtpNone)
		if !campoEnviado(next, "password") && old.Password != "" && !semCripto {
			if !strings.EqualFold(strings.TrimSpace(cfg.Host), strings.TrimSpace(old.Host)) {
				return nil, fmt.Errorf("notifier: informe a senha de novo ao trocar o servidor SMTP; " +
					"a guardada não acompanha o canal para outro servidor")
			}
			cfg.Password = old.Password
		}
		return json.Marshal(cfg)
	},
}

// ---------- ntfy ----------

var edicaoNtfy = edicao{
	ocultar: func(raw json.RawMessage) (any, error) {
		var cfg ntfyConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
		return struct {
			Server   string `json:"server,omitempty"`
			Topic    string `json:"topic"`
			TokenSet bool   `json:"token_set"`
		}{cfg.Server, cfg.Topic, cfg.Token != ""}, nil
	},

	manter: func(next, prev json.RawMessage) (json.RawMessage, error) {
		var cfg, old ntfyConfig
		if err := json.Unmarshal(next, &cfg); err != nil {
			return next, nil
		}
		lerGuardado(prev, &old)

		if !campoEnviado(next, "token") && old.Token != "" {
			if !strings.EqualFold(servidorNtfy(cfg.Server), servidorNtfy(old.Server)) {
				return nil, fmt.Errorf("notifier: informe o token de novo ao trocar o servidor do ntfy; " +
					"o guardado não acompanha o canal para outro servidor")
			}
			cfg.Token = old.Token
		}
		return json.Marshal(cfg)
	},
}

// servidorNtfy é o servidor como NewNtfy o entende, para comparar.
func servidorNtfy(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return ntfyServidorPublico
	}
	return s
}

// ---------- auxiliares ----------

// lerGuardado lê a configuração gravada. Ilegível conta como vazia: não
// há o que preservar, e a validação da nova decide o resto.
func lerGuardado(prev json.RawMessage, v any) {
	if len(prev) > 0 {
		_ = json.Unmarshal(prev, v)
	}
}

// campoEnviado informa se a configuração traz o campo em alguma grafia
// que o encoding/json casaria com ele: "Password" preenche o campo
// password, e não olhar essa grafia descartaria a senha nova enviada.
func campoEnviado(raw json.RawMessage, campo string) bool {
	fields := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for k := range fields {
		if strings.EqualFold(k, campo) {
			return true
		}
	}
	return false
}
