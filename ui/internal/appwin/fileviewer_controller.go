package appwin

import (
	"fmt"
	"strings"

	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The file viewer's screens glue (F10.4e): what a transcript click opens,
// where the viewer's reads fold back in, and the root candidates the
// session's workspace names. The workspace and the roots always come from
// the session's own workspace record — never parsed from transcript text.

// openFileRef opens one file or folder badge click in the right-pane viewer.
func (s *screens) openFileRef(ref mdview.FileRef) {
	if s.open == "" {
		return
	}
	sess, ok := s.sidebar.Lookup(s.open)
	if !ok || sess.WorkspaceID == "" {
		s.fail(fmt.Errorf("this session has no workspace to read files from"))
		return
	}
	s.viewer.openFile(ref, sess.WorkspaceID)
	s.showPane(paneViewer)
	s.armViewerRead()
}

// openDiffViewer opens an Edited row's full diff from the file_change item
// itself, with no file read (desktop-app v0.10 §5.4).
func (s *screens) openDiffViewer(itemID string) {
	if s.open == "" {
		return
	}
	t := s.transcripts[s.open]
	if t == nil {
		return
	}
	item, ok := t.ItemByID(itemID)
	if !ok {
		return
	}
	s.viewer.openDiff(item, fileChangeViewerPath(item))
	s.showPane(paneViewer)
}

// fileChangeViewerPath names the file a file_change item edited, from the
// item's own enrichment (title or diff header), not from prose.
func fileChangeViewerPath(item *session.Item) string {
	view := item.Display()
	if view.Title != "" {
		return view.Title
	}
	if len(view.Files) > 0 && view.Files[0].Header != "" {
		if after, ok := strings.CutPrefix(view.Files[0].Header, "diff --git "); ok {
			parts := strings.Fields(after)
			if len(parts) >= 2 {
				return strings.TrimPrefix(parts[1], "b/")
			}
		}
		return view.Files[0].Header
	}
	return ""
}

// armViewerRead resolves the viewer's pending target once the workspace's
// roots are known, arming one worker read against the first root candidate.
func (s *screens) armViewerRead() {
	v := s.viewer
	if !v.open || !v.loading || v.path != "" || v.errMsg != "" || v.mode == fileViewDiff {
		return
	}
	if _, known := s.workspaces.DetailFor(v.wsID); !known {
		s.requestWorkspace(v.wsID)
		return
	}
	v.applyRoots(s.viewerRootCandidates(v.wsID))
	s.armViewerRequest()
}

// armViewerRequest fires the next pending read when one is armed.
func (s *screens) armViewerRequest() {
	if s.viewer.open && s.viewer.loading && len(s.viewer.pendingRoots) > 0 && s.viewerWorker != nil {
		s.viewer.request(s.viewerWorker)
	}
}

// applyFileViewResults folds the worker's finished reads into the viewer;
// an outside-root refusal re-arms the next root candidate.
func (s *screens) applyFileViewResults(results []fileResult) {
	for i := range results {
		s.viewer.apply(&results[i])
	}
	s.armViewerRequest()
}

// viewerRootCandidates lists the workspace's existing roots in try order.
func (s *screens) viewerRootCandidates(wsID string) []workspace.Root {
	d, ok := s.workspaces.DetailFor(wsID)
	if !ok {
		return nil
	}
	out := make([]workspace.Root, 0, len(d.Roots))
	for _, r := range d.Roots {
		if !r.Missing {
			out = append(out, r)
		}
	}
	return out
}
