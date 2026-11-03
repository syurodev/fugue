package codec

import (
	"bytes"
	"errors"
	"testing"
)

func TestCanonicalContainerDepthRoundTrip(t *testing.T) {
	t.Parallel()
	for _, depth := range []int{501, maxTraversalDepth, maxTraversalDepth + 1} {
		input := bytes.Repeat([]byte("["), depth)
		input = append(input, '1')
		input = append(input, bytes.Repeat([]byte("]"), depth)...)
		var decoded any
		err := UnmarshalCanonical(input, &decoded)
		if depth > maxTraversalDepth {
			if !errors.Is(err, ErrMalformedJSON) {
				t.Fatalf("over-limit decode error = %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := MarshalCanonical(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, input) {
			t.Fatal("container round-trip changed input")
		}
	}
}

func TestCanonicalIgnoredFields(t *testing.T) {
	t.Parallel()
	type hiddenEmbed struct{ Public string }
	value := struct {
		hiddenEmbed
		Visible      string
		Ignored      string  `json:"-"`
		IgnoredFloat float64 `json:"-"`
		private      string
		privateFloat float64
	}{hiddenEmbed: hiddenEmbed{Public: "public"}, Visible: "visible", Ignored: string([]byte{0xff}), private: string([]byte{0xff}), IgnoredFloat: 1.5, privateFloat: 1.5}
	encoded, err := MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"Public":"public","Visible":"visible"}` {
		t.Fatal("unexpected encoded fields")
	}
	value.Public = string([]byte{0xff})
	if _, err := MarshalCanonical(value); !errors.Is(err, ErrInvalidUTF8) {
		t.Fatalf("embedded exported field not validated: %v", err)
	}
}

func TestCanonicalShadowedFieldsIgnored(t *testing.T) {
	t.Parallel()
	type hiddenEmbed struct{ Value float64 }
	value := struct {
		hiddenEmbed
		Value string
	}{hiddenEmbed: hiddenEmbed{Value: 1.5}, Value: "kept"}
	if _, err := MarshalCanonical(value); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalCyclesRejected(t *testing.T) {
	t.Parallel()
	var value any
	value = &value
	if _, err := MarshalCanonical(value); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("pointer cycle error = %v", err)
	}
}

type (
	embed0  struct{ Value string }
	embed1  struct{ embed0 }
	embed2  struct{ embed1 }
	embed3  struct{ embed2 }
	embed4  struct{ embed3 }
	embed5  struct{ embed4 }
	embed6  struct{ embed5 }
	embed7  struct{ embed6 }
	embed8  struct{ embed7 }
	embed9  struct{ embed8 }
	embed10 struct{ embed9 }
	embed11 struct{ embed10 }
	embed12 struct{ embed11 }
	embed13 struct{ embed12 }
	embed14 struct{ embed13 }
	embed15 struct{ embed14 }
	embed16 struct{ embed15 }
	embed17 struct{ embed16 }
	embed18 struct{ embed17 }
	embed19 struct{ embed18 }
	embed20 struct{ embed19 }
	embed21 struct{ embed20 }
	embed22 struct{ embed21 }
	embed23 struct{ embed22 }
	embed24 struct{ embed23 }
	embed25 struct{ embed24 }
	embed26 struct{ embed25 }
	embed27 struct{ embed26 }
	embed28 struct{ embed27 }
	embed29 struct{ embed28 }
	embed30 struct{ embed29 }
	embed31 struct{ embed30 }
	embed32 struct{ embed31 }
	embed33 struct{ embed32 }
	embed34 struct{ embed33 }
	embed35 struct{ embed34 }
	embed36 struct{ embed35 }
	embed37 struct{ embed36 }
	embed38 struct{ embed37 }
	embed39 struct{ embed38 }
	embed40 struct{ embed39 }
)

func TestCanonicalDeepEmbeddingRoundTrip(t *testing.T) {
	t.Parallel()
	value := embed40{}
	value.Value = "promoted"
	encoded, err := MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded embed40
	if err := UnmarshalCanonical(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Value != value.Value {
		t.Fatal("deeply promoted field lost")
	}
}

func TestCanonicalRecursiveEmbedding(t *testing.T) {
	t.Parallel()
	type recursive struct {
		*recursive
		Value string
	}
	value := recursive{Value: "kept"}
	encoded, err := MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded recursive
	if err := UnmarshalCanonical(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.recursive != nil {
		t.Fatal("recursive embed should remain nil")
	}
	if decoded.Value != value.Value {
		t.Fatal("recursive embedding lost field")
	}
}

func TestCanonicalNilEmbeddedPointer(t *testing.T) {
	t.Parallel()
	type embedded struct{ Value string }
	value := struct{ *embedded }{}
	encoded, err := MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{}` {
		t.Fatal("nil embed should emit no fields")
	}
}

func TestCanonicalRepeatedEmbeddingIgnored(t *testing.T) {
	t.Parallel()
	type leaf struct{ Value float64 }
	type left struct{ leaf }
	type right struct{ leaf }
	value := struct {
		left
		right
	}{left: left{leaf{Value: 1.5}}, right: right{leaf{Value: 2.5}}}
	encoded, err := MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{}` {
		t.Fatal("repeated embedding should be ambiguous")
	}
}
