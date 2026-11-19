package teams

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateOutputAgainstSchema(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`)
	oversize := json.RawMessage(`"` + strings.Repeat("a", MaxReportOutputBytes) + `"`)

	cases := []struct {
		name      string
		schema    json.RawMessage
		output    json.RawMessage
		wantValid bool
	}{
		{"empty schema", nil, nil, true},
		{"valid output", schema, json.RawMessage(`{"count":3}`), true},
		{"wrong type", schema, json.RawMessage(`{"count":"three"}`), false},
		{"missing output", schema, nil, false},
		{"uncompilable schema", json.RawMessage(`{"type":123}`), json.RawMessage(`{}`), false},
		{"oversize output", schema, oversize, false},
	}
	for _, tc := range cases {
		ok, errText := ValidateOutputAgainstSchema(tc.schema, tc.output)
		if ok != tc.wantValid || (tc.wantValid && errText != "") || (!tc.wantValid && errText == "") {
			t.Fatalf("%s: got (%v, %q), want valid=%v", tc.name, ok, errText, tc.wantValid)
		}
	}
}
