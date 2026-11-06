package domain

import "time"

// TOTP é o segundo fator de uma conta.
type TOTP struct {
	UserID int64

	// Secret é o segredo em base32, já decifrado. Em repouso fica cifrado
	// (ver sqlstore), e nunca sai pela API depois do cadastro.
	Secret string

	// Enabled distingue o cadastro confirmado do pendente. Pendente não
	// vale para login: o segredo existe, mas ninguém provou ainda que o
	// aplicativo o tem.
	Enabled bool

	// LastStep é o último passo de tempo aceito. Códigos de passo igual
	// ou anterior são recusados, para o mesmo código não valer duas vezes.
	LastStep int64

	CreatedAt time.Time
	EnabledAt *time.Time
}

// LoginChallenge é o meio do caminho de um login com dois fatores: a
// senha já conferiu, o código ainda não.
type LoginChallenge struct {
	// Hash é o que fica no banco; o cliente guarda o segredo cru, como no
	// cookie de sessão.
	Hash      string
	UserID    int64
	Attempts  int
	CreatedAt time.Time
	ExpiresAt time.Time
}
