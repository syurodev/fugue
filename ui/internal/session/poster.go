package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

// postBuffer bounds the write queue. A write dropped because the queue is
// full reports a failure to the composer rather than vanishing: the draft is
// the user's words, and the composer restores it from the failed post.
const postBuffer = 32

// processIdentity names this app process in the command ids, together with
// the poster's sequence: hostnames and process ids are unique while a process
// runs, so two posters — here or in another app process — never mint the same
// id, which the host would refuse as a conflict.
func processIdentity() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

// PostKind names the write a poster runs.
type PostKind uint8

// The write kinds, the composer's actions (F10.4b).
const (
	PostStart PostKind = iota
	PostResume
	PostSend
	PostSteer
	PostInterrupt
)

// PostResult is one finished write, delivered to the window goroutine.
type PostResult struct {
	Kind PostKind
	// Session is the session the write acted on, and Started the session a
	// start or resume created.
	Session, Started string
	// Text is the input a failed send or steer carried, so the composer can
	// put the user's words back.
	Text string
	// Done reports an interrupt whose stop request was delivered inside the
	// deadline; false means the stop keeps running on the host and the
	// turn's end arrives on the session's topics.
	Done bool
	// Err is the write's failure, if any.
	Err error
}

// postOp is one queued write with the command id that deduplicates it on the
// host: every post mints its own id, so a retry by the user is a new command
// and never attaches to a finished operation (the host attaches a retry to
// the original only when the caller repeats the same id).
type postOp struct {
	kind    PostKind
	session string
	text    string
	vendor  string
	// workspace and root name a start chooses; a resume leaves them empty
	// and the host uses the session's stored binding.
	workspace, root string
	profile         client.SessionProfile
	commandID       string
}

// Poster performs the composer's writes off the window goroutine: one
// goroutine takes posts, runs the blocking pkg/client calls against the
// manager's current connection, and hands the results back. A post while the
// host is unreachable fails at once instead of vanishing, so the composer can
// put the draft back.
type Poster struct {
	clientOf func() *client.Client
	newID    func() string
	ops      chan *postOp
	results  chan PostResult
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewPoster builds a poster that writes through clientOf, which returns the
// manager's current connection or nil while disconnected.
func NewPoster(clientOf func() *client.Client) *Poster {
	return &Poster{
		clientOf: clientOf,
		newID:    newCommandIDs(),
		ops:      make(chan *postOp, postBuffer),
		results:  make(chan PostResult, postBuffer),
		done:     make(chan struct{}),
	}
}

// Start runs the poster's goroutine. It ends with Stop or the context.
func (p *Poster) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.run(runCtx)
}

// Stop ends the poster's goroutine and waits for it. It is idempotent.
func (p *Poster) Stop() {
	p.stopOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
	})
	<-p.done
}

// Results receives the finished writes. The window goroutine drains it every
// frame.
func (p *Poster) Results() <-chan PostResult { return p.results }

// StartSession posts a session start in a workspace root (Q-53 A carries the
// profile's client-chosen values).
func (p *Poster) StartSession(vendor, workspaceID, rootName string, profile client.SessionProfile) {
	p.queue(&postOp{kind: PostStart, vendor: vendor, workspace: workspaceID, root: rootName, profile: profile})
}

// ResumeSession posts a resume of a detached session. The workspace and root
// stay empty: the session is bound to the ones it was created in, and the
// host uses its stored binding.
func (p *Poster) ResumeSession(sessionID string, profile client.SessionProfile) {
	p.queue(&postOp{kind: PostResume, session: sessionID, profile: profile})
}

// Send posts one user message or slash command to a ready session.
func (p *Poster) Send(sessionID, text string) {
	p.queue(&postOp{kind: PostSend, session: sessionID, text: text})
}

// Steer posts one input to a running turn.
func (p *Poster) Steer(sessionID, text string) {
	p.queue(&postOp{kind: PostSteer, session: sessionID, text: text})
}

// Interrupt posts the stop of a session's running turn.
func (p *Poster) Interrupt(sessionID string) {
	p.queue(&postOp{kind: PostInterrupt, session: sessionID})
}

// queue hands one post over, failing it at once when the queue is full. The
// command id is minted here, per post, so the id belongs to the logical
// write and not to the moment it runs.
func (p *Poster) queue(op *postOp) {
	op.commandID = p.newID()
	select {
	case p.ops <- op:
	default:
		p.fail(op, errPostQueueFull)
	}
}

// run serves posts until the context ends.
func (p *Poster) run(ctx context.Context) {
	defer close(p.done)
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-p.ops:
			c := p.clientOf()
			if c == nil {
				p.fail(op, client.ErrDisconnected)
				continue
			}
			if !p.deliver(ctx, c, op) {
				return
			}
		}
	}
}

// deliver runs one write and hands its result to the window; it reports
// whether the poster should keep serving.
func (p *Poster) deliver(ctx context.Context, c *client.Client, op *postOp) bool {
	res := PostResult{Kind: op.kind, Session: op.session, Text: op.text}
	var err error
	switch op.kind {
	case PostStart:
		started, startErr := c.StartSession(ctx, op.commandID, op.vendor, op.workspace, op.root, op.profile)
		res.Started, err = started.ID, startErr
	case PostResume:
		started, resumeErr := c.ResumeSession(ctx, op.commandID, op.session, "", "", op.profile)
		res.Started, err = started.ID, resumeErr
	case PostSend:
		err = c.Send(ctx, op.commandID, op.session, op.text)
	case PostSteer:
		err = c.Steer(ctx, op.commandID, op.session, op.text)
	case PostInterrupt:
		done, stopErr := c.Interrupt(ctx, op.commandID, op.session)
		res.Done, err = done, stopErr
	}
	if err != nil {
		res.Err = err
	}
	select {
	case p.results <- res:
		return true
	case <-ctx.Done():
		return false
	}
}

// fail hands back a post that never ran, with the user's text intact.
func (p *Poster) fail(op *postOp, err error) {
	p.deliverResult(&PostResult{Kind: op.kind, Session: op.session, Text: op.text, Err: err})
}

// deliverResult hands one result over, dropping it only when the poster is
// stopping.
func (p *Poster) deliverResult(res *PostResult) {
	select {
	case p.results <- *res:
	default:
	}
}

// newCommandIDs returns the poster's command-id minter: a hash of this
// process's identity and a sequence number, unique per post across every app
// process — two posts never share an id, which the host would refuse as a
// conflict — without reaching for randomness the module does not inject.
func newCommandIDs() func() string {
	identity := processIdentity()
	var seq uint64
	return func() string {
		seq++
		sum := sha256.Sum256([]byte(identity + ":" + strconv.FormatUint(seq, 10)))
		return hex.EncodeToString(sum[:16])
	}
}

// errPostQueueFull is the failure of a post the queue could not take; it is
// wrapped, never constructed, at the call site.
var errPostQueueFull = errors.New("the composer's write queue is full")
