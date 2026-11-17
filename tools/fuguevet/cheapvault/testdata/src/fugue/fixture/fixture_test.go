package fixture

import (
	"testing"

	"fugue/internal/crypto"
	"fugue/internal/vault"
)

func TestFixture(t *testing.T) {
	s := &vault.Service{}
	_ = s.Setup("long master password") // want `vault.Service.Setup pays the production Argon2id cost`
	_ = crypto.NewKeyring("password")   // want `crypto.NewKeyring pays the production Argon2id cost`
}
