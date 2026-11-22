package appwin

import (
	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The window's per-frame worker-result drains (F10.5a-F10.5e): every worker
// hands its finished operations back over one channel, and the window
// goroutine drains each into UI state at the top of the frame. The window's
// loop and layout live in window.go; both files are one package.

// drainMutResults takes every mutation result the mutator delivered this
// frame; the window goroutine folds them into the dialogs.
func drainMutResults(m *workspace.Mutator) []workspace.MutResult {
	muts := make([]workspace.MutResult, 0, 4)
drain:
	for {
		select {
		case res := <-m.Results():
			muts = append(muts, res)
		default:
			break drain
		}
	}
	return muts
}

func drainVaultResults(w *vault.Worker) []vault.Result {
	out := make([]vault.Result, 0, 4)
drain:
	for {
		select {
		case res := <-w.Results():
			out = append(out, res)
		default:
			break drain
		}
	}
	return out
}

func drainInboxResults(w *inbox.Worker) []inbox.Result {
	out := make([]inbox.Result, 0, 4)
drain:
	for {
		select {
		case res := <-w.Results():
			out = append(out, res)
		default:
			break drain
		}
	}
	return out
}

func drainAccountsResults(w *accounts.Worker) []accounts.Result {
	out := make([]accounts.Result, 0, 4)
drain:
	for {
		select {
		case res := <-w.Results():
			out = append(out, res)
		default:
			break drain
		}
	}
	return out
}

func drainSettingsResults(w *settings.Worker) []settings.Result {
	out := make([]settings.Result, 0, 4)
drain:
	for {
		select {
		case res := <-w.Results():
			out = append(out, res)
		default:
			break drain
		}
	}
	return out
}

func drainFileViewResults(w *fileViewWorker) []fileResult {
	out := make([]fileResult, 0, 4)
drain:
	for {
		select {
		case res := <-w.Results():
			out = append(out, res)
		default:
			break drain
		}
	}
	return out
}
