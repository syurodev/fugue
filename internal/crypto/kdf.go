package crypto

import (
	"cmp"
	"crypto/rand"
	"io"

	"golang.org/x/crypto/argon2"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// AlgorithmArgon2id names the only supported key derivation function.
const AlgorithmArgon2id = "argon2id"

// The bounds accept the interactive profile while keeping a tampered keyring
// from requesting a multi-gigabyte allocation or excessive CPU work during
// unlock on a desktop machine.
const (
	kdfMinMemoryKiB   uint32 = 32768  // 32 MiB
	kdfMaxMemoryKiB   uint32 = 262144 // 256 MiB
	kdfMinIterations  uint32 = 2
	kdfMaxIterations  uint32 = 6
	kdfMinParallelism uint8  = 1
	kdfMaxParallelism uint8  = 8
	kdfMinKeyLength   uint32 = 16
	kdfMaxKeyLength   uint32 = 64
	kdfMinSaltLength         = 8
	kdfMaxSaltLength         = 64
)

// KDFParams holds the Argon2id parameters and the salt. Persist it next to the
// wrapped key so derivation stays reproducible after upgrades.
type KDFParams struct {
	Algorithm   string `json:"algorithm"`
	Memory      uint32 `json:"memory"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
	KeyLength   uint32 `json:"key_length"`
	Salt        []byte `json:"salt"`
}

// DefaultKDFParams returns interactive-strength Argon2id parameters (64 MiB,
// 3 passes, 4 lanes) with a fresh random 16-byte salt. The cost is fixed;
// changing it is a format change that needs an ADR.
func DefaultKDFParams() (KDFParams, error) {
	return newKDFParams(rand.Reader)
}

func newKDFParams(random io.Reader) (KDFParams, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(random, salt); err != nil {
		return KDFParams{}, errcode.Wrap(errcode.Internal, "random source failed", err)
	}
	return KDFParams{
		Algorithm:   AlgorithmArgon2id,
		Memory:      64 * 1024,
		Iterations:  3,
		Parallelism: 4,
		KeyLength:   32,
		Salt:        salt,
	}, nil
}

// DeriveKey derives a key of params.KeyLength bytes from the password.
func DeriveKey(password string, params KDFParams) ([]byte, error) {
	if password == "" {
		return nil, errcode.New(errcode.InvalidArgument, "password is required")
	}
	if err := params.validate(); err != nil {
		return nil, err
	}
	return argon2.IDKey([]byte(password), params.Salt, params.Iterations, params.Memory, params.Parallelism, params.KeyLength), nil
}

func (p KDFParams) validate() error {
	if p.Algorithm != AlgorithmArgon2id {
		return errcode.New(errcode.InvalidArgument, "kdf algorithm is unsupported")
	}
	if !within(p.Memory, kdfMinMemoryKiB, kdfMaxMemoryKiB) {
		return errcode.New(errcode.InvalidArgument, "kdf memory is out of the allowed range")
	}
	if !within(p.Iterations, kdfMinIterations, kdfMaxIterations) {
		return errcode.New(errcode.InvalidArgument, "kdf iterations are out of the allowed range")
	}
	if !within(p.Parallelism, kdfMinParallelism, kdfMaxParallelism) {
		return errcode.New(errcode.InvalidArgument, "kdf parallelism is out of the allowed range")
	}
	if !within(p.KeyLength, kdfMinKeyLength, kdfMaxKeyLength) {
		return errcode.New(errcode.InvalidArgument, "kdf key length is out of the allowed range")
	}
	if !within(len(p.Salt), kdfMinSaltLength, kdfMaxSaltLength) {
		return errcode.New(errcode.InvalidArgument, "kdf salt length is out of the allowed range")
	}
	return nil
}

func within[T cmp.Ordered](value, low, high T) bool {
	return value >= low && value <= high
}
