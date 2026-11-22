package appwin

import (
	"fmt"
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/secrets"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	secretFilterAll = iota
	secretFilterGlobal
	secretFilterWorkspace
)

const vaultLockedSecretsMsg = "Vault is locked — unlock to add, replace, or delete secrets."

type secretRowWidgets struct {
	card       *kit.CardState
	scopeTag   *kit.TagState
	replaceBtn *kit.ButtonState
	deleteBtn  *kit.ButtonState
}

// secretsView owns the standalone Secret Store screen (F10.15b, desktop-app v0.12 §5.6):
// write-only metadata list grouped/filtered by Global and current Workspace, with
// add, replace-value, and delete-confirm flows, and locked-vault gating (INV-09).
type secretsView struct {
	filterScope int
	lastErr     string

	scopeFilter *kit.SegmentedState
	addBtn      *kit.ButtonState
	backBtn     *kit.ButtonState
	countTag    *kit.TagState
	lockBanner  *kit.BannerState
	errBanner   *kit.BannerState
	empty       *kit.EmptyStateState
	scroll      *kit.ScrollAreaState
	rows        map[string]*secretRowWidgets

	addDlg     *secretAddDialog
	replaceDlg *secretReplaceDialog
	deleteDlg  *secretDeleteDialog
}

func newSecretsView() *secretsView {
	return &secretsView{
		scopeFilter: kit.NewSegmentedState(),
		addBtn:      kit.NewButtonState(),
		backBtn:     kit.NewButtonState(),
		countTag:    kit.NewTagState(),
		lockBanner:  kit.NewBannerState(),
		errBanner:   kit.NewBannerState(),
		empty:       kit.NewEmptyStateState(),
		scroll:      kit.NewScrollAreaState(),
		rows:        map[string]*secretRowWidgets{},
		addDlg:      newSecretAddDialog(),
		replaceDlg:  newSecretReplaceDialog(),
		deleteDlg:   newSecretDeleteDialog(),
	}
}

func (v *secretsView) widgetsFor(secretID string) *secretRowWidgets {
	if w, ok := v.rows[secretID]; ok {
		return w
	}
	w := &secretRowWidgets{
		card:       kit.NewCardState(),
		scopeTag:   kit.NewTagState(),
		replaceBtn: kit.NewButtonState(),
		deleteBtn:  kit.NewButtonState(),
	}
	v.rows[secretID] = w
	return w
}

func (v *secretsView) clearSecretInputs() {
	v.addDlg.clearSecret()
	v.replaceDlg.clearSecret()
}

func (s *screens) currentWorkspace() (id, name string) {
	if s.open != "" {
		if sess, ok := s.sidebar.Lookup(s.open); ok && sess.WorkspaceID != "" {
			if ws, wsOK := s.workspaces.Lookup(sess.WorkspaceID); wsOK {
				return ws.ID, ws.Name
			}
			return sess.WorkspaceID, sess.WorkspaceID
		}
	}
	if list := s.workspaces.List(); len(list) > 0 {
		return list[0].ID, list[0].Name
	}
	return "", ""
}

func (s *screens) applySecretsDelivery(u hostconn.Update) {
	if u.Snapshot != nil {
		s.fail(s.secrets.ApplySnapshot(u.Snapshot.State))
		return
	}
	if u.Event != nil {
		s.fail(s.secrets.ApplyEvent(u.Event.Type, u.Event.Payload))
		if u.Event.Type == client.EventSecretPut && s.secretsWorker != nil {
			req := secrets.Request{Kind: secrets.OpListSecrets}
			s.secretsWorker.Do(&req)
		}
	}
}

func (s *screens) applySecretsResults(results []secrets.Result) {
	for i := range results {
		res := &results[i]
		switch res.Kind {
		case secrets.OpListSecrets:
			if res.Err != nil {
				s.secretsView.lastErr = res.Err.Error()
				continue
			}
			s.secretsView.lastErr = ""
			s.secrets.ApplyList(res.Secrets)
		case secrets.OpPutSecret:
			s.secretsView.addDlg.applied(res, s.secrets)
			s.secretsView.replaceDlg.applied(res, s.secrets)
		case secrets.OpDeleteSecret:
			s.secretsView.deleteDlg.applied(res, s.secrets)
		}
	}
}

func (v *secretsView) layout(gtx *layout.Context, env *kit.Env, sc *screens) {
	layoutRightPane(gtx, env,
		func(gtx *layout.Context) layout.Dimensions { return v.layoutHeader(gtx, env, sc) },
		func(gtx *layout.Context) layout.Dimensions { return v.layoutBody(gtx, env, sc) },
	)
}

func (v *secretsView) layoutHeader(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	wsID, _ := sc.currentWorkspace()
	items := v.filteredItems(sc.secrets, wsID)
	locked := sc.vault.Locked()
	titleTok, titleCol := accountPaneTitleStyle(env.Th)
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		rigidLabel(env, titleTok, titleCol, "Secrets"),
		spacerW(theme.SpaceSM),
		rigidTag(env, sc.fail, v.countTag, kit.TagProps{
			Text:    fmt.Sprintf("%d", len(items)),
			Variant: kit.TagNeutral,
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
		}),
		rigidInboxButton(env, sc.fail, v.addBtn, kit.ButtonProps{
			Text:     "Add secret",
			Variant:  kit.Primary,
			Size:     kit.SizeSM,
			Disabled: locked || sc.secretsWorker == nil,
		}, func() {
			v.addDlg.open(wsID)
		}),
		spacerW(theme.SpaceSM),
		rigidInboxButton(env, sc.fail, v.backBtn, kit.ButtonProps{
			Text: labelBackToChat, Variant: kit.Ghost, Size: kit.SizeSM,
		}, func() { sc.grid.closeSlotOf(KindSecrets) }),
	)
}

func (v *secretsView) filteredItems(m *secrets.Model, wsID string) []client.SecretMeta {
	switch v.filterScope {
	case secretFilterGlobal:
		return m.GlobalSecrets()
	case secretFilterWorkspace:
		return m.WorkspaceSecrets(wsID)
	default:
		return m.AllForScope(wsID)
	}
}

func (v *secretsView) layoutBody(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	wsID, wsName := sc.currentWorkspace()
	locked := sc.vault.Locked()
	items := v.filteredItems(sc.secrets, wsID)

	children := make([]layout.FlexChild, 0, len(items)*2+6)
	if locked {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			d, _, err := v.lockBanner.Layout(env, gtx, kit.BannerProps{
				Kind:    kit.BannerWarning,
				Title:   "Vault is locked",
				Message: vaultLockedSecretsMsg,
				Visible: true,
			})
			sc.fail(err)
			return d
		}), spacerH(theme.SpaceSM))
	}
	if v.lastErr != "" {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			d, _, err := v.errBanner.Layout(env, gtx, kit.BannerProps{
				Kind:    kit.BannerDanger,
				Title:   "Could not load secrets",
				Message: v.lastErr,
				Visible: true,
			})
			sc.fail(err)
			return d
		}), spacerH(theme.SpaceSM))
	}
	children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return v.layoutFilterBar(gtx, env, sc, wsID, wsName)
	}), spacerH(theme.SpaceMD))

	if len(items) == 0 {
		children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return v.layoutEmpty(&local, env, sc, locked, wsID)
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	}

	listChildren := make([]layout.FlexChild, 0, len(items)*2)
	for i := range items {
		meta := items[i]
		listChildren = append(listChildren, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutSecretRow(gtx, env, sc, &meta, wsName, locked)
		}))
		if i < len(items)-1 {
			listChildren = append(listChildren, spacerH(theme.SpaceSM))
		}
	}
	children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		local := gtx
		return v.scroll.Layout(env, &local, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, listChildren...)
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func (v *secretsView) layoutFilterBar(gtx *layout.Context, env *kit.Env, sc *screens, wsID, wsName string) layout.Dimensions {
	wsLabel := "Workspace"
	if wsName != "" {
		wsLabel = wsName
	}
	changed, next, err := v.scopeFilter.Layout(env, gtx, kit.SegmentedProps{
		Options: []kit.SegmentedOption{
			{ID: "all", Label: "All"},
			{ID: client.SecretScopeGlobal, Label: "Global"},
			{ID: client.SecretScopeWorkspace, Label: wsLabel, Disabled: wsID == ""},
		},
		Selected: v.filterScope,
	})
	sc.fail(err)
	if changed {
		v.filterScope = next
	}
	return v.scopeFilter.Measure(env, gtx, kit.SegmentedProps{
		Options: []kit.SegmentedOption{
			{ID: "all", Label: "All"},
			{ID: client.SecretScopeGlobal, Label: "Global"},
			{ID: client.SecretScopeWorkspace, Label: wsLabel, Disabled: wsID == ""},
		},
		Selected: v.filterScope,
	})
}

func (v *secretsView) layoutEmpty(gtx *layout.Context, env *kit.Env, sc *screens, locked bool, wsID string) layout.Dimensions {
	primary := ""
	if !locked && sc.secretsWorker != nil {
		primary = "Add secret"
	}
	dims, ev, err := v.empty.Layout(env, gtx, &kit.EmptyStateProps{
		Icon:          kit.IconEye,
		Title:         "No secrets stored",
		Description:   "Add write-only secrets for agent secret questions and module environments.",
		PrimaryAction: primary,
	})
	sc.fail(err)
	if ev.PrimaryClicked && !locked {
		v.addDlg.open(wsID)
	}
	return dims
}

func (v *secretsView) layoutSecretRow(
	gtx *layout.Context,
	env *kit.Env,
	sc *screens,
	meta *client.SecretMeta,
	wsName string,
	locked bool,
) layout.Dimensions {
	w := v.widgetsFor(meta.SecretID)
	c := env.Th.Chrome()
	scopeText := "Global"
	if meta.ScopeKind == client.SecretScopeWorkspace {
		if wsName != "" {
			scopeText = wsName
		} else {
			scopeText = "Workspace"
		}
	}
	updatedText := ""
	if !meta.UpdatedAt.IsZero() {
		updatedText = "Updated " + meta.UpdatedAt.Local().Format("2006-01-02 15:04")
	}
	disabled := locked || sc.secretsWorker == nil
	dims, _, err := w.card.Layout(env, gtx, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						local := gtx
						return kit.Label(env, &local, theme.TypeCodeSmall(), c.Fg, meta.Name)
					}),
					spacerW(theme.SpaceSM),
					rigidTag(env, sc.fail, w.scopeTag, kit.TagProps{Text: scopeText, Variant: kit.TagNeutral}),
				)
			}),
			spacerH(theme.SpaceXS),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						var col []layout.FlexChild
						if meta.Description != "" {
							col = append(col, rigidParagraph(env, theme.TypeSmall(), c.FgMuted, meta.Description))
						}
						if updatedText != "" {
							if len(col) > 0 {
								col = append(col, spacerH(theme.SpaceXXS))
							}
							col = append(col, rigidLabel(env, theme.TypeCaption(), c.FgMuted, updatedText))
						}
						if len(col) == 0 {
							return layout.Dimensions{}
						}
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx, col...)
					}),
					spacerW(theme.SpaceSM),
					rigidInboxButton(env, sc.fail, w.replaceBtn, kit.ButtonProps{
						Text:     "Replace",
						Variant:  kit.Secondary,
						Size:     kit.SizeSM,
						Disabled: disabled,
					}, func() {
						v.replaceDlg.open(meta)
					}),
					spacerW(theme.SpaceXS),
					rigidInboxButton(env, sc.fail, w.deleteBtn, kit.ButtonProps{
						Text:     "Delete",
						Variant:  kit.Danger,
						Size:     kit.SizeSM,
						Disabled: disabled,
					}, func() {
						v.deleteDlg.open(meta)
					}),
				)
			}),
		)
	})
	sc.fail(err)
	return dims
}

func (v *secretsView) layoutDialogs(env *kit.Env, gtx *layout.Context, sc *screens) {
	v.addDlg.layout(env, gtx, sc)
	v.replaceDlg.layout(env, gtx, sc)
	v.deleteDlg.layout(env, gtx, sc)
}
