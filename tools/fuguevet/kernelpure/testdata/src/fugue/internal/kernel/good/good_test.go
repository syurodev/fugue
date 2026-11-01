package good

import (
	"os"
	"testing"
)

func TestEnvironmentIsFineInTests(t *testing.T) { _ = os.Args }
