// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// standIn replaces one subcommand's runner for the length of one test and
// returns what the stand-in was called with. The dispatch test must not
// start the real server.
func standIn(t *testing.T, name string) *[]string {
	t.Helper()
	var got []string
	orig, ok := runners[name]
	if !ok {
		t.Fatalf("no runner for subcommand %q", name)
	}
	runners[name] = func(args []string) { got = args }
	t.Cleanup(func() { runners[name] = orig })
	return &got
}

// capture runs fn with os.Stdout and os.Stderr captured, and returns what
// each stream received.
func capture(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	fn()

	outW.Close()
	errW.Close()
	var outBuf, errBuf bytes.Buffer
	done := make(chan struct{})
	go func() {
		io.Copy(&outBuf, outR)
		io.Copy(&errBuf, errR)
		close(done)
	}()
	<-done
	return outBuf.String(), errBuf.String()
}

// TestEverySubcommandDispatches pins that `core <command>` reaches the
// runner of exactly that command, carrying the arguments after it.
func TestEverySubcommandDispatches(t *testing.T) {
	for _, name := range []string{"serve", "worker", "migrate", "seed"} {
		t.Run(name, func(t *testing.T) {
			called := standIn(t, name)
			var code int
			_, _ = capture(t, func() { code = run([]string{name, "leftover-arg"}) })
			if code != 0 {
				t.Fatalf("run %s: exit code %d, want 0", name, code)
			}
			if len(*called) != 1 || (*called)[0] != "leftover-arg" {
				t.Fatalf("runner of %s saw %v, want [leftover-arg]", name, *called)
			}
		})
	}
}

// TestHelpPrintsEverySubcommand pins the help path: exit 0, and the text
// on stdout names every subcommand.
func TestHelpPrintsEverySubcommand(t *testing.T) {
	var code int
	stdout, stderr := capture(t, func() { code = run([]string{"help"}) })
	if code != 0 {
		t.Fatalf("run help: exit code %d, want 0", code)
	}
	if stderr != "" {
		t.Fatalf("run help wrote to stderr: %q", stderr)
	}
	for _, name := range []string{"serve", "worker", "migrate", "seed"} {
		if !strings.Contains(stdout, name) {
			t.Fatalf("help text does not name %q:\n%s", name, stdout)
		}
	}
}

// TestNoArgumentsIsAUsageError pins that a bare `core` prints the usage on
// stderr and exits non-zero.
func TestNoArgumentsIsAUsageError(t *testing.T) {
	var code int
	stdout, stderr := capture(t, func() { code = run(nil) })
	if code == 0 {
		t.Fatal("run with no arguments: exit code 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("bare core wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Fatalf("bare core did not print the usage:\n%s", stderr)
	}
}

// TestUnknownCommandExitsNonZero pins the refusal of an unknown
// subcommand.
func TestUnknownCommandExitsNonZero(t *testing.T) {
	var code int
	stdout, stderr := capture(t, func() { code = run([]string{"reschedule"}) })
	if code == 0 {
		t.Fatal("run with an unknown command: exit code 0, want non-zero")
	}
	if stdout != "" {
		t.Fatalf("unknown command wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, `unknown command "reschedule"`) {
		t.Fatalf("the error does not name the unknown command:\n%s", stderr)
	}
}

// TestUnknownFlagIsRefused pins that a flag no subcommand defines is
// refused (exit non-zero) and the runner never runs.
func TestUnknownFlagIsRefused(t *testing.T) {
	called := standIn(t, "serve")
	var code int
	capture(t, func() { code = run([]string{"serve", "--port", "9999"}) })
	if code == 0 {
		t.Fatal("run serve --port: exit code 0, want non-zero")
	}
	if *called != nil {
		t.Fatalf("the serve runner ran with %v; an unknown flag must be refused first", *called)
	}
}

// TestMigrateTakesTheReportFlag pins that `core migrate -report units`, the
// form ADR 0006 section 8, migration 099 and CONTRACT-CHANGES name for the
// read only units pre flight report, reaches the migrate runner with its
// flag, while every other subcommand still refuses any flag.
func TestMigrateTakesTheReportFlag(t *testing.T) {
	called := standIn(t, "migrate")
	var code int
	capture(t, func() { code = run([]string{"migrate", "-report", "units"}) })
	if code != 0 {
		t.Fatalf("run migrate -report units: exit code %d, want 0", code)
	}
	if len(*called) != 2 || (*called)[0] != "-report" || (*called)[1] != "units" {
		t.Fatalf("the migrate runner saw %v, want [-report units]", *called)
	}
}
