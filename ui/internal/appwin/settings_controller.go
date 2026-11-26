package appwin

import (
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/osappearance"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	labelCancel = "Cancel"
	// slotContentInset is the shared slot-content inset on every side; tests
	// that hover laid-out rows derive positions from it instead of pixels.
	slotContentInset = theme.SpaceLG
)

// openSettings opens the settings slot and arms its reads (F10.5d). The
// shell's Ctrl/Cmd+, shortcut and the sidebar row land here.
func (s *screens) openSettings() {
	s.grid.openCompanion(SlotRef{Kind: KindSettings})
	if s.settingsWorker == nil {
		return
	}
	if !s.settings.Prefs.Loaded {
		req := settings.Request{Kind: settings.OpLoadPrefs}
		s.settingsWorker.Do(&req)
	}
	s.ensureSettingsWorkspaceSelected()
}

// layoutSlotContent is the shared slot-content contract (§5.3): fills the
// slot surface and insets the body cleanly. The slot header owns the title,
// pane actions and close/arrange controls, so slot content never draws its
// own heading or "Back to …" button.
func layoutSlotContent(gtx *layout.Context, env *kit.Env, bodyFn func(*layout.Context) layout.Dimensions) layout.Dimensions {
	paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.UniformInset(slotContentInset).Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		return bodyFn(&local)
	})
}

func (s *screens) ensureSettingsWorkspaceSelected() {
	if s.settings.WorkspaceID == "" {
		if wsID, wsName := s.defaultSettingsWorkspace(); wsID != "" {
			s.settings.SelectWorkspace(wsID, wsName)
		}
	}
	if s.settings.WorkspaceID != "" && !s.settings.Loaded && !s.settings.Loading && s.settingsWorker != nil {
		s.settings.Loading = true
		req := settings.Request{Kind: settings.OpLoadWorkspace, WorkspaceID: s.settings.WorkspaceID}
		s.settingsWorker.Do(&req)
	}
}

func (s *screens) defaultSettingsWorkspace() (string, string) {
	if s.open != "" {
		if sess, ok := s.sidebar.Lookup(s.open); ok && sess.WorkspaceID != "" {
			if ws, wsOK := s.workspaces.Lookup(sess.WorkspaceID); wsOK {
				return ws.ID, ws.Name
			}
		}
	}
	list := s.workspaces.List()
	if len(list) == 0 {
		return "", ""
	}
	return list[0].ID, list[0].Name
}

func (s *screens) applyAppStateDelivery(u hostconn.Update) {
	if u.Snapshot != nil {
		snap, err := client.DecodeAppStateSnapshot(u.Snapshot.State)
		if err != nil {
			s.fail(err)
			return
		}
		s.fail(s.settings.Prefs.ApplySnapshot(&snap))
		s.applyLayoutSnapshot(&snap)
		return
	}
	if u.Event != nil {
		ev, err := client.DecodeAppStateEvent(u.Event.Type, u.Event.Payload)
		if err != nil {
			s.fail(err)
			return
		}
		if s.settings.Prefs.NeedsReloadForEvent(ev) && s.settingsWorker != nil {
			req := settings.Request{Kind: settings.OpLoadPrefs}
			s.settingsWorker.Do(&req)
		}
	}
}

func (s *screens) applySettingsResults(results []settings.Result) {
	for i := range results {
		res := &results[i]
		switch res.Kind {
		case settings.OpLoadPrefs:
			if res.Err != nil {
				s.settings.Prefs.Err = res.Err.Error()
				continue
			}
			s.fail(s.settings.Prefs.ApplyDocument(res.PrefsRevision, res.PrefsRaw))
		case settings.OpSavePrefs:
			s.settings.Prefs.Saving = false
			if res.Err != nil {
				s.settings.Prefs.Err = res.Err.Error()
				continue
			}
			s.settings.Prefs.Revision = res.PrefsRevision
			s.settings.Prefs.HigherSchema = false
			s.settings.Prefs.Err = ""
		case settings.OpLoadWorkspace:
			s.settings.Loading = false
			if res.Err != nil {
				s.settings.LoadErr = res.Err.Error()
				continue
			}
			s.settings.ApplyWorkspaceLoaded(res.Workspace, res.Effective)
			s.settingsView.syncFromModel(s.settings)
		case settings.OpSaveWorkspace:
			s.applyWorkspaceSaveResult(res)
		case settings.OpValidateWorkspace:
			s.settings.ApplyValidationResult(res.WorkspaceID, res.RawRevision, res.Issues, res.Err)
		case settings.OpConfirmPolicy, settings.OpRejectPolicy:
			s.applyPolicyDecisionResult(res)
		}
	}
}

func (s *screens) applyWorkspaceSaveResult(res *settings.Result) {
	s.settings.Saving = false
	if res.CASConflict {
		s.settings.ApplyCASConflictReload(res.Workspace, res.Effective, res.Err)
		return
	}
	if res.Err != nil {
		s.settings.SaveErr = res.Err.Error()
		s.settings.PolicyConfirmArmed = false
		s.settings.PendingRawPolicyDiff = nil
		return
	}
	s.settings.SaveErr = ""
	s.settings.ApplyWorkspaceLoaded(res.Workspace, res.Effective)
	s.settingsView.syncFromModel(s.settings)
}

func (s *screens) applyPolicyDecisionResult(res *settings.Result) {
	s.settings.PolicyDecisionBusy = false
	if res.CASConflict {
		s.settings.ApplyCASConflictReload(res.Workspace, res.Effective, res.Err)
		return
	}
	if res.Err != nil {
		s.settings.SaveErr = res.Err.Error()
		return
	}
	s.settings.SaveErr = ""
	s.settings.ApplyWorkspaceLoaded(res.Workspace, res.Effective)
	s.settingsView.syncFromModel(s.settings)
}

func (s *screens) updatePreferences(next settings.Preferences) {
	s.settings.Prefs.Current = next
	s.settings.Prefs.Err = ""
	if s.settingsWorker == nil {
		return
	}
	s.settings.Prefs.Saving = true
	req := settings.Request{
		Kind:          settings.OpSavePrefs,
		CommandID:     s.settings.Prefs.NextCommandID(),
		PrefsRevision: s.settings.Prefs.Revision,
		Prefs:         next,
	}
	s.settingsWorker.Do(&req)
}

func (s *screens) requestReloadSettings() {
	if s.settingsWorker == nil {
		return
	}
	prefsReq := settings.Request{Kind: settings.OpLoadPrefs}
	s.settingsWorker.Do(&prefsReq)
	if s.settings.WorkspaceID != "" {
		s.settings.Loading = true
		wsReq := settings.Request{Kind: settings.OpLoadWorkspace, WorkspaceID: s.settings.WorkspaceID}
		s.settingsWorker.Do(&wsReq)
	}
}

func (s *screens) submitWorkspaceSettings(confirmPolicy bool) {
	if s.settingsWorker == nil || !s.settings.Loaded || s.settings.Saving || s.settingsView.hasFieldValidationError() {
		return
	}
	patch, ok := s.settings.BuildPatch()
	if !ok {
		return
	}
	if len(s.settings.DirtyPolicyFields()) > 0 && !confirmPolicy {
		s.settings.PolicyConfirmArmed = true
		return
	}
	s.settings.Saving = true
	s.settings.SaveErr = ""
	s.settings.PolicyConfirmArmed = false
	req := settings.Request{
		Kind:         settings.OpSaveWorkspace,
		CommandID:    s.settings.Prefs.NextCommandID(),
		WorkspaceID:  s.settings.WorkspaceID,
		Patch:        patch,
		ExpectedHash: s.settings.ConfigHash,
	}
	s.settingsWorker.Do(&req)
}

func (s *screens) onRawTOMLChanged(text string) {
	changed, rev := s.settings.ApplyRawEdit(text)
	if !changed || s.settingsWorker == nil || !s.settings.Loaded {
		return
	}
	req := settings.Request{
		Kind:        settings.OpValidateWorkspace,
		WorkspaceID: s.settings.WorkspaceID,
		RawTOML:     []byte(text),
		RawRevision: rev,
	}
	s.settingsWorker.Do(&req)
}

func (s *screens) submitRawTOML(confirmPolicy bool) {
	if s.settingsWorker == nil || !s.settings.Loaded || s.settings.Saving || !s.settings.RawDirty() {
		return
	}
	if !confirmPolicy {
		if diffs, err := s.settings.RawPolicyDiff(); err == nil && len(diffs) > 0 {
			s.settings.PolicyConfirmArmed = true
			s.settings.PendingRawPolicyDiff = diffs
			return
		}
	}
	s.settings.Saving = true
	s.settings.SaveErr = ""
	s.settings.PolicyConfirmArmed = false
	s.settings.PendingRawPolicyDiff = nil
	req := settings.Request{
		Kind:         settings.OpSaveWorkspace,
		WorkspaceID:  s.settings.WorkspaceID,
		RawTOML:      []byte(s.settings.RawDraft),
		ExpectedHash: s.settings.ConfigHash,
	}
	s.settingsWorker.Do(&req)
}

func (s *screens) confirmPendingPolicy() {
	if s.settingsWorker == nil || !s.settings.Loaded || s.settings.PolicyDecisionBusy {
		return
	}
	s.settings.PolicyDecisionBusy = true
	s.settings.SaveErr = ""
	req := settings.Request{
		Kind:         settings.OpConfirmPolicy,
		WorkspaceID:  s.settings.WorkspaceID,
		ExpectedHash: s.settings.ConfigHash,
	}
	s.settingsWorker.Do(&req)
}

func (s *screens) rejectPendingPolicy() {
	if s.settingsWorker == nil || !s.settings.Loaded || s.settings.PolicyDecisionBusy {
		return
	}
	s.settings.PolicyDecisionBusy = true
	s.settings.SaveErr = ""
	req := settings.Request{
		Kind:         settings.OpRejectPolicy,
		WorkspaceID:  s.settings.WorkspaceID,
		ExpectedHash: s.settings.ConfigHash,
	}
	s.settingsWorker.Do(&req)
}

func (s *screens) reloadRawFromHost() {
	s.settings.DiscardRawDraft()
	s.settingsView.syncRawFromModel(s.settings)
}

func (s *screens) currentOSAppearance() osappearance.Appearance {
	if s.osAppearance == nil {
		return osappearance.Unknown
	}
	return s.osAppearance.Current()
}

func (s *screens) applyLivePreferences(env *kit.Env, light, dark *theme.Theme) {
	prefs := s.settings.Prefs.Current
	if prefs.UIScale > 0 {
		env.UIScale = settings.NormalizeUIScale(prefs.UIScale)
	}
	switch prefs.ThemeMode {
	case settings.ThemeDark:
		env.Th = dark
	case settings.ThemeLight:
		env.Th = light
	case settings.ThemeSystem:
		if s.currentOSAppearance() == osappearance.Dark {
			env.Th = dark
		} else {
			env.Th = light
		}
	default:
		if s.currentOSAppearance() == osappearance.Dark {
			env.Th = dark
		} else {
			env.Th = light
		}
	}
}
