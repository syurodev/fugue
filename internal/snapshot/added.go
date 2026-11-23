package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxAddedLine bounds one added line handed to the visitor. A longer line is
// cut at this size and the scan reports it, so a minified bundle cannot grow
// the read buffer.
const maxAddedLine = 1 << 20

// AddedScan reports what AddedLines covered. Bytes counts the added content
// handed to the visitor. Truncated is set when the byte cap or a line cap cut
// the scan short, so the caller can say the result is incomplete. Binary counts
// the files git reported as binary; their content is never read.
type AddedScan struct {
	Bytes     int64
	Binary    int
	Truncated bool
}

// AddedLine is one added line of a tree-to-tree diff. Text aliases a reused
// buffer and is valid only until the visitor returns; a visitor must not keep
// it. Number is the 1-based line number in the new file.
type AddedLine struct {
	Path   string
	Number int
	Text   []byte
}

// AddedLines streams the lines added between fromTree and toTree to visit, in
// diff order, reading at most maxBytes of added content. It runs git with no
// external diff, no textconv and no renames, against the shadow repository's
// own configuration, so nothing from the user's repository can alter the result
// or run a command. Removed and context lines are never produced. The diff is
// streamed: memory stays at one bounded line buffer however large the diff is,
// and the buffer is cleared before return because it holds file content.
func (r *Repo) AddedLines(ctx context.Context, fromTree, toTree string, maxBytes int64, visit func(*AddedLine)) (AddedScan, error) {
	if fromTree == "" || toTree == "" {
		return AddedScan{}, ErrTreeMissing
	}
	if fromTree == toTree {
		return AddedScan{}, nil
	}
	runCtx, cancel := r.withGitTimeout(ctx)
	defer cancel()
	cmd := r.gitCommand(runCtx, "diff-tree", "-r", "-p", "-U0", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color", "--no-prefix", fromTree, toTree)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return AddedScan{}, fmt.Errorf("snapshot: pipe the added-line diff: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return AddedScan{}, fmt.Errorf("snapshot: start the added-line diff: %w", err)
	}
	reader := &lineReader{r: stdout, buf: make([]byte, maxAddedLine)}
	defer clear(reader.buf)
	scan, parseErr := parseAddedLines(reader, maxBytes, visit)
	if scan.Truncated || parseErr != nil {
		// Stopping early is the documented outcome, not a failure of git; either
		// way git must not stay blocked on a pipe nobody reads.
		cancel()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return scan, parseErr
	}
	if waitErr != nil && !scan.Truncated {
		return scan, fmt.Errorf("snapshot: git diff-tree: %w", waitErr)
	}
	return scan, nil
}

// addedParser walks a `diff-tree -p -U0` stream. Header lines are only
// recognised before the first hunk of a file: inside a hunk every line carries
// a prefix, so a file whose content looks like a header cannot forge one.
type addedParser struct {
	scan     AddedScan
	cur      AddedLine
	inHunk   bool
	maxBytes int64
	visit    func(*AddedLine)
}

func parseAddedLines(reader *lineReader, maxBytes int64, visit func(*AddedLine)) (AddedScan, error) {
	p := &addedParser{maxBytes: maxBytes, visit: visit}
	for {
		line, long, err := reader.next()
		if errors.Is(err, io.EOF) {
			return p.scan, nil
		}
		if err != nil {
			return p.scan, fmt.Errorf("snapshot: read the added-line diff: %w", err)
		}
		switch {
		case bytes.HasPrefix(line, []byte("diff --git ")):
			p.inHunk = false
			p.cur = AddedLine{}
		case !p.inHunk:
			p.header(line)
		case p.hunkLine(line, long):
			return p.scan, nil
		}
	}
}

func (p *addedParser) header(line []byte) {
	switch {
	case bytes.HasPrefix(line, []byte("+++ ")):
		p.cur.Path = diffPath(line[len("+++ "):])
	case bytes.HasPrefix(line, []byte("Binary files ")):
		p.scan.Binary++
	case bytes.HasPrefix(line, []byte("@@ ")):
		p.inHunk = true
		p.cur.Number = hunkStart(line)
	}
}

// hunkLine handles one line inside a hunk and reports whether the byte cap
// stopped the scan.
func (p *addedParser) hunkLine(line []byte, long bool) (stop bool) {
	switch {
	case bytes.HasPrefix(line, []byte("@@ ")):
		p.cur.Number = hunkStart(line)
	case len(line) > 0 && line[0] == '+':
		text := line[1:]
		if p.scan.Bytes+int64(len(text)) > p.maxBytes {
			p.scan.Truncated = true
			return true
		}
		p.scan.Bytes += int64(len(text))
		if long {
			p.scan.Truncated = true
		}
		p.cur.Text = text
		p.visit(&p.cur)
		p.cur.Number++
	}
	return false
}

// hunkStart reads the new-file start line of "@@ -a,b +c,d @@". A malformed
// header yields 0, which only affects the reported line number.
func hunkStart(header []byte) int {
	_, rest, ok := strings.Cut(string(header), " +")
	if !ok {
		return 0
	}
	digits, _, _ := strings.Cut(rest, " ")
	digits, _, _ = strings.Cut(digits, ",")
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}
	return n
}

// diffPath decodes the file name of a "+++ " header: git appends a tab after a
// name containing a space and quotes a name with control characters.
func diffPath(raw []byte) string {
	name := strings.TrimSuffix(string(raw), "\t")
	if unquoted, err := strconv.Unquote(name); err == nil {
		return unquoted
	}
	return name
}

// lineReader splits a stream into lines over one caller-owned buffer, so the
// buffer can be cleared when the scan ends. A line longer than the buffer is
// returned cut at the buffer size with long set, and its remainder is dropped.
type lineReader struct {
	r          io.Reader
	buf        []byte
	start, end int
	skipping   bool
}

func (l *lineReader) next() (line []byte, long bool, err error) {
	for {
		if line, long, ok := l.take(); ok {
			return line, long, nil
		}
		if err := l.fill(); err != nil {
			if rest, ok := l.rest(err); ok {
				return rest, false, nil
			}
			return nil, false, err
		}
	}
}

// take returns the next complete line already buffered, or ok=false when more
// input is needed. The remainder of an over-long line is dropped here.
func (l *lineReader) take() (line []byte, long, ok bool) {
	for {
		pending := l.buf[l.start:l.end]
		i := bytes.IndexByte(pending, '\n')
		switch {
		case i >= 0 && l.skipping:
			l.start += i + 1
			l.skipping = false
		case i >= 0:
			l.start += i + 1
			return pending[:i], false, true
		case l.skipping:
			l.start, l.end = 0, 0
			return nil, false, false
		case l.start == 0 && l.end == len(l.buf):
			l.skipping = true
			l.end = 0
			return pending, true, true
		default:
			return nil, false, false
		}
	}
}

// fill reads more input behind the buffered data. It fails only when the
// reader returned nothing and an error.
func (l *lineReader) fill() error {
	if l.start > 0 {
		l.end = copy(l.buf, l.buf[l.start:l.end])
		l.start = 0
	}
	n, err := l.r.Read(l.buf[l.end:])
	l.end += n
	if err != nil && n == 0 {
		return fmt.Errorf("read the diff stream: %w", err)
	}
	return nil
}

// rest returns a final line that has no trailing newline when the stream ended.
func (l *lineReader) rest(err error) ([]byte, bool) {
	if !errors.Is(err, io.EOF) || l.skipping || l.end <= l.start {
		return nil, false
	}
	line := l.buf[l.start:l.end]
	l.start = l.end
	return line, true
}
