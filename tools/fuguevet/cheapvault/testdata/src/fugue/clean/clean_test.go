package clean

import (
	"testing"

	"fugue/internal/vault"
)

func TestNoSetup(t *testing.T) {
	var s *vault.Service
	_ = s
}
