package appwin

import (
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type accountCardWidgets struct {
	card        *kit.CardState
	stateTag    *kit.TagState
	loginBtn    *kit.ButtonState
	cancelBtn   *kit.ButtonState
	dismissBtn  *kit.ButtonState
	loginTag    *kit.TagState
	progress    map[string]*kit.ProgressState
	rowHover    map[string]*windowRowHover
	linkButtons map[string]*kit.ButtonState
}

// accountsView owns the vendor accounts, F9.10 quota windows, and CLI login
// flow pane (F10.5c, F10.22, desktop-app v0.8 §5.5). Until F10.20's slot grid
// lands, it occupies the right pane when selected from the sidebar or toggled
// with Ctrl/Cmd+Shift+A.
type accountsView struct {
	refreshing bool
	listErr    string
	openErr    string
	openURL    func(string) error

	empty     *kit.EmptyStateState
	errBanner *kit.BannerState
	scroll    *kit.ScrollAreaState
	cards     map[string]*accountCardWidgets
}

func newAccountsView() *accountsView {
	return &accountsView{
		openURL:   openHTTPSURL,
		empty:     kit.NewEmptyStateState(),
		errBanner: kit.NewBannerState(),
		scroll:    kit.NewScrollAreaState(),
		cards:     map[string]*accountCardWidgets{},
	}
}

func (v *accountsView) widgetsFor(vendor string) *accountCardWidgets {
	if w, ok := v.cards[vendor]; ok {
		return w
	}
	w := &accountCardWidgets{
		card:        kit.NewCardState(),
		stateTag:    kit.NewTagState(),
		loginBtn:    kit.NewButtonState(),
		cancelBtn:   kit.NewButtonState(),
		dismissBtn:  kit.NewButtonState(),
		loginTag:    kit.NewTagState(),
		progress:    map[string]*kit.ProgressState{},
		rowHover:    map[string]*windowRowHover{},
		linkButtons: map[string]*kit.ButtonState{},
	}
	v.cards[vendor] = w
	return w
}

func (s *screens) applyAccountTopicDelivery(
	u hostconn.Update,
	applySnap func(string, []byte) error,
	applyEv func(string, []byte) error,
) {
	prevInc := s.accounts.Incarnation()
	if u.Snapshot != nil {
		s.fail(applySnap(u.Snapshot.Incarnation, u.Snapshot.State))
		if prevInc != "" && u.Snapshot.Incarnation != "" && prevInc != u.Snapshot.Incarnation && s.accountsWorker != nil {
			req := accounts.Request{Kind: accounts.OpListAccounts}
			s.accountsWorker.Do(&req)
		}
		return
	}
	if u.Event != nil {
		s.fail(applyEv(u.Event.Type, u.Event.Payload))
	}
}

func (s *screens) applyQuotaDelivery(u hostconn.Update) {
	s.applyAccountTopicDelivery(u, s.accounts.ApplyQuotaSnapshot, s.accounts.ApplyQuotaEvent)
}

func (s *screens) applyLoginDelivery(u hostconn.Update) {
	s.applyAccountTopicDelivery(u, s.accounts.ApplyLoginSnapshot, s.accounts.ApplyLoginEvent)
}

func (s *screens) applyAccountsResults(results []accounts.Result) {
	for i := range results {
		res := &results[i]
		switch res.Kind {
		case accounts.OpListAccounts:
			s.accountsView.refreshing = false
			if res.Err != nil {
				s.accountsView.listErr = res.Err.Error()
				continue
			}
			s.accountsView.listErr = ""
			s.accounts.ApplyListAccounts(res.Incarnation, res.Accounts)
		case accounts.OpStartLogin:
			s.accounts.ApplyStartLoginResult(res)
		case accounts.OpCancelLogin:
			s.accounts.ApplyCancelLoginResult(res)
		}
	}
}

func (s *screens) requestRefreshAccounts() {
	if s.accountsWorker == nil || s.accountsView.refreshing {
		return
	}
	s.accountsView.refreshing = true
	req := accounts.Request{Kind: accounts.OpListAccounts}
	s.accountsWorker.Do(&req)
}

func (v *accountsView) layout(gtx *layout.Context, env *kit.Env, sc *screens) {
	layoutSlotContent(gtx, env, func(gtx *layout.Context) layout.Dimensions {
		return v.layoutBody(gtx, env, sc)
	})
}

func (v *accountsView) layoutBody(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	bannerErr := v.listErr
	if bannerErr == "" {
		bannerErr = v.openErr
	}
	accs := sc.accounts.Accounts()
	if !sc.accounts.HasSnapshot() && len(accs) == 0 {
		return v.layoutEmpty(gtx, env, sc, bannerErr)
	}
	children := make([]layout.FlexChild, 0, len(accs)*2+2)
	if bannerErr != "" {
		children = append(children, v.rigidBanner(env, sc, bannerErr), spacerH(theme.SpaceSM))
	}
	for i := range accs {
		acc := accs[i]
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutAccountCard(gtx, env, sc, &acc)
		}))
		if i < len(accs)-1 {
			children = append(children, spacerH(theme.SpaceMD))
		}
	}
	return v.scroll.Layout(env, gtx, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	})
}

func (v *accountsView) layoutEmpty(gtx *layout.Context, env *kit.Env, sc *screens, bannerErr string) layout.Dimensions {
	primary := ""
	if sc.accountsWorker != nil {
		primary = "Refresh"
	}
	var children []layout.FlexChild
	if bannerErr != "" {
		children = append(children, v.rigidBanner(env, sc, bannerErr), spacerH(theme.SpaceMD))
	}
	children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			dims, ev, err := v.empty.Layout(env, &local, &kit.EmptyStateProps{
				Icon:          kit.IconHelp,
				Title:         "Account status unknown",
				Description:   "Refresh to read vendor login status and rate-limit windows from the host.",
				PrimaryAction: primary,
			})
			sc.fail(err)
			if ev.PrimaryClicked {
				sc.requestRefreshAccounts()
			}
			return dims
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func (v *accountsView) rigidBanner(env *kit.Env, sc *screens, msg string) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		dims, _, err := v.errBanner.Layout(env, gtx, kit.BannerProps{
			Kind: kit.BannerDanger, Title: "Accounts error", Message: msg, Visible: true,
		})
		sc.fail(err)
		return dims
	})
}

func (v *accountsView) layoutAccountCard(gtx *layout.Context, env *kit.Env, sc *screens, acc *client.Account) layout.Dimensions {
	w := v.widgetsFor(acc.Vendor)
	local := *gtx
	local.Constraints.Min.X = local.Constraints.Max.X
	dims, _, err := w.card.Layout(env, &local, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutCardHeader(gtx, env, sc, w, acc) }),
			spacerH(theme.SpaceMD),
		}
		children = append(children, layoutQuotaWindows(env, w, acc, gtx.Now)...)
		if login, ok := sc.accounts.Login(acc.Vendor); ok && login.Active() {
			children = append(children, v.layoutLoginSection(env, sc, w, acc.Vendor, &login)...)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	sc.fail(err)
	return dims
}

func (v *accountsView) layoutCardHeader(gtx *layout.Context, env *kit.Env, sc *screens, w *accountCardWidgets, acc *client.Account) layout.Dimensions {
	vendorTok, vendorCol := accountVendorNameStyle(env.Th)
	login, hasLogin := sc.accounts.Login(acc.Vendor)
	running := hasLogin && login.Active()
	items := []kit.FlowItem{
		flowTitle(env, gtx, vendorTok, vendorCol, acc.Vendor),
	}
	if tagProps, showChip := accountStatusChipProps(acc, gtx.Now); showChip {
		items = append(items, flowTag(env, gtx, sc.fail, w.stateTag, tagProps))
	}
	if running {
		items = append(items, atEnd(flowButton(env, gtx, sc.fail, w.cancelBtn, kit.ButtonProps{
			Text: labelCancel, Variant: kit.Danger, Size: kit.SizeSM, Busy: login.Cancelling, Disabled: sc.accountsWorker == nil,
		}, func() {
			if sc.accountsWorker == nil {
				return
			}
			sc.accounts.BeginCancelLogin(acc.Vendor)
			req := accounts.Request{Kind: accounts.OpCancelLogin, Vendor: acc.Vendor}
			sc.accountsWorker.Do(&req)
		})))
	} else if acc.State == client.AccountStateNeedsLogin {
		items = append(items, atEnd(flowButton(env, gtx, sc.fail, w.loginBtn, kit.ButtonProps{
			Text: "Sign in", Variant: kit.Secondary, Size: kit.SizeSM, Disabled: sc.accountsWorker == nil,
		}, func() {
			if sc.accountsWorker == nil || sc.accounts.IsLoginRunning(acc.Vendor) {
				return
			}
			sc.accounts.BeginStartLogin(acc.Vendor)
			req := accounts.Request{Kind: accounts.OpStartLogin, Vendor: acc.Vendor}
			sc.accountsWorker.Do(&req)
		})))
	}
	return flowHeader(gtx, items...)
}

func accountStatusChipProps(acc *client.Account, now time.Time) (kit.TagProps, bool) {
	switch accounts.NormalizeState(acc.State) {
	case client.AccountStateOK:
		return kit.TagProps{}, false
	case client.AccountStatePaused:
		return kit.TagProps{Text: formatPausedChip(acc.PausedUntil, now), Variant: kit.TagStatus, Status: theme.StatusWarning}, true
	case client.AccountStateNeedsLogin:
		return kit.TagProps{Text: "Needs sign-in", Variant: kit.TagStatus, Status: theme.StatusDanger}, true
	default:
		return kit.TagProps{Text: "Unknown", Variant: kit.TagNeutral}, true
	}
}
