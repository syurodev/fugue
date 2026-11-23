package shottest

import "os"

func Golden(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func Keep(path string, png []byte) error {
	return os.WriteFile(path, png, 0o600)
}
