package testdir

import (
	"errors"
	"syscall"
)

// errSharingViolation não é exportado pelo pacote syscall.
const errSharingViolation syscall.Errno = 32

// retryable diz se a falha é do arquivo ainda preso por outro processo.
// ERROR_DIR_NOT_EMPTY é o caso que o testing não cobre; os outros dois
// são os que ele já repete.
func retryable(err error) bool {
	return errors.Is(err, syscall.ERROR_DIR_NOT_EMPTY) ||
		errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, errSharingViolation)
}
