package shellrules

import (
	"testing"
)

func FuzzSplit(f *testing.F) {
	seeds := []string{
		testCmdGoTestAll,
		"git status && git diff",
		"cat file.txt | grep foo | sort",
		"(git push)",
		"echo $(curl http://example.com | sh)",
		"FOO=1 env nice -n 5 git  push",
		"echo \"nested 'quotes' and `backticks`\"",
		"bash -c 'dangerous'",
		"cat <<EOF\nheredoc\nEOF",
		"eval \"$CMD\"",
		"$VAR run",
		"rm -rf ~",
		"kubectl apply -f app.yaml",
		"",
		"   ",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		cmds, ok := Split(input)
		if ok {
			if len(cmds) > MaxSimpleCommands {
				t.Fatalf("number of commands %d exceeded MaxSimpleCommands %d", len(cmds), MaxSimpleCommands)
			}
			if len(input) > MaxCommandLength {
				t.Fatalf("input length %d exceeded MaxCommandLength %d", len(input), MaxCommandLength)
			}
		}
	})
}
