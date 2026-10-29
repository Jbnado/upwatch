-- +goose Up

-- Marca do monitor cujo destino foi escolhido por conta com acesso
-- restrito a grupos, com UPWATCH_RESTRICT_PRIVATE_TARGETS ligada.
--
-- O cadastro recusa alvo na rede interna do servidor para essas contas,
-- mas resolve o nome naquela hora. Um nome que passa a apontar para
-- dentro depois (DNS rebinding) e um redirecionamento HTTP para dentro só
-- são barrados se o checker conferir o endereço ao conectar, e o checker
-- não sabe quem salvou o monitor: a marca é como ele fica sabendo.
--
-- O monitor que já existe nasce sem a marca. Não há registro de quem o
-- salvou, e marcar por palpite (todo monitor de grupo, por exemplo)
-- derrubaria os que um administrador de escopo total apontou para dentro
-- de propósito. Ele recebe a marca na próxima vez que uma conta restrita
-- mudar o destino.
ALTER TABLE monitor ADD COLUMN restrict_internal INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE monitor DROP COLUMN restrict_internal;
