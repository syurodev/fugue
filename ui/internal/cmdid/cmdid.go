package cmdid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
)

// processIdentity names this app process and one minter instance in command
// ids, together with an atomic sequence: hostnames and process ids separate
// processes, the per-minter random nonce separates concurrent minters within
// the same process (such as multiple windows), and the atomic sequence
// separates calls on the same minter.
func processIdentity() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), rand.Text())
}

// New returns a command-id minter: a hash of this process-and-minter identity
// and an atomic sequence number, unique per command across every minter in
// this process and across every app process — two commands never share an id,
// which the host would refuse as a conflict.
func New() func() string {
	var seq atomic.Uint64
	return newWithIdentity(processIdentity(), &seq)
}

func newWithIdentity(identity string, seq *atomic.Uint64) func() string {
	return func() string {
		n := seq.Add(1)
		sum := sha256.Sum256([]byte(identity + ":" + strconv.FormatUint(n, 10)))
		return hex.EncodeToString(sum[:16])
	}
}
