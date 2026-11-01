package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrCommitLint reports a commit message or PR title that breaks the rules of
// AGENTS.md: Conventional Commits, and no attribution trailers.
var ErrCommitLint = errors.New("commit lint failed")

var (
	conventional = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9][a-z0-9._/,-]*\))?!?: \S.*$`)
	attribution  = regexp.MustCompile(`(?im)^\s*(co-authored-by|signed-off-by|reviewed-by|acked-by|tested-by|reported-by|suggested-by|helped-by|generated-by|assisted-by)\s*:`)
	generatedBy  = regexp.MustCompile(`(?i)generated with \[|🤖`)
	exemptTitle  = regexp.MustCompile(`^(Merge |Revert "|fixup! |squash! )`)
)

// lintBody reports attribution trailers and generator footers in a message.
func lintBody(subject, body string) []string {
	var out []string
	if m := attribution.FindStringSubmatch(body); m != nil {
		out = append(out, fmt.Sprintf("%q: contains a %s trailer; commit messages carry no attribution", subject, m[1]))
	}
	if generatedBy.MatchString(body) {
		out = append(out, fmt.Sprintf("%q: contains a generator footer; commit messages carry no attribution", subject))
	}
	return out
}

// lintTitle checks a PR title, which squash merges use as the commit subject.
func lintTitle(title string) []string {
	out := lintBody(title, title)
	if !conventional.MatchString(title) {
		out = append(out, fmt.Sprintf("%q: not a Conventional Commit, expected type(scope): summary", title))
	}
	return out
}

// lintMessage checks one commit message: the subject must be conventional
// unless it is a merge, revert, fixup or squash; no message may carry attribution.
func lintMessage(message string) []string {
	subject, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	out := lintBody(subject, message)
	if !exemptTitle.MatchString(subject) && !conventional.MatchString(subject) {
		out = append(out, fmt.Sprintf("%q: subject is not a Conventional Commit, expected type(scope): summary", subject))
	}
	return out
}

func runCommitLint(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags("commitlint")
	title := fs.String("title", "", "PR title to check")
	base := fs.String("base", "", "base ref; with -head, every commit in base..head is checked for attribution")
	head := fs.String("head", "HEAD", "head ref")
	dir := fs.String("dir", ".", "repository directory")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	var problems []string
	if *title != "" {
		problems = append(problems, lintTitle(*title)...)
	}
	if *base != "" {
		commits, err := commitMessages(ctx, *dir, *base, *head)
		if err != nil {
			return err
		}
		for _, m := range commits {
			problems = append(problems, lintBody(firstLine(m), m)...)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w:\n%s", ErrCommitLint, strings.Join(problems, "\n"))
	}
	return say(stdout, "commit lint ok")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// commitMessages returns the full message of every commit in base..head.
func commitMessages(ctx context.Context, dir, base, head string) ([]string, error) {
	out, err := capture(ctx, dir, "git", "log", "--format=%B%x1e", base+".."+head)
	if err != nil {
		return nil, err
	}
	var msgs []string
	for _, m := range strings.Split(out, "\x1e") {
		if strings.TrimSpace(m) != "" {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

// readMessageFile reads the file git passes to the hook, through a root so the
// path cannot leave its directory.
func readMessageFile(name string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		return nil, fmt.Errorf("open commit message directory: %w", err)
	}
	data, readErr := root.ReadFile(filepath.Base(name))
	closeErr := root.Close()
	if readErr != nil {
		return nil, fmt.Errorf("read commit message: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close commit message directory: %w", closeErr)
	}
	return data, nil
}

// runCommitMsg is the local commit-msg hook: it checks the message file git
// passes, ignoring comment lines.
func runCommitMsg(_ context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: commitmsg takes one message file", ErrUsage)
	}
	data, err := readMessageFile(args[0])
	if err != nil {
		return err
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "#") {
			kept = append(kept, line)
		}
	}
	if problems := lintMessage(strings.Join(kept, "\n")); len(problems) > 0 {
		return fmt.Errorf("%w:\n%s", ErrCommitLint, strings.Join(problems, "\n"))
	}
	return say(stdout, "commit message ok")
}
