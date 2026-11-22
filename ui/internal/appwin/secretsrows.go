package appwin

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	secretActionReplace = "replace-secret"
	secretActionDelete  = "delete-secret"
)

type secretScopeWidgets struct {
	card *kit.CardState
	tag  *kit.TagState
}

func newSecretScopeWidgets() secretScopeWidgets {
	return secretScopeWidgets{card: kit.NewCardState(), tag: kit.NewTagState()}
}

func (v *secretsView) layoutGroups(gtx *layout.Context, env *kit.Env, sc *screens, items []client.SecretMeta, wsName string) layout.Dimensions {
	var global, workspace []client.SecretMeta
	for i := range items {
		if items[i].ScopeKind == client.SecretScopeGlobal {
			global = append(global, items[i])
		} else {
			workspace = append(workspace, items[i])
		}
	}
	var groups []layout.FlexChild
	if len(global) > 0 {
		groups = append(groups, v.scopeGroup(env, sc, v.global, global, labelGlobal, kit.IconGlobe))
	}
	if len(workspace) > 0 {
		if len(groups) > 0 {
			groups = append(groups, spacerH(theme.SpaceLG))
		}
		groups = append(groups, v.scopeGroup(env, sc, v.workspace, workspace, workspaceScopeLabel(wsName), kit.IconFolder))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, groups...)
}

func (v *secretsView) scopeGroup(env *kit.Env, sc *screens, widgets secretScopeWidgets, items []client.SecretMeta, scope string, icon kit.IconName) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
			rigidTag(env, sc.fail, widgets.tag, kit.TagProps{Text: scope, Icon: icon}),
			spacerH(theme.SpaceXS),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				dims, _, err := widgets.card.Layout(env, gtx, kit.CardProps{Padding: theme.SpaceSM}, func(gtx layout.Context) layout.Dimensions {
					return v.layoutRows(&gtx, env, sc, items)
				})
				sc.fail(err)
				return dims
			}),
		)
	})
}

func (v *secretsView) layoutRows(gtx *layout.Context, env *kit.Env, sc *screens, items []client.SecretMeta) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(items)*4)
	for i := range items {
		if i > 0 {
			children = append(children, spacerH(theme.SpaceXS), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Divider(env, gtx, kit.DividerProps{})
			}), spacerH(theme.SpaceXS))
		}
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutSecretRow(gtx, env, sc, &items[i])
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func (v *secretsView) layoutSecretRow(gtx *layout.Context, env *kit.Env, sc *screens, meta *client.SecretMeta) layout.Dimensions {
	w := v.widgetsFor(meta.SecretID)
	props := kit.IconButtonProps{
		Icon: kit.IconEllipsis, Label: "Actions for " + meta.Name,
		Disabled: sc.vault.Locked() || sc.secretsWorker == nil,
	}
	var button image.Point
	dims := layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = 0
			dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				rigidTag(env, sc.fail, w.name, kit.TagProps{Text: meta.Name, Code: true}),
				spacerH(theme.SpaceXXS),
				rigidWidget(func(gtx *layout.Context) layout.Dimensions {
					return layoutSecretRowDetails(gtx, env, meta)
				}),
			)
			dims.Size.X = gtx.Constraints.Max.X
			return dims
		}),
		spacerW(theme.SpaceXS),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			clicked, err := w.actions.Layout(env, gtx, &props)
			sc.fail(err)
			if clicked {
				v.menuFor = meta.SecretID
				v.menu.SetOpener(w.actions.Tag())
				v.menu.OpenKeyboard()
			}
			dims := w.actions.Measure(env, gtx, &props)
			button = dims.Size
			return dims
		}),
	)
	if v.menuFor == meta.SecretID {
		pos := image.Pt(dims.Size.X-button.X, (dims.Size.Y-button.Y)/2)
		v.layoutRowMenu(gtx, env, sc, meta, image.Rectangle{Min: pos, Max: pos.Add(button)}, props.Disabled)
	}
	return dims
}

func (v *secretsView) layoutRowMenu(gtx *layout.Context, env *kit.Env, sc *screens, meta *client.SecretMeta, anchor image.Rectangle, disabled bool) {
	if disabled {
		v.menu.Close()
	}
	res, err := v.menu.Layout(env, gtx, kit.MenuProps{
		Anchor:   anchor,
		AlignEnd: true,
		Items: []kit.MenuItem{
			{ID: secretActionReplace, Label: "Replace value", Disabled: disabled},
			{ID: secretActionDelete, Label: labelDelete, Disabled: disabled},
		},
	})
	sc.fail(err)
	switch res.Chosen {
	case secretActionReplace:
		v.replaceDlg.open(meta)
	case secretActionDelete:
		v.deleteDlg.open(meta)
	}
}

func layoutSecretRowDetails(gtx *layout.Context, env *kit.Env, meta *client.SecretMeta) layout.Dimensions {
	muted := env.Th.Chrome().FgMuted
	var children []layout.FlexChild
	if meta.Description != "" {
		children = append(children, rigidParagraph(env, theme.TypeSmall(), muted, meta.Description))
	}
	if !meta.UpdatedAt.IsZero() {
		if len(children) > 0 {
			children = append(children, spacerH(theme.SpaceXXS))
		}
		updated := "Updated " + meta.UpdatedAt.Local().Format("2006-01-02 15:04")
		children = append(children, rigidLabel(env, theme.TypeCaption(), muted, updated))
	}
	if len(children) == 0 {
		return layout.Dimensions{}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}
