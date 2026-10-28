package probe

func isWindows() bool { return true }

// checkPrivate não se aplica no Windows, onde o acesso é por ACL e não
// pelos bits de permissão. A sonda roda em Linux; aqui ela só compila
// para desenvolvimento.
func checkPrivate(string) error { return nil }
