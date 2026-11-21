package transcriptview

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type rowVerb uint8

const (
	verbCalled rowVerb = iota
	verbRead
	verbEdited
	verbCreated
	verbDeleted
	verbSearched
	verbListed
	verbRan
	verbFetched
	verbWebSearch
	verbCommitted
	verbOpenedPR
	verbBranch
	verbSubagent
	verbPlan
	verbApproval
	verbQuestion
)

type toolRowSpec struct {
	verb       rowVerb
	icon       kit.IconName
	verbLabel  string
	path       string
	line       int
	endLine    int
	query      string
	dir        string
	countLabel string
	command    string
	url        string
	toolName   string
	gitRef     *mdview.GitRef
	linesAdd   int64
	linesDel   int64
	hasDiffSt  bool
	expandable bool
	warningRow bool
	actionText string
}

func openFile(cb Callbacks, ref mdview.FileRef) {
	// TODO(F10.4e): wire OnOpenFile to the right-pane read-only file/diff viewer.
	if cb.OnOpenFile != nil {
		cb.OnOpenFile(ref)
		return
	}
	if cb.OnFileChip != nil {
		cb.OnFileChip(ref)
	}
}

func gitRegistryFor(cb Callbacks) mdview.GitRefRegistry {
	if cb.GitActions != nil {
		return cb.GitActions
	}
	return mdview.DefaultGitRefRegistry(cb.OnOpenLink)
}

func inspectToolRow(item *session.Item) toolRowSpec {
	switch {
	case item.Kind == kindApproval || isWaitingItem(item):
		return approvalOrQuestionSpec(item)
	case item.Kind == kindFileChange:
		return fileChangeSpec(item)
	case item.Kind == kindSubagent:
		return toolRowSpec{verb: verbSubagent, icon: kit.IconWrench, verbLabel: "Ran subagent", query: subagentTitle(item)}
	case item.Kind == kindPlan:
		return toolRowSpec{verb: verbPlan, icon: kit.IconFileText, verbLabel: "Updated plan", query: item.Display().Title}
	case item.Kind == kindCommand:
		f := parseToolInput(item.Display().Input)
		return terminalSpec(item, &f, commandTitle(item))
	}
	if ref, subj, ok := detectGitRowRef(item); ok {
		return gitRowSpec(item, &ref, subj)
	}
	return classifyToolByName(item)
}

func isWaitingItem(item *session.Item) bool {
	return item.Kind == kindToolCall && (item.Metadata.RequestKind != "" || item.Metadata.Status == "waiting")
}

func approvalOrQuestionSpec(item *session.Item) toolRowSpec {
	verb, label, action := verbApproval, "Waiting for approval", "Open inbox"
	if item.Metadata.RequestKind == "question" {
		verb, label, action = verbQuestion, "Asked you", "Answer"
	}
	fields := parseToolInput(item.Display().Input)
	obj := firstNonEmpty(item.Display().Title, fields.Command, fields.Description, fields.Query, item.Display().Text, item.Metadata.RequestKind)
	return toolRowSpec{verb: verb, icon: kit.IconHelp, verbLabel: label, query: obj, warningRow: true, actionText: action}
}

func fileChangeSpec(item *session.Item) toolRowSpec {
	m, path := item.Metadata, fileChangePath(item)
	hasTxt := item.Display().Text != ""
	switch strings.ToLower(m.ChangeKind) {
	case "add", "create", "created":
		return toolRowSpec{verb: verbCreated, icon: kit.IconPencil, verbLabel: "Created", path: path, linesAdd: m.LinesAdded, hasDiffSt: m.LinesAdded > 0, expandable: hasTxt}
	case "delete", "deleted", "remove":
		return toolRowSpec{verb: verbDeleted, icon: kit.IconPencil, verbLabel: "Deleted", path: path, linesDel: m.LinesRemoved, hasDiffSt: m.LinesRemoved > 0}
	default:
		return toolRowSpec{verb: verbEdited, icon: kit.IconPencil, verbLabel: "Edited", path: path, linesAdd: m.LinesAdded, linesDel: m.LinesRemoved, hasDiffSt: m.LinesAdded > 0 || m.LinesRemoved > 0, expandable: hasTxt}
	}
}

type toolClassifier struct {
	tokens []string
	build  func(item *session.Item, f *toolInputFields) toolRowSpec
}

func toolClassifiers() []toolClassifier {
	return []toolClassifier{
		{[]string{"read", "view", "view_file", "cat"}, readRowSpec},
		{[]string{"write_to_file", "create_file", "write"}, func(it *session.Item, f *toolInputFields) toolRowSpec {
			return toolRowSpec{verb: verbCreated, icon: kit.IconPencil, verbLabel: "Created", path: f.FilePath, expandable: it.Display().Text != ""}
		}},
		{[]string{"delete", "remove_file"}, func(_ *session.Item, f *toolInputFields) toolRowSpec {
			return toolRowSpec{verb: verbDeleted, icon: kit.IconPencil, verbLabel: "Deleted", path: f.FilePath}
		}},
		{[]string{"edit", "patch", "replace"}, func(it *session.Item, f *toolInputFields) toolRowSpec {
			return toolRowSpec{verb: verbEdited, icon: kit.IconPencil, verbLabel: "Edited", path: f.FilePath, expandable: it.Display().Text != ""}
		}},
		{[]string{"list_dir", "find_by_name", "glob", "ls"}, listRowSpec},
		{[]string{"web_search", "search_web"}, webSearchSpec},
		{[]string{"grep", "search"}, searchRowSpec},
		{[]string{"bash", "shell", "command", "exec", "terminal"}, func(it *session.Item, f *toolInputFields) toolRowSpec {
			return terminalSpec(it, f, "")
		}},
		{[]string{"fetch", "http", "web"}, func(it *session.Item, f *toolInputFields) toolRowSpec {
			return toolRowSpec{verb: verbFetched, icon: kit.IconGlobe, verbLabel: "Fetched", url: f.URL, expandable: it.Display().Text != ""}
		}},
		{[]string{"subagent", "agent", "task"}, func(it *session.Item, f *toolInputFields) toolRowSpec {
			return toolRowSpec{verb: verbSubagent, icon: kit.IconWrench, verbLabel: "Ran subagent", query: firstNonEmpty(f.Description, it.Display().Title, "subagent")}
		}},
		{[]string{"plan"}, func(it *session.Item, f *toolInputFields) toolRowSpec {
			return toolRowSpec{verb: verbPlan, icon: kit.IconFileText, verbLabel: "Updated plan", query: firstNonEmpty(f.Description, it.Display().Title, "plan")}
		}},
	}
}

func classifyToolByName(item *session.Item) toolRowSpec {
	low := strings.ToLower(item.Metadata.Tool)
	fields := parseToolInput(item.Display().Input)
	for _, cl := range toolClassifiers() {
		for _, tok := range cl.tokens {
			if strings.Contains(low, tok) {
				return cl.build(item, &fields)
			}
		}
	}
	hasOut := item.Display().Text != "" || item.Display().Input != ""
	return toolRowSpec{
		verb: verbCalled, icon: kit.IconWrench, verbLabel: "Called",
		toolName: firstNonEmpty(item.Metadata.Tool, item.Display().Title, "tool"), expandable: hasOut,
	}
}

func readRowSpec(item *session.Item, f *toolInputFields) toolRowSpec {
	start, end := f.StartLine, f.EndLine
	if start == 0 && f.Offset > 0 {
		start = f.Offset
		if f.Limit > 0 {
			end = start + f.Limit - 1
		}
	}
	return toolRowSpec{
		verb: verbRead, icon: kit.IconFileText, verbLabel: "Read",
		path: firstNonEmpty(f.FilePath, item.Display().Title), line: start, endLine: end,
		expandable: item.Display().Text != "",
	}
}

func listRowSpec(item *session.Item, f *toolInputFields) toolRowSpec {
	entries := parseListEntries(item.Display().Text)
	return toolRowSpec{
		verb: verbListed, icon: kit.IconFolder, verbLabel: "Listed",
		dir: firstNonEmpty(f.FilePath, f.Directory, "."), countLabel: countLabel(len(entries), "files"),
		expandable: len(entries) > 0,
	}
}

func searchRowSpec(item *session.Item, f *toolInputFields) toolRowSpec {
	matches := parseSearchResults(item.Display().Text)
	return toolRowSpec{
		verb: verbSearched, icon: kit.IconSearch, verbLabel: "Searched",
		query: f.Pattern, dir: firstNonEmpty(f.Directory, f.SearchPath, f.FilePath),
		countLabel: countLabel(len(matches), "results"), expandable: len(matches) > 0,
	}
}

func terminalSpec(item *session.Item, f *toolInputFields, fallback string) toolRowSpec {
	cmd := firstNonEmpty(f.Command, fallback, item.Display().Title, item.Metadata.Tool)
	line, _, _ := strings.Cut(cmd, "\n")
	return toolRowSpec{
		verb: verbRan, icon: kit.IconTerminal, verbLabel: "Ran",
		command: cmd, query: line, expandable: cmd != "" || item.Display().Text != "",
	}
}

func webSearchSpec(item *session.Item, f *toolInputFields) toolRowSpec {
	links := parseHTTPSLinks(item.Display().Text)
	return toolRowSpec{
		verb: verbWebSearch, icon: kit.IconGlobe, verbLabel: "Searched the web",
		query: f.Pattern, countLabel: countLabel(len(links), "results"),
		expandable: len(links) > 0 || item.Display().Text != "",
	}
}

func countLabel(n int, noun string) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d %s", n, noun)
}

func gitRowSpec(item *session.Item, ref *mdview.GitRef, subject string) toolRowSpec {
	cp := *ref
	switch cp.Kind {
	case mdview.GitPR:
		return toolRowSpec{verb: verbOpenedPR, icon: kit.IconGitPullRequest, verbLabel: "Opened PR", query: subject, gitRef: &cp}
	case mdview.GitBranch:
		return toolRowSpec{verb: verbBranch, icon: kit.IconGitBranch, verbLabel: "Branch", query: subject, gitRef: &cp}
	case mdview.GitCommit:
		return toolRowSpec{verb: verbCommitted, icon: kit.IconGitCommit, verbLabel: "Committed", query: subject, gitRef: &cp, expandable: item.Display().Text != ""}
	}
	return toolRowSpec{verb: verbCommitted, icon: kit.IconGitCommit, verbLabel: "Committed", query: subject, gitRef: &cp}
}

func detectGitRowRef(item *session.Item) (mdview.GitRef, string, bool) {
	if eff, ok := detectCommitEffect(item); ok {
		return mdview.GitRef{Kind: mdview.GitCommit, Text: shortHash(eff.Hash), Hash: eff.Hash, Subject: eff.Message}, eff.Message, true
	}
	f := parseToolInput(item.Display().Input)
	txt := item.Display().Text
	for line := range strings.SplitSeq(txt, "\n") {
		if pr, ok := parsePRLine(strings.TrimSpace(line)); ok {
			return pr, item.Display().Title, true
		}
	}
	if strings.Contains(f.Command, "git checkout") || strings.Contains(f.Command, "git switch") {
		if br := extractBranchName(f.Command, txt); br != "" {
			return mdview.GitRef{Kind: mdview.GitBranch, Text: br}, "", true
		}
	}
	return mdview.GitRef{}, "", false
}

func parsePRLine(line string) (mdview.GitRef, bool) {
	for tok := range strings.FieldsSeq(line) {
		for _, cr := range mdview.ParseChips(tok) {
			if gr, ok := cr.AsGitRef(); ok && gr.Kind == mdview.GitPR {
				return gr, true
			}
		}
	}
	return mdview.GitRef{}, false
}

func extractBranchName(cmd, out string) string {
	fields := strings.Fields(cmd)
	for i := len(fields) - 1; i >= 0; i-- {
		tok := fields[i]
		if tok != "" && !strings.HasPrefix(tok, "-") && tok != "git" && tok != "checkout" && tok != "switch" {
			return tok
		}
	}
	if after, ok := strings.CutPrefix(strings.TrimSpace(out), "Switched to branch '"); ok {
		if br, _, cut := strings.Cut(after, "'"); cut {
			return br
		}
	}
	return ""
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func renderToolRowLine(
	env *kit.Env, gtx *layout.Context, c *cardState,
	spec *toolRowSpec, rightText string, rightDanger, expanded bool, cb Callbacks,
) layout.Dimensions {
	ch := env.Th.Chrome()
	verbCol, rightCol := ch.FgMuted, ch.FgSubtle
	if spec.warningRow {
		verbCol = env.Th.StatusTone(theme.StatusWarning).Fg
	}
	if rightDanger {
		rightCol = env.Th.Status().Danger
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			d, err := env.Icon(&l, spec.icon, theme.IconSM, ch.IconMuted)
			logTVErr(err)
			return d
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return drawToolRowObject(env, &l, c, spec, verbCol, cb)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return drawToolRowRightAction(env, &l, c, spec, rightText, rightCol, expanded, cb)
		}),
	)
}

func drawToolRowRightAction(
	env *kit.Env, gtx *layout.Context, c *cardState,
	spec *toolRowSpec, rightText string, rightCol color.NRGBA, expanded bool, cb Callbacks,
) layout.Dimensions {
	caret := kit.IconChevronRight
	if expanded {
		caret = kit.IconChevronDown
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if spec.actionText == "" {
				return layout.Dimensions{}
			}
			l := gtx
			bp := kit.ButtonProps{Text: spec.actionText, Variant: kit.Ghost, Size: kit.SizeSM}
			if clicked, err := c.inboxBtn.Layout(env, &l, bp); err != nil {
				logTVErr(err)
			} else if clicked && cb.OnOpenInbox != nil {
				cb.OnOpenInbox()
			}
			return c.inboxBtn.Measure(env, &l, bp)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !spec.hasDiffSt {
				return layout.Dimensions{}
			}
			l := gtx
			return drawDiffStatCounts(env, &l, spec.linesAdd, spec.linesDel)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if rightText == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeSmall(), rightCol, rightText)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !spec.expandable {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				d, err := env.Icon(&l, caret, theme.IconXS, env.Th.Chrome().IconMuted)
				logTVErr(err)
				return d
			})
		}),
	)
}

func drawDiffStatCounts(env *kit.Env, gtx *layout.Context, added, removed int64) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if added == 0 && removed == 0 {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeCodeSmall(), env.Th.Status().Success, fmt.Sprintf("+%d", added))
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if removed == 0 {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeCodeSmall(), env.Th.Status().Danger, fmt.Sprintf("−%d", removed))
			})
		}),
	)
}

func wrapSurfaceTone(_ *kit.Env, gtx *layout.Context, fill color.NRGBA, draw func(*layout.Context) layout.Dimensions) layout.Dimensions {
	rec := op.Record(gtx.Ops)
	dims := layout.Inset{Top: theme.SpaceSM, Bottom: theme.SpaceSM, Left: theme.SpaceMD, Right: theme.SpaceMD}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return draw(&l)
	})
	call := rec.Stop()
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusMD)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}
