# Política de segurança

## Como relatar

Use o [relatório privado de
vulnerabilidade](https://github.com/Jbnado/upwatch/security/advisories/new)
do GitHub. Não abra issue pública para falha de segurança — a issue fica
visível antes de existir correção, e quem opera uma instalação exposta
fica sem defesa nesse intervalo.

Respondo em até 72 horas. Se não houver resposta nesse prazo, insista por
issue pública pedindo contato, sem detalhar a falha.

## O que é vulnerabilidade aqui

O UpWatch tem duas superfícies alcançáveis sem credencial, e são elas que
mais interessam:

**A página pública de estado** (`/status/...` e `/api/v1/public/...`).
Qualquer coisa que faça essa superfície revelar o que ela não deveria é
falha: endereço de alvo, causa detectada pela sonda, mensagem de
verificação, identificador de monitor, existência de página desligada,
ou informação de uma página de estado sobre alvos de outra.

**A exposição de métricas** (`/metrics`). Ela publica contagens, estados
e nomes de monitor. Endereço de alvo aparecendo ali é falha.

Também interessam: escalada de privilégio pela API, execução de código,
injeção de SQL, travessia de caminho, e qualquer caminho que permita
escrita sem sessão válida.

## O que não é

- Ausência de limite de requisições nas rotas autenticadas. O UpWatch
  pressupõe que quem tem credencial é de dentro.
- A página pública responder a quem tem o endereço. Esse é o propósito
  dela; use uma página desligada ou um slug não divulgado se o recorte
  não deve circular.
- `UPWATCH_SECURE_COOKIES` desligado por padrão. É deliberado, está
  documentado, e ligá-lo é a primeira linha da seção de produção do
  README.
- Relatório automatizado de varredura sem impacto demonstrado.

## Versões

O projeto ainda não tem versão estável. Correções entram na `main` e no
próximo lançamento; não há retroporte para versões anteriores.
