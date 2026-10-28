package domain

import "time"

// Transition é uma mudança confirmada de estado, como fica no histórico.
//
// StateChange é o evento que o motor comunica a quem avisa; Transition é
// o registro que sobra dele, com o monitor e a causa. Sem este registro a
// tela de detalhe só conseguia deduzir mudanças das batidas da janela, e
// dizia "Nenhuma mudança no período" com o monitor fora do ar desde antes
// da janela começar.
type Transition struct {
	ID        int64     `json:"id"`
	MonitorID int64     `json:"monitor_id"`
	From      Status    `json:"from"`
	To        Status    `json:"to"`
	At        time.Time `json:"at"`
	// Cause é a mensagem da batida que confirmou a mudança.
	Cause string `json:"cause,omitempty"`
}
