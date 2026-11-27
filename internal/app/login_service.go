package app

// The login half of the accounts service (F9.10b): start the vendor CLI's own
// login command as a supervised process, relay what it prints verbatim, and
// stop it on cancel or timeout. The host never opens a browser, never reads,
// writes or parses a vendor credential (VR-03), and never infers a login from
// the login process's output alone — a login counts as successful only after
// the F9.10a status probe says logged in.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

// LoginCodeMaxBytes is the maximum length of a pasted browser login code.
const LoginCodeMaxBytes = 1024

// Login progress states on the `login` topic. The first two are intermediate
// (published latest-for-key, coalescable); the last five are final outcomes.
const (
	LoginStarted       = "started"
	LoginWaiting       = "waiting"
	LoginSucceeded     = "succeeded"
	LoginFailed        = "failed"
	LoginCancelled     = "cancelled"
	LoginTimedOut      = "timed_out"
	LoginNeedsTerminal = "needs_terminal"
)

// LoginProgress is the topic shape of one login attempt. Prompt is what the
// CLI printed, relayed verbatim: untrusted data that the client shows and
// that never reaches logs, errors or audit metadata.
type LoginProgress struct {
	Vendor  string `json:"vendor"`
	LoginID string `json:"login_id"`
	State   string `json:"state"`
	Prompt  string `json:"prompt,omitempty"`
}

// LoginView is the safe result of StartLogin. The login itself is followed on
// the `login` topic: it runs long after the RPC returns.
type LoginView struct {
	LoginID string `json:"login_id"`
	Vendor  string `json:"vendor"`
	State   string `json:"state"`
}

type loginTopicSnapshot struct {
	Logins []LoginProgress `json:"logins"`
}

// loginRun is one running login attempt: the reserved per-vendor slot, the
// process handle once the spawn returned, and the relayed prompt.
type loginRun struct {
	id            string
	vendor        string
	principal     string
	needsTerminal bool
	acceptsCode   bool

	cancelOnce sync.Once
	cancel     chan struct{}
	done       chan struct{}

	stdinMu sync.Mutex
	mu      sync.Mutex
	run     vendor.LoginRun
	prompt  strings.Builder
}

type loginCodeResult struct {
	loginID   string
	principal string
	err       error
}

// StartLogin runs the vendor CLI's own login command for the local principal
// (F9.10b, VR-03). At most one login per vendor runs at a time; a second
// StartLogin for the same vendor is a typed AlreadyExists. A vendor CLI with
// no login command (agy, Q-112) answers a typed FailedPrecondition that
// points the user at the CLI itself. The RPC returns at once; the progress
// streams on the `login` topic and the login outlives the RPC.
func (s *AccountsService) StartLogin(ctx context.Context, principal, vendorName string) (LoginView, error) {
	if !IsLocalPrincipal(principal) {
		return LoginView{}, errcode.New(errcode.PermissionDenied, "only local principals may use accounts")
	}
	if s.goTask == nil {
		return LoginView{}, errcode.New(errcode.Unavailable, "the host did not provide a task spawner")
	}
	ad, err := s.vendors.adapterFor(vendorName)
	if err != nil {
		return LoginView{}, err
	}
	starter, ok := ad.(vendor.LoginStarter)
	if !ok {
		return LoginView{}, errcode.New(errcode.FailedPrecondition, vendorName+" has no login command; sign in from the "+vendorName+" app")
	}
	// The identity is allocated before anything spawns, so a failed allocation
	// can never leave a login process running.
	id, err := s.ids.NewID()
	if err != nil {
		return LoginView{}, errcode.Wrap(errcode.Internal, "allocate a login identity", err)
	}
	cmd := starter.LoginCommand()
	slot := &loginRun{
		id:            id,
		vendor:        vendorName,
		principal:     principal,
		needsTerminal: cmd.NeedsTerminal,
		acceptsCode:   cmd.AcceptsCode,
		cancel:        make(chan struct{}),
		done:          make(chan struct{}),
	}
	s.loginsMu.Lock()
	if cur, running := s.logins[vendorName]; running {
		s.loginsMu.Unlock()
		return LoginView{}, errcode.New(errcode.AlreadyExists, "a login is already running for "+vendorName+" (login "+cur.id+")")
	}
	s.logins[vendorName] = slot
	s.loginsMu.Unlock()
	// The spawn work (binary resolution can probe the CLI) must not be cut
	// short by the RPC's return; the process outlives the call like a session.
	spawnCtx := context.WithoutCancel(ctx)
	run, err := starter.StartLogin(spawnCtx, vendor.AccountHome{})
	if err != nil {
		s.loginsMu.Lock()
		delete(s.logins, vendorName)
		s.loginsMu.Unlock()
		close(slot.done)
		return LoginView{}, errcode.Wrap(errcode.CodeOf(err), "start the "+vendorName+" login", err)
	}
	slot.mu.Lock()
	slot.run = run
	slot.mu.Unlock()
	s.publishLogin(slot.progress(LoginStarted), true)
	s.goTask("app.login."+vendorName, func(taskCtx context.Context) error {
		s.monitorLogin(taskCtx, slot)
		return nil
	})
	return LoginView{LoginID: id, Vendor: vendorName, State: LoginStarted}, nil
}

// CancelLogin stops the running login process of one vendor and returns once
// it has ended. A vendor with no running login is a typed NotFound.
func (s *AccountsService) CancelLogin(ctx context.Context, principal, vendorName string) error {
	if !IsLocalPrincipal(principal) {
		return errcode.New(errcode.PermissionDenied, "only local principals may use accounts")
	}
	s.loginsMu.Lock()
	slot, ok := s.logins[vendorName]
	s.loginsMu.Unlock()
	if !ok {
		return errcode.New(errcode.NotFound, "no login is running for "+vendorName)
	}
	slot.requestCancel()
	ctx, cancel := context.WithTimeout(ctx, LoginCancelLimit)
	defer cancel()
	select {
	case <-slot.done:
		return nil
	case <-ctx.Done():
		return errcode.Wrap(errcode.DeadlineExceeded, "stop the "+vendorName+" login process", ctx.Err())
	}
}

// requestCancel asks the monitor to stop the process; safe to call twice.
func (slot *loginRun) requestCancel() {
	slot.cancelOnce.Do(func() { close(slot.cancel) })
}

// appendPrompt relays one printed line verbatim into the bounded prompt and
// reports whether the prompt changed. The limit is checked before anything
// is written (separator included), so a flooding CLI can neither grow the
// prompt nor make the host publish an event for a dropped line.
func (slot *loginRun) appendPrompt(line string) bool {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	need := len(line)
	if slot.prompt.Len() > 0 {
		need++
	}
	if slot.prompt.Len()+need > LoginPromptLimit {
		return false
	}
	if slot.prompt.Len() > 0 {
		slot.prompt.WriteByte('\n')
	}
	slot.prompt.WriteString(line)
	return true
}

func (slot *loginRun) promptText() string {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	return slot.prompt.String()
}

func (slot *loginRun) progress(state string) LoginProgress {
	return LoginProgress{Vendor: slot.vendor, LoginID: slot.id, State: state, Prompt: slot.promptText()}
}

// runningProgress is the topic-snapshot view of a login that may still run.
func (slot *loginRun) runningProgress() LoginProgress {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	state := LoginStarted
	if slot.run != nil && slot.prompt.Len() > 0 {
		state = LoginWaiting
	}
	return LoginProgress{Vendor: slot.vendor, LoginID: slot.id, State: state, Prompt: slot.prompt.String()}
}

// monitorLogin follows one login process until it ends, the timeout fires,
// the user cancels or the host stops, then releases the slot and publishes the
// final outcome.
func (s *AccountsService) monitorLogin(ctx context.Context, slot *loginRun) {
	defer close(slot.done)
	defer s.releaseLoginSlot(slot)
	timer := s.clk.NewTimer(LoginTimeout)
	defer timer.Stop()
	lines := slot.run.Lines()
	var outcome string
	for outcome == "" {
		var shutdown bool
		outcome, shutdown = s.stepLoginMonitor(ctx, slot, &lines, timer)
		if shutdown {
			return
		}
	}
	// Release the per-vendor slot before publishing the terminal outcome so a
	// subscriber reacting immediately with StartLogin sees the slot free.
	s.releaseLoginSlot(slot)
	s.publishLogin(slot.progress(outcome), false)
}

func (s *AccountsService) stepLoginMonitor(ctx context.Context, slot *loginRun, lines *<-chan string, timer clock.Timer) (outcome string, shutdown bool) {
	select {
	case line, ok := <-*lines:
		if !ok {
			// stdout ended before the process: keep waiting for the exit.
			*lines = nil
			return "", false
		}
		if slot.appendPrompt(line) {
			s.publishLogin(slot.progress(LoginWaiting), true)
		}
		return "", false
	case <-slot.run.Done():
		// Done may close before pumpStream drains buffered lines or closes Lines();
		// finish draining before collecting the exit outcome and publishing.
		override, stop := s.drainLoginLines(ctx, slot, *lines, timer)
		if stop {
			return "", true
		}
		if override != "" {
			return override, false
		}
		return s.loginOutcome(ctx, slot), false
	case <-timer.C():
		slot.stop(ctx, s.log())
		<-slot.run.Done()
		return slot.timeoutOutcome(), false
	case <-slot.cancel:
		slot.stop(ctx, s.log())
		<-slot.run.Done()
		return LoginCancelled, false
	case <-ctx.Done():
		// The host is stopping: stop the process like a session and end
		// without a final event, the ephemeral topic dies with the host.
		slot.stop(ctx, s.log())
		return "", true
	}
}

func (s *AccountsService) drainLoginLines(ctx context.Context, slot *loginRun, lines <-chan string, timer clock.Timer) (outcome string, shutdown bool) {
	for lines != nil {
		select {
		case line, ok := <-lines:
			if !ok {
				return "", false
			}
			if slot.appendPrompt(line) {
				s.publishLogin(slot.progress(LoginWaiting), true)
			}
		case <-timer.C():
			return slot.timeoutOutcome(), false
		case <-slot.cancel:
			return LoginCancelled, false
		case <-ctx.Done():
			return "", true
		}
	}
	return "", false
}

// Q-112: a CLI that would read a pasted code from its own stdin cannot be
// finished by the host, so the timeout asks the user to run the login in their
// own terminal instead.
func (slot *loginRun) timeoutOutcome() string {
	if slot.needsTerminal {
		return LoginNeedsTerminal
	}
	return LoginTimedOut
}

// loginOutcome classifies how a login process ended. An exit code alone is
// never success: the F9.10a status probe must say logged in first (VR-03),
// and any end the OS cannot explain is a failure.
func (s *AccountsService) loginOutcome(ctx context.Context, slot *loginRun) string {
	end := slot.run.End()
	if !end.Ran || end.ExitCode != 0 {
		return LoginFailed
	}
	probeCtx, cancel := context.WithTimeout(ctx, AccountsRefreshLimit)
	defer cancel()
	s.vendors.refreshLogin(probeCtx, slot.vendor)
	if account, ok := s.accounts.Account(slot.vendor); ok && account.Login == vendor.LoginActive {
		return LoginSucceeded
	}
	return LoginFailed
}

func (s *AccountsService) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// stop runs the vendor-runtime §8 stop order on the login process group.
func (slot *loginRun) stop(ctx context.Context, logger *slog.Logger) {
	stopCtx, cancel := context.WithTimeout(ctx, LoginCancelLimit)
	defer cancel()
	slot.mu.Lock()
	run := slot.run
	slot.mu.Unlock()
	if run == nil {
		return
	}
	if _, err := run.Stop(stopCtx); err != nil {
		logger.WarnContext(stopCtx, "stop a login process", "vendor", slot.vendor, "code", errcode.CodeOf(err))
	}
}

// releaseLoginSlot removes the slot from the running login map.
func (s *AccountsService) releaseLoginSlot(slot *loginRun) {
	s.loginsMu.Lock()
	if cur, ok := s.logins[slot.vendor]; ok && cur == slot {
		delete(s.logins, slot.vendor)
	}
	s.loginsMu.Unlock()
}

// publishLogin sends one login progress event. Intermediate states are
// latest-for-key (coalescable); a final outcome is a control event the hub
// never drops, so a subscriber can never miss how a login ended.
func (s *AccountsService) publishLogin(p LoginProgress, latest bool) {
	payload, err := json.Marshal(p)
	if err != nil {
		s.log().Warn("marshal a login event", "code", errcode.CodeOf(err))
		return
	}
	if _, err := s.hub.Publish(TopicLogin, Event{Type: EventLoginProgress, Key: p.Vendor, Payload: payload, LatestForKey: latest}); err != nil {
		s.log().Warn("publish a login event", "code", errcode.CodeOf(err))
	}
}

// loginSnapshot is the `login` topic snapshot: the running attempts. It reads
// memory only, as an ephemeral snapshot must.
func (s *AccountsService) loginSnapshot(context.Context) ([]byte, error) {
	s.loginsMu.Lock()
	views := make([]LoginProgress, 0, len(s.logins))
	for _, slot := range s.logins {
		views = append(views, slot.runningProgress())
	}
	s.loginsMu.Unlock()
	raw, err := json.Marshal(loginTopicSnapshot{Logins: views})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal the login topic snapshot", err)
	}
	return raw, nil
}
