-- +goose Up

-- Papel da conta.
--
-- Duas opções: quem administra e quem só olha. O padrão é 'admin' porque
-- toda conta que já existe quando esta migration roda foi criada quando
-- não havia distinção — rebaixá-las trancaria a instalação para fora.
ALTER TABLE app_user ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';

-- Consultar quantos administradores restam é o que impede remover ou
-- rebaixar o último. Sem índice, isso seria varredura a cada operação de
-- conta — barato hoje, e ainda assim é a consulta que protege o acesso.
CREATE INDEX idx_app_user_role ON app_user (role);

-- +goose Down
DROP INDEX idx_app_user_role;
ALTER TABLE app_user DROP COLUMN role;
