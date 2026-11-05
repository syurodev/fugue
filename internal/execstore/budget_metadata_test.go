package execstore

import "testing"

func TestExtendBudgetMetadataSchema(t *testing.T) {
	t.Parallel()
	valid := []string{
		`{"Kind":"steps","ExtensionAmount":50,"Actor":"user"}`,
		`{"Kind":"tool_calls","ExtensionAmount":200}`,
		`{"Kind":"time"}`,
	}
	for _, input := range valid {
		if _, err := parseAndValidateMetadata(cmdExtendBudget, []byte(input)); err != nil {
			t.Errorf("rejected %s: %v", input, err)
		}
	}
	invalid := []string{
		`{"Kind":"cost"}`,
		`{"Kind":"steps","ExtensionAmount":0}`,
		`{"Kind":"steps","ExtensionAmount":-5}`,
		`{"Kind":"steps","ExtensionAmount":1.5}`,
		`{"Kind":"steps","Prompt":"not metadata"}`,
	}
	for _, input := range invalid {
		if _, err := parseAndValidateMetadata(cmdExtendBudget, []byte(input)); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestBudgetCommandMetadataFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind, field string
		want        bool
	}{
		{cmdExtendBudget, "Kind", true},
		{cmdExtendBudget, "ExtensionAmount", true},
		{cmdExtendBudget, fieldActor, true},
		{cmdExtendBudget, "Reason", false},
		{cmdRequestStop, fieldActor, true},
	}
	for _, tc := range cases {
		if got := isSafeMetadataField(tc.kind, tc.field); got != tc.want {
			t.Errorf("isSafeMetadataField(%q, %q) = %v, want %v", tc.kind, tc.field, got, tc.want)
		}
	}
	if !isMetadataOnlyPermitted(cmdExtendBudget) {
		t.Error("ExtendBudget must be a metadata-only command")
	}
	if _, err := parseAndValidateMetadata(cmdRequestStop, []byte(`{"Reason":"budget","Mode":"terminal","Actor":"user"}`)); err != nil {
		t.Errorf("budget end metadata rejected: %v", err)
	}
}
