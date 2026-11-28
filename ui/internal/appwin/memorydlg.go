package appwin

import (
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/memory"
)

type memoryInvalidateDialog struct {
	dialog         *kit.DialogState
	cancel, submit *kit.ButtonState
	reason         *kit.TextFieldState
	supersededBy   *kit.TextFieldState
	wsID           string
	entryID        string
	label          string
	revision       uint64
	busy           bool
	lastErr        string
}

func newMemoryInvalidateDialog() *memoryInvalidateDialog {
	return &memoryInvalidateDialog{
		dialog:       kit.NewDialogState(),
		cancel:       kit.NewButtonState(),
		submit:       kit.NewButtonState(),
		reason:       kit.NewTextFieldState(),
		supersededBy: kit.NewTextFieldState(),
	}
}

func (d *memoryInvalidateDialog) open(wsID string, entry *client.MemoryEntry) {
	if entry == nil {
		return
	}
	d.wsID = wsID
	d.entryID = entry.ID
	d.label = entry.Label
	d.revision = entry.Revision
	d.reason.SetText("")
	d.supersededBy.SetText("")
	d.busy = false
	d.lastErr = ""
	d.dialog.Open()
}

func (d *memoryInvalidateDialog) applied(res *memory.Result, v *memoryView) {
	if !d.dialog.IsOpen() {
		return
	}
	d.busy = false
	if res.Err != nil {
		d.lastErr = hostErrorMessage(res.Err)
		return
	}
	d.lastErr = ""
	d.dialog.Close()
	v.mode = memModeDetail
}

func (d *memoryInvalidateDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens, v *memoryView) {
	if !d.dialog.IsOpen() {
		return
	}
	reasonTrim := strings.TrimSpace(d.reason.Text())
	canSubmit := reasonTrim != "" && !sc.vault.Locked() && sc.memoryWorker != nil

	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:         kit.DialogForm,
		Title:           "Invalidate " + d.label,
		Description:     "Invalidated entries stay in revision history but are excluded from default search and overview.",
		Cancel:          d.cancel,
		Primary:         d.submit,
		PrimaryLabel:    "Invalidate",
		PrimaryVariant:  kit.Danger,
		PrimaryDisabled: !canSubmit,
		Dirty:           reasonTrim != "" || strings.TrimSpace(d.supersededBy.Text()) != "",
		Busy:            d.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			body := newSecretDlgBody(env, gtx, bands, sc.fail)
			body.textField(d.reason, &kit.TextFieldProps{
				Label:       "Reason (required)",
				Placeholder: "Why is this entry no longer valid?",
				Disabled:    d.busy,
			}, canSubmit, d.dialog)
			body.textField(d.supersededBy, &kit.TextFieldProps{
				Label:       "Superseded by (optional entry ID)",
				Placeholder: "Optional replacement entry ID",
				Disabled:    d.busy,
			}, canSubmit, d.dialog)
			if d.lastErr != "" {
				body.label(d.lastErr, env.Th.Status().Danger)
			}
			return body.size()
		},
	})
	sc.fail(err)
	if res.Submitted && canSubmit && !d.busy {
		d.busy = true
		d.lastErr = ""
		v.detailBusy = true
		sc.memoryWorker.Do(&memory.Request{
			Kind:        memory.OpWriteEntry,
			WorkspaceID: d.wsID,
			Write: client.WriteMemoryInput{
				WorkspaceID:      d.wsID,
				Op:               client.MemoryOpInvalidate,
				ID:               d.entryID,
				ExpectedRevision: d.revision,
				Reason:           reasonTrim,
				SupersededBy:     strings.TrimSpace(d.supersededBy.Text()),
			},
		})
	}
}
