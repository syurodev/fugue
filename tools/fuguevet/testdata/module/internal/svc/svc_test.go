package svc

import (
	"testing"

	"fixture/internal/platform/clock"
	"fixture/internal/vault"
)

func TestSpeed(t *testing.T) {
	_ = clock.Real{}.Now()
}

func TestSetup(t *testing.T) {
	_ = (&vault.Service{}).Setup("long master password")
}
