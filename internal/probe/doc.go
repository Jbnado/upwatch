// Package probe é a sonda para redes privadas.
//
// Ela roda dentro da rede que o UpWatch central não alcança, executa as
// verificações com os mesmos checkers do central e reporta cada resultado
// por push. O fluxo é de mão única: a sonda só abre conexão de saída,
// manda estado, causa curta e latência, e nunca recebe comando nem
// configuração do central. Comprometer o central não dá controle sobre as
// máquinas onde as sondas rodam.
package probe
