package codec

import (
	"bytes"
	"testing"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/kernel"
)

const (
	fuzzAcceptRun   = "AcceptRun"
	fuzzAdmitRun    = "AdmitRun"
	fuzzRequestStop = "RequestStop"
	fuzzResumeRun   = "ResumeRun"
)

func FuzzCanonicalJSON(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{}`),
		[]byte(`{"a":1,"b":"hello"}`),
		[]byte(`[1,2,3]`),
		[]byte(`{"nested":{"arr":[true,false,null]}}`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`{"val":1.23}`),
		[]byte(`{"num":9223372036854775807}`),
		[]byte(`{"num":-9223372036854775808}`),
		[]byte(`"string scalar"`),
		[]byte(`true`),
		[]byte(`null`),
		[]byte(`{"a":1} trailing`),
		{0xff, 0xfe},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if err := ValidateCanonicalJSON(data); err != nil {
			return
		}
		if !utf8.Valid(data) {
			t.Fatalf("ValidateCanonicalJSON accepted invalid UTF-8 data")
		}
		var dst any
		if unmarshalErr := UnmarshalCanonical(data, &dst); unmarshalErr != nil {
			t.Fatalf("UnmarshalCanonical failed on valid JSON: %v", unmarshalErr)
		}
		if dst == nil {
			return
		}
		reEncoded, marshalErr := MarshalCanonical(dst)
		if marshalErr != nil {
			t.Fatalf("MarshalCanonical failed on unmarshaled valid data: %v", marshalErr)
		}
		if validateErr := ValidateCanonicalJSON(reEncoded); validateErr != nil {
			t.Fatalf("re-encoded data failed ValidateCanonicalJSON: %v", validateErr)
		}
	})
}

func FuzzCanonicalRoundTrip(f *testing.F) {
	f.Add("run-1", 42, true, "alpha")
	f.Add("run-empty", 0, false, "")
	f.Add("run-unicode-🔥", -999, true, "tags-日本語")

	f.Fuzz(func(t *testing.T, name string, count int, enabled bool, tag string) {
		if !utf8.ValidString(name) || !utf8.ValidString(tag) {
			return
		}

		orig := sampleStruct{
			Name:    name,
			Count:   count,
			Enabled: enabled,
			Tags:    []string{tag},
			Meta:    map[string]string{"k": tag},
		}

		encoded, err := MarshalCanonical(orig)
		if err != nil {
			return
		}

		var decoded sampleStruct
		if decErr := UnmarshalCanonical(encoded, &decoded); decErr != nil {
			t.Fatalf("failed to unmarshal valid canonical JSON: %v", decErr)
		}
		if diff := cmp.Diff(orig, decoded); diff != "" {
			t.Fatalf("round-trip mismatch (-want +got):\n%s", diff)
		}
		reEncoded, reErr := MarshalCanonical(decoded)
		if reErr != nil {
			t.Fatalf("failed to re-marshal decoded struct: %v", reErr)
		}
		if !bytes.Equal(encoded, reEncoded) {
			t.Fatalf("canonical encoding not idempotent: %s != %s", encoded, reEncoded)
		}
	})
}

func FuzzDecodeCommand(f *testing.F) {
	kinds := []string{
		fuzzAcceptRun,
		fuzzAdmitRun,
		fuzzRequestStop,
		fuzzResumeRun,
		"NoSuchCommand",
	}
	seeds := [][]byte{
		[]byte(`{}`),
		[]byte(`{"CommandID":"cmd-1","RunID":"run-1"}`),
		[]byte(`{"CommandID":"cmd-1","CommandID":"cmd-2"}`),
		[]byte(`{"Invalid":true}`),
		[]byte(`{"Count":1.5}`),
	}
	for _, k := range kinds {
		for _, s := range seeds {
			f.Add(k, s)
		}
	}

	f.Fuzz(func(t *testing.T, kind string, data []byte) {
		cmd, err := DecodeCommand(kind, data)
		if err != nil {
			return
		}
		assertFuzzCommandInvariants(t, kind, cmd)
	})
}

func assertFuzzCommandInvariants(t *testing.T, kind string, cmd kernel.Command) {
	t.Helper()
	if cmd == nil || isNil(cmd) {
		t.Fatalf("DecodeCommand returned nil command for kind %s without error", kind)
	}
	if cmd.CommandName() != kind {
		t.Fatalf("command kind mismatch: got %s, want %s", cmd.CommandName(), kind)
	}
	encoded, encErr := EncodeCommand(cmd)
	if encErr != nil {
		t.Fatalf("failed to re-encode decoded command %s: %v", kind, encErr)
	}
	if valErr := ValidateCanonicalJSON(encoded); valErr != nil {
		t.Fatalf("re-encoded command is not valid canonical JSON: %v", valErr)
	}
	reDecoded, decErr := DecodeCommand(kind, encoded)
	if decErr != nil {
		t.Fatalf("failed to re-decode re-encoded command %s: %v", kind, decErr)
	}
	if diff := cmp.Diff(cmd, reDecoded); diff != "" {
		t.Fatalf("round-trip command mismatch (-orig +reDecoded):\n%s", diff)
	}
}
