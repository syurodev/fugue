package codex

import (
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

// Measured Codex slash command names on 0.167.x (`vendor-runtime §9`).
const (
	SlashCompact = "/compact"
	SlashReview  = "/review"
)

// SlashCommand describes one Codex session command published by Fugue
// (`vendor-runtime §9`). Ported from DevKit `internal/agenthub/codex_commands.go`
// and measured against `codex-cli 0.167.0`.
type SlashCommand struct {
	Name        string
	Description string
	Argument    string
}

// SlashCommandCall is the prepared JSON-RPC call for a recognized slash command.
type SlashCommandCall struct {
	Name   string
	Method string
	Adopt  bool
	Target *ReviewTarget
}

// SlashCommands returns the Codex slash commands measured working on 0.167.x.
func SlashCommands() []SlashCommand {
	return []SlashCommand{
		{
			Name:        SlashCompact,
			Description: "Summarize earlier turns to free up the context window",
		},
		{
			Name:        SlashReview,
			Description: "Review changes — uncommitted, `base <branch>`, `commit <sha>`, or instructions",
			Argument:    "[base <branch> | commit <sha> | instructions]",
		},
	}
}

// SlashCommandNames returns the slash command names published on session init.
func SlashCommandNames() []string {
	cmds := SlashCommands()
	names := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		names = append(names, cmd.Name)
	}
	return names
}

// ParseSlashCommand checks whether text is a `/fugue:` command (which must
// never reach Codex) or one of the measured Codex slash commands (`/compact`,
// `/review`), and prepares the corresponding RPC parameters.
func ParseSlashCommand(text string) (SlashCommandCall, bool, error) {
	if vendor.IsFugueCommand(text) {
		return SlashCommandCall{}, false, vendor.ErrFugueCommand
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") || strings.ContainsRune(trimmed, '\n') {
		return SlashCommandCall{}, false, nil
	}
	head, rest, _ := strings.Cut(trimmed, " ")
	args := strings.TrimSpace(rest)
	switch head {
	case SlashCompact:
		if args != "" {
			return SlashCommandCall{}, true, errcode.New(errcode.InvalidArgument, "/compact takes no arguments")
		}
		return SlashCommandCall{
			Name:   SlashCompact,
			Method: MethodThreadCompactStart,
			Adopt:  true,
		}, true, nil
	case SlashReview:
		target, err := parseReviewTarget(args)
		if err != nil {
			return SlashCommandCall{}, true, err
		}
		return SlashCommandCall{
			Name:   SlashReview,
			Method: MethodReviewStart,
			Target: &target,
		}, true, nil
	default:
		return SlashCommandCall{}, false, nil
	}
}

func parseReviewTarget(args string) (ReviewTarget, error) {
	if args == "" {
		return ReviewTarget{Type: ReviewTargetUncommitted}, nil
	}
	kind, rest, hasRest := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	switch kind {
	case "base":
		if !hasRest || rest == "" || strings.ContainsAny(rest, " \t") {
			return ReviewTarget{}, errcode.New(errcode.InvalidArgument, "usage: /review base <branch>")
		}
		return ReviewTarget{Type: ReviewTargetBaseBranch, Branch: rest}, nil
	case "commit":
		if !hasRest || !isCommitSHA(rest) {
			return ReviewTarget{}, errcode.New(errcode.InvalidArgument, "usage: /review commit <sha>")
		}
		return ReviewTarget{Type: ReviewTargetCommit, SHA: rest}, nil
	default:
		return ReviewTarget{Type: ReviewTargetCustom, Instructions: args}, nil
	}
}

func isCommitSHA(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
