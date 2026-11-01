package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// Random fills p with random bytes. Production code uses CryptoRandom; tests
// use Deterministic.
type Random interface {
	Read(p []byte) error
}

// Generator returns identifiers. They are strings so the kernel, which imports
// nothing, can carry them.
type Generator interface {
	NewID() (string, error)
}

// CryptoRandom reads from the operating system's cryptographic source.
type CryptoRandom struct{}

// Read fills p, failing if the source cannot supply enough bytes.
func (CryptoRandom) Read(p []byte) error {
	if _, err := rand.Read(p); err != nil {
		return fmt.Errorf("read random bytes: %w", err)
	}
	return nil
}

// Deterministic is a repeatable byte stream for tests: the same seed yields the
// same bytes. It is not random and must never feed a key.
type Deterministic struct {
	mu      sync.Mutex
	seed    string
	counter uint64
}

// NewDeterministic returns a stream determined by seed.
func NewDeterministic(seed string) *Deterministic { return &Deterministic{seed: seed} }

// Read fills p from a hash chain over the seed.
func (d *Deterministic) Read(p []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for filled := 0; filled < len(p); {
		var block [8]byte
		binary.BigEndian.PutUint64(block[:], d.counter)
		d.counter++
		sum := sha256.Sum256(append([]byte(d.seed), block[:]...))
		filled += copy(p[filled:], sum[:])
	}
	return nil
}

const (
	uuidBytes   = 16
	uuidText    = 36
	millisBytes = 6
)

// UUIDv7 makes RFC 9562 version 7 identifiers: a 48-bit millisecond timestamp
// followed by random bits, so identifiers sort by creation time. Two identifiers
// made in the same millisecond are ordered only by their random part.
type UUIDv7 struct {
	clock  clock.Clock
	random Random
}

// NewUUIDv7 returns a generator reading time from c and bits from r.
func NewUUIDv7(c clock.Clock, r Random) *UUIDv7 { return &UUIDv7{clock: c, random: r} }

// NewID returns a new identifier in canonical lowercase text form.
func (g *UUIDv7) NewID() (string, error) {
	var b [uuidBytes]byte
	if err := g.random.Read(b[millisBytes:]); err != nil {
		return "", fmt.Errorf("new id: %w", err)
	}
	ms := g.clock.Now().UnixMilli()
	for i := range millisBytes {
		b[millisBytes-1-i] = byte(ms >> (8 * i) & 0xff)
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	return format(b), nil
}

func format(b [uuidBytes]byte) string {
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// IsUUID reports whether s is a canonical lowercase UUID, as the Host API
// requires of a command_id.
func IsUUID(s string) bool {
	if len(s) != uuidText {
		return false
	}
	for i := range len(s) {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isLowerHex(s[i]) {
				return false
			}
		}
	}
	return true
}

func isLowerHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }

// Sequence returns "<prefix>-1", "<prefix>-2", ... for tests that want readable,
// predictable identifiers.
type Sequence struct {
	mu     sync.Mutex
	prefix string
	n      int
}

// NewSequence returns a Sequence with the given prefix.
func NewSequence(prefix string) *Sequence { return &Sequence{prefix: prefix} }

// NewID returns the next identifier.
func (s *Sequence) NewID() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("%s-%d", s.prefix, s.n), nil
}
