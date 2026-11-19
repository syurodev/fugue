package appwin

import (
	"fmt"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
)

// The screens' composer glue (F10.4b): the reads the composer and the start
// flow drive, the finished writes they fold back, and the connection state
// that gates the input. The screens themselves — sidebar, transcript,
// layout — live in screens.go; both files are one screens type.

// applyComposerResult folds one composer read into the screens: the open
// session's slash commands, the vendors' capabilities, and the start flow's
// workspace reads.
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
		s.composer.start.applyWorkspaces(r.Workspaces)
		if r.Err != nil {
			s.fail(fmt.Errorf("list the workspaces: %w", r.Err))
		}
	case session.ResultWorkspace:
		s.composer.start.applyDetail(r.Workspace)
		if r.Err != nil {
			s.fail(fmt.Errorf("read the workspace: %w", r.Err))
		}
	case session.ResultSessions, session.ResultPage:
		// The list and page reads fold in applyResult.
	}
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
	s.fetcher.RequestWorkspaces()
}

// requestWorkspace reads one workspace's roots.
func (s *screens) requestWorkspace(id string) {
	s.fetcher.RequestWorkspace(id)
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
