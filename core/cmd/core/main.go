// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Command core is the one binary of the Gable server: each subcommand runs
// one role. `core serve` is what the container image runs by default; the
// old entry points (cmd/server, cmd/migrate, cmd/seed) remain as thin
// calls into the same packages, so `go run ./cmd/server` and `core serve`
// run the same code.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/gablelbm/gable/internal/app/migrate"
	"github.com/gablelbm/gable/internal/app/seed"
	"github.com/gablelbm/gable/internal/app/serve"
	"github.com/gablelbm/gable/internal/app/worker"
)

// usage is the help text `core` prints on `help` and on a usage error.
const usage = `core is the one Gable server binary; each subcommand runs one role.

Usage:

	core <command>

Commands:

	serve	run the HTTP API server (the container image's default)
	worker	run the background jobs (the idempotency retention purge)
	migrate	apply the SQL migrations from migrations/
	seed	populate the database with demo data (gated on DEMO_SEED=1)

Configuration comes from the environment (DATABASE_URL, AUTH_MODE, ...);
no subcommand takes flags today.
`

// runners maps each subcommand to the package Run that implements it. It
// is a variable so the dispatch test can stand in for a runner instead of
// starting a server.
var runners = map[string]func(args []string){
	"serve":   func([]string) { serve.Run() },
	"worker":  func([]string) { worker.Run() },
	"migrate": func([]string) { migrate.Run() },
	"seed":    func([]string) { seed.Run() },
}

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches args to one subcommand and returns the process exit code.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	name := args[0]
	switch name {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	}
	runner, ok := runners[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "core: unknown command %q\n\n%s", name, usage)
		return 2
	}
	// The subcommand parses its own flags; none defines any today, so an
	// unknown flag is refused here rather than silently ignored.
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2 // flag has already printed the error and its usage
	}
	runner(fs.Args())
	return 0
}
