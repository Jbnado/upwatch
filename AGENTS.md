# Orientações para agentes

## Fonte de contexto

- Ao receber um handoff ou instruções do responsável pelo projeto, leia o material indicado antes de atuar e siga a ordem, os limites de escopo e as regras de divulgação descritos nele.
- O relatório completo da auditoria fica fora deste repositório. Não o copie para cá nem publique achados ainda não corrigidos em issues ou pull requests.

## Projeto

- UpWatch é um monitor de uptime auto-hospedado. O backend usa Go e chi; o frontend usa React, TypeScript, Vite, Vitest e pnpm.
- O store suporta SQLite e PostgreSQL. Todo método novo de store exige um caso correspondente na suíte compartilhada em `internal/store/storetest`.
- Identificadores de código ficam em inglês. Código, comentários, mensagens de erro, commits e pull requests ficam em português do Brasil.

## Regras de implementação

- Use TDD: escreva um teste que falhe antes da correção, confirme a falha esperada e só então altere o código de produção.
- Para cada teste novo, prove que ele detecta a regressão removendo temporariamente a correção, executando o teste e restaurando a correção.
- Rode a suíte Go com `-race` e as verificações do frontend quando a mudança atingir essas áreas. Registre falhas ambientais conhecidas em vez de atribuí-las ao código.
- Pesquise como projetos comparáveis resolvem problemas de segurança ou comportamento antes de desenhar recursos novos.
- Prefira configuração a limites arbitrários que contrariem a promessa do UpWatch de não restringir quantos monitores o usuário pode cadastrar.
- Não altere a rota de login nem os arquivos `internal/auth/auth.go`, `internal/auth/attempts*.go` ou `internal/domain/auth.go` enquanto o trabalho descrito no handoff disser que estão reservados para outra mudança.

## Git e revisão

- Fluxo padrão: branch de mudança, pull request com squash para `next`; pull request de release de `next` para `main` com merge commit.
- Não faça merge de pull request de release sem instrução explícita do responsável pelo projeto.
- Assuntos de commit usam `tipo: descrição em minúsculas`, sem ponto final. O corpo explica o defeito e registra decisões e custos relevantes.
- Não inclua atribuição de ferramenta em commits ou pull requests: sem `Co-Authored-By`, menção a geração por IA ou rodapé equivalente.
- Descrições de pull request devem explicar compatibilidade para quem atualiza e registrar decisões relevantes sobre o que ficou de fora e por quê.
