package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

const (
	parallelStatThreshold    = 256
	fastImportColdThreshold  = 256
	fastImportIncrementalMin = 2000
	fastImportBatchFiles     = 256
	fastImportBatchMaxBytes  = 4 << 20
	fastImportTempRef        = "refs/fugue/import-tmp"
)

type candidateEntry struct {
	rel  string
	mode string
}

type fastImportBatch struct {
	data    []byte
	skipped []Skip
	err     error
}

func statWorkerCount(items int) int {
	if items < parallelStatThreshold {
		return 1
	}
	w := min(max(runtime.GOMAXPROCS(0), 4), 8)
	return min(w, items)
}

func shouldUseFastImport(trackedCount, untrackedCount int) bool {
	if untrackedCount < fastImportColdThreshold {
		return false
	}
	return trackedCount == 0 || untrackedCount >= fastImportIncrementalMin || untrackedCount > trackedCount
}

func (r *Repo) snapshotFastImportLocked(
	ctx context.Context,
	checkpointID, slot string,
	tracked, toRemove, toRefresh []string,
	untracked []candidateEntry,
	skipped []Skip,
) (string, []Skip, error) {
	baseTree, err := r.prepareBaseTree(ctx, tracked, toRemove, toRefresh)
	if err != nil {
		return "", nil, err
	}
	importSkips, err := r.runFastImport(ctx, baseTree, untracked)
	if err != nil {
		return "", nil, err
	}
	skipped = append(skipped, importSkips...)

	rawTree, err := r.git(ctx, nil, "rev-parse", fastImportTempRef+"^{tree}")
	if err != nil {
		return "", nil, err
	}
	tree := strings.TrimSpace(string(rawTree))
	if tree == "" {
		return "", nil, errcode.New(errcode.Internal, "snapshot: git rev-parse returned an empty tree hash")
	}
	if _, err := r.git(ctx, nil, "update-ref", RefName(checkpointID, slot), tree); err != nil {
		return "", nil, err
	}
	if _, err := r.git(ctx, nil, "update-ref", "-d", fastImportTempRef); err != nil {
		return "", nil, err
	}
	if _, err := r.git(ctx, nil, "read-tree", tree); err != nil {
		return "", nil, err
	}
	if _, err := r.git(ctx, nil, "update-index", "-q", "--refresh"); err != nil {
		return "", nil, err
	}
	return tree, skipped, nil
}

func (r *Repo) prepareBaseTree(ctx context.Context, tracked, toRemove, toRefresh []string) (string, error) {
	if len(tracked) == 0 {
		return "", nil
	}
	if err := r.applyIndexUpdates(ctx, toRemove, toRefresh, nil); err != nil {
		return "", err
	}
	if len(toRefresh) == 0 {
		return "", nil
	}
	rawTree, err := r.git(ctx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(rawTree)), nil
}

func (r *Repo) runFastImport(ctx context.Context, baseTree string, entries []candidateEntry) ([]Skip, error) {
	runCtx, cancel := r.withGitTimeout(ctx)
	defer cancel()

	cmd := r.gitCommand(runCtx, "-c", "pack.compression=0", "fast-import", "--force", "--quiet", "--date-format=raw")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("snapshot: pipe fast-import stdin: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		closeStreamQuietly(stdin)
		return nil, fmt.Errorf("snapshot: start git fast-import: %w", err)
	}

	skipped, streamErr := r.streamFastImport(runCtx, stdin, baseTree, entries)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()

	if streamErr != nil {
		return nil, formatFastImportErr(streamErr, stderr.String())
	}
	if waitErr != nil {
		return nil, formatFastImportErr(waitErr, stderr.String())
	}
	if closeErr != nil {
		return nil, fmt.Errorf("snapshot: close git fast-import stdin: %w", closeErr)
	}
	return skipped, nil
}

func formatFastImportErr(err error, stderrText string) error {
	msg := strings.TrimSpace(stderrText)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("snapshot: git fast-import: %s: %w", msg, err)
}

func (r *Repo) streamFastImport(ctx context.Context, dst io.Writer, baseTree string, entries []candidateEntry) ([]Skip, error) {
	var header bytes.Buffer
	header.WriteString("commit " + fastImportTempRef + "\ncommitter fugue <fugue@local> 0 +0000\ndata 0\n\n")
	if baseTree != "" {
		header.WriteString("M 040000 ")
		header.WriteString(baseTree)
		header.WriteString(" \"\"\n")
	}
	if _, err := dst.Write(header.Bytes()); err != nil {
		return nil, fmt.Errorf("write fast-import header: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return r.streamEntriesParallel(ctx, dst, entries)
}

func (r *Repo) streamEntriesParallel(ctx context.Context, dst io.Writer, entries []candidateEntry) ([]Skip, error) {
	wsRoot, err := os.OpenRoot(r.workTree)
	if err != nil {
		return nil, fmt.Errorf("snapshot: open workspace root: %w", err)
	}
	defer closeStreamQuietly(wsRoot)

	workers := statWorkerCount(len(entries))
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	batches := make(chan fastImportBatch, workers)
	var next atomic.Int64
	var active atomic.Int64
	active.Store(int64(workers))

	tasks := make([]*supervise.Task, 0, workers)
	for range workers {
		tasks = append(tasks, supervise.Go(workerCtx, "snapshot-fast-import-read", func(tctx context.Context) error {
			defer func() {
				if active.Add(-1) == 0 {
					close(batches)
				}
			}()
			r.produceFastImportBatches(tctx, wsRoot, entries, &next, batches)
			return nil
		}))
	}

	skipped, writeErr := consumeFastImportBatches(ctx, dst, batches)
	if writeErr != nil {
		cancel()
	}
	for _, task := range tasks {
		if waitErr := task.Wait(); waitErr != nil && writeErr == nil {
			writeErr = waitErr
		}
	}
	return skipped, writeErr
}

func consumeFastImportBatches(ctx context.Context, dst io.Writer, batches <-chan fastImportBatch) ([]Skip, error) {
	var skipped []Skip
	var firstErr error
	for batch := range batches {
		if firstErr != nil {
			continue
		}
		if batch.err != nil {
			firstErr = batch.err
			continue
		}
		if len(batch.skipped) > 0 {
			skipped = append(skipped, batch.skipped...)
		}
		if len(batch.data) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			firstErr = err
			continue
		}
		if _, err := dst.Write(batch.data); err != nil {
			firstErr = fmt.Errorf("write fast-import batch: %w", err)
		}
	}
	return skipped, firstErr
}

func (r *Repo) produceFastImportBatches(
	ctx context.Context,
	wsRoot *os.Root,
	entries []candidateEntry,
	next *atomic.Int64,
	out chan<- fastImportBatch,
) {
	total := int64(len(entries))
	for ctx.Err() == nil {
		start := int(next.Add(fastImportBatchFiles) - fastImportBatchFiles)
		if start >= int(total) {
			return
		}
		end := min(start+fastImportBatchFiles, len(entries))
		if !r.emitFastImportSlice(ctx, wsRoot, entries[start:end], out) {
			return
		}
	}
}

func (r *Repo) emitFastImportSlice(ctx context.Context, wsRoot *os.Root, entries []candidateEntry, out chan<- fastImportBatch) bool {
	var buf bytes.Buffer
	buf.Grow(len(entries) * 80)
	var skipped []Skip
	for _, e := range entries {
		if ctx.Err() != nil {
			sendBatch(ctx, out, fastImportBatch{err: ctx.Err()})
			return false
		}
		data, skip, ok := r.readEntryPayload(wsRoot, e)
		if skip != nil {
			skipped = append(skipped, *skip)
		}
		if !ok {
			continue
		}
		appendFastImportRecord(&buf, e.mode, e.rel, data)
		if buf.Len() >= fastImportBatchMaxBytes {
			if !sendBatch(ctx, out, fastImportBatch{data: buf.Bytes(), skipped: skipped}) {
				return false
			}
			buf = bytes.Buffer{}
			skipped = nil
		}
	}
	if buf.Len() > 0 || len(skipped) > 0 {
		return sendBatch(ctx, out, fastImportBatch{data: buf.Bytes(), skipped: skipped})
	}
	return true
}

func sendBatch(ctx context.Context, out chan<- fastImportBatch, batch fastImportBatch) bool {
	select {
	case out <- batch:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *Repo) readEntryPayload(wsRoot *os.Root, e candidateEntry) ([]byte, *Skip, bool) {
	fsRel := filepath.FromSlash(e.rel)
	if e.mode == gitModeSymlink {
		target, err := wsRoot.Readlink(fsRel)
		if err != nil {
			return nil, &Skip{Path: e.rel, Reason: SkipUnreadable}, false
		}
		return []byte(target), nil, true
	}
	data, err := wsRoot.ReadFile(fsRel)
	if err != nil {
		return nil, &Skip{Path: e.rel, Reason: SkipUnreadable}, false
	}
	if int64(len(data)) > r.limits.MaxFileBytes {
		return nil, &Skip{Path: e.rel, Reason: SkipTooLarge, Bytes: int64(len(data))}, false
	}
	return data, nil, true
}

func appendFastImportRecord(buf *bytes.Buffer, mode, rel string, data []byte) {
	buf.WriteString("M ")
	buf.WriteString(mode)
	buf.WriteString(" inline ")
	writeFastImportPath(buf, rel)
	buf.WriteString("\ndata ")
	buf.WriteString(strconv.Itoa(len(data)))
	buf.WriteByte('\n')
	buf.Write(data)
	buf.WriteByte('\n')
}

func writeFastImportPath(buf *bytes.Buffer, p string) {
	if !strings.HasPrefix(p, "\"") && !strings.ContainsAny(p, "\n\r\\") {
		buf.WriteString(p)
		return
	}
	buf.WriteByte('"')
	for i := range len(p) {
		writeQuotedPathByte(buf, p[i])
	}
	buf.WriteByte('"')
}

func writeQuotedPathByte(buf *bytes.Buffer, c byte) {
	switch {
	case c == '"':
		buf.WriteString(`\"`)
	case c == '\\':
		buf.WriteString(`\\`)
	case c == '\n':
		buf.WriteString(`\n`)
	case c == '\r':
		buf.WriteString(`\r`)
	case c < 0x20 || c == 0x7f:
		buf.WriteByte('\\')
		buf.WriteByte('0' + (c>>6)&7)
		buf.WriteByte('0' + (c>>3)&7)
		buf.WriteByte('0' + c&7)
	default:
		buf.WriteByte(c)
	}
}

func closeStreamQuietly(c io.Closer) {
	if err := c.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return
	}
}
