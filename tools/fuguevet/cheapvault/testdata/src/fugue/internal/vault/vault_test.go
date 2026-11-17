package vault

import "testing"

// The vault package tests the production cost itself.
func TestSetup(t *testing.T) {
	_ = (&Service{}).Setup("long master password")
}
