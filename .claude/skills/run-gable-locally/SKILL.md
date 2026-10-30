---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
name: run-gable-locally
description: Start the whole Gable stack locally with seeded demo data and stop or reset it. Checks prerequisites (Docker with compose v2, disk, memory, free ports), runs make up-mail, waits for health, proves the seed through the API, then reports the URL, how dev sign-in works, which demo persona to pick for which role, Mailpit's URL, and how to stop, reset and rebuild. Use when the user says "start Gable", "run Gable locally", "spin up the demo", "boot the ERP", "I just cloned the repo, what now", "start Gable locally with demo data", "/start-gable", "/stop-gable", or wants to try Gable without installing Go or Node.
---

# run-gable-locally: the whole ERP on one machine

One command builds and starts everything in Docker: Postgres 16, a one shot migrate
and seed step that loads the invented "Gable Building Supply" demo data, the core API
(`AUTH_MODE=dev`), the worker, the web image (the front door at `/` and the desk
everywhere else, `/api` proxied), and, with `make up-mail`, Mailpit. Nothing outside
Docker is installed, and nothing is published beyond 127.0.0.1.

**Dev mode is local only.** `AUTH_MODE=dev` switches authentication off: every caller
that reaches the port has full admin reach, so the stack binds 127.0.0.1 and is for a
personal machine against throwaway demo data. It is never for a server anyone else can
reach ([`SECURITY.md`](../../../SECURITY.md)).

Run every step for real and read its output. Report what you did and what answered,
with the timings; never say a step passed without having run it.

---

## 1 · Prerequisites: check, do not assume

Run these and read the answers before starting anything:

```bash
docker version --format '{{.Server.Version}}'   # the daemon answers with a version
docker compose version                          # v2.x, the make targets call `docker compose`
df -h .                                         # the first run wants 10 GB free or more
free -h                                         # Linux and WSL2: roughly 4 GB available for the builds
```

- If `docker version` errors, Docker is installed but not running (Docker Desktop not
  started, or the daemon down). Say so and stop: the user starts Docker, then reruns.
- If `docker` or `docker compose` is missing entirely, send the user to
  <https://docs.docker.com/get-docker/> (on Windows: Docker Desktop with WSL2, and run
  from inside WSL2). macOS and Linux run it natively.
- The first run pulls a Go image, a Node image, Postgres and Mailpit, then compiles the
  backend and builds the web bundle inside Docker: 10 GB free covers it, and the builds
  want roughly 4 GB of memory available.

Free ports. The stack binds 127.0.0.1 on four host ports. Check listeners:

```bash
# Linux and WSL2 (empty output: all four are free)
ss -ltn | grep -E ':(8080|5434|8025|1025)\b'
# macOS
lsof -nP -iTCP -sTCP:LISTEN | grep -E ':(8080|5434|8025|1025)\b'
```

If a port is taken, start with the matching override instead (section 5 has the full
list), for example `GABLE_WEB_PORT=8180 make up-mail`. Only the web port matters for
browsing the app; the others are the stack's own.

## 2 · Start

```bash
make up-mail
```

This is `make up` plus the Mailpit profile with the core's outgoing mail routed into
it, so mail the demo sends is read in Mailpit's inbox instead of the core's log. Plain
`make up` starts the same stack without Mailpit. The target builds the two local images
(`gable-core:local`, `gable-web:local`), starts every service and waits until each is
healthy; the first build takes a while (record it). Watch the command finish: compose
prints which service failed when one does, most often a taken port ("port is already
allocated"), which section 1 should have caught.

No Go, Node or Postgres is installed on the host for this; it all runs in Docker.

## 3 · Prove the stack and the seed

`make up-mail` already waited for health; prove the rest through the API. With a port
override, put the override's number in the URLs.

```bash
# The front door answers
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/
# A known demo customer answers: the seed's Tarnwick Construction
curl -s 'http://127.0.0.1:8080/api/v1/customers?q=Tarnwick'
# A known demo product answers: the seed's 2x4x8 SPF Premium
curl -s 'http://127.0.0.1:8080/api/v1/pos/products/search?q=LUM-248-PREM'
# Mailpit answers (only under make up-mail)
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8025/
```

Read each body: the customers call returns its `items` array holding
`"name": "Tarnwick Construction"`; the product search returns a row with
`"sku": "LUM-248-PREM"` and `"description": "2x4x8 SPF Premium"`; both curl status
checks print `200`. If a body is an error shape or the array is empty, the seed did not
finish or core is not up: `make ps`, then `make logs` (the migrate service's log names
a failed migration or seed) and report what you see.

The deeper end to end check is `make smoke` (`scripts/smoke.sh`: a quote through the
API, idempotency across a core restart, the events feed). Optional; run it when the
user asks for proof beyond the seed.

## 4 · Tell the user what they have

Give a short message with, in this order:

1. **The URL**: `http://127.0.0.1:8080` (or the `GABLE_WEB_PORT` override).
2. **How sign in works**: the stack runs in dev mode, so the door asks for no
   password. It shows one card per demo persona; click one. Or type any display name
   under "Sign in with another name". Local only.
3. **Which persona for which role** (the four the first run names; the demo set has
   nine cards in total):

   | To see the product as | Pick the card | What it opens |
   |---|---|---|
   | The counter clerk | Jordan Pike, Cashier, Kelowna Main Yard | Point of Sale: the counter, a sale on account or cash, returns |
   | The manager | Morgan Hale, Owner, all three yards | The dashboard (orders, dispatch, AR), stock across yards |
   | Finance | Robin Diaz, Finance, Kelowna Main Yard | Reporting and AR aging, a customer statement |
   | The owner | Morgan Hale, Owner (the demo set has one owner persona, so the owner and the manager are the same person) | Every tile |

   The other cards: Avery Brooks (Sales), Casey Lin (Purchasing), Sam Ortiz
   (Warehouse), Taylor Reed (Sales, West Kelowna), Drew Patel (Cashier, Lake Country)
   and Alex Moreno (Driver).
4. **Mailpit's URL**: `http://127.0.0.1:8025` (or the `GABLE_MAILPIT_UI_PORT`
   override). Mail the demo sends, a statement for instance, lands there under
   `make up-mail` and never leaves the machine.
5. **How to stop**: `/stop-gable`, or `make down`.
6. **How to reset and rebuild**: section 6 below, in one line each.

Say plainly: this is dev mode on 127.0.0.1 with invented demo data, never a server.

## 5 · Ports and names, the whole list

| What | Default | Override |
|---|---|---|
| Web, front door and desk | 127.0.0.1:8080 | `GABLE_WEB_PORT` |
| Postgres | 127.0.0.1:5434 | `GABLE_PG_PORT` |
| Mailpit inbox page | 127.0.0.1:8025 | `GABLE_MAILPIT_UI_PORT` |
| Mailpit SMTP | 127.0.0.1:1025 | `GABLE_MAILPIT_SMTP_PORT` |
| Postgres container name | `gable_stack_postgres` | `GABLE_PG_CONTAINER` |
| Compose project (and volume prefix) | `gable-stack` | `STACK_PROJECT=...` on the make command |

The stack runs in its own compose project (`gable-stack`), so it never shares a volume
with the Postgres that `make db` starts for the from source workflow.

## 6 · Stop, reset, rebuild

```bash
make down    # stops and removes the stack's containers AND its volume
```

Check it left nothing (both empty):

```bash
docker ps -a --filter label=com.docker.compose.project=gable-stack
docker volume ls --filter label=com.docker.compose.project=gable-stack
```

Only the stack's own data is removed; a Postgres started with `make db` is another
project and is not touched.

- **Reset the demo data**: the stack's seed repaves the demo data on every start, so
  reset is `make down && make up-mail` (or `make up`). For the from source workflow
  (`make db`), `DEMO_SEED=1 make reset-db` drops, recreates, migrates and seeds
  `gable_db` (`DEMO_SEED=1` is what lets the seed write at all; without it the seed
  connects to nothing and exits 0).
- **Rebuild after changing code**: the make targets build on every run, so `make
  up-mail` again picks the change up. For a from scratch image build,
  `docker image rm gable-core:local gable-web:local` first, then start again.
- **Look inside**: `make ps` for state, `make logs` to follow (the exit test's deep
  check is `make smoke`).

## 7 · What never happens here

- No secret is printed or written. The stack's Postgres password is the compose dev
  default already in the repository, and everything else the stack needs is generated
  per run by the compose file itself.
- The demo data is invented (Gable Building Supply, Kelowna); no real dealer's data.
- Nothing is started outside Docker, nothing listens beyond 127.0.0.1, and no variant
  of this stack is for a reachable host. A real deployment runs the published image
  without the devauth build and with real authentication (ADR 0012), never this stack.
