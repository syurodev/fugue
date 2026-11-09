package hostconn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/pkg/client"
)

func TestHostExecutableRejectsWritableBinaryAndDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	bin := filepath.Join(dir, "fugue")
	testExecutable(t, bin, []byte("x"))
	if err := validateExecutable(bin); err != nil {
		t.Fatal(err)
	}
	if err := validateExecutableForUID(bin, int64(os.Getuid())+1); !errors.Is(err, ErrUnsafeExecutable) {
		t.Fatalf("wrong owner: %v", err)
	}
	testSetMode(t, bin, 0o720)
	if err := validateExecutable(bin); !errors.Is(err, ErrUnsafeExecutable) {
		t.Fatalf("writable binary: %v", err)
	}
	testSetMode(t, bin, 0o700)
	testSetMode(t, dir, 0o720)
	if err := validateExecutable(bin); !errors.Is(err, ErrUnsafeExecutable) {
		t.Fatalf("writable directory: %v", err)
	}
}

func TestHostStartupTimeoutIsTyped(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	result := make(chan error, 1)
	go func() {
		_, err := waitForHost(t.Context(), clk, func(context.Context) (*client.Client, error) { return nil, client.ErrHostNotRunning })
		result <- err
	}()
	for range 2 {
		<-clk.armed
	}
	clk.fireAll()
	select {
	case err := <-result:
		if !errors.Is(err, ErrStartupTimeout) {
			t.Fatalf("timeout: %v", err)
		}
	case <-t.Context().Done():
		t.Fatal("wait did not return")
	}
}

func TestHostStartupTimeoutCancelsBlockedDial(t *testing.T) {
	t.Parallel()
	clk := newFakeClock()
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		_, err := waitForHost(t.Context(), clk, func(ctx context.Context) (*client.Client, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		result <- err
	}()
	<-started
	<-clk.armed
	clk.fireAll()
	deadline, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrStartupTimeout) {
			t.Fatalf("timeout: %v", err)
		}
	case <-deadline.Done():
		t.Fatal("blocked dial ignored startup deadline")
	}
}

func TestHostLogIsBoundedAndDrainsInput(t *testing.T) {
	t.Parallel()
	input := strings.NewReader("abcdefgh")
	var output bytes.Buffer
	if err := client.WriteBoundedLog(&output, input, 4); err != nil {
		t.Fatal(err)
	}
	if output.String() != "abcd" || input.Len() != 0 {
		t.Fatalf("unexpected bounded log: %q, remaining %d", output.String(), input.Len())
	}
}

func TestHostLogUsesPrivateFilesAndRejectsUnsafeDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	f, err := client.OpenHostLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %o", info.Mode().Perm())
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	testSetMode(t, filepath.Join(dir, "logs"), 0o777)
	if _, err := client.OpenHostLog(dir); err == nil {
		t.Fatal("unsafe log directory accepted")
	}
}

func TestDetachedHostOutputIsCapturedAndChildrenReaped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	appPath := filepath.Join(dir, "fugue-app")
	hostPath := filepath.Join(dir, "fugue")
	testExecutable(t, appPath, []byte("#!/bin/sh\n/bin/cat >&3\n"))
	testExecutable(t, hostPath, []byte("#!/bin/sh\nprintf ready\n"))
	dataDir := filepath.Join(dir, "data")
	children, err := launchDetached(t.Context(), appPath, dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if childErr := <-children.host; childErr != nil {
		t.Fatal(childErr)
	}
	if logErr := <-children.log; logErr != nil {
		t.Fatal(logErr)
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeQuietly(root)
	data, err := root.ReadFile("logs/host.log")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ready" {
		t.Fatalf("unexpected log: %q", data)
	}
}

func TestHostManagerStartsAbsentHost(t *testing.T) {
	t.Parallel()
	appPath, data := lifecycleFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	m := lifecycleManager(ctx, data, appPath)
	defer m.Stop()
	waitConnectedForTest(t, ctx, m)
	m.Stop()
	if successes := waitLifecycleChildren(t, ctx, []*Manager{m}); successes != 1 {
		t.Fatalf("successful hosts = %d", successes)
	}
}

func TestConcurrentAppStartsShareOneHost(t *testing.T) {
	t.Parallel()
	appPath, data := lifecycleFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	managers := []*Manager{lifecycleManager(ctx, data, appPath), lifecycleManager(ctx, data, appPath)}
	defer stopManagers(managers)
	for _, m := range managers {
		waitConnectedForTest(t, ctx, m)
	}
	assertSameHost(t, ctx, managers)
	stopManagers(managers)
	if successes := waitLifecycleChildren(t, ctx, managers); successes != 1 {
		t.Fatalf("successful hosts = %d", successes)
	}
}

func lifecycleFixture(t *testing.T) (string, string) {
	t.Helper()
	bin := buildFugue(t)
	testSetMode(t, bin, 0o700)
	dir := filepath.Dir(bin)
	testSetMode(t, dir, 0o700)
	app := filepath.Join(dir, "fugue-app")
	testExecutable(t, app, []byte("#!/bin/sh\n/bin/cat >&3\n"))
	return app, filepath.Join(shortDir(t), "data")
}

func lifecycleManager(ctx context.Context, data, app string) *Manager {
	m := NewManager(&Config{DataDir: data, Params: lifecycleParams(), AutoStart: true, ExecutablePath: app, Topics: []string{testTopicHost}, Updates: make(chan Update, 128)})
	m.Start(ctx)
	return m
}

func stopManagers(managers []*Manager) {
	for _, m := range managers {
		m.Stop()
	}
}

func assertSameHost(t *testing.T, ctx context.Context, managers []*Manager) {
	t.Helper()
	var incarnation string
	for _, m := range managers {
		m.mu.Lock()
		conn := m.conn
		m.mu.Unlock()
		status, err := conn.HostStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.Incarnation == "" || (incarnation != "" && incarnation != status.Incarnation) {
			t.Fatal("different or missing host incarnation")
		}
		incarnation = status.Incarnation
	}
}

func waitLifecycleChildren(t *testing.T, ctx context.Context, managers []*Manager) int {
	t.Helper()
	successes := 0
	for _, m := range managers {
		m.mu.Lock()
		children := m.children
		m.mu.Unlock()
		if children.host == nil {
			continue
		}
		select {
		case err := <-children.host:
			if err == nil {
				successes++
			}
		case <-ctx.Done():
			t.Fatal("host did not exit")
		}
		select {
		case err := <-children.log:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("log sink did not exit")
		}
	}
	return successes
}

func testSetMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func testExecutable(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	testSetMode(t, path, 0o700)
}

type failingLogWriter struct{}

func (failingLogWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestHostLogDrainsEvenAfterWriteFailure(t *testing.T) {
	t.Parallel()
	src := strings.NewReader("abcdefgh")
	err := client.WriteBoundedLog(failingLogWriter{}, src, 4)
	if !errors.Is(err, io.ErrClosedPipe) || src.Len() != 0 {
		t.Fatalf("write error or drain: %v, remaining %d", err, src.Len())
	}
}

func lifecycleParams() client.Params {
	p := appParams()
	p.Version = testDevVersion
	return p
}

func TestHostEnvironmentExcludesSecrets(t *testing.T) {
	t.Parallel()
	got := hostEnvironment([]string{"HOME=/home/user", "PATH=/usr/bin", "SECRET_TOKEN=do-not-pass", "SYNX_FUGUE_DATA_DIR=/other"}, "/safe/data")
	for _, value := range got {
		if strings.Contains(value, "SECRET_TOKEN") || strings.Contains(value, "/other") {
			t.Fatal("unsafe environment forwarded")
		}
	}
	if !slices.Contains(got, "HOME=/home/user") || !slices.Contains(got, "PATH=/usr/bin") || !slices.Contains(got, "SYNX_FUGUE_DATA_DIR=/safe/data") {
		t.Fatalf("missing expected configuration: %v", got)
	}
}
