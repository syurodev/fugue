package cmdid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"sync"
)

// processIdentity names this app process in command ids, together with a
// per-minter sequence: hostnames and process ids are unique while a process
// runs, so two minters — here or in another app process — never mint the same
// id, which the host would refuse as a conflict.
func processIdentity() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

// New returns a command-id minter: a hash of this process's identity and a
// sequence number, unique per command across every app process — two commands
// never share an id, which the host would refuse as a conflict — without
// reaching for randomness.
func New() func() string {
	return newWithIdentity(processIdentity())
}

func newWithIdentity(identity string) func() string {
	var mu sync.Mutex
	var seq uint64
	return func() string {
		mu.Lock()
		seq++
		n := seq
		mu.Unlock()
		sum := sha256.Sum256([]byte(identity + ":" + strconv.FormatUint(n, 10)))
		return hex.EncodeToString(sum[:16])
	}
}
