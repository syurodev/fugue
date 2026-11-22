package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
)

const testPruneSocket = "/tmp/f.sock"

func TestParseTaskPruneArgs(t *testing.T) {
	t.Parallel()
	valid := []struct {
		args    []string
		wantS   string
		wantRet time.Duration
	}{
		{[]string{taskCommand, taskPruneSubcommand}, "", 0},
		{[]string{taskCommand, taskPruneSubcommand, exclusiveSocketFlag, testPruneSocket}, testPruneSocket, 0},
		{[]string{taskCommand, taskPruneSubcommand, exclusiveSocketFlag + "=" + testPruneSocket, taskRetentionFlag + "=12h"}, testPruneSocket, 12 * time.Hour},
		{[]string{taskCommand, taskPruneSubcommand, taskRetentionFlag, "48h"}, "", 48 * time.Hour},
	}
	for _, tc := range valid {
		sock, ret, matched, ok := parseTaskPruneArgs(tc.args)
		if !matched || !ok || sock != tc.wantS || ret != tc.wantRet {
			t.Fatalf("parseTaskPruneArgs(%v) = (%q, %v, %v, %v), want (%q, %v, true, true)",
				tc.args, sock, ret, matched, ok, tc.wantS, tc.wantRet)
		}
	}
	for _, bad := range [][]string{
		{taskCommand, taskPruneSubcommand, exclusiveSocketFlag},
		{taskCommand, taskPruneSubcommand, taskRetentionFlag, "not-a-dur"},
		{taskCommand, taskPruneSubcommand, taskRetentionFlag + "=-1h"},
		{taskCommand, taskPruneSubcommand, "--unknown"},
	} {
		_, _, matched, ok := parseTaskPruneArgs(bad)
		if !matched || ok {
			t.Fatalf("parseTaskPruneArgs(%v) = (%v, %v), want matched=true valid=false", bad, matched, ok)
		}
	}
	var out bytes.Buffer
	if code := runMain(t.Context(), []string{taskCommand, taskPruneSubcommand, "--bad"}, &out, &bytes.Buffer{}); code != exitUsage || !strings.Contains(out.String(), "fugue task prune") {
		t.Fatalf("runMain invalid task prune = (%d, %q), want exitUsage with usage", code, out.String())
	}
}

func TestRunTaskPruneAgainstHost(t *testing.T) {
	t.Parallel()
	sockPath, hp := startExclusiveTestHost(t, 2)
	if hp.Runtime() == nil {
		t.Fatal("host runtime is nil")
	}
	var out, errOut bytes.Buffer
	code := RunTaskPrune(t.Context(), &out, &errOut, config.MapEnv{}, sockPath, 0, testVersion)
	if code != 0 {
		t.Fatalf("RunTaskPrune exit = %d, stderr = %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "pruned 0 worktree(s), 0 branch(es), 0 admin entry(ies)") {
		t.Fatalf("RunTaskPrune output = %q", out.String())
	}
}
