package vendor

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestFugueNamespaceInputIsRejected(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"/fugue:team",
		"/fugue:solo lead the migration",
		"/fugue:",
	} {
		err := ValidateInput(Input{Text: text})
		if !errors.Is(err, ErrFugueCommand) {
			t.Fatalf("ValidateInput(%q) = %v, want ErrFugueCommand", text, err)
		}
		if code := errcode.CodeOf(err); code != errcode.InvalidArgument {
			t.Fatalf("ValidateInput(%q) code = %s, want invalid_argument", text, code)
		}
	}
}

func TestVendorInputWithoutFuguePrefixPasses(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"read the notes and summarize",
		"/model sonnet",
		"/compact",
		"a /fugue: mention inside the message",
	} {
		if err := ValidateInput(Input{Text: text}); err != nil {
			t.Fatalf("ValidateInput(%q) = %v, want nil", text, err)
		}
	}
}

func TestEmptyVendorInputIsRejected(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "   ", "\n\t"} {
		err := ValidateInput(Input{Text: text})
		if code := errcode.CodeOf(err); code != errcode.InvalidArgument {
			t.Fatalf("ValidateInput(%q) code = %s, want invalid_argument", text, code)
		}
	}
}
