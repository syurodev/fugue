package codec

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	dominanceIgnored      = "shadowed"
	dominanceSelected     = "selected"
	dominanceSelectedJSON = `{"Value":"selected"}`
)

type dominancePlain struct{ Value string }

type dominanceTagged struct {
	Chosen string `json:"Value"`
}

type dominanceOtherPlain struct{ Value string }

type dominanceCase struct {
	name string
	body any
	want string
}

func taggedDominanceCases() []dominanceCase {
	return []dominanceCase{
		{"tagged last", struct {
			dominancePlain
			dominanceTagged
		}{dominancePlain{dominanceIgnored}, dominanceTagged{"selected"}}, dominanceSelectedJSON},
		{"tagged first", struct {
			dominanceTagged
			dominancePlain
		}{dominanceTagged{dominanceSelected}, dominancePlain{dominanceIgnored}}, dominanceSelectedJSON},
		{"tagged resolves untagged ambiguity", struct {
			dominancePlain
			dominanceOtherPlain
			dominanceTagged
		}{dominancePlain{dominanceIgnored}, dominanceOtherPlain{dominanceIgnored}, dominanceTagged{"selected"}}, dominanceSelectedJSON},
		{"untagged cannot resolve tagged ambiguity", ambiguousTaggedBody(true), `{}`},
	}
}

func ambiguousDominanceCases() []dominanceCase {
	return []dominanceCase{
		{"two tagged", ambiguousTaggedBody(false), `{}`},
		{"two untagged", struct {
			dominancePlain
			dominanceOtherPlain
		}{dominancePlain{dominanceIgnored}, dominanceOtherPlain{dominanceIgnored}}, `{}`},
		{"shallow untagged beats deeper tagged", struct {
			dominanceTagged
			Value string
		}{dominanceTagged{dominanceIgnored}, "shallow"}, `{"Value":"shallow"}`},
	}
}

// Runtime types let us exercise intentionally conflicting tags without a
// compile-time structtag finding from go vet.
func ambiguousTaggedBody(includePlain bool) any {
	fields := []reflect.StructField{
		{Name: "First", Type: reflect.TypeFor[string](), Tag: `json:"Value"`},
		{Name: "Second", Type: reflect.TypeFor[string](), Tag: `json:"Value"`},
	}
	if includePlain {
		fields = append(fields, reflect.StructField{Name: "Value", Type: reflect.TypeFor[string]()})
	}
	return reflect.New(reflect.StructOf(fields)).Elem().Interface()
}

func TestCanonicalFieldTagDominance(t *testing.T) {
	t.Parallel()
	cases := append(taggedDominanceCases(), ambiguousDominanceCases()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertCanonicalFieldDominance(t, tc)
		})
	}
}

func assertCanonicalFieldDominance(t *testing.T, tc dominanceCase) {
	t.Helper()
	encoded, err := MarshalCanonical(tc.body)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != tc.want {
		t.Fatalf("encoding = %s, want %s", encoded, tc.want)
	}
	decoded := reflect.New(reflect.TypeOf(tc.body)).Interface()
	if err := UnmarshalCanonical(encoded, decoded); err != nil {
		t.Fatal(err)
	}
	standard := reflect.New(reflect.TypeOf(tc.body)).Interface()
	if err := json.Unmarshal(encoded, standard); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(standard, decoded, cmp.AllowUnexported(tc.body)); diff != "" {
		t.Fatalf("field selection differs from encoding/json (-want +got):\n%s", diff)
	}
	if tc.want == `{}` {
		err := UnmarshalCanonical([]byte(`{"Value":"ambiguous"}`), decoded)
		if !errors.Is(err, ErrUnknownField) {
			t.Fatalf("ambiguous field error = %v, want ErrUnknownField", err)
		}
	}
}
