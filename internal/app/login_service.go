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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/process"
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

// SubmitLoginCode relays one pasted browser login code to the running login
// process's stdin pipe (host-api v0.22 §5.5). Only the principal that started
// the login may submit, only while it runs, and only for an AcceptsCode vendor.
func (s *AccountsService) SubmitLoginCode(ctx context.Context, principal, commandID, loginID string, code redact.Secret) error {
	if !IsLocalPrincipal(principal) {
		return errcode.New(errcode.PermissionDenied, "only local principals may use accounts")
	}
	if commandID == "" {
		return errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	if loginID == "" {
		return errcode.New(errcode.InvalidArgument, "login_id is required")
	}
	if err := validateLoginCode(code); err != nil {
		return err
	}

	s.loginCmdMu.Lock()
	if prev, ok := s.loginDedup.Get(commandID); ok {
		s.loginCmdMu.Unlock()
		if prev.principal != principal {
			return errcode.New(errcode.PermissionDenied, "login belongs to another principal")
		}
		if prev.loginID != loginID {
			return errcode.New(errcode.InvalidArgument, "command_id was previously used with a different login_id")
		}
		return prev.err
	}
	if inf, ok := s.loginInflight[commandID]; ok {
		s.loginCmdMu.Unlock()
		return awaitLoginInflight(ctx, principal, loginID, inf)
	}
	inf := &inflightCommand[loginCodeResult]{done: make(chan struct{})}
	s.loginInflight[commandID] = inf
	s.loginCmdMu.Unlock()

	res := loginCodeResult{loginID: loginID, principal: principal}
	defer func() {
		s.loginCmdMu.Lock()
		delete(s.loginInflight, commandID)
		inf.result = res
		inf.err = res.err
		close(inf.done)
		s.loginCmdMu.Unlock()
	}()

	res.err = s.executeSubmitLoginCode(principal, loginID, code)
	s.loginDedup.Put(commandID, res)
	return res.err
}

func awaitLoginInflight(ctx context.Context, principal, loginID string, inf *inflightCommand[loginCodeResult]) error {
	select {
	case <-inf.done:
		if inf.result.principal != principal {
			return errcode.New(errcode.PermissionDenied, "login belongs to another principal")
		}
		if inf.result.loginID != loginID {
			return errcode.New(errcode.InvalidArgument, "command_id was previously used with a different login_id")
		}
		return inf.err
	case <-ctx.Done():
		return fmt.Errorf("context canceled: %w", ctx.Err())
	}
}

// validateLoginCode checks the length cap and printable-ASCII constraint on
// a temporary byte copy of code and zeroes that copy before returning (Go
// strings in the RPC request and redact.Secret cannot be zeroed).
func validateLoginCode(code redact.Secret) error {
	buf := []byte(code.Reveal())
	defer clear(buf)
	if len(buf) == 0 {
		return errcode.New(errcode.InvalidArgument, "login code is required")
	}
	if len(buf) > LoginCodeMaxBytes {
		return errcode.New(errcode.InvalidArgument, "login code exceeds 1 KiB limit")
	}
	for _, b := range buf {
		if b < 0x20 || b > 0x7e {
			return errcode.New(errcode.InvalidArgument, "login code must contain printable ASCII only")
		}
	}
	return nil
}

func (s *AccountsService) executeSubmitLoginCode(principal, loginID string, code redact.Secret) error {
	slot := s.runningLoginByID(loginID)
	if slot == nil {
		return errcode.New(errcode.FailedPrecondition, "login is not running")
	}
	if slot.principal != principal {
		return errcode.New(errcode.PermissionDenied, "login belongs to another principal")
	}
	if !slot.acceptsCode {
		return errcode.New(errcode.FailedPrecondition, "vendor login does not accept a pasted code")
	}
	slot.stdinMu.Lock()
	defer slot.stdinMu.Unlock()
	slot.mu.Lock()
	run := slot.run
	slot.mu.Unlock()
	if run == nil {
		return errcode.New(errcode.FailedPrecondition, "login process is not running")
	}
	if err := run.WriteCode(code); err != nil {
		if errors.Is(err, process.ErrStdinClosed) {
			return errcode.Wrap(errcode.FailedPrecondition, "login process is no longer accepting input", err)
		}
		return errcode.Wrap(errcode.CodeOf(err), "write login code to process", err)
	}
	return nil
}

func (s *AccountsService) runningLoginByID(loginID string) *loginRun {
	s.loginsMu.Lock()
	defer s.loginsMu.Unlock()
	for _, slot := range s.logins {
		if slot.id == loginID {
			return slot
		}
	}
	return nil
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
// the user cancels or the host stops, then publishes the final outcome.
func (s *AccountsService) monitorLogin(ctx context.Context, slot *loginRun) {
	defer s.finishLogin(slot)
	timer := s.clk.NewTimer(LoginTimeout)
	defer timer.Stop()
	lines := slot.run.Lines()
	var outcome string
	for outcome == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				// stdout ended before the process: keep waiting for the exit.
				lines = nil
				continue
			}
			if slot.appendPrompt(line) {
				s.publishLogin(slot.progress(LoginWaiting), true)
			}
		case <-slot.run.Done():
			outcome = s.loginOutcome(ctx, slot)
		case <-timer.C():
			slot.stop(ctx, s.log())
			<-slot.run.Done()
			// Q-112: a CLI that would read a pasted code from its own stdin
			// cannot be finished by the host, so the timeout asks the user to
			// run the login in their own terminal instead.
			if slot.needsTerminal {
				outcome = LoginNeedsTerminal
			} else {
				outcome = LoginTimedOut
			}
		case <-slot.cancel:
			slot.stop(ctx, s.log())
			<-slot.run.Done()
			outcome = LoginCancelled
		case <-ctx.Done():
			// The host is stopping: stop the process like a session and end
			// without a final event, the ephemeral topic dies with the host.
			slot.stop(ctx, s.log())
			return
		}
	}
	s.publishLogin(slot.progress(outcome), false)
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

// finishLogin removes the slot and releases CancelLogin waiters.
func (s *AccountsService) finishLogin(slot *loginRun) {
	s.loginsMu.Lock()
	if cur, ok := s.logins[slot.vendor]; ok && cur == slot {
		delete(s.logins, slot.vendor)
	}
	s.loginsMu.Unlock()
	close(slot.done)
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
