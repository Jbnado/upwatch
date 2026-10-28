package checker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/secretbox"
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

// secretHolder é a configuração de um tipo que guarda credencial, lida na
// struct do próprio checker.
type secretHolder interface {
	// secretField aponta para a senha.
	secretField() *string
	// transportLevel ordena a proteção da conexão que leva a senha: maior
	// é mais forte.
	transportLevel() int
	// secretHeaders devolve os cabeçalhos da requisição, nos tipos que os
	// têm; os de nome sensível são credencial também.
	secretHeaders() map[string]string
}

// secretKind descreve como cada tipo guarda a credencial.
type secretKind struct {
	// field é o nome canônico da senha na configuração, e marker o campo
	// que a substitui na saída.
	field, marker string
	decode        func(json.RawMessage) (secretHolder, error)
	// headers é o campo dos cabeçalhos, nos tipos que os têm; os de nome
	// sensível são credencial também.
	headers string
}

// decodeAs lê a configuração na struct do checker, com as mesmas regras
// do encoding/json que o checker usa ao verificar.
//
// É o que fecha a brecha da grafia: o json casa "Password", "PASSWORD" e
// até "paſſword" com o campo password, e só olhar a grafia canônica num
// mapa deixava as outras passarem inteiras pelo GET. Lendo na struct, o
// que a ocultação vê é exatamente o que a verificação usa.
func decodeAs[T any, P interface {
	*T
	secretHolder
}](raw json.RawMessage) (secretHolder, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return P(&v), nil
}

// Audience é para quem a configuração sai pela API.
type Audience int

const (
	// Readers é quem só lê, e também o fluxo de eventos, que vai para todo
	// mundo conectado. É o valor zero de propósito: quem esquecer de
	// escolher entrega o mínimo.
	Readers Audience = iota
	// Admins cadastram os monitores e leem de volta o que copiam para a
	// rotina monitorada.
	Admins
)

func (a Audience) String() string {
	if a == Admins {
		return "administrador"
	}
	return "leitor"
}

// operatorSecrets são os segredos que o administrador lê de volta e quem
// só lê, não, pelo nome do campo na configuração.
//
// O token do push compõe o endereço que o administrador copia para a
// rotina, então escondê-lo dele como a uma senha obrigaria a gerar outro
// a cada visita ao formulário. Nas mãos de um observador, é a credencial
// do /push: mantém o monitor verde com o serviço parado, ou o derruba com
// um "down" falso.
var operatorSecrets = map[domain.MonitorType]string{
	domain.MonitorPush: "token",
}

// secretKinds são os tipos cuja configuração carrega credencial que não
// volta para ninguém. A lista mora aqui, e não em cada handler, para a
// omissão no GET e a preservação na edição nunca divergirem.
var secretKinds = map[domain.MonitorType]secretKind{
	domain.MonitorRedis:    {"password", "password_set", decodeAs[RedisConfig], ""},
	domain.MonitorPostgres: {"password", "password_set", decodeAs[PostgresConfig], ""},
	domain.MonitorMySQL:    {"password", "password_set", decodeAs[MySQLConfig], ""},
	domain.MonitorHTTP:     {"basic_auth_pass", "basic_auth_pass_set", decodeAs[HTTPConfig], "headers"},
}

// SecretFields diz onde a configuração do tipo guarda segredo, para a
// cifragem em repouso.
//
// Sai das mesmas listas que RedactConfig usa: com duas, um campo novo de
// senha entraria numa e não na outra, e ficaria oculto na tela e em texto
// puro no backup. O token do push entra também: o administrador o lê de
// volta pela API, mas no banco é a credencial do /push como qualquer
// outra.
func SecretFields(t domain.MonitorType) secretbox.Fields {
	var f secretbox.Fields
	if field, ok := operatorSecrets[t]; ok {
		f.Names = append(f.Names, field)
	}
	if kind, ok := secretKinds[t]; ok {
		f.Names = append(f.Names, kind.field)
		if kind.headers != "" {
			f.Map, f.MapSecret = kind.headers, sensitiveHeader
		}
	}
	return f
}

func (c *RedisConfig) secretField() *string             { return &c.Password }
func (c *RedisConfig) secretHeaders() map[string]string { return nil }
func (c *RedisConfig) transportLevel() int {
	switch {
	case !c.TLS:
		return 0
	case c.SkipTLSVerify:
		return 1
	}
	return 2
}

// postgresSSLLevels ordena os sslmode do libpq; vazio é prefer.
var postgresSSLLevels = map[string]int{
	"disable": 0, "allow": 1, "": 2, "prefer": 2,
	"require": 3, "verify-ca": 4, "verify-full": 5,
}

func (c *PostgresConfig) secretField() *string             { return &c.Password }
func (c *PostgresConfig) secretHeaders() map[string]string { return nil }
func (c *PostgresConfig) transportLevel() int              { return postgresSSLLevels[c.SSLMode] }

// mysqlTLSLevels ordena os modos de TLS do driver: preferred aceita
// conexão sem TLS, skip-verify cifra sem conferir o certificado.
var mysqlTLSLevels = map[string]int{
	"": 0, "false": 0, "preferred": 1, "skip-verify": 2, "true": 3,
}

func (c *MySQLConfig) secretField() *string             { return &c.Password }
func (c *MySQLConfig) secretHeaders() map[string]string { return nil }
func (c *MySQLConfig) transportLevel() int              { return mysqlTLSLevels[c.TLS] }

// No HTTP, sair de HTTPS é troca de alvo e já cai na regra do endereço;
// aqui resta a verificação do certificado.
func (c *HTTPConfig) secretField() *string             { return &c.BasicAuthPass }
func (c *HTTPConfig) secretHeaders() map[string]string { return c.Headers }
func (c *HTTPConfig) transportLevel() int {
	if c.SkipTLSVerify {
		return 0
	}
	return 1
}

// sensitiveHeaderWords marcam um cabeçalho como credencial quando aparecem
// no nome, sem diferenciar caixa: Authorization, Cookie, X-Api-Key,
// X-Auth-Token, X-Client-Secret. Mascarar um cabeçalho inofensivo custa
// pouco; deixar passar um token custa a credencial.
var sensitiveHeaderWords = []string{"auth", "cookie", "token", "key", "secret", "password", "passwd", "session", "credential"}

func sensitiveHeader(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range sensitiveHeaderWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// RedactConfig devolve a configuração como ela sai pela API para o
// público dado. É o único lugar que decide o que é segredo em cada tipo.
//
// O segredo de operador (operatorSecrets) volta ao administrador e sai
// mascarado para os demais. A credencial (secretKinds) não volta para
// ninguém: a senha é trocada pelo marcador (password_set, basic_auth_pass_set): a
// interface precisa saber que existe uma para dizer "deixe em branco para
// manter", sem saber qual é. Os cabeçalhos de credencial continuam na
// lista com o valor mascarado, para a interface mostrar que existem.
// Uma configuração que não se consegue interpretar sai vazia, nunca crua:
// na dúvida, não se publica o que pode conter segredo.
func RedactConfig(t domain.MonitorType, raw json.RawMessage, to Audience) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	if field, ok := operatorSecrets[t]; ok {
		if to == Admins {
			return raw
		}
		return maskField(raw, field)
	}
	kind, ok := secretKinds[t]
	if !ok {
		return raw
	}
	cfg, err := kind.decode(raw)
	if err != nil {
		return nil
	}
	had := *cfg.secretField() != ""
	*cfg.secretField() = ""
	headers := cfg.secretHeaders()
	for name, value := range headers {
		if value != "" && sensitiveHeader(name) {
			headers[name] = secretMask
		}
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	if !had {
		return out
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(out, &fields); err != nil {
		return nil
	}
	fields[kind.marker] = json.RawMessage("true")
	out, err = json.Marshal(fields)
	if err != nil {
		return nil
	}
	return out
}

// maskField troca pela máscara o valor do campo em toda grafia que o
// encoding/json casaria com ele (ver fieldSent), e devolve o resto da
// configuração como veio. Quem a lê sabe que o segredo existe sem saber
// qual é.
func maskField(raw json.RawMessage, field string) json.RawMessage {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	for k := range fields {
		if strings.EqualFold(k, field) {
			fields[k] = json.RawMessage(`"` + secretMask + `"`)
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil
	}
	return out
}

// CanonicalConfig prepara a configuração de um monitor novo: a mesma
// normalização da edição, sem nada guardado para preservar.
func CanonicalConfig(m domain.Monitor) (json.RawMessage, error) {
	return CarrySecrets(m, domain.Monitor{})
}

// CarrySecrets prepara a configuração de uma edição, mantendo a senha
// guardada quando a requisição não a traz.
//
// Como a senha não volta no GET, editar qualquer outro campo chega sem
// ela; tratar a ausência como "sem senha" apagaria a credencial a cada
// salvamento. Senha presente, mesmo vazia e em qualquer grafia que o
// checker aceite, é a nova. Um cabeçalho de credencial que volta com a
// máscara retoma o valor guardado.
//
// A configuração sai reescrita pela struct do checker, então só a grafia
// canônica chega ao banco, sem o marcador nem campo desconhecido.
//
// A credencial só acompanha o monitor no mesmo endereço e sem TLS mais
// fraco. Do contrário, quem pode editar monitores mas não ler a senha a
// obteria trocando o alvo por um servidor seu, ou desligando a
// verificação do certificado para quem estiver no caminho: o UpWatch a
// entregaria na verificação seguinte.
func CarrySecrets(next, prev domain.Monitor) (json.RawMessage, error) {
	kind, ok := secretKinds[next.Type]
	if !ok || len(next.Config) == 0 {
		return next.Config, nil
	}
	cfg, err := kind.decode(next.Config)
	if err != nil {
		// A validação do checker devolve o erro com a mensagem certa.
		return next.Config, nil
	}
	var old secretHolder
	if prev.Type == next.Type && len(prev.Config) > 0 {
		old, _ = kind.decode(prev.Config)
	}

	mayCarry := func(what string) error {
		if prev.Target != next.Target {
			return fmt.Errorf("checker: informe %s de novo ao trocar o endereço; o valor guardado não acompanha o monitor para outro servidor", what)
		}
		if cfg.transportLevel() < old.transportLevel() {
			return fmt.Errorf("checker: informe %s de novo ao enfraquecer o TLS; o valor guardado não acompanha o monitor para uma conexão menos protegida", what)
		}
		return nil
	}

	if !fieldSent(next.Config, kind.field) && old != nil && *old.secretField() != "" {
		if err := mayCarry("a senha"); err != nil {
			return nil, err
		}
		*cfg.secretField() = *old.secretField()
	}

	headers := cfg.secretHeaders()
	for name, value := range headers {
		if value != secretMask || !sensitiveHeader(name) {
			continue
		}
		stored, ok := storedHeader(old, name)
		if !ok {
			return nil, fmt.Errorf("checker: o cabeçalho %s chegou mascarado e não há valor guardado; informe o valor", name)
		}
		if err := mayCarry("o valor do cabeçalho " + name); err != nil {
			return nil, err
		}
		headers[name] = stored
	}

	return json.Marshal(cfg)
}

// fieldSent informa se a configuração traz o campo em alguma grafia que
// o encoding/json casaria com ele (ver decodeAs).
func fieldSent(raw json.RawMessage, field string) bool {
	fields := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for k := range fields {
		if strings.EqualFold(k, field) {
			return true
		}
	}
	return false
}

// storedHeader procura o valor guardado de um cabeçalho, pelo nome exato
// e, na falta, sem diferenciar caixa, como o HTTP trata nomes.
func storedHeader(old secretHolder, name string) (string, bool) {
	if old == nil {
		return "", false
	}
	headers := old.secretHeaders()
	if v, ok := headers[name]; ok && v != secretMask {
		return v, true
	}
	for k, v := range headers {
		if strings.EqualFold(k, name) && v != secretMask {
			return v, true
		}
	}
	return "", false
}
