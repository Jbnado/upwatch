package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
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

// rotateSecretKey é o comando `upwatch rotate-secret-key`: recifra todas
// as credenciais com uma chave nova, numa transação só.
//
// Roda com o UpWatch parado. Uma instância no ar continuaria gravando
// com a chave antiga o que fosse editado depois da troca, e esses valores
// não abririam mais com a nova.
func rotateSecretKey(args []string) error {
	fs := flag.NewFlagSet("rotate-secret-key", flag.ContinueOnError)
	configPath := fs.String("config", os.Getenv("UPWATCH_CONFIG"), "caminho do arquivo de configuração (opcional)")
	oldFile := fs.String("old-key-file", "",
		"arquivo com a chave atual; sem ele, vale a da partida (UPWATCH_SECRET_KEY, UPWATCH_SECRET_KEY_FILE ou a gerada ao lado do banco SQLite)")
	newFile := fs.String("new-key-file", "",
		"arquivo com a chave nova; sem ele, vale UPWATCH_NEW_SECRET_KEY ou UPWATCH_NEW_SECRET_KEY_FILE")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	oldKey, err := currentKey(cfg, *oldFile)
	if err != nil {
		return err
	}
	newKey, err := nextKey(*newFile)
	if err != nil {
		return err
	}
	if oldKey == newKey {
		return errors.New("rotate-secret-key: a chave nova é igual à atual")
	}

	// A antiga abre o banco; a nova também lê, para uma troca rodada de
	// novo depois de já confirmada não ser recusada.
	box, err := secretbox.New(oldKey, newKey)
	if err != nil {
		return err
	}
	st, err := sqlstore.Open(cfg.Database.Driver, cfg.Database.DSN, secretbox.Fixed(box))
	if err != nil {
		return explainKeyError(cfg, err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			slog.Error("falha ao fechar o banco", "erro", err)
		}
	}()

	if err := st.RotateSecretKey(context.Background(), newKey); err != nil {
		return err
	}
	slog.Info("credenciais recifradas com a chave nova. Antes de subir o UpWatch, aponte UPWATCH_SECRET_KEY_FILE " +
		"(ou UPWATCH_SECRET_KEY) para ela: a antiga não abre mais o banco")
	if *oldFile == "" && cfg.SecretKey == "" {
		slog.Warn("a chave antiga era a gerada ao lado do banco; apague o arquivo dela, que não abre mais nada",
			"arquivo", autoKeyPath(cfg))
	}
	return nil
}

// currentKey é a chave que abre o banco hoje: a do arquivo dado, a da
// configuração ou a gerada ao lado do banco SQLite, nesta ordem.
func currentKey(cfg config.Config, file string) (secretbox.Key, error) {
	switch {
	case file != "":
		return readKeyFile(file)
	case cfg.SecretKey != "":
		return secretbox.ParseKey(cfg.SecretKey)
	}
	if path := autoKeyPath(cfg); path != "" {
		if _, err := os.Stat(path); err == nil {
			return readKeyFile(path)
		}
	}
	return secretbox.Key{}, errors.New("rotate-secret-key: informe a chave atual com --old-key-file, UPWATCH_SECRET_KEY ou UPWATCH_SECRET_KEY_FILE")
}

// nextKey é a chave para a qual se troca.
func nextKey(file string) (secretbox.Key, error) {
	if file != "" {
		return readKeyFile(file)
	}
	v, ok, err := config.LookupSecret("NEW_SECRET_KEY")
	if err != nil {
		return secretbox.Key{}, err
	}
	if !ok {
		return secretbox.Key{}, errors.New("rotate-secret-key: informe a chave nova com --new-key-file, UPWATCH_NEW_SECRET_KEY ou UPWATCH_NEW_SECRET_KEY_FILE; gere uma com `openssl rand -base64 32`")
	}
	return secretbox.ParseKey(v)
}

func readKeyFile(path string) (secretbox.Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return secretbox.Key{}, fmt.Errorf("rotate-secret-key: lendo %s: %w", path, err)
	}
	k, err := secretbox.ParseKey(string(data))
	if err != nil {
		return secretbox.Key{}, fmt.Errorf("rotate-secret-key: %s: %w", path, err)
	}
	return k, nil
}

// forgetSecrets é o comando `upwatch forget-secrets`: a saída para a
// chave perdida. Apaga as credenciais cifradas e o sentinela; o resto do
// banco fica, e cada credencial é digitada de novo pela interface depois
// de subir com uma chave nova.
//
// Exige --yes: com a chave certa à mão, o comando só destruiria o que
// ainda dava para ler.
func forgetSecrets(args []string) error {
	fs := flag.NewFlagSet("forget-secrets", flag.ContinueOnError)
	configPath := fs.String("config", os.Getenv("UPWATCH_CONFIG"), "caminho do arquivo de configuração (opcional)")
	yes := fs.Bool("yes", false, "confirma que a chave se perdeu e que as credenciais cifradas podem ser apagadas")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*yes {
		return errors.New("forget-secrets: isto apaga todas as credenciais cifradas de monitores e canais, " +
			"que precisarão ser digitadas de novo; rode só se a chave se perdeu, e confirme com --yes")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	n, err := sqlstore.ForgetSecrets(cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		return err
	}
	slog.Warn("credenciais cifradas apagadas; suba com a chave nova e digite-as de novo pela interface",
		"valores_apagados", n)
	if path := autoKeyPath(cfg); path != "" {
		if _, err := os.Stat(path); err == nil {
			slog.Warn("a chave gerada ao lado do banco ainda existe e não abre o que foi apagado; "+
				"se ela não é a nova, apague-a para a próxima subida gerar outra", "arquivo", path)
		}
	}
	return nil
}
