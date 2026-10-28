package checker

import (
	"encoding/json"

	"github.com/Jbnado/upwatch/internal/domain"
)

// pushTokenMask substitui o token do push na configuração devolvida a
// quem não administra.
const pushTokenMask = "***"

// RedactPushToken devolve a configuração do monitor push com o token
// mascarado, para sair pela API a quem só lê.
//
// O token é a credencial do /push: quem o tem mantém o monitor verde com o
// serviço parado, ou o derruba com um "down" falso. O administrador
// continua recebendo a configuração inteira, porque precisa copiar o
// endereço para a rotina; o observador recebe a máscara, que diz que há
// um token sem dizer qual. Uma configuração que não se consegue
// interpretar sai vazia, nunca crua.
//
// O PR #53 cria RedactConfig, em secret.go, para as senhas dos checkers
// de protocolo, que saem mascaradas para todo mundo. Este é o mínimo para
// o push até lá: a unificação das duas — um lugar só decidindo o que é
// segredo em cada tipo — acontece no merge.
func RedactPushToken(t domain.MonitorType, raw json.RawMessage) json.RawMessage {
	if t != domain.MonitorPush || len(raw) == 0 {
		return raw
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	if _, ok := fields["token"]; ok {
		fields["token"] = json.RawMessage(`"` + pushTokenMask + `"`)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return out
}
