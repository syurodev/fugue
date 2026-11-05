package coordinator

import (
	"testing"

	"github.com/syurodev/fugue/internal/kernel"
)

func TestCommandMetadataFields(t *testing.T) {
	t.Parallel()

	accept := new(kernel.AcceptRunCommand)
	accept.CommandID = "c1"
	accept.RunID = "r1"
	accept.Origin = "o1"

	cmdID, runID, origin, token := commandMetadata(accept)
	if cmdID != "c1" || runID != "r1" || origin != "o1" || token != 0 {
		t.Errorf("unexpected metadata: %q, %q, %q, %d", cmdID, runID, origin, token)
	}

	admit := new(kernel.AdmitRunCommand)
	admit.CommandID = "c2"
	admit.RunID = "r2"
	admit.Origin = "o2"
	admit.Token = 2

	cmdID, runID, origin, token = commandMetadata(admit)
	if cmdID != "c2" || runID != "r2" || origin != "o2" || token != 2 {
		t.Errorf("unexpected metadata for admit: %q, %q, %q, %d", cmdID, runID, origin, token)
	}
}

func TestCommandMetadataNil(t *testing.T) {
	t.Parallel()

	cmdID, runID, origin, token := commandMetadata(nil)
	if cmdID != "" || runID != "" || origin != "" || token != 0 {
		t.Errorf("expected empty metadata for nil, got %q, %q, %q, %d", cmdID, runID, origin, token)
	}
}

func TestSetCommandMetadata(t *testing.T) {
	t.Parallel()

	accept := new(kernel.AcceptRunCommand)
	setCommandMetadata(accept, "new-c", "new-r", "new-o", 99)
	cmdID, runID, origin, _ := commandMetadata(accept)
	if cmdID != "new-c" || runID != "new-r" || origin != "new-o" {
		t.Errorf("setCommandMetadata failed: got %q, %q, %q", cmdID, runID, origin)
	}

	admit := new(kernel.AdmitRunCommand)
	setCommandMetadata(admit, "new-c", "new-r", "new-o", 99)
	_, _, _, token := commandMetadata(admit)
	if token != 99 {
		t.Errorf("setCommandMetadata token failed: got %d", token)
	}

	setCommandMetadata(nil, "c", "r", "o", 1)
}
