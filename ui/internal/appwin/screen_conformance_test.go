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
	opts := effortOptions()
	if len(opts) == 0 || opts[0].ID != effortDefaultID || opts[0].Label != "vendor default" {
		t.Fatalf("effortOptions()[0] = %+v, want {ID:%q Label:%q}", opts[0], effortDefaultID, "vendor default")
	}
	for _, opt := range opts {
		if opt.ID == "" {
			t.Fatalf("effortOptions() contains empty ID option %+v; Select treats empty ID as unset", opt)
		}
	}

	fx := newComposerFixture(t, stateReady, client.Capability{})
	f := fx.c.start
	if f.effortSel != "" || f.effortValue() != "" || f.dirty() {
		t.Fatalf("initial startFlow state = (sel:%q val:%q dirty:%v), want empty/clean", f.effortSel, f.effortValue(), f.dirty())
	}

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
	// Label sits at y=0; placeholder text run sits inside the control box below the label.
	hasPlaceholderRun := false
	for _, r := range runs {
		if r.Min.Y > 0 && r.Dx() > 0 && r.Dy() > 0 {
			hasPlaceholderRun = true
			break
		}
	}
	if !hasPlaceholderRun {
		t.Fatalf("effort select with unset value produced no placeholder text run inside box (runs=%v)", runs)
	}

	f.effortSel = effortDefaultID
	if f.effortValue() != "" || f.dirty() {
		t.Fatalf("effortDefaultID state = (val:%q dirty:%v), want empty/clean", f.effortValue(), f.dirty())
	}

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
	h := newFrameRunner(t)
	sc, req := proposalFixture(t, nil)
	sc.settings.WorkspaceID = "w1"
	sc.settings.Loaded = true
	sc.settings.Draft.ApprovalMode = "auto"
	sc.settings.Draft.SandboxProfile = "workspace-write"
	sc.secrets.ApplyList([]client.SecretMeta{{SecretID: "sec-1", Name: "API_TOKEN", ScopeKind: client.SecretScopeGlobal}})

	var wsDims, policyDims, secretDims, roleModelDims, addRoleDims, addBtnDims layout.Dimensions
	var budgetLeft, budgetRight layout.Dimensions

	form := sc.inboxView.formFor(req)
	view := sc.teamViews[proposalFixtureTeam]
	form.proposal = newProposalCard(&view)
	form.proposal.addOpen = true
	qf := &questionFormState{
		questionID:   "q-sec",
		secretMode:   1,
		secretPicker: kit.NewSelectState(),
	}

	h.frame(t, image.Pt(colW, 1200), func(gtx *layout.Context) {
		col := *gtx
		col.Constraints = layout.Constraints{Max: image.Pt(colW, 1200)}

		wsChild := sc.settingsView.workspaceSelectChild(h.env, sc, []kit.SelectOption{{ID: "w1", Label: "alpha"}})
		wsDims = layout.Flex{Axis: layout.Vertical}.Layout(col, wsChild)

		policyDims = sc.settingsView.layoutPolicySelect(&col, h.env, sc, "approval_mode", "Approval mode", "",
			sc.settingsView.approvalSelect, approvalModeOptions(), sc.settings.Draft.ApprovalMode, func(string) {})

		secretDims = layoutSecretPicker(&col, h.env, sc, qf, false, sc.secrets.AllForScope("w1"), "alpha")

		roleField := form.proposal.models["coder"]
		nodeRows := proposalNodeFields(h.env, sc, &view.Tasks[1], roleField,
			proposalModelOptions(view.Tasks, nil, view.Tasks[1].Vendor), true, false)
		roleModelDims = layout.Flex{Axis: layout.Vertical}.Layout(col, nodeRows[len(nodeRows)-1])

		budgetLeft = layoutProposalTextField(h.env, sc, &layout.Context{
			Ops:         col.Ops,
			Metric:      col.Metric,
			Source:      col.Source,
			Now:         col.Now,
			Constraints: layout.Exact(image.Pt(colW/2, col.Constraints.Max.Y)),
		}, form.proposal.maxAgents, "Max agents", "Default", false)
		budgetRight = layoutProposalTextField(h.env, sc, &layout.Context{
			Ops:         col.Ops,
			Metric:      col.Metric,
			Source:      col.Source,
			Now:         col.Now,
			Constraints: layout.Exact(image.Pt(colW/2, col.Constraints.Max.Y)),
		}, form.proposal.deadline, "Deadline (sec.)", "Default", false)

		addRows := form.proposal.addTaskRows(h.env, sc, &view, false)
		if len(addRows) != 8 {
			t.Fatalf("len(addRows) = %d, want 8", len(addRows))
		}
		addRoleDims = layout.Flex{Axis: layout.Vertical}.Layout(col, addRows[3])
		addBtnDims = layout.Flex{Axis: layout.Vertical}.Layout(col, addRows[7])
	})

	for name, got := range map[string]int{
		"settings workspace select":       wsDims.Size.X,
		"settings policy select":          policyDims.Size.X,
		"inbox secret picker select":      secretDims.Size.X,
		"proposal Role model combobox":    roleModelDims.Size.X,
		"proposal Role for new task":      addRoleDims.Size.X,
		"proposal budget max agents":      budgetLeft.Size.X,
		"proposal budget deadline":        budgetRight.Size.X,
	} {
		want := colW
		if name == "proposal budget max agents" || name == "proposal budget deadline" {
			want = colW / 2
		}
		if got != want {
			t.Errorf("%s width = %d, want %d", name, got, want)
		}
	}
	if wantH := h.gtx.Dp(theme.ControlMD); addBtnDims.Size.Y != wantH {
		t.Errorf("proposal Add task button height = %d, want md (%d)", addBtnDims.Size.Y, wantH)
	}
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
