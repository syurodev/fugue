package appwin

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"unicode"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/cmdid"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

var errBlockedHTTPSURL = errors.New("only valid https URLs may be opened")

const (
	claudeCodeGuidanceLine = "Finish in the browser window Claude opened; if the page shows a code, paste it here."
	claudeFallbackURLLabel = "Open sign-in page (shows a code to paste here)"
	claudeCodeSentLabel    = "Code sent, waiting for Claude"
	claudeSignInAgainLabel = "Sign in again"
)

func vendorAcceptsCode(vendor string) bool {
	return vendor == client.VendorClaude
}

func (v *accountsView) layoutLoginSection(env *kit.Env, sc *screens, w *accountCardWidgets, vendor string, login *accounts.LoginEntry) []layout.FlexChild {
	c := env.Th.Chrome()
	tagProps, summary := loginStatePresentation(vendor, login)
	headerChildren := []layout.FlexChild{
		rigidTag(env, sc.fail, w.loginTag, tagProps),
		spacerW(theme.SpaceSM),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return kit.Paragraph(env, &local, theme.TypeSmall(), c.Fg, summary)
		}),
	}
	if !login.Active() {
		headerChildren = append(headerChildren, spacerW(theme.SpaceSM),
			rigidInboxButton(env, sc.fail, w.dismissBtn, kit.ButtonProps{
				Text: "Dismiss", Variant: kit.Ghost, Size: kit.SizeSM,
			}, func() { sc.accounts.DismissLogin(vendor) }))
	}
	children := []layout.FlexChild{
		spacerH(theme.SpaceSM),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return kit.Divider(env, gtx, kit.DividerProps{Orientation: kit.Horizontal})
		}),
		spacerH(theme.SpaceSM),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, headerChildren...)
		}),
	}
	secTok, secCol := accountLoginSectionLabelStyle(env.Th)
	cliTok, cliCol := accountLoginCLITextStyle(env.Th)
	if login.State == client.LoginStateNeedsTerminal {
		children = append(children, spacerH(theme.SpaceXS),
			rigidLabel(env, cliTok, cliCol, "Terminal command: "+terminalLoginCommand(vendor)))
	}
	if login.Active() && vendorAcceptsCode(vendor) {
		children = append(children, v.layoutLoginCodeControls(env, sc, w, vendor, login)...)
	}
	if login.Prompt != "" {
		children = append(children,
			spacerH(theme.SpaceSM),
			rigidLabel(env, secTok, secCol, "CLI output (untrusted plain text):"),
			spacerH(theme.SpaceXXS),
			rigidParagraph(env, cliTok, cliCol, login.Prompt),
		)
		children = append(children, v.layoutHTTPSLinks(env, sc, w, vendor, login.Prompt)...)
	}
	return children
}

func (v *accountsView) layoutLoginCodeControls(env *kit.Env, sc *screens, w *accountCardWidgets, vendor string, login *accounts.LoginEntry) []layout.FlexChild {
	c := env.Th.Chrome()
	children := []layout.FlexChild{
		spacerH(theme.SpaceSM),
		rigidParagraph(env, theme.TypeSmall(), c.Fg, claudeCodeGuidanceLine),
		spacerH(theme.SpaceXS),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					local := gtx
					props := kit.TextFieldProps{
						Placeholder: "Paste code here…",
						Variant:     kit.TextFieldSecret,
						NoReveal:    true,
						Error:       login.CodeError,
						Disabled:    login.SubmittingCode,
					}
					ev, err := w.codeField.Layout(env, &local, &props)
					sc.fail(err)
					if ev.Submitted {
						v.submitLoginCode(sc, w, vendor, login)
					}
					return w.codeField.Measure(env, &local, &props)
				}),
				spacerW(theme.SpaceSM),
				rigidInboxButton(env, sc.fail, w.submitCodeBtn, kit.ButtonProps{
					Text:     "Submit",
					Variant:  kit.Primary,
					Size:     kit.SizeMD,
					Busy:     login.SubmittingCode,
					Disabled: sc.accountsWorker == nil || login.LoginID == "",
				}, func() {
					v.submitLoginCode(sc, w, vendor, login)
				}),
			)
		}),
	}
	if login.CodeSent {
		children = append(children, spacerH(theme.SpaceXS),
			rigidParagraph(env, theme.TypeSmall(), c.FgMuted, claudeCodeSentLabel))
	}
	return children
}

func (v *accountsView) submitLoginCode(sc *screens, w *accountCardWidgets, vendor string, login *accounts.LoginEntry) {
	if sc.accountsWorker == nil || login.LoginID == "" || login.SubmittingCode {
		return
	}
	code := strings.TrimSpace(w.codeField.Text())
	if code == "" {
		return
	}
	w.codeField.SetText("")
	w.codeField = kit.NewTextFieldState()
	if v.newCodeCommandID == nil {
		v.newCodeCommandID = cmdid.New()
	}
	cmdID := sc.accounts.BeginSubmitLoginCode(vendor, v.newCodeCommandID())
	req := accounts.Request{
		Kind:      accounts.OpSubmitLoginCode,
		Vendor:    vendor,
		CommandID: cmdID,
		LoginID:   login.LoginID,
		Code:      code,
	}
	sc.accountsWorker.Do(&req)
}

func loginStatePresentation(vendor string, login *accounts.LoginEntry) (kit.TagProps, string) {
	switch login.State {
	case client.LoginStateStarted, client.LoginStateWaiting:
		return kit.TagProps{Text: "Login running", Variant: kit.TagStatus, Status: theme.StatusInfo}, runningLoginHint(vendor)
	case client.LoginStateSucceeded:
		return kit.TagProps{Text: "Login succeeded", Variant: kit.TagStatus, Status: theme.StatusSuccess}, "Account login verified."
	case client.LoginStateCancelled:
		return kit.TagProps{Text: "Cancelled", Variant: kit.TagNeutral}, "Login cancelled."
	case client.LoginStateTimedOut:
		return kit.TagProps{Text: "Timed out", Variant: kit.TagStatus, Status: theme.StatusWarning}, "Login timed out after 15 minutes. Start a new login to try again."
	case client.LoginStateNeedsTerminal:
		return kit.TagProps{Text: "Needs terminal", Variant: kit.TagStatus, Status: theme.StatusWarning}, "Browser callback did not finish within the 15-minute timeout. Run the command below in a terminal."
	case accounts.LoginStateUnsupported:
		return kit.TagProps{Text: "Unsupported", Variant: kit.TagStatus, Status: theme.StatusWarning}, unsupportedLoginMessage(vendor, login)
	case client.LoginStateFailed:
		return kit.TagProps{Text: failedLabel, Variant: kit.TagStatus, Status: theme.StatusDanger}, failedLoginMessage(vendor, login)
	default:
		return kit.TagProps{Text: accounts.AccountStateUnknown, Variant: kit.TagNeutral}, "Login state unknown."
	}
}

func unsupportedLoginMessage(vendor string, login *accounts.LoginEntry) string {
	if login.ErrorMessage != "" {
		return login.ErrorMessage
	}
	return vendor + " has no login command; sign in from the " + vendor + " app."
}

func failedLoginMessage(vendor string, login *accounts.LoginEntry) string {
	if login.ErrorMessage != "" {
		return login.ErrorMessage
	}
	if vendorAcceptsCode(vendor) {
		return "Claude did not accept the login."
	}
	return "CLI login failed."
}

func runningLoginHint(vendor string) string {
	switch vendor {
	case client.VendorCodex:
		return "Codex device flow (--device-auth, 15m timeout). One login per vendor."
	case client.VendorClaude:
		return "Claude browser login (15m timeout). One login per vendor."
	default:
		return "CLI login in progress (15m timeout). One login per vendor."
	}
}

func terminalLoginCommand(vendor string) string {
	if vendor == client.VendorClaude {
		return "claude auth login"
	}
	return vendor + " login"
}

func (v *accountsView) layoutHTTPSLinks(env *kit.Env, sc *screens, w *accountCardWidgets, vendor, prompt string) []layout.FlexChild {
	urls := extractHTTPSURLs(prompt)
	if len(urls) == 0 {
		return nil
	}
	if vendorAcceptsCode(vendor) {
		children := make([]layout.FlexChild, 0, len(urls)*2)
		for _, raw := range urls {
			btn := ensureLinkButton(w.linkButtons, raw)
			u := raw
			children = append(children, spacerH(theme.SpaceXS),
				rigidInboxButton(env, sc.fail, btn, kit.ButtonProps{
					Text: claudeFallbackURLLabel, Variant: kit.Secondary, Size: kit.SizeSM, Wrap: true,
				}, func() { v.openHTTPS(u) }))
		}
		return children
	}
	secTok, secCol := accountLoginSectionLabelStyle(env.Th)
	cliTok, cliCol := accountLoginCLITextStyle(env.Th)
	children := []layout.FlexChild{
		spacerH(theme.SpaceSM),
		rigidLabel(env, secTok, secCol, "HTTPS URLs (click to open):"),
	}
	for _, raw := range urls {
		btn := ensureLinkButton(w.linkButtons, raw)
		u := raw
		children = append(children, spacerH(theme.SpaceXXS),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						local := gtx
						return kit.Paragraph(env, &local, cliTok, cliCol, u)
					}),
					spacerW(theme.SpaceSM),
					rigidInboxButton(env, sc.fail, btn, kit.ButtonProps{
						Text: "Open URL", Variant: kit.Secondary, Size: kit.SizeSM,
					}, func() { v.openHTTPS(u) }),
				)
			}))
	}
	return children
}

func ensureLinkButton(m map[string]*kit.ButtonState, u string) *kit.ButtonState {
	if b, ok := m[u]; ok {
		return b
	}
	b := kit.NewButtonState()
	m[u] = b
	return b
}

func (v *accountsView) openHTTPS(raw string) {
	if !isSafeHTTPSURL(raw) {
		v.openErr = errBlockedHTTPSURL.Error()
		return
	}
	if v.openURL == nil {
		return
	}
	if err := v.openURL(raw); err != nil {
		v.openErr = err.Error()
		return
	}
	v.openErr = ""
}

// extractHTTPSURLs scans untrusted prompt text for valid https:// URLs,
// preserving order and deduplicating. Non-https schemes are ignored.
func extractHTTPSURLs(prompt string) []string {
	var out []string
	seen := map[string]bool{}
	fields := strings.FieldsFunc(prompt, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`"'<>()[]{}`, r)
	})
	for _, tok := range fields {
		candidate := strings.TrimRight(tok, ".,;:!?")
		if !isSafeHTTPSURL(candidate) || seen[candidate] {
			continue
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	return out
}

func isSafeHTTPSURL(raw string) bool {
	if raw == "" || strings.IndexFunc(raw, isUnsafeURLRune) >= 0 {
		return false
	}
	const prefix = "https://"
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return false
	}
	rest := raw[len(prefix):]
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	return rest != "" && !strings.Contains(rest, "@") && !strings.Contains(rest, `\`)
}

func isUnsafeURLRune(r rune) bool {
	if unicode.IsControl(r) || unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '\u202A', '\u202B', '\u202C', '\u202D', '\u202E', '\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}

func openHTTPSURL(raw string) error {
	if !isSafeHTTPSURL(raw) {
		return errBlockedHTTPSURL
	}
	name := "xdg-open"
	if runtime.GOOS == osDarwin {
		name = "open"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("find %s: %w", name, err)
	}
	cmd := &exec.Cmd{Path: path, Args: []string{name, raw}}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	go func() {
		if waitErr := cmd.Wait(); waitErr != nil {
			slog.Warn("accounts: opener failed", "opener", name, "err", waitErr)
		}
	}()
	return nil
}
