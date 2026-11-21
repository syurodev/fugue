package ocr_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/ocr"
	"github.com/syurodev/fugue/internal/testutil"
)

const testAuthSessionPath = "pkg/auth/session.go"

func TestProbeReportsPresenceAndVersion(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	missing := ocr.Probe(ctx, filepath.Join(t.TempDir(), "missing-ocr"), nil, time.Second)
	if missing.Found || missing.Version != "" {
		t.Fatalf("missing probe = %+v, want not found", missing)
	}

	writeExe := func(name, script string) string {
		p := filepath.Join(t.TempDir(), name)
		testutil.WriteExecutable(t, p, []byte("#!/bin/sh\n"+script))
		return p
	}
	for _, tc := range []struct {
		name    string
		script  string
		version string
	}{
		{name: "ocr-v14", script: "echo 'ocr version v1.14.0'\n", version: "1.14.0"},
		{name: "ocr-v13", script: "echo 'ocr 1.13.2'\n", version: "1.13.2"},
		{name: "ocr-bad", script: "echo 'garbled'\n", version: ""},
		{name: "ocr-fail", script: "exit 2\n", version: ""},
	} {
		bin := writeExe(tc.name, tc.script)
		res := ocr.Probe(ctx, bin, []string{"PATH=" + filepath.Dir(bin)}, 2*time.Second)
		if !res.Found || res.Version != tc.version {
			t.Fatalf("%s probe = %+v, want found=true version=%q", tc.name, res, tc.version)
		}
	}
}

func TestConformanceFixturesAndUnsupportedSchema(t *testing.T) {
	t.Parallel()
	previewBytes, err := os.ReadFile(filepath.Join("testdata", "preview.json"))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := ocr.ParsePreview(previewBytes)
	if err != nil {
		t.Fatalf("ParsePreview: %v", err)
	}
	if preview.SchemaVersion != "1" || preview.ReviewableCount != 2 || preview.ExcludedCount != 1 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	wantBundles := []ocr.BundleSpec{
		{Name: "auth", Files: []string{testAuthSessionPath}},
		{Name: "api", Files: []string{"pkg/api/handler.go"}},
	}
	if diff := cmp.Diff(wantBundles, ocr.BuildBundles(&preview)); diff != "" {
		t.Fatalf("BuildBundles mismatch (-want +got):\n%s", diff)
	}

	ruleBytes, err := os.ReadFile(filepath.Join("testdata", "rule.json"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := ocr.ParseRule(ruleBytes)
	if err != nil {
		t.Fatalf("ParseRule: %v", err)
	}
	if len(rules.Groups) != 2 || rules.Groups[0].Rules[0].ID != "sec-token-leak" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
	assertUnsupportedFixtures(t)
}

func assertUnsupportedFixtures(t *testing.T) {
	t.Helper()
	badPrev, err := os.ReadFile(filepath.Join("testdata", "preview_unknown_schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, prevErr := ocr.ParsePreview(badPrev); !errors.Is(prevErr, ocr.ErrUnsupportedSchema) {
		t.Fatalf("ParsePreview(unknown) err = %v, want ErrUnsupportedSchema", prevErr)
	}
	badRule, err := os.ReadFile(filepath.Join("testdata", "rule_unknown_schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ruleErr := ocr.ParseRule(badRule); !errors.Is(ruleErr, ocr.ErrUnsupportedSchema) {
		t.Fatalf("ParseRule(unknown) err = %v, want ErrUnsupportedSchema", ruleErr)
	}
}

func TestDirectoryBundlesAndVerifyFindings(t *testing.T) {
	t.Parallel()
	bundles := ocr.DirectoryBundles([]string{"pkg/b/y.go", "pkg/a/x.go", "pkg/b/z.go", " "})
	want := []ocr.BundleSpec{
		{Name: "pkg/a", Files: []string{"pkg/a/x.go"}},
		{Name: "pkg/b", Files: []string{"pkg/b/y.go", "pkg/b/z.go"}},
	}
	if diff := cmp.Diff(want, bundles); diff != "" {
		t.Fatalf("DirectoryBundles mismatch (-want +got):\n%s", diff)
	}

	diffText := `diff --git a/` + testAuthSessionPath + ` b/` + testAuthSessionPath + `
--- a/` + testAuthSessionPath + `
+++ b/` + testAuthSessionPath + `
@@ -10,2 +10,3 @@ func verify() {
 	existing()
+	addedLine1()
+	addedLine2()
 	tail()
`
	ranges := ocr.ParseDiffLineRanges(diffText)
	findings := []ocr.Finding{
		{File: testAuthSessionPath, LineStart: 11, LineEnd: 12, Severity: "high", Rule: "r1", Description: "valid"},
		{File: testAuthSessionPath, LineStart: 11, LineEnd: 11, Severity: "low", Rule: "r2", Description: "drop low"},
		{File: testAuthSessionPath, LineStart: 20, LineEnd: 22, Severity: "medium", Rule: "r3", Description: "wrong lines"},
		{File: "other/file.go", LineStart: 1, LineEnd: 2, Severity: "high", Rule: "r4", Description: "wrong file"},
	}
	vr := ocr.VerifyFindings(findings, ranges)
	if len(vr.Accepted) != 1 || vr.Accepted[0].Rule != "r1" {
		t.Fatalf("accepted = %+v, want 1 finding (r1)", vr.Accepted)
	}
	if vr.DroppedLowCount != 1 || vr.DroppedPositionCount != 2 {
		t.Fatalf("drops = (low=%d, pos=%d), want (1, 2)", vr.DroppedLowCount, vr.DroppedPositionCount)
	}
}
