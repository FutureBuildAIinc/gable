# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# Route census

Item R1-2. This document reconciles the generated route census with the
planner's counts and the contract inventories. The census itself is
`core/api/ROUTES.txt`, generated from the Go sources; this document is
the narrative around it.

## The tool and the test

- `core/internal/routecensus` walks every non-test Go file of the module
  with `go/ast`, finds each `Handle` and `HandleFunc` call, resolves the
  pattern argument as a constant string (plain literals, concatenated and
  named constants, `net/http` method constants, function local and package
  level constants), and reports one route per registration: method, pattern,
  the registering package relative to the Go module root, and the handler
  expression as written.
- `core/cmd/census` prints the census; `go run ./cmd/census -write` from
  the Go module root regenerates `api/ROUTES.txt`. Paths stay relative to
  the Go module root, so the move of `backend` to `core` (R1-3) needs no
  change in the tool, the file or the test.
- `core/internal/routecensus/routecensus_test.go` re-collects the census
  from the sources and compares it with `api/ROUTES.txt`. On a difference it
  fails, naming every added and removed route, with the regenerate command
  in the message. It needs no database and runs under plain
  `go test ./...`.

Four failure modes, all loud:

1. A registration whose pattern is not a statically resolvable string fails
   the census. A registration style the tool does not understand can never
   silently miss a route; the coder extends the resolver or inlines the
   pattern. The same holds for a `Handle` or `HandleFunc` method value:
   every `Handle` or `HandleFunc` selector that is not the callee of a call
   is reported as unresolved, in every binding form (`f := mux.HandleFunc`
   and `var f = mux.HandleFunc`, a package level var, a returned method
   value, a struct literal field, a method value passed to a helper), and a
   call through the variable, field or helper registers where the walk
   cannot see it. The allow list `allowMethodValues` in the same file names
   the bindings the repo has reviewed as not a router; today it holds one
   entry, the exposure notifier's `notifier.Handle` event bus subscription
   in `cmd/server/wire_exposure.go`. The same holds for a pattern name the
   enclosing function binds as a receiver, parameter, named result or
   variable: the census refuses to guess the value a shadowing binding
   would carry at run time.
2. The same method and pattern registered more than once fails the census,
   unless the registrations sit in the if and else branches of one if/else,
   the only mutual exclusion the census can see (the portal login limiter
   is one). Two different registrations of one pattern cannot both be
   honoured, and an identical pair registered twice outside mutually
   exclusive branches panics the ServeMux at boot.
3. A restricted registration fails the census: an `http.NewServeMux`
   outside `cmd/server`, and a `Handle` whose handler mounts an
   `http.StripPrefix` or a sub mux. Such a mount rewrites the paths of
   everything under it, so the census could not list the mounted routes
   under their real paths. The allow list in
   `core/internal/routecensus/routecensus.go` (`allowMounts`) names the
   mounts the repo has accepted. Today it holds one entry, the `/uploads/`
   file server of `cmd/server`, which the census lists as its outer route
   (no method prefix, answering every method); anything else on the list
   is a deliberate, reviewed extension of it.
4. A route added or removed without regenerating `ROUTES.txt` fails the
   census test, naming the route. The diff works on the whole row, so a
   change in any column, handler included, is reported as the old row
   removed and the new row added.

The walk applies the go tool's own exclusions: directories with a leading
dot or underscore, `testdata`, and files whose build constraints exclude
them under the default build tags are not read. Calls inside the
`pkg/apps` `gatedRouter` forwarders whose pattern cannot resolve are
skipped as delegation; a call inside them whose pattern resolves is a
real registration and is counted.

The file carries no total line: a counted total inside the file would let
two branches that each add routes merge the same number silently while the
real sum grows. The test recomputes the whole set; counts live in this
document.

## Why a source walk and not a recording router

The alternative, building the real router behind a recording wrapper, was
rejected for two reasons.

1. The real router is assembled in `cmd/server/main.go` behind a database
   connection (pool, audit logger, portal demo customer lookup, registry
   sync, schedulers). Recording a boot without a database would require
   refactoring `main` into an injectable builder first, which is its own
   item (R1-4), not the census.
2. Part of the surface is conditional on configuration, so one boot records
   one configuration, not the declared surface. The source walk lists every
   registration the sources make, whatever the flags, and this document
   names the conditional ones.

## The counts

345 distinct routes. By prefix:

| Prefix | Routes |
|---|---|
| `/api/v1` (excluding `/api/v1/a2a`) | 288 |
| `/api/portal/v1` | 38 |
| `/api/integration` | 10 |
| `/api/v1/a2a` | 1 |
| `/api/partner/v1` | 3 |
| `/health`, `/healthz/live`, `/healthz/ready` | 3 |
| `/metrics` | 1 |
| `/uploads/` (no method prefix, answers every method) | 1 |
| Total | 345 |

By method: GET 164, POST 125, PUT 29, DELETE 21, PATCH 5, and the one
method-less `/uploads/` pattern.

40 packages register routes: 38 packages under `internal/`, plus `pkg/apps`
(the Apps API platform surface) and `cmd/server` (health, metrics,
uploads, the A2A receiver). The largest modules are pricing and portal with
34 routes each, then delivery 25 and pos 19.

### Conditional registrations

All of these are listed in `ROUTES.txt` like any other route.

- The 12 category pricing routes (`internal/pricing`) mount only when
  `CATEGORY_PRICING_ENABLED=true`.
- The A2A purchase order receiver (`POST /api/v1/a2a/purchase-order`)
  mounts only when `FB_BRAIN_ENABLED` is on and `FB_BRAIN_PUBLIC_KEY_PATH`
  is set.
- `POST /api/portal/v1/login` is registered twice in
  `internal/portal/handler.go`, through an if/else around the strict login
  limiter. Exactly one branch runs at a time and both register the same
  handler, so the route appears once in the census.
- 11 routes mount through the apps enablement gate: millwork 2,
  configurator 5 (both under the millwork app key) and governance 4. The
  gate checks enablement per request, it does not change which patterns are
  registered.

A default boot (both feature flags off) mounts 332 of the 345 declared
routes.

## Reconciliation

| Counter | Count | Reading |
|---|---|---|
| The planner: HandleFunc registrations | 304 | 303 non-test `HandleFunc` call sites plus 1 in a test fixture (`pkg/apps/registry_test.go:95`); the census walks non-test files only |
| The planner: routes in all | about 346 | the 346 raw non-test registration call sites (`HandleFunc` 303 plus `Handle` 43) |
| This census: distinct routes | 345 | 346 raw call sites minus the portal login if/else double |
| Commons inventory (`commons-routes.json`) | 345 | identical set: the 345 method and path pairs equal `ROUTES.txt` exactly, zero differences |
| MVP inventory (`mvp-routes.json`) | 280 | a different codebase (`GableLBM-main` at its master); the commons descends from it, so the sets differ; listed for completeness, not comparable route by route |

Every difference, explained:

1. Planner 304 versus census 303 `HandleFunc` sites: the extra one is in a
   test file, which the census deliberately skips (tests register fixtures,
   not server surface).
2. Planner about 346 versus census 345 routes: the planner counted raw
   registration call sites; the portal login if/else registers one logical
   route through two of them. 346 sites name 345 distinct routes.
3. Commons inventory 345 registration sites versus this census's 346 raw
   sites: the inventory counted the login double as one route, which is the
   census's distinct-route reading. Both readings arrive at 345 routes, and
   the inventory's route set matches `ROUTES.txt` route for route.

The census agrees with the planner's numbers within their own terms and
with the commons inventory exactly. R1-7 can take `ROUTES.txt` as the list
of operations to describe: every route in it needs exactly one operation.

## What the census does not cover

- Routes registered only in test files.
- Files the go tool does not build: names beginning with a dot or an
  underscore, files excluded by their build constraints under the default
  build tags, and directories so named. The census lists what a default
  build of this module registers.
- Routes a fork or plugin would register at runtime; the census reads this
  module's sources.
- The routes an `http.StripPrefix` or sub mux mount carries under their
  real paths; such mounts fail the census unless they are on the allow
  list, and a listed mount appears once as its outer route.
- Non-HTTP surfaces (worker crons, the event bus); they are not routes.
