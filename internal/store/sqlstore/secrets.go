package sqlstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/secretbox"
)

// A cifragem em repouso das credenciais.
//
// O store cifra a configuração ao gravar e decifra ao ler, e é o único
// lugar que faz isso: acima dele, monitores e canais chegam com o texto
// puro em memória, como sempre. Quais campos são segredo, o checker e o
// notifier dizem; como se cifra, o secretbox. Aqui fica só onde cada
// registro mora, que entra no dado associado da cifra.

const (
	monitorTable = "monitor"
	channelTable = "notification_channel"
)

// keyCheck é o valor conhecido do registro-sentinela (ver a migração
// 00007).
const keyCheck = "upwatch-secret-key-check"

var keyCheckSlot = secretbox.Slot{Table: "secret_key_check", ID: 1, Field: "value"}

func monitorFields(typeName string) secretbox.Fields {
	t, err := domain.ParseMonitorType(typeName)
	if err != nil {
		return secretbox.Fields{}
	}
	return checker.SecretFields(t)
}

// hashPushToken é o que a coluna push_token guarda. A busca do /push é
// por igualdade e não precisa do valor; o token inteiro, que o
// administrador copia para a rotina, fica cifrado na configuração.
func hashPushToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// unlockSecrets resolve a chave, confere que ela é a do banco e cifra o
// que ainda estiver em texto puro.
func (s *Store) unlockSecrets(keys secretbox.Source) error {
	if keys == nil {
		return fmt.Errorf("sqlstore: %w", secretbox.ErrKeyMissing)
	}
	ctx := context.Background()

	sealed, err := s.hasSealedSecrets(ctx)
	if err != nil {
		return err
	}
	box, err := keys(sealed)
	if err != nil {
		return fmt.Errorf("sqlstore: chave de segredos: %w", err)
	}

	err = s.db.inTx(ctx, func(t *tx) error {
		if err := verifyKey(ctx, t, box); err != nil {
			return err
		}
		return sealAtRest(ctx, t, box)
	})
	if err != nil {
		return err
	}
	s.db.secrets = box
	return nil
}

// hasSealedSecrets informa se o banco já tem algo cifrado: o sentinela,
// ou um valor cifrado que sobrou de antes de um rollback que o apagou.
func (s *Store) hasSealedSecrets(ctx context.Context) (bool, error) {
	for _, q := range []string{
		`SELECT 1 FROM secret_key_check`,
		`SELECT 1 FROM monitor WHERE config LIKE '%"enc:%'`,
		`SELECT 1 FROM notification_channel WHERE config LIKE '%"enc:%'`,
	} {
		var one int
		err := s.db.QueryRowContext(ctx, q+` LIMIT 1`).Scan(&one)
		switch {
		case err == nil:
			return true, nil
		case !errors.Is(err, sql.ErrNoRows):
			return false, fmt.Errorf("sqlstore: procurando segredo cifrado: %w", err)
		}
	}
	return false, nil
}

// verifyKey abre o sentinela com a chave dada, e o cria na primeira
// subida. Duas instâncias subindo juntas num banco novo não brigam: a
// segunda encontra o sentinela da primeira e o confere.
func verifyKey(ctx context.Context, t *tx, box *secretbox.Box) error {
	stored, err := readKeyCheck(ctx, t)
	if errors.Is(err, sql.ErrNoRows) {
		sealed, err := box.Seal(keyCheck, keyCheckSlot)
		if err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx,
			`INSERT INTO secret_key_check (id, value) VALUES (1, ?) ON CONFLICT (id) DO NOTHING`, sealed); err != nil {
			return fmt.Errorf("sqlstore: gravando o sentinela da chave: %w", err)
		}
		stored, err = readKeyCheck(ctx, t)
		if err != nil {
			return fmt.Errorf("sqlstore: relendo o sentinela da chave: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("sqlstore: lendo o sentinela da chave: %w", err)
	}

	if plain, err := box.Open(stored, keyCheckSlot); err != nil || plain != keyCheck {
		return fmt.Errorf("sqlstore: %w; ela não abre o que este banco cifrou. "+
			"Sem a chave original, as credenciais guardadas não voltam", secretbox.ErrWrongKey)
	}
	return nil
}

func readKeyCheck(ctx context.Context, t *tx) (string, error) {
	var v string
	err := t.QueryRowContext(ctx, `SELECT value FROM secret_key_check WHERE id = 1`).Scan(&v)
	return v, err
}

// storedConfig é uma linha com configuração, como está gravada.
type storedConfig struct {
	id        int64
	typ       string
	config    string
	pushToken sql.NullString
}

func readConfigs(ctx context.Context, t *tx, query string, withPush bool) ([]storedConfig, error) {
	rows, err := t.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: lendo configurações: %w", err)
	}
	defer rows.Close()

	var out []storedConfig
	for rows.Next() {
		var c storedConfig
		dest := []any{&c.id, &c.typ, &c.config}
		if withPush {
			dest = append(dest, &c.pushToken)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo configurações: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sealAtRest cifra o que uma instalação anterior gravou em texto puro, e
// troca o token do push na coluna da busca pelo hash dele.
//
// Roda em toda partida, não uma vez só: uma volta temporária à versão
// anterior gravaria texto puro de novo, e a próxima subida precisa
// cifrá-lo. Por isso é idempotente — o que já está cifrado e o hash que
// já confere ficam como estão, e um banco em dia não é reescrito.
func sealAtRest(ctx context.Context, t *tx, box *secretbox.Box) error {
	monitors, err := readConfigs(ctx, t, `SELECT id, type, config, push_token FROM monitor`, true)
	if err != nil {
		return err
	}
	for _, m := range monitors {
		fields := monitorFields(m.typ)
		plain, err := box.OpenConfig(json.RawMessage(m.config), fields, monitorTable, m.id)
		if err != nil {
			return fmt.Errorf("sqlstore: %w", err)
		}

		config := m.config
		if secretbox.HasPlaintext(json.RawMessage(m.config), fields) {
			sealed, err := box.SealConfig(plain, fields, monitorTable, m.id)
			if err != nil {
				return fmt.Errorf("sqlstore: cifrando o monitor %d: %w", m.id, err)
			}
			config = string(sealed)
		}

		// A coluna só tem valor no monitor que é dono do token; o que a
		// 00005 deixou sem coluna, por repetir o token de outro, continua
		// sem.
		push := m.pushToken
		if push.Valid {
			typ, _ := domain.ParseMonitorType(m.typ)
			hashed := hashPushToken(checker.PushTokenOf(domain.Monitor{Type: typ, Config: plain}))
			push = sql.NullString{String: hashed, Valid: hashed != ""}
		}

		if config == m.config && push == m.pushToken {
			continue
		}
		if _, err := t.ExecContext(ctx, `UPDATE monitor SET config = ?, push_token = ? WHERE id = ?`,
			config, push, m.id); err != nil {
			return fmt.Errorf("sqlstore: cifrando o monitor %d: %w", m.id, err)
		}
	}

	channels, err := readConfigs(ctx, t, `SELECT id, type, config FROM notification_channel`, false)
	if err != nil {
		return err
	}
	for _, c := range channels {
		fields := notifier.SecretFields(c.typ)
		if !secretbox.HasPlaintext(json.RawMessage(c.config), fields) {
			// Abrir mesmo assim confere a chave contra cada valor.
			if _, err := box.OpenConfig(json.RawMessage(c.config), fields, channelTable, c.id); err != nil {
				return fmt.Errorf("sqlstore: %w", err)
			}
			continue
		}
		plain, err := box.OpenConfig(json.RawMessage(c.config), fields, channelTable, c.id)
		if err != nil {
			return fmt.Errorf("sqlstore: %w", err)
		}
		sealed, err := box.SealConfig(plain, fields, channelTable, c.id)
		if err != nil {
			return fmt.Errorf("sqlstore: cifrando o canal %d: %w", c.id, err)
		}
		if _, err := t.ExecContext(ctx, `UPDATE notification_channel SET config = ? WHERE id = ?`,
			string(sealed), c.id); err != nil {
			return fmt.Errorf("sqlstore: cifrando o canal %d: %w", c.id, err)
		}
	}
	return nil
}

// RotateSecretKey recifra todas as credenciais com a chave nova, numa
// transação só: ou tudo passa para a nova, ou nada muda.
//
// A caixa em uso lê; aberta com a chave antiga e a nova (ver
// secretbox.New), uma troca interrompida e rodada de novo encontra parte
// já na nova e segue. Depois de confirmada, o store passa a usar só a
// nova, e a antiga deixa de abrir o banco.
func (s *Store) RotateSecretKey(ctx context.Context, newKey secretbox.Key) error {
	reader := s.db.secrets
	if reader == nil {
		return fmt.Errorf("sqlstore: %w", secretbox.ErrKeyMissing)
	}
	if reader.SealsWith(newKey) {
		return errors.New("sqlstore: a chave nova é a que já está em uso; confira se o arquivo apontado é o certo")
	}
	writer, err := secretbox.New(newKey)
	if err != nil {
		return err
	}

	reseal := func(t *tx, table string, rows []storedConfig, fieldsOf func(string) secretbox.Fields) error {
		for _, r := range rows {
			fields := fieldsOf(r.typ)
			plain, err := reader.OpenConfig(json.RawMessage(r.config), fields, table, r.id)
			if err != nil {
				return fmt.Errorf("sqlstore: %w", err)
			}
			sealed, err := writer.SealConfig(plain, fields, table, r.id)
			if err != nil {
				return fmt.Errorf("sqlstore: recifrando %s %d: %w", table, r.id, err)
			}
			if bytes.Equal(sealed, json.RawMessage(r.config)) {
				continue
			}
			if _, err := t.ExecContext(ctx, `UPDATE `+table+` SET config = ? WHERE id = ?`, string(sealed), r.id); err != nil {
				return fmt.Errorf("sqlstore: recifrando %s %d: %w", table, r.id, err)
			}
		}
		return nil
	}

	err = s.db.inTx(ctx, func(t *tx) error {
		if err := verifyKey(ctx, t, reader); err != nil {
			return err
		}
		monitors, err := readConfigs(ctx, t, `SELECT id, type, config FROM monitor`, false)
		if err != nil {
			return err
		}
		if err := reseal(t, monitorTable, monitors, monitorFields); err != nil {
			return err
		}
		channels, err := readConfigs(ctx, t, `SELECT id, type, config FROM notification_channel`, false)
		if err != nil {
			return err
		}
		if err := reseal(t, channelTable, channels, notifier.SecretFields); err != nil {
			return err
		}
		check, err := writer.Seal(keyCheck, keyCheckSlot)
		if err != nil {
			return err
		}
		_, err = t.ExecContext(ctx, `UPDATE secret_key_check SET value = ? WHERE id = 1`, check)
		return err
	})
	if err != nil {
		return err
	}
	s.db.secrets = writer
	return nil
}

// ForgetSecrets apaga as credenciais cifradas e o sentinela da chave, sem
// precisar dela, e devolve quantos valores apagou.
//
// É a saída para a chave perdida. O valor cifrado sem a chave é ruído, e
// mantê-lo só impediria a partida; o resto do banco — monitores,
// histórico, incidentes, páginas, contas — fica intacto. A próxima subida
// encontra um banco sem nada cifrado, aceita uma chave nova, e cada
// credencial é digitada de novo pela interface. O token do push esquecido
// sai também da coluna da busca: o hash dele faria o /push aceitar um
// token que a interface não mostra mais.
func ForgetSecrets(driver, dsn string) (int, error) {
	s, err := connect(driver, dsn)
	if err != nil {
		return 0, err
	}
	defer func() { _ = s.Close() }()
	if err := s.migrate(); err != nil {
		return 0, err
	}

	ctx := context.Background()
	erased := 0
	forget := func(t *tx, table string, rows []storedConfig, fieldsOf func(string) secretbox.Fields) error {
		for _, r := range rows {
			config, n, err := secretbox.ForgetSealed(json.RawMessage(r.config), fieldsOf(r.typ))
			if err != nil {
				return fmt.Errorf("sqlstore: %s %d: %w", table, r.id, err)
			}
			if n == 0 {
				continue
			}
			erased += n
			push := r.pushToken
			if push.Valid {
				typ, _ := domain.ParseMonitorType(r.typ)
				if checker.PushTokenOf(domain.Monitor{Type: typ, Config: config}) == "" {
					push = sql.NullString{}
				}
			}
			q, args := `UPDATE `+table+` SET config = ? WHERE id = ?`, []any{string(config), r.id}
			if table == monitorTable {
				q, args = `UPDATE monitor SET config = ?, push_token = ? WHERE id = ?`, []any{string(config), push, r.id}
			}
			if _, err := t.ExecContext(ctx, q, args...); err != nil {
				return fmt.Errorf("sqlstore: apagando as credenciais de %s %d: %w", table, r.id, err)
			}
		}
		return nil
	}

	err = s.db.inTx(ctx, func(t *tx) error {
		monitors, err := readConfigs(ctx, t, `SELECT id, type, config, push_token FROM monitor`, true)
		if err != nil {
			return err
		}
		if err := forget(t, monitorTable, monitors, monitorFields); err != nil {
			return err
		}
		channels, err := readConfigs(ctx, t, `SELECT id, type, config FROM notification_channel`, false)
		if err != nil {
			return err
		}
		if err := forget(t, channelTable, channels, notifier.SecretFields); err != nil {
			return err
		}
		_, err = t.ExecContext(ctx, `DELETE FROM secret_key_check`)
		return err
	})
	return erased, err
}

// sealForWrite cifra a configuração a gravar no registro dado.
func (d *db) sealForWrite(config string, fields secretbox.Fields, table string, id int64) (string, error) {
	if fields.Empty() {
		return config, nil
	}
	if d.secrets == nil {
		return "", fmt.Errorf("sqlstore: %w", secretbox.ErrKeyMissing)
	}
	sealed, err := d.secrets.SealConfig(json.RawMessage(config), fields, table, id)
	if err != nil {
		return "", fmt.Errorf("sqlstore: %s %d: %w", table, id, err)
	}
	return string(sealed), nil
}

// openForRead decifra a configuração lida do registro dado.
func (d *db) openForRead(config string, fields secretbox.Fields, table string, id int64) (json.RawMessage, error) {
	if fields.Empty() {
		return json.RawMessage(config), nil
	}
	if d.secrets == nil {
		return nil, fmt.Errorf("sqlstore: %w", secretbox.ErrKeyMissing)
	}
	plain, err := d.secrets.OpenConfig(json.RawMessage(config), fields, table, id)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: %w", err)
	}
	return plain, nil
}

// sameJSON compara duas configurações pelo conteúdo.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}
