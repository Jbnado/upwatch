//go:build !windows

package testdir

// retryable é sempre falso fora do Windows: lá apagar arquivo aberto é
// permitido, e uma falha na remoção é defeito de verdade.
func retryable(error) bool { return false }
