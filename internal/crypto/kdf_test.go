package crypto

import (
	"bytes"
	"io"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func minimalParams(t *testing.T) KDFParams {
	t.Helper()
	params, err := newKDFParams(newStream())
	if err != nil {
		t.Fatal(err)
	}
	params.Memory = kdfMinMemoryKiB
	params.Iterations = kdfMinIterations
	params.Parallelism = kdfMinParallelism
	return params
}

func TestDeriveKeyIsDeterministic(t *testing.T) {
	t.Parallel()
	params := minimalParams(t)
	first, err := DeriveKey("correct horse", params)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DeriveKey("correct horse", params)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("the same password and parameters derived different keys")
	}
	if len(first) != int(params.KeyLength) {
		t.Errorf("key length = %d, want %d", len(first), params.KeyLength)
	}
}

func TestDeriveKeyDiffersByPasswordAndSalt(t *testing.T) {
	t.Parallel()
	params := minimalParams(t)
	base, err := DeriveKey("pw", params)
	if err != nil {
		t.Fatal(err)
	}
	otherPassword, err := DeriveKey("pw2", params)
	if err != nil {
		t.Fatal(err)
	}
	params.Salt = bytes.Repeat([]byte{9}, 16)
	otherSalt, err := DeriveKey("pw", params)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base, otherPassword) || bytes.Equal(base, otherSalt) {
		t.Error("a different password or salt derived the same key")
	}
}

func TestDeriveKeyRequiresAPassword(t *testing.T) {
	t.Parallel()
	_, err := DeriveKey("", minimalParams(t))
	wantCode(t, err, errcode.InvalidArgument)
}

func TestDeriveKeyRejectsUnsafeParameters(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*KDFParams){
		"another algorithm":    func(p *KDFParams) { p.Algorithm = "scrypt" },
		"no algorithm":         func(p *KDFParams) { p.Algorithm = "" },
		"memory too low":       func(p *KDFParams) { p.Memory = kdfMinMemoryKiB - 1 },
		"memory too high":      func(p *KDFParams) { p.Memory = kdfMaxMemoryKiB + 1 },
		"iterations too low":   func(p *KDFParams) { p.Iterations = kdfMinIterations - 1 },
		"iterations too high":  func(p *KDFParams) { p.Iterations = kdfMaxIterations + 1 },
		"parallelism zero":     func(p *KDFParams) { p.Parallelism = 0 },
		"parallelism too high": func(p *KDFParams) { p.Parallelism = kdfMaxParallelism + 1 },
		"key length too low":   func(p *KDFParams) { p.KeyLength = kdfMinKeyLength - 1 },
		"key length too high":  func(p *KDFParams) { p.KeyLength = kdfMaxKeyLength + 1 },
		"no salt":              func(p *KDFParams) { p.Salt = nil },
		"salt too short":       func(p *KDFParams) { p.Salt = make([]byte, kdfMinSaltLength-1) },
		"salt too long":        func(p *KDFParams) { p.Salt = make([]byte, kdfMaxSaltLength+1) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			params := minimalParams(t)
			mutate(&params)
			_, err := DeriveKey("password", params)
			wantCode(t, err, errcode.InvalidArgument)
		})
	}
}

// Validating is cheap; deriving at the upper bounds would allocate 256 MiB.
func TestValidateAcceptsTheBoundsOfEveryField(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*KDFParams){
		"lowest memory":       func(p *KDFParams) { p.Memory = kdfMinMemoryKiB },
		"highest memory":      func(p *KDFParams) { p.Memory = kdfMaxMemoryKiB },
		"lowest iterations":   func(p *KDFParams) { p.Iterations = kdfMinIterations },
		"highest iterations":  func(p *KDFParams) { p.Iterations = kdfMaxIterations },
		"lowest parallelism":  func(p *KDFParams) { p.Parallelism = kdfMinParallelism },
		"highest parallelism": func(p *KDFParams) { p.Parallelism = kdfMaxParallelism },
		"lowest key length":   func(p *KDFParams) { p.KeyLength = kdfMinKeyLength },
		"highest key length":  func(p *KDFParams) { p.KeyLength = kdfMaxKeyLength },
		"shortest salt":       func(p *KDFParams) { p.Salt = make([]byte, kdfMinSaltLength) },
		"longest salt":        func(p *KDFParams) { p.Salt = make([]byte, kdfMaxSaltLength) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			params := minimalParams(t)
			mutate(&params)
			if err := params.validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDefaultKDFParamsUsesTheInteractiveProfileAndAFreshSalt(t *testing.T) {
	t.Parallel()
	first, err := DefaultKDFParams()
	if err != nil {
		t.Fatal(err)
	}
	second, err := DefaultKDFParams()
	if err != nil {
		t.Fatal(err)
	}
	if first.Algorithm != AlgorithmArgon2id || first.Memory != 64*1024 || first.Iterations != 3 ||
		first.Parallelism != 4 || first.KeyLength != 32 || len(first.Salt) != 16 {
		t.Errorf("profile = %+v", first)
	}
	if bytes.Equal(first.Salt, second.Salt) {
		t.Error("two calls returned the same salt")
	}
	if err := first.validate(); err != nil {
		t.Error(err)
	}
}

func TestNewKDFParamsFailsWhenTheRandomSourceFails(t *testing.T) {
	t.Parallel()
	_, err := newKDFParams(io.LimitReader(newStream(), 3))
	wantCode(t, err, errcode.Internal)
}
