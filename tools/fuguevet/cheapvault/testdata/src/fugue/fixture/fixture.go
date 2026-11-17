package fixture

import "fugue/internal/vault"

// Production code may set the vault up; the rule covers test files only.
func Boot(s *vault.Service) error { return s.Setup("long master password") }
