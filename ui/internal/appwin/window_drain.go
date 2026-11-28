package appwin

import (
	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/folderpicker"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/secrets"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The window's per-frame worker-result drains (F10.5a-F10.5e): every worker
// hands its finished operations back over one channel, and the window
// goroutine drains each into UI state at the top of the frame. The window's
// loop and layout live in window.go; both files are one package.

// drainChan takes every buffered result off one channel without blocking,
// the loop every worker drain shares.
func drainChan[T any](ch <-chan T) []T {
	out := make([]T, 0, 4)
drain:
	for {
		select {
		case res := <-ch:
			out = append(out, res)
		default:
			break drain
		}
	}
	return out
}

// drainMutResults takes every mutation result the mutator delivered this
// frame; the window goroutine folds them into the dialogs.
func drainMutResults(m *workspace.Mutator) []workspace.MutResult {
	return drainChan(m.Results())
}

func drainVaultResults(w *vault.Worker) []vault.Result {
	return drainChan(w.Results())
}

func drainInboxResults(w *inbox.Worker) []inbox.Result {
	return drainChan(w.Results())
}

func drainAccountsResults(w *accounts.Worker) []accounts.Result {
	return drainChan(w.Results())
}

func drainSettingsResults(w *settings.Worker) []settings.Result {
	return drainChan(w.Results())
}

func drainSecretsResults(w *secrets.Worker) []secrets.Result {
	return drainChan(w.Results())
}

func drainFileViewResults(w *fileViewWorker) []fileResult {
	return drainChan(w.Results())
}

// drainPickerResults takes every folder pick the picker delivered this frame;
// the window goroutine folds them into the open dialog's list.
func drainPickerResults(p *folderpicker.Picker) []folderpicker.Result {
	return drainChan(p.Results())
}
