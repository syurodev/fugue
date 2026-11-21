package appstate

// The document envelope checks of host-api §5.2 (owner option A, base#22):
// a document is JSON the app owns — an object with an integer
// schema_version >= 1 and anything else the app puts in it. The host reads
// the envelope and nothing else, so a layout change never touches the Host
// API contract. The schema_version rule is what lets a newer app keep its
// saved layout untouched for an older binary to leave alone.

import (
	"encoding/json"
	"strconv"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// The size limits of host-api §5.2: layout at most 64 KiB, preferences at
// most 16 KiB, and at most 16 saved windows.
const (
	MaxLayoutBytes      = 64 * 1024
	MaxPreferencesBytes = 16 * 1024
	MaxWindows          = 16
)

// ValidateWindowLayout checks one window layout document.
func ValidateWindowLayout(layout []byte) error {
	return validateDocument(layout, MaxLayoutBytes, ErrLayoutTooLarge)
}

// ValidatePreferences checks the app preferences document.
func ValidatePreferences(prefs []byte) error {
	return validateDocument(prefs, MaxPreferencesBytes, ErrPreferencesTooLarge)
}

// validateDocument enforces the envelope: within the size limit, valid JSON,
// an object, and an integer schema_version >= 1. Anything else fails typed.
func validateDocument(raw []byte, limit int, tooLarge error) error {
	if len(raw) > limit {
		return tooLarge
	}
	var probe struct {
		SchemaVersion json.RawMessage `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return errcode.Wrap(errcode.AppStateInvalidDocument, "the document is not valid JSON with an integer schema_version >= 1", ErrInvalidDocument)
	}
	if len(probe.SchemaVersion) == 0 {
		return errcode.Wrap(errcode.AppStateInvalidDocument, "schema_version is missing", ErrInvalidDocument)
	}
	var version json.Number
	if err := json.Unmarshal(probe.SchemaVersion, &version); err != nil {
		return errcode.Wrap(errcode.AppStateInvalidDocument, "schema_version is not a number", ErrInvalidDocument)
	}
	n, err := strconv.ParseInt(version.String(), 10, 64)
	if err != nil || n < 1 {
		return errcode.Wrap(errcode.AppStateInvalidDocument, "schema_version is not a positive integer", ErrInvalidDocument)
	}
	return nil
}
