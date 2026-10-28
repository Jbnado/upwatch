package main

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/Jbnado/upwatch/internal/config"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

// autoKeyName é o arquivo da chave gerada, ao lado do banco SQLite.
const autoKeyName = "secret.key"

// autoKeyPath é onde a chave gerada mora. Só o SQLite tem um diretório de
// dados; no PostgreSQL, que roda em mais de uma instância, cada uma
// sortearia a sua, e uma não leria o que a outra gravou.
func autoKeyPath(cfg config.Config) string {
	if cfg.Database.Driver != "sqlite" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfg.Database.DSN), autoKeyName)
}

// openStore abre o armazenamento configurado, com a chave das
// credenciais.
//
// A escolha do driver vive no sqlstore, ao lado das duas implementações,
// e não aqui: um segundo lugar decidindo isso é um segundo lugar para
// esquecer de atualizar — foi exatamente o que aconteceu quando o driver
// PostgreSQL entrou e este switch continuou recusando-o.
func openStore(cfg config.Config) (*sqlstore.Store, error) {
	keys := secretbox.Resolver{
		Configured: cfg.SecretKey,
		AutoPath:   autoKeyPath(cfg),
		Logger:     slog.Default(),
	}
	st, err := sqlstore.Open(cfg.Database.Driver, cfg.Database.DSN, keys.Box)
	if err != nil {
		return nil, explainKeyError(cfg, err)
	}
	return st, nil
}

// explainKeyError acrescenta ao erro de chave o que quem opera precisa
// fazer, com o nome das variáveis, que o store não conhece.
func explainKeyError(cfg config.Config, err error) error {
	switch {
	case errors.Is(err, secretbox.ErrWrongKey):
		return fmt.Errorf("%w. Confira UPWATCH_SECRET_KEY ou UPWATCH_SECRET_KEY_FILE: a chave precisa ser a mesma com que as credenciais foram gravadas", err)
	case errors.Is(err, secretbox.ErrKeyMissing) && autoKeyPath(cfg) == "":
		return fmt.Errorf("%w. No PostgreSQL a chave não é gerada sozinha, porque cada instância sortearia a sua: "+
			"gere uma com `openssl rand -base64 32`, guarde-a fora do servidor do banco e aponte UPWATCH_SECRET_KEY_FILE "+
			"(ou UPWATCH_SECRET_KEY) para ela em todas as instâncias", err)
	case errors.Is(err, secretbox.ErrKeyMissing):
		return fmt.Errorf("%w. Aponte UPWATCH_SECRET_KEY_FILE (ou UPWATCH_SECRET_KEY) para a chave com que o banco foi cifrado; "+
			"a gerada pelo UpWatch ficava em %s", err, autoKeyPath(cfg))
	}
	return err
}
