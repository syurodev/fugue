package transcriptview

import (
	"image/color"
	"path/filepath"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func drawToolRowObject(
	env *kit.Env, gtx *layout.Context, c *cardState, spec *toolRowSpec, verbCol color.NRGBA, cb Callbacks,
) layout.Dimensions {
	kids := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return kit.Label(env, &l, theme.TypeBody(), verbCol, spec.verbLabel)
	})}
	add := func(fn func(layout.Context) layout.Dimensions) {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, fn)
		}))
	}
	appendPrimaryObjectKids(env, c, spec, cb, add)
	appendTrailingObjectKids(env, c, spec, cb, add)
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, kids...)
}

func appendPrimaryObjectKids(
	env *kit.Env, c *cardState, spec *toolRowSpec, cb Callbacks, add func(func(layout.Context) layout.Dimensions),
) {
	if spec.gitRef != nil {
		ref := *spec.gitRef
		add(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return mdview.DrawGitRefChip(env, &l, c.chipClick("g:"+ref.Text), ref, gitRegistryFor(cb))
		})
	}
	if spec.path != "" {
		bKind := mdview.BadgeFile
		if spec.verb == verbDeleted {
			bKind = mdview.BadgeDeleted
		}
		ref := mdview.FileRef{Path: spec.path, Line: spec.line, EndLine: spec.endLine, Raw: spec.path}
		add(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return mdview.DrawBadge(env, &l, c.chipClick("b:"+ref.Path), mdview.BadgeSpec{Kind: bKind, File: ref}, func(r mdview.FileRef) {
				openFile(cb, r)
			})
		})
	}
	if spec.query != "" {
		tp := theme.TypeBody()
		if spec.verb == verbRan || spec.verb == verbSearched {
			tp = theme.TypeCodeSmall()
		}
		q := spec.query
		add(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return kit.Label(env, &l, tp, env.Th.Chrome().Fg, q)
		})
	}
}

func appendTrailingObjectKids(
	env *kit.Env, c *cardState, spec *toolRowSpec, cb Callbacks, add func(func(layout.Context) layout.Dimensions),
) {
	ch := env.Th.Chrome()
	if spec.dir != "" {
		if spec.verb == verbSearched {
			add(func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, theme.TypeBody(), ch.FgMuted, "in")
			})
		}
		dir := mdview.FileRef{Path: spec.dir, Raw: spec.dir}
		add(func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return mdview.DrawBadge(env, &l, c.chipClick("d:"+dir.Path), mdview.BadgeSpec{Kind: mdview.BadgeFolder, File: dir}, func(r mdview.FileRef) {
				openFile(cb, r)
			})
		})
	}
	for _, seg := range []struct {
		txt string
		tp  theme.Type
		col color.NRGBA
	}{
		{spec.url, theme.TypeCodeSmall(), ch.Fg},
		{spec.toolName, theme.TypeCodeSmall(), ch.Fg},
		{spec.countLabel, theme.TypeSmall(), ch.FgSubtle},
	} {
		if seg.txt != "" {
			s := seg
			add(func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Label(env, &l, s.tp, s.col, s.txt)
			})
		}
	}
}

func parseSearchResults(txt string) []mdview.FileRef {
	var out []mdview.FileRef
	for line := range strings.SplitSeq(txt, "\n") {
		tok, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if tok == "" {
			continue
		}
		if c1 := strings.IndexByte(tok, ':'); c1 > 0 {
			numStr, _, _ := strings.Cut(tok[c1+1:], ":")
			if ln, err := strconv.Atoi(numStr); err == nil && ln > 0 {
				out = append(out, mdview.FileRef{Path: tok[:c1], Line: ln, Raw: tok[:c1] + ":" + numStr})
				continue
			}
		}
		for _, cr := range mdview.ParseChips(tok) {
			if cr.Kind == mdview.ChipFile {
				out = append(out, cr.File)
			}
		}
	}
	return out
}

func parseListEntries(txt string) []mdview.BadgeSpec {
	var out []mdview.BadgeSpec
	for line := range strings.SplitSeq(txt, "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "total ") {
			continue
		}
		kind, clean := mdview.BadgeFile, strings.TrimSuffix(entry, "/")
		if strings.HasSuffix(entry, "/") {
			kind = mdview.BadgeFolder
		}
		out = append(out, mdview.BadgeSpec{Kind: kind, File: mdview.FileRef{Path: clean, Raw: clean}})
	}
	return out
}

func parseHTTPSLinks(txt string) []string {
	var out []string
	for tok := range strings.FieldsSeq(txt) {
		tok = strings.TrimRight(tok, ").,;]>")
		if strings.HasPrefix(tok, "https://") {
			out = append(out, tok)
		}
	}
	return out
}

func drawToolExpandedBody(
	env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item, spec *toolRowSpec, cb Callbacks,
) layout.Dimensions {
	switch spec.verb {
	case verbRan:
		return drawTerminalExpanded(env, gtx, c, item, spec, cb)
	case verbSearched:
		return drawSearchBadgeResults(env, gtx, c, item.Display().Text, cb)
	case verbListed:
		return drawListedBadgeResults(env, gtx, c, item.Display().Text, cb)
	case verbFetched:
		return drawFetchedExpanded(env, gtx, c, spec, item.Display().Text, cb)
	case verbWebSearch:
		return drawWebSearchExpanded(env, gtx, c, item.Display().Text, cb)
	case verbCalled:
		return drawMCPJSONExpanded(env, gtx, c, item, cb)
	case verbEdited, verbCreated:
		return drawDiffOrOutputExpanded(env, gtx, c, item, spec.path, spec.verb == verbEdited, cb)
	case verbCommitted:
		// The row already carries the hash and subject; expanding shows the
		// command's own output rather than a card that repeats the row.
		if item.Display().Text == "" {
			return layout.Dimensions{}
		}
		return drawDiffOrOutputExpanded(env, gtx, c, item, "", false, cb)
	case verbRead, verbDeleted, verbOpenedPR, verbBranch, verbSubagent, verbPlan, verbApproval, verbQuestion:
		if item.Display().Text == "" {
			return layout.Dimensions{}
		}
		return drawDiffOrOutputExpanded(env, gtx, c, item, "", false, cb)
	}
	return layout.Dimensions{}
}

func drawTerminalExpanded(
	env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item, spec *toolRowSpec, cb Callbacks,
) layout.Dimensions {
	l := *gtx
	dims, res := mdview.LayoutCodeBlock(env, &l, c.code, &mdview.CodeBlockProps{
		Command: spec.command, ExitCode: item.Metadata.ExitCode,
		Duration: formatDuration(item.Metadata.DurationMillis),
		Raw:      item.Display().Text, Truncated: item.Display().Truncated,
	})
	if res.RequestFull && cb.OnFullOutput != nil {
		cb.OnFullOutput(item.ItemID)
	}
	return dims
}

func drawSearchBadgeResults(env *kit.Env, gtx *layout.Context, c *cardState, text string, cb Callbacks) layout.Dimensions {
	refs := parseSearchResults(text)
	specs := make([]mdview.BadgeSpec, len(refs))
	for i := range refs {
		specs[i] = mdview.BadgeSpec{Kind: mdview.BadgeFile, File: refs[i]}
	}
	return drawBadgeList(env, gtx, c, "sr:", specs, cb)
}

func drawListedBadgeResults(env *kit.Env, gtx *layout.Context, c *cardState, text string, cb Callbacks) layout.Dimensions {
	return drawBadgeList(env, gtx, c, "ls:", parseListEntries(text), cb)
}

func drawBadgeList(env *kit.Env, gtx *layout.Context, c *cardState, prefix string, specs []mdview.BadgeSpec, cb Callbacks) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(specs))
	for i := range specs {
		sp := specs[i]
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return mdview.DrawBadge(env, &l, c.chipClick(prefix+sp.File.Raw), sp, func(r mdview.FileRef) {
					openFile(cb, r)
				})
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func drawFetchedExpanded(
	env *kit.Env, gtx *layout.Context, c *cardState, spec *toolRowSpec, text string, cb Callbacks,
) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return drawHTTPSLinkRow(env, &gtx, c.chipClick("u:"+spec.url), spec.url, cb)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if strings.TrimSpace(text) == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Top: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return kit.Paragraph(env, &l, theme.TypeSmall(), env.Th.Chrome().FgMuted, text)
			})
		}),
	)
}

func drawWebSearchExpanded(env *kit.Env, gtx *layout.Context, c *cardState, text string, cb Callbacks) layout.Dimensions {
	links := parseHTTPSLinks(text)
	if len(links) == 0 {
		l := *gtx
		return kit.Paragraph(env, &l, theme.TypeSmall(), env.Th.Chrome().FgMuted, text)
	}
	children := make([]layout.FlexChild, 0, len(links))
	for _, u := range links {
		link := u
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return drawHTTPSLinkRow(env, &l, c.chipClick("wl:"+link), link, cb)
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func drawHTTPSLinkRow(env *kit.Env, gtx *layout.Context, cl *widget.Clickable, u string, cb Callbacks) layout.Dimensions {
	if !strings.HasPrefix(u, "https://") {
		l := *gtx
		return kit.Label(env, &l, theme.TypeCodeSmall(), env.Th.Chrome().FgMuted, u)
	}
	if cl.Clicked(*gtx) && cb.OnOpenLink != nil {
		cb.OnOpenLink(u)
	}
	return cl.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return kit.Label(env, &l, theme.TypeCodeSmall(), env.Th.Link(), u)
	})
}

func drawMCPJSONExpanded(env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item, cb Callbacks) layout.Dimensions {
	if c.inputCode == nil {
		c.inputCode = mdview.NewCodeBlockState()
	}
	inRaw, outRaw := strings.TrimSpace(item.Display().Input), strings.TrimSpace(item.Display().Text)
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if inRaw == "" {
				return layout.Dimensions{}
			}
			l := gtx
			dims, _ := mdview.LayoutCodeBlock(env, &l, c.inputCode, &mdview.CodeBlockProps{
				Format: "json", Icon: kit.IconWrench, IsOutputCard: true, Raw: inRaw,
			})
			return dims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if outRaw == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Top: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return drawDiffOrOutputExpanded(env, &gtx, c, item, "", false, cb)
			})
		}),
	)
}

func drawDiffOrOutputExpanded(
	env *kit.Env, gtx *layout.Context, c *cardState, item *session.Item, path string, isDiff bool, cb Callbacks,
) layout.Dimensions {
	fmtStr := detectOutputFormat(item)
	if isDiff {
		fmtStr = formatDiff
	}
	title := ""
	if path != "" {
		title = filepath.Base(path)
	}
	l := *gtx
	dims, res := mdview.LayoutCodeBlock(env, &l, c.code, &mdview.CodeBlockProps{
		Title: title, Format: fmtStr, Icon: kit.IconFileCode,
		IsOutputCard: !isDiff, IsDiff: isDiff || fmtStr == formatDiff,
		ShowLineNumbers: isDiff || fmtStr == formatDiff,
		Raw:             item.Display().Text, Truncated: item.Display().Truncated,
	})
	if res.RequestFull && cb.OnFullOutput != nil {
		cb.OnFullOutput(item.ItemID)
	}
	return dims
}
