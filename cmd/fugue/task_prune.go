package main

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/pkg/client"
)

const (
	taskCommand          = "task"
	taskPruneSubcommand  = "prune"
	taskRetentionFlag    = "--retention"
	taskPruneClientTitle = "fugue"
)

// parseTaskPruneArgs parses `task prune [--socket <path>] [--retention <duration>]`.
func parseTaskPruneArgs(args []string) (socket string, retention time.Duration, matched, valid bool) {
	if len(args) < 2 || args[0] != taskCommand || args[1] != taskPruneSubcommand {
		return "", 0, false, false
	}
	rest := args[2:]
	for len(rest) > 0 {
		if val, next, isSock, ok := consumeFlagValue(rest, exclusiveSocketFlag); isSock {
			if !ok {
				return "", 0, true, false
			}
			socket, rest = val, next
			continue
		}
		if dur, next, isRet, ok := parseRetentionFlag(rest); isRet {
			if !ok {
				return "", 0, true, false
			}
			retention, rest = dur, next
			continue
		}
		return "", 0, true, false
	}
	return socket, retention, true, true
}

func parseRetentionFlag(args []string) (dur time.Duration, rest []string, matched, ok bool) {
	val, next, matched, ok := consumeFlagValue(args, taskRetentionFlag)
	if !matched || !ok {
		return 0, next, matched, false
	}
	dur, err := time.ParseDuration(val)
	if err != nil || dur < 0 {
		return 0, nil, true, false
	}
	return dur, next, true, true
}

func consumeFlagValue(args []string, flag string) (val string, rest []string, matched, ok bool) {
	arg := args[0]
	if arg == flag {
		if len(args) < 2 || args[1] == "" {
			return "", nil, true, false
		}
		return args[1], args[2:], true, true
	}
	if cut, hasPrefix := strings.CutPrefix(arg, flag+"="); hasPrefix {
		if cut == "" {
			return "", nil, true, false
		}
		return cut, args[1:], true, true
	}
	return "", args, false, false
}

// RunTaskPrune asks the running host to prune ended-team task worktrees and
// branches past the retention window (orchestration §9, F11.11).
func RunTaskPrune(ctx context.Context, out, errOut io.Writer, env config.Env, socket string, retention time.Duration, ver string) int {
	resolvedSocket, err := resolveExclusiveSocket(env, socket)
	if err != nil {
		writeCommandErr(errOut, err)
		return exitFailure
	}
	cli, dialErr := client.Dial(ctx, resolvedSocket, client.Params{
		Kind:        client.KindCLI,
		Name:        taskPruneClientTitle,
		Version:     ver,
		APIVersions: []string{"v1"},
	})
	if dialErr != nil {
		writeCommandErr(errOut, dialErr)
		return exitFailure
	}
	defer cli.Close()

	res, pruneErr := cli.PruneWorktrees(ctx, retention)
	if pruneErr != nil {
		writeCommandErr(errOut, pruneErr)
		return exitFailure
	}
	if err := say(out, "pruned %d worktree(s), %d branch(es), %d admin entry(ies)",
		len(res.RemovedWorktrees), len(res.RemovedBranches), res.RemovedAdminEntries); err != nil {
		writeCommandErr(errOut, err)
		return exitFailure
	}
	return 0
}
