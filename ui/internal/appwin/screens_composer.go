package appwin

import (
	"fmt"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The screens' composer glue (F10.4b): the reads the composer and the start
// flow drive, the finished writes they fold back, and the connection state
// that gates the input. The screens themselves — sidebar, transcript,
// layout — live in screens.go; both files are one screens type.

// applyComposerResult folds one composer read into the screens: the open
// session's slash commands, the vendors' capabilities, and the workspace model
// reads that feed both the sidebar and the start dialog.
func (s *screens) applyComposerResult(r session.Result) {
	switch r.Kind {
	case session.ResultCommands:
		s.pendingCommands[r.Session] = false
		cmds, ok := s.commands[r.Session]
		if !ok {
			cmds = &session.Commands{}
			s.commands[r.Session] = cmds
		}
		cmds.ApplyList(r.Commands)
		if r.Err != nil {
			s.fail(fmt.Errorf("read the commands: %w", r.Err))
		}
	case session.ResultProbe:
		s.pendingProbe = false
		s.composer.applyProbes(r.Probes)
		if r.Err != nil {
			s.fail(fmt.Errorf("probe the vendors: %w", r.Err))
		}
	case session.ResultWorkspaces:
		s.pendingWorkspaces = false
		if r.Err != nil {
			s.fail(fmt.Errorf("list the workspaces: %w", r.Err))
			return
		}
		s.workspaces.ApplyList(workspaceSummariesFromClient(r.Workspaces))
		s.pruneWorkspaceHeaders()
		s.reconcileWorkspaceDialogs()
	case session.ResultWorkspace:
		if r.Err != nil {
			s.fail(fmt.Errorf("read the workspace: %w", r.Err))
			return
		}
		s.workspaces.ApplyDetail(workspaceDetailFromClient(&r.Workspace))
		s.composer.start.reconcile(s)
	case session.ResultSessions, session.ResultPage:
		// The list and page reads fold in applyResult.
	}
}

// workspaceSummariesFromClient maps pkg/client workspace summaries into the
// workspace model's summary slice.
func workspaceSummariesFromClient(list []client.WorkspaceSummary) []workspace.Summary {
	out := make([]workspace.Summary, 0, len(list))
	for _, w := range list {
		out = append(out, workspace.Summary{
			ID:                        w.ID,
			Name:                      w.Name,
			MissingRoots:              w.MissingRoots,
			ConfigRevision:            w.ConfigRevision,
			PendingPolicyConfirmation: w.PendingPolicyConfirmation,
		})
	}
	return out
}

// workspaceDetailFromClient maps pkg/client's Workspace into the workspace
// model's Detail, carrying ConfigHash, ConfigRevision, Roots and PolicyDiff.
func workspaceDetailFromClient(w *client.Workspace) *workspace.Detail {
	det := &workspace.Detail{
		ID:             w.ID,
		Name:           w.Name,
		ConfigHash:     w.ConfigHash,
		ConfigRevision: w.ConfigRevision,
		MissingRoots:   w.MissingRoots,
		Pending:        w.PendingPolicyConfirmation,
		Roots:          make([]workspace.Root, 0, len(w.Roots)),
		Diff:           make([]workspace.DiffEntry, 0, len(w.PolicyDiff)),
	}
	for _, r := range w.Roots {
		det.Roots = append(det.Roots, workspace.Root{Name: r.Name, Path: r.Path, Access: r.Access, Missing: r.Missing})
	}
	for _, d := range w.PolicyDiff {
		det.Diff = append(det.Diff, workspace.DiffEntry{Key: d.Key, OldValue: d.OldValue, NewValue: d.NewValue})
	}
	return det
}

// applyPostResult folds one finished write into the screens. A failed send
// or steer puts the draft back while the user has not typed since; a start
// marks the session to open on its created event.
func (s *screens) applyPostResult(p session.PostResult) {
	switch p.Kind {
	case session.PostStart:
		if p.Err != nil {
			s.composer.start.startPosted(p.Err)
			return
		}
		s.lastStarted = p.Started
		s.composer.start.startPosted(nil)
	case session.PostResume:
		if p.Err != nil {
			s.composer.stateFor(p.Session).lastErr = p.Err.Error()
		}
	case session.PostSend, session.PostSteer:
		if p.Err == nil {
			return
		}
		st := s.composer.stateFor(p.Session)
		st.lastErr = p.Err.Error()
		if st.area.Text() == "" && p.Text != "" {
			st.area.SetText(p.Text)
			st.area.MoveCaretToEnd()
		}
	case session.PostInterrupt:
		if p.Err != nil {
			s.composer.stateFor(p.Session).lastErr = p.Err.Error()
		}
	}
}

// connected reports the manager's connection state and whether the host is
// reachable, which disables the composer while it is not.
func (s *screens) connected() (hostconn.State, bool) {
	state, _ := s.m.State()
	return state, state == hostconn.StateConnected
}

// commandsFor returns the session's command model, empty until its first
// read lands.
func (s *screens) commandsFor(id string) *session.Commands {
	cmds, ok := s.commands[id]
	if !ok {
		cmds = &session.Commands{}
		s.commands[id] = cmds
	}
	return cmds
}

// commandsPending reports a commands read still in flight, which the slash
// menu shows as loading.
func (s *screens) commandsPending(id string) bool { return s.pendingCommands[id] }

// workspacesPending reports the start flow's workspace list read in flight.
func (s *screens) workspacesPending() bool { return s.pendingWorkspaces }

// requestCommands reads a session's commands once per trigger.
func (s *screens) requestCommands(id string) {
	if s.pendingCommands[id] {
		return
	}
	s.pendingCommands[id] = true
	s.fetcher.RequestCommands(id)
}

// requestWorkspaces reads the workspace list once per trigger.
func (s *screens) requestWorkspaces() {
	if s.pendingWorkspaces {
		return
	}
	s.pendingWorkspaces = true
	s.wsReads.RequestWorkspaces()
}

// requestWorkspace reads one workspace's detail if no read is already in flight.
func (s *screens) requestWorkspace(id string) {
	if s.wsDetailReq[id] {
		return
	}
	s.wsDetailReq[id] = true
	s.wsReads.RequestWorkspace(id)
}

// probeMissingVendors re-probes when the session list names a vendor the
// composer has no capability for: a vendor joined the list under a
// connection that already probed, and the composer gates Steer and stop on
// the probed capability. One probe is in flight at a time; a failed probe
// (a vendor CLI the host cannot resolve, say) retries on the next list
// change rather than on every frame.
func (s *screens) probeMissingVendors() {
	if s.pendingProbe {
		return
	}
	for _, sess := range s.sidebar.List() {
		if sess.Vendor != "" && !s.composer.knowsVendor(sess.Vendor) {
			s.pendingProbe = true
			s.fetcher.RequestProbe()
			return
		}
	}
}
