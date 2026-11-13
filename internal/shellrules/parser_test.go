package shellrules

import (
	"strings"
	"testing"
)

func checkSplitBases(t *testing.T, input string, wantCmds []string) {
	t.Helper()
	cmds, ok := Split(input)
	if !ok {
		t.Fatalf("expected command to be parsable: %q", input)
	}
	if len(cmds) != len(wantCmds) {
		t.Fatalf("got %d commands, want %d", len(cmds), len(wantCmds))
	}
	for i, want := range wantCmds {
		if cmds[i].Base != want {
			t.Errorf("command %d: got base %q, want %q", i, cmds[i].Base, want)
		}
	}
}

func TestSplitControlOperators(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		wantCmds []string
	}{
		{"single command", testCmdGoTestAll, []string{"go"}},
		{"pipeline", "cat file.txt | grep foo | sort", []string{"cat", "grep", "sort"}},
		{"and list", "make build && make test", []string{testCmdMake, testCmdMake}},
		{"or list", "test -f out || make out", []string{"test", testCmdMake}},
		{"semicolon sequence", "git status; git diff", []string{testCmdGit, testCmdGit}},
		{"background", "sleep 10 & echo done", []string{"sleep", testCmdEcho}},
		{"combined operators", "make && make test; echo all done", []string{testCmdMake, testCmdMake, testCmdEcho}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkSplitBases(t, tc.input, tc.wantCmds)
		})
	}
}

func TestSplitSubshellsAndSubstitutions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		input     string
		wantBases []string
	}{
		{"subshell parentheses", "(git status)", []string{testCmdGit}},
		{"command substitution dollar paren", "echo $(git status)", []string{testCmdEcho, testCmdGit}},
		{"command substitution backticks", "echo `git status`", []string{testCmdEcho, testCmdGit}},
		{"substitution inside double quotes", "echo \"output is $(git rev-parse HEAD)\"", []string{testCmdEcho, testCmdGit}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkSplitBases(t, tc.input, tc.wantBases)
		})
	}
}

func TestSplitUnparsableConstructs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
	}{
		{"heredoc operator", "cat <<EOF\nsecret\nEOF"},
		{"eval command", "eval \"git push\""},
		{"source command", "source ./env.sh"},
		{"dot source command", ". ./env.sh"},
		{"dynamic command name dollar var", "$CMD arg1"},
		{"dynamic command name braced", "${DYNAMIC_CMD} arg1"},
		{"dynamic command substitution as command", "$(which git) push"},
		{"bash -c with string", "bash -c 'rm -rf /'"},
		{"sh -c with string", "sh -c \"git push\""},
		{"zsh -c with string", "zsh -c \"echo hi\""},
		{"unclosed single quote", "echo 'unclosed"},
		{"unclosed double quote", "echo \"unclosed"},
		{"unclosed backtick", "echo `unclosed"},
		{"unclosed subshell paren", "(echo hi"},
		{"unclosed brace", "{ echo hi;"},
		{"command length exceeds max", strings.Repeat("a", MaxCommandLength+1)},
		{"nesting depth exceeds max", strings.Repeat("$(", MaxNestingDepth+1) + "echo hi" + strings.Repeat(")", MaxNestingDepth+1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmds, ok := Split(tc.input)
			if ok {
				t.Fatalf("expected input to be unparsable, got ok=true with cmds=%v", cmds)
			}
		})
	}
}

func TestSplitEmptyOrWhitespace(t *testing.T) {
	t.Parallel()

	cmds, ok := Split("   ")
	if !ok {
		t.Fatal("whitespace string should return ok=true")
	}
	if len(cmds) != 0 {
		t.Fatalf("expected 0 commands, got %d", len(cmds))
	}

	cmds, ok = Split("")
	if !ok {
		t.Fatal("empty string should return ok=true")
	}
	if len(cmds) != 0 {
		t.Fatalf("expected 0 commands, got %d", len(cmds))
	}
}
