package logs

import (
	"errors"
	"fmt"
	"log/slog"

	"fugue/internal/platform/redact"
)

type config struct {
	Password string
	Host     string
}

func Bad(token string, s redact.Secret, dek []byte, cfg config, err error) error {
	slog.Info("login", "token", token)       // want `token looks like a secret`
	slog.Info("login", "secret", s)          // want `redact.Secret value s`
	fmt.Println(dek)                         // want `key material dek`
	fmt.Println(string(dek))                 // want `key material dek`
	_ = fmt.Sprintf("p=%s", cfg.Password)    // want `Password looks like a secret`
	_ = errors.New(token)                    // want `token looks like a secret`
	_ = errors.New("bad " + token)           // want `token looks like a secret`
	return fmt.Errorf("fail: %w %v", err, s) // want `redact.Secret value s`
}

func Good(token string, s redact.Secret, dek []byte, cfg config, err error, inputTokens int) error {
	slog.Info("login", "token", redact.String(token))
	slog.Info("login", "secret", s.Redacted())
	slog.Info("usage", "tokens", inputTokens)
	fmt.Println(redact.Bytes(dek))
	fmt.Println("host", cfg.Host)
	if errors.Is(err, errors.ErrUnsupported) {
		return errors.New("unsupported")
	}
	return fmt.Errorf("fail: %w", err)
}
