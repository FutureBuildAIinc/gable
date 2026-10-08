// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
)

// dbRequired mirrors testutil's rule for GABLE_TEST_REQUIRE_DB: when the
// suite has declared a database must be present, its absence is a failure
// rather than a skip.
func dbRequired() bool {
	switch os.Getenv(testutil.RequireDBEnv) {
	case "", "0", "false", "FALSE", "no":
		return false
	default:
		return true
	}
}

// requireDatabaseURL returns the DATABASE_URL the test command carried, or
// ends the test when there is none. Unlike config.Load it never falls back
// to the built-in dev default: a default URL names another deployment's
// database, and this test starts a real process against it.
func requireDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url != "" {
		return url
	}
	if dbRequired() {
		t.Fatalf("%s is set but DATABASE_URL is not; the worker test cannot name a database", testutil.RequireDBEnv)
	}
	t.Skip(testutil.SkipReason)
	return ""
}

// TestCoreWorkerStopsCleanlyOnSignal builds the real core binary, runs
// `core worker` against the test command's database, waits for its startup
// line, stops it with SIGINT and asserts a clean exit: exit code 0, with
// the shutdown log showing the graceful order (the purge scheduler stops
// and drains before the database pool closes).
func TestCoreWorkerStopsCleanlyOnSignal(t *testing.T) {
	dbURL := requireDatabaseURL(t)

	bin := filepath.Join(t.TempDir(), "core")
	// The test binary runs with this package's directory as its working
	// directory, so the package to build is the current one.
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build cmd/core: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "worker")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dbURL)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start core worker: %v", err)
	}
	workerPID := cmd.Process.Pid
	t.Cleanup(func() {
		// Belt and braces: anything this test started is stopped by its
		// own pid before the report.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	var (
		mu      sync.Mutex
		lines   []string
		started = make(chan struct{})
	)
	var wg sync.WaitGroup
	scan := func(r *bufio.Reader, watch bool) {
		defer wg.Done()
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				mu.Lock()
				lines = append(lines, strings.TrimRight(line, "\n"))
				mu.Unlock()
				if watch && strings.Contains(line, "Worker started") {
					select {
					case started <- struct{}{}:
					default:
					}
				}
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go scan(bufio.NewReader(stdout), true)
	go scan(bufio.NewReader(stderr), false)

	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatalf("core worker (pid %d) did not report start within 30s; output so far:\n%s", workerPID, strings.Join(append([]string{}, lines...), "\n"))
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT to core worker (pid %d): %v", workerPID, err)
	}

	// Wait closes the pipes once the process exits, so the readers must reach
	// EOF first or the last log lines can be lost (os/exec: it is incorrect to
	// call Wait before all reads from the pipe have completed).
	done := make(chan error, 1)
	go func() {
		wg.Wait()
		done <- cmd.Wait()
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		mu.Lock()
		output := strings.Join(lines, "\n")
		mu.Unlock()
		t.Fatalf("core worker (pid %d) did not exit within 30s of SIGINT; output so far:\n%s", workerPID, output)
	}

	mu.Lock()
	defer mu.Unlock()
	output := strings.Join(lines, "\n")
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("core worker exited with code %d, want 0; output:\n%s", code, output)
	}
	for _, want := range []string{
		"Shutdown signal received",
		"stopping idempotency purge scheduler",
		"idempotency purge scheduler stopped",
		"closing database pool",
		"database pool closed",
		"clean shutdown complete",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("clean shutdown is missing %q; output:\n%s", want, output)
		}
	}
	// The purge stop must be logged before the pool close: same graceful
	// order as serve, jobs drain before the pool closes.
	var stopAt, closeAt = -1, -1
	for i, l := range lines {
		if strings.Contains(l, "idempotency purge scheduler stopped") && stopAt < 0 {
			stopAt = i
		}
		if strings.Contains(l, "database pool closed") && closeAt < 0 {
			closeAt = i
		}
	}
	if !(0 <= stopAt && stopAt < closeAt) {
		t.Fatalf("the purge scheduler stop (line %d) must precede the pool close (line %d); output:\n%s", stopAt, closeAt, output)
	}
}
