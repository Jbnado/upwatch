package secretbox

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
)

// Source entrega a caixa ao store depois de ele olhar o banco.
//
// sealed diz se o banco já tem segredo cifrado. É o que separa a
// instalação nova, que pode ganhar uma chave gerada na hora, da que perdeu
// a chave, que não pode.
type Source func(sealed bool) (*Box, error)

// Fixed entrega sempre a mesma caixa.
func Fixed(b *Box) Source {
	return func(bool) (*Box, error) { return b, nil }
}

// Ephemeral entrega uma caixa de chave sorteada que só existe na memória.
// Serve a teste e a quem não reabre o banco; numa instalação, a chave se
// perderia no primeiro reinício.
func Ephemeral() Source {
	return func(bool) (*Box, error) {
		k, err := GenerateKey()
		if err != nil {
			return nil, err
		}
		return New(k)
	}
}

// Resolver decide a chave da instalação.
type Resolver struct {
	// Configured é a chave em base64 vinda da configuração; vazio se
	// nenhuma foi dada.
	Configured string
	// AutoPath é onde a chave gerada mora, ao lado do banco SQLite. Vazio
	// proíbe gerar: no PostgreSQL cada instância sortearia a sua.
	AutoPath string
	// Logger recebe o aviso da chave gerada.
	Logger *slog.Logger
}

// Box resolve a chave, nesta ordem: a configurada; a gerada numa subida
// anterior; uma nova, se o banco ainda não tem segredo cifrado e há onde
// guardá-la.
func (r Resolver) Box(sealed bool) (*Box, error) {
	log := r.Logger
	if log == nil {
		log = slog.Default()
	}

	if r.Configured != "" {
		k, err := ParseKey(r.Configured)
		if err != nil {
			return nil, err
		}
		if r.AutoPath != "" {
			if _, err := os.Stat(r.AutoPath); err == nil {
				log.Warn("há uma chave de segredos gerada ao lado do banco, mas a configurada é outra; "+
					"depois de conferir que a configurada abre o banco, apague o arquivo: quem copia o volume leva a chave junto",
					"arquivo", r.AutoPath)
			}
		}
		return New(k)
	}

	if r.AutoPath != "" {
		data, err := os.ReadFile(r.AutoPath)
		switch {
		case err == nil:
			k, err := ParseKey(string(data))
			if err != nil {
				return nil, fmt.Errorf("lendo %s: %w", r.AutoPath, err)
			}
			return New(k)
		case !errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("secretbox: lendo a chave em %s: %w", r.AutoPath, err)
		}
	}

	if sealed {
		return nil, fmt.Errorf("%w: o banco já tem segredos cifrados, e sem a chave com que foram gravados não dá para abri-los", ErrKeyMissing)
	}
	if r.AutoPath == "" {
		return nil, ErrKeyMissing
	}
	return r.generate(log)
}

// generate sorteia a chave e a grava legível só pelo dono. O arquivo é
// criado em modo exclusivo: duas subidas simultâneas não sobrescrevem a
// chave uma da outra.
func (r Resolver) generate(log *slog.Logger) (*Box, error) {
	k, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(r.AutoPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("secretbox: gravando a chave gerada em %s: %w", r.AutoPath, err)
	}
	_, werr := f.WriteString(k.Encode() + "\n")
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(r.AutoPath)
		return nil, fmt.Errorf("secretbox: gravando a chave gerada em %s: %w", r.AutoPath, err)
	}

	log.Warn("chave de segredos gerada ao lado do banco; ela cifra as credenciais dos monitores e canais. "+
		"Ao lado do banco ela protege pouco: copie-a para fora do volume, aponte UPWATCH_SECRET_KEY_FILE para a cópia e apague esta. "+
		"Sem ela, as credenciais guardadas se perdem",
		"arquivo", r.AutoPath)
	return New(k)
}
