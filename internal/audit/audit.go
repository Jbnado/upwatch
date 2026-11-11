// Package audit padroniza as linhas de evento de segurança.
//
// Um lugar só para os nomes das ações e para a escrita da linha, porque
// um log de auditoria só serve se as linhas tiverem todas os mesmos
// campos: quem coleta filtra por "ação=", e quem audita precisa de ator,
// alvo e origem sempre nas mesmas chaves.
//
// O pacote existe à parte do servidor porque não é só o servidor que
// registra: `upwatch disable-2fa` desliga a verificação em duas etapas
// com o servidor parado, e é a ação mais drástica do sistema. Escrita no
// formato dela, ficaria fora do filtro que encontra todo o resto.
package audit

import "log/slog"

// Ações registradas no log de auditoria.
//
// Nomes estáveis, em minúsculas e separados por ponto, para servirem de
// filtro: quem coleta o log procura por "ação=login.senha_errada", e um
// texto reescrito não pode quebrar esse filtro.
const (
	AçãoLoginSenhaErrada  = "login.senha_errada"
	AçãoLoginCódigoErrado = "login.codigo_errado"
	AçãoLimiteBloqueio    = "limite.bloqueio"
	AçãoContaBloqueada    = "login.conta_bloqueada"
	AçãoParBloqueado      = "login.par_bloqueado"
	AçãoOrigemBloqueada   = "login.origem_bloqueada"

	AçãoTokenCriado      = "token.criado"
	AçãoTokenRevogado    = "token.revogado"
	AçãoTokenRotacionado = "token.rotacionado"
	// AçãoTokenReuso registra que um token rotacionado foi usado depois de o
	// sucessor já estar em uso: sinal de vazamento do segredo antigo.
	AçãoTokenReuso = "token.reuso"
	// AçãoTokenValidadeEsticada registra quem afastou o vencimento de um
	// token, e quando. Encurtar ou adicionar prazo não gera linha — é
	// sempre mais restritivo; esticar amplia o alcance de uma credencial
	// que já existe, e é o que precisa de rastro.
	AçãoTokenValidadeEsticada = "token.validade_esticada"

	AçãoSenhaTrocada       = "senha.trocada"
	AçãoUsuárioCriado      = "usuario.criado"
	AçãoUsuárioRemovido    = "usuario.removido"
	AçãoAcessoTrocado      = "usuario.acesso_trocado"
	AçãoSetupTokenErrado   = "setup.token_errado"
	AçãoPushTokenErrado    = "push.token_errado"
	AçãoSenhaAtualErrada   = "senha.atual_errada"
	AçãoMetricsTokenErrado = "metrics.token_errado"

	Ação2FALigada                      = "2fa.ligada"
	Ação2FADesligada                   = "2fa.desligada"
	Ação2FARecuperaçãoNova             = "2fa.recuperacao_trocada"
	Ação2FARecuperaçãoUsada            = "2fa.recuperacao_usada"
	Ação2FADesligadaPorAdmin           = "2fa.desligada_por_admin"
	Ação2FADesligadaPelaLinhaDeComando = "2fa.desligada_pela_linha_de_comando"
)

// MaxAtorLength limita o campo ator da linha.
//
// O mesmo teto do nome de conta (domain.MaxUsernameLength), repetido
// aqui em vez de importado para o pacote de auditoria não depender do
// domínio: ele existe para ser chamado de qualquer lugar, inclusive da
// linha de comando.
const MaxAtorLength = 64

// AtorAnônimo é o ator das ações sem conta identificada.
const AtorAnônimo = "anônimo"

const (
	// AtorLinhaDeComando é quem roda um subcomando de manutenção. Não há
	// conta nem sessão: a prova é o acesso ao servidor e ao banco, e é
	// isso que o ator diz.
	AtorLinhaDeComando = "linha-de-comando"

	// OrigemLocal é a origem de quem não chegou pela rede.
	OrigemLocal = "local"
)

// Evento é um evento de segurança.
//
// Campos nomeados, e não argumentos em sequência, porque quatro strings
// seguidas se trocam de lugar sem o compilador reclamar — e uma linha com
// ator e alvo invertidos acusa a vítima.
type Evento struct {
	// Ator é quem fez, ou tentou fazer.
	Ator string
	// Ação é uma das constantes acima.
	Ação string
	// Alvo é sobre o que a ação foi: a conta, o identificador ou a rota.
	Alvo string
	// Origem é de onde veio.
	Origem string
}

// Registra escreve o evento.
//
// Nada aqui recebe senha, código ou token: o Evento só tem campos de
// identificação, e quem chama passa nome de conta, identificador ou rota.
// Confira em internal/api/audit_secrets_test.go, TestAuditNeverLogsSecrets.
//
// Tudo em INFO, inclusive o que é grave: o nível diria a duas pessoas
// coisas diferentes sobre a mesma linha, e quem filtra por nível perderia
// eventos. A gravidade está na ação.
func Registra(l *slog.Logger, e Evento) {
	if l == nil {
		l = slog.Default()
	}
	if e.Ator == "" {
		e.Ator = AtorAnônimo
	}
	// Truncado porque o ator pode vir do corpo da requisição: o login
	// registra o nome oferecido, e o corpo aceito vai a 1 MiB. Não há
	// forja de linha — o handler do slog escapa \n e aspas —, mas trinta
	// requisições de rajada com um nome enorme enchem o coletor de quem
	// paga por volume. O limite é o do nome de conta: acima dele não há
	// conta nenhuma.
	if len(e.Ator) > MaxAtorLength {
		e.Ator = e.Ator[:MaxAtorLength] + "…"
	}
	l.Info("evento de segurança",
		"ação", e.Ação,
		"ator", e.Ator,
		"alvo", e.Alvo,
		"origem", e.Origem,
	)
}
