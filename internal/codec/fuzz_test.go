package codec

import (
	"testing"
	"unicode/utf8"
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
		err := ValidateCanonicalJSON(data)
		if err == nil {
			var dst any
			if unmarshalErr := UnmarshalCanonical(data, &dst); unmarshalErr != nil {
				t.Fatalf("UnmarshalCanonical failed on valid JSON: %v", unmarshalErr)
			}
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
		if err := UnmarshalCanonical(encoded, &decoded); err != nil {
			t.Fatalf("failed to unmarshal valid canonical JSON: %v", err)
		}
		if decoded.Name != orig.Name || decoded.Count != orig.Count || decoded.Enabled != orig.Enabled {
			t.Fatalf("round-trip mismatch: got %+v, want %+v", decoded, orig)
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
		if _, err := DecodeCommand(kind, data); err != nil {
			// Decoding errors on fuzz data are expected; verify no panic occurred.
			return
		}
	})
}
