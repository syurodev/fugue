package appwin

import (
	"image"
	"testing"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestStartFlowEffortUnselectedSendsVendorDefault(t *testing.T) {
	t.Parallel()
	assertEffortOptions(t)

	fx := newComposerFixture(t, stateReady, client.Capability{})
	f := fx.c.start
	if f.effortSel != "" || f.effortValue() != "" || f.dirty() {
		t.Fatalf("initial startFlow state = (sel:%q val:%q dirty:%v), want empty/clean", f.effortSel, f.effortValue(), f.dirty())
	}

	assertEffortPlaceholderRun(t, f)

	f.effortSel = effortDefaultID
	if f.effortValue() != "" || f.dirty() {
		t.Fatalf("effortDefaultID state = (val:%q dirty:%v), want empty/clean", f.effortValue(), f.dirty())
	}

	assertStartSubmitsEmptyEffort(t, fx)
}

func assertEffortOptions(t *testing.T) {
	t.Helper()
	opts := effortOptions()
	if len(opts) == 0 || opts[0].ID != effortDefaultID || opts[0].Label != labelVendorDefault {
		t.Fatalf("effortOptions()[0] = %+v, want {ID:%q Label:%q}", opts[0], effortDefaultID, labelVendorDefault)
	}
	for _, opt := range opts {
		if opt.ID == "" {
			t.Fatalf("effortOptions() contains empty ID option %+v; Select treats empty ID as unset", opt)
		}
	}
}

func assertEffortPlaceholderRun(t *testing.T, f *startFlow) {
	t.Helper()
	h := newFrameRunner(t)
	var runs []image.Rectangle
	h.env.RunRecorder = func(r image.Rectangle) { runs = append(runs, r) }

	const colW = 400
	var boxDims layout.Dimensions
	h.frame(t, image.Pt(colW, 200), func(gtx *layout.Context) {
		f.layEffort(h.env, func(_ event.Tag, draw func(*layout.Context) (layout.Dimensions, error)) {
			local := *gtx
			local.Constraints.Min.X = colW
			var err error
			boxDims, err = draw(&local)
			if err != nil {
				t.Fatalf("layEffort draw: %v", err)
			}
		})
	})
	if boxDims.Size.X != colW {
		t.Fatalf("effort select width = %d, want column width %d", boxDims.Size.X, colW)
	}
	for _, r := range runs {
		// Label sits at y=0; placeholder text run sits inside the control box below the label.
		if r.Min.Y > 0 && r.Dx() > 0 && r.Dy() > 0 {
			return
		}
	}
	t.Fatalf("effort select with unset value produced no placeholder text run inside box (runs=%v)", runs)
}

func assertStartSubmitsEmptyEffort(t *testing.T, fx *composerFixture) {
	t.Helper()
	f := fx.c.start
	fx.sc.applyResult(session.Result{
		Kind:       session.ResultWorkspaces,
		Workspaces: []client.WorkspaceSummary{{ID: testWorkspace, Name: testBackend, ConfigRevision: 1}},
	})
	f.chooseWorkspace(testWorkspace, fx.sc)
	fx.sc.applyResult(session.Result{
		Kind: session.ResultWorkspace,
		Workspace: client.Workspace{
			ID: testWorkspace, Name: testBackend, ConfigHash: "hash-ws1", ConfigRevision: 1,
			Roots: []client.WorkspaceRoot{{Name: testMainRoot, Path: "/repo", Access: "rw"}},
		},
	})
	f.effortSel = ""
	f.submit(fx.sc, fx.c)
	calls := fx.awaitWrite()
	if len(calls) != 1 || calls[0].effort != "" {
		t.Fatalf("submitted start calls = %+v, want Effort == %q (vendor default)", calls, "")
	}
}

func TestFormColumnSelectAndComboboxSpanWidth(t *testing.T) {
	t.Parallel()
	const colW = 480
	sc, form, view := conformanceFormFixtures(t)
	h := newFrameRunner(t)
	qf := &questionFormState{questionID: "q-sec", secretMode: 1, secretPicker: kit.NewSelectState()}

	widths := make(map[string]int, 7)
	var addBtnH int
	h.frame(t, image.Pt(colW, 1200), func(gtx *layout.Context) {
		col := *gtx
		col.Constraints = layout.Constraints{Max: image.Pt(colW, 1200)}
		addBtnH = measureFormColumnWidths(t, h.env, col, sc, form, view, qf, widths)
		measureBudgetWidths(h.env, col, sc, form.proposal, widths)
	})

	for name, got := range widths {
		want := colW
		if name == "proposal budget max agents" || name == "proposal budget deadline" {
			want = colW / 2
		}
		if got != want {
			t.Errorf("%s width = %d, want %d", name, got, want)
		}
	}
	if wantH := h.gtx.Dp(theme.ControlMD); addBtnH != wantH {
		t.Errorf("proposal Add task button height = %d, want md (%d)", addBtnH, wantH)
	}
}

func conformanceFormFixtures(t *testing.T) (*screens, *cardFormState, client.TeamView) {
	t.Helper()
	sc, req := proposalFixture(t, nil)
	sc.settings.WorkspaceID = "w1"
	sc.settings.Loaded = true
	sc.settings.Draft.ApprovalMode = "auto"
	sc.settings.Draft.SandboxProfile = "workspace-write"
	var meta client.SecretMeta
	meta.SecretID = "1"
	meta.Name = "DEMO_SETTING"
	meta.ScopeKind = client.SecretScopeGlobal
	sc.secrets.ApplyList([]client.SecretMeta{meta})
	form := sc.inboxView.formFor(req)
	view := sc.teamViews[proposalFixtureTeam]
	form.proposal = newProposalCard(&view)
	form.proposal.addOpen = true
	return sc, form, view
}

func measureFormColumnWidths(t *testing.T, env *kit.Env, col layout.Context, sc *screens, form *cardFormState, view client.TeamView, qf *questionFormState, out map[string]int) int {
	t.Helper()
	wsChild := sc.settingsView.workspaceSelectChild(env, sc, []kit.SelectOption{{ID: "w1", Label: testRawWSName}})
	out["settings workspace select"] = layout.Flex{Axis: layout.Vertical}.Layout(col, wsChild).Size.X
	out["settings policy select"] = sc.settingsView.layoutPolicySelect(&col, env, sc, "approval_mode", "Approval mode", "",
		sc.settingsView.approvalSelect, approvalModeOptions(), sc.settings.Draft.ApprovalMode, func(string) {}).Size.X
	out["inbox secret picker select"] = layoutSecretPicker(&col, env, sc, qf, false, sc.secrets.AllForScope("w1"), testRawWSName).Size.X

	roleField := form.proposal.models["coder"]
	nodeRows := proposalNodeFields(env, sc, &view.Tasks[1], roleField,
		proposalModelOptions(view.Tasks, nil, view.Tasks[1].Vendor), true, false)
	out["proposal Role model combobox"] = layout.Flex{Axis: layout.Vertical}.Layout(col, nodeRows[len(nodeRows)-1]).Size.X

	addRows := form.proposal.addTaskRows(env, sc, &view, false)
	if len(addRows) != 8 {
		t.Fatalf("len(addRows) = %d, want 8", len(addRows))
	}
	out["proposal Role for new task"] = layout.Flex{Axis: layout.Vertical}.Layout(col, addRows[3]).Size.X
	return layout.Flex{Axis: layout.Vertical}.Layout(col, addRows[7]).Size.Y
}

func measureBudgetWidths(env *kit.Env, col layout.Context, sc *screens, p *proposalCard, out map[string]int) {
	half := col
	half.Constraints = layout.Exact(image.Pt(col.Constraints.Max.X/2, col.Constraints.Max.Y))
	out["proposal budget max agents"] = layoutProposalTextField(env, sc, &half, p.maxAgents, "Max agents", "Default", false).Size.X
	out["proposal budget deadline"] = layoutProposalTextField(env, sc, &half, p.deadline, "Deadline (sec.)", "Default", false).Size.X
}

func TestTeamChannelComposerButtonsUseMDHeight(t *testing.T) {
	t.Parallel()
	sc := teamShotScreens(t, tabChannel)
	id := sc.teamSummaries[0].TeamID
	view := sc.teamViews[id]
	comp := newTeamChannelComposer()

	h := newFrameRunner(t)
	var sendDims, mentionDims, recoveryDims layout.Dimensions
	h.frame(t, image.Pt(640, 200), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Max: gtx.Constraints.Max}
		var submit bool
		sendDims = layout.Flex{}.Layout(local, comp.sendAction(h.env, sc, false, &submit))
		mentionDims = layout.Flex{}.Layout(local, comp.mentionAction(&local, h.env, sc, false))
		recoveryDims = sc.layoutAgentRecovery(&local, h.env, id, view.Tasks[0].TaskID, sc.teamDetail(id, view.Tasks[0].TaskID))
	})

	wantMD := h.gtx.Dp(theme.ControlMD)
	if sendDims.Size.Y != wantMD {
		t.Errorf("composer Send button height = %d, want md (%d)", sendDims.Size.Y, wantMD)
	}
	if mentionDims.Size.Y != wantMD {
		t.Errorf("composer @ button height = %d, want md (%d)", mentionDims.Size.Y, wantMD)
	}

	detail := sc.teamDetail(id, view.Tasks[0].TaskID)
	wantRecoveryW := 2 * h.gtx.Dp(theme.SpaceSM)
	for i, label := range []string{"Continue", "Keep diff", "Cancel"} {
		wantRecoveryW += detail.recovery[i].Measure(h.env, &h.gtx, kit.ButtonProps{Text: label, Size: kit.SizeSM}).Size.X
	}
	if recoveryDims.Size.X != wantRecoveryW {
		t.Errorf("agent recovery row width = %d, want %d (buttons + SpaceSM gaps)", recoveryDims.Size.X, wantRecoveryW)
	}
}
