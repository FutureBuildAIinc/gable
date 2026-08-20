---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
name: check-my-contribution
description: Run the real pre-flight gates before opening a pull request against Gable — the exact commands CI runs (go build, go vet, go test -race, npx tsc --noEmit, npm run lint, npm run test, npm run build, reuse lint), plus SPDX headers matching LICENSE-MAP, no committed secrets or binaries, and that the PR targets staging. Use when the user says "check my work", "am I ready to open a PR", "run the pre-flight", "will CI pass", "did I break anything", "review my changes before I push", "/preflight", or is about to commit or push.
---

# check-my-contribution — the pre-flight, run for real

Run the gates. Report what failed and why. **Do not declare "looks good" without having
actually executed the commands.**

---

## 0 · The source of truth

The repo keeps the local pre-flight and CI deliberately in sync:

- [`Makefile`](../../../Makefile) — `make help` lists every gate. `make preflight` is the
  aggregate.
- [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml) — what actually gates a
  merge.

**Read both before you start.** They are maintained; this skill can drift. If a command below
disagrees with the Makefile, the Makefile wins — and that's a docs bug worth reporting.

```bash
make help
```

### What actually blocks a merge

| CI job | Gate? | What it runs |
|---|---|---|
| **Backend** | ✅ merge gate | `go vet ./...`, `go build ./...`, `go run ./cmd/migrate`, `go test -race -coverprofile=… ./...` |
| **Frontend** | ✅ merge gate | `npm ci`, `npx tsc --noEmit`, `npm run lint`, tests with coverage, `npm run build` |
| **License (REUSE)** | ✅ merge gate | `reuse lint` (report) then `python3 .github/scripts/reuse_gate.py` (the gate) |
| **Docker Build** | ✅ merge gate | builds `backend/Dockerfile` and `app/Dockerfile` |
| **Vulnerabilities** | ⚠️ **advisory only** | pinned `govulncheck` — `continue-on-error`, also runs nightly |

`govulncheck` is deliberately **not** a merge gate: an upstream CVE landing overnight must not
turn a contributor's unrelated PR red. If it's the only thing complaining, note it and move on.

---

## 1 · What actually changed

```bash
git rev-parse --abbrev-ref HEAD
git status --short
git diff --stat origin/staging...HEAD 2>/dev/null || git diff --stat
```

Note which top-level areas are touched — `backend/`, `app/`, `docs/`, `.github/`, root. That
determines which gates matter and which license applies (§4).

## 2 · Run the gates

**The fast path — everything CI gates on:**

```bash
make preflight
```

That is `preflight-backend` (build, vet, test) + `preflight-frontend` (typecheck, lint, test,
build) + `license-check`. If it passes, you're clear.

**Individually, when you need to isolate a failure:**

```bash
# Backend
make build            # cd backend && go build ./...
make vet              # cd backend && go vet ./...
make test             # cd backend && go test -race ./...     (needs Postgres)
make test-short       # cd backend && go test -short ./...    (no Postgres — DB tests self-skip)
make cover            # coverage profile + function summary

# Frontend
make fe-install       # cd app && npm ci
make fe-typecheck     # cd app && npx tsc --noEmit
make fe-lint          # cd app && npm run lint
make fe-test          # cd app && npm run test -- --run
make fe-build         # cd app && npm run build

# Licensing
make license-check    # reuse lint (report) + .github/scripts/reuse_gate.py (the gate)

# Advisory
make vuln             # pinned govulncheck — informational, not a merge gate
```

**Things that catch people out:**

- CI runs `go test -race`, not plain `go test`. A data race plain `go test` tolerates turns CI
  red. `make test` uses `-race`; use it.
- `make test` needs Postgres. `make up` boots it (port **5434** locally, the docker-compose
  mapping — not 5432). Without Docker, `make test-short` is the honest fallback: DB-backed
  tests skip themselves under `-short`.
- If you added a migration, prove it applies from empty: `make reset-db` (drop, create,
  migrate, seed). CI does the equivalent with `go run ./cmd/migrate` against a fresh database.
- `npm run build` is `tsc -b && vite build`, so it type-checks again. If `npx tsc --noEmit`
  passes but the build fails, it's usually a project-reference or asset-resolution problem,
  not your types.
- `make license-check` needs the `reuse` tool: `pipx install reuse==6.2.0` (the version CI
  pins). If it isn't installed, say `SKIPPED` — don't claim it passed.

## 3 · Repo hygiene

```bash
# No build binaries. This repo has shipped 60MB+ blobs before — see CLAUDE.md gotchas.
git diff --cached --name-only | while read -r f; do
  [ -f "$f" ] && file "$f" | grep -q "executable\|ELF\|Mach-O" && echo "BINARY: $f"
done

# Anything unusually large?
git diff --name-only origin/staging...HEAD 2>/dev/null | while read -r f; do
  [ -f "$f" ] && du -h "$f"
done | sort -rh | head

# Key-shaped strings in the diff
git diff origin/staging...HEAD 2>/dev/null | \
  grep -nE 'sk-or-v1-|dop_v1_|ghp_|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----'

# .env files
git diff --name-only origin/staging...HEAD 2>/dev/null | grep -E '(^|/)\.env(\.|$)'
```

If a secret shows up, **do not just delete the line and commit again** — it stays in history.
Rotate the credential first, then rewrite the branch (`git reset` + recommit, or
`git rebase -i`) before pushing.

Also confirm you have not introduced `AUTH_MODE=dev` into any config that could reach a public
host. It is fine in `.do/app-demo.yaml` and `.do/app-staging.yaml` (demo data only); never
anywhere else. See [`SECURITY.md`](../../../SECURITY.md).

## 4 · SPDX header must match the directory

Gable is licensed **per component** and the REUSE gate is a merge gate — a file with no
licensing information fails the build. Every source file carries an `SPDX-License-Identifier`
matching its directory, per [`LICENSE-MAP.md`](../../../LICENSE-MAP.md) and
[`REUSE.toml`](../../../REUSE.toml).

| Path prefix | Required identifier |
|---|---|
| `backend/internal/` | `LicenseRef-OpenLBM-Commons-1.0` |
| `backend/pkg/` *(except `backend/pkg/apps/`)* | `LicenseRef-OpenLBM-Commons-1.0` |
| `backend/cmd/` | `LicenseRef-OpenLBM-Commons-1.0` |
| `backend/migrations/` | `LicenseRef-OpenLBM-Commons-1.0` |
| `backend/pkg/apps/` | `LicenseRef-OpenLBM-Connector-1.0` |
| `app/` | `LicenseRef-OpenLBM-Surface-1.0` |
| `docs/` | `LicenseRef-OpenLBM-Docs-1.0` |
| `.claude/` | `LicenseRef-OpenLBM-Docs-1.0` |

**Most specific path wins.** `backend/pkg/apps/` is the connector seam, carved out of the
`backend/pkg/` Commons default — a new file there is **Connector**, not Commons. Getting that
backwards is the most common licensing mistake in this repo.

Check every file you added:

<!-- REUSE-IgnoreStart -->

```bash
for f in $(git diff --name-only --diff-filter=A origin/staging...HEAD 2>/dev/null || git diff --name-only --diff-filter=A); do
  printf '%-60s %s\n' "$f" "$(grep -m1 -o 'SPDX-License-Identifier: .*' "$f" 2>/dev/null || echo 'MISSING')"
done
```

Header format by file type:

```go
// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
```
```ts
// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
```
```sql
-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
```
```markdown
<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->
```

<!-- REUSE-IgnoreEnd -->

Files that can't hold a comment (images, JSON, binaries) get a sidecar: `<filename>.license`
containing the two `SPDX-` lines. A Markdown or YAML file that opens with `---` frontmatter
puts the tags as `#` comments **inside** the frontmatter so it still parses — that's what the
files in `.claude/` do.

Then run the machine check:

```bash
make license-check
```

Unsure which license applies? Use the **`licensing-check`** skill.

## 5 · Convention checks (the things reviewers ask for)

From [`CLAUDE.md`](../../../CLAUDE.md) § Pre-Flight Checks — verify each that applies:

- [ ] **New DB columns**: UUID v4 PKs (`uuid_generate_v4()`), `DECIMAL(19,4)` for physical
      quantities (never float), money-as-cents in application code, every quantity paired
      with a UOM id.
- [ ] **New migration**: plain numbered SQL in `backend/migrations/`, continuing the sequence;
      additive and reversible; applies from empty.
- [ ] **New endpoint**: under the correct prefix (`/api/v1`, `/api/portal/v1`,
      `/api/integration`, `/api/v1/a2a`) **and** wired into a `RegisterRoutes` call in
      `backend/cmd/server/main.go`. An endpoint that isn't wired in silently doesn't exist.
- [ ] **New public path**: if it must skip auth, it has to be in the whitelist in
      `backend/cmd/server/main.go` — and adding one deserves a security review.
- [ ] **Money on ERP pages**: rendered with `formatCents()` from `app/src/lib/utils.ts`, never
      `.toFixed(2)` on a cents field. Portal pages receive dollars and must not use it.
- [ ] **UI**: design tokens from `app/tailwind.config.js`, no hardcoded colours; JetBrains
      Mono for numbers/SKUs/prices/dimensions.
- [ ] **New page**: component under `app/src/pages/…`, registered in `app/src/routes.ts` with
      a lazy `load: () => import(...)` and the correct `layout`. (Converted apps declare routes
      in `app/src/apps/<key>.ts` instead.)
- [ ] **HTTP from the frontend**: via `services/fetchClient.ts`, never a bare `fetch`.
- [ ] **Financial operations**: audit-logged via `pkg/audit.Logger`.

## 6 · The PR itself

- **Target branch is `staging`.** Not `main`. `CONTRIBUTING.md`, the PR template, and the
  README all say so; maintainers fast-forward `staging → main` after review.
  ```bash
  gh pr create --base staging --fill    # if you use the GitHub CLI
  ```
  (Older notes mention `community` — that was the demo-branch model. `staging` is current.)
- **Fill in `.github/PULL_REQUEST_TEMPLATE.md` honestly.** Only tick a pre-flight box you
  actually ran. An unticked box with a note is fine; a ticked box that's false is not.
- **Commits are focused** — one logical change each, with a message saying what and why.
- **The CLA.** Inbound contributions are licensed under the same OpenLBM Standard license as
  the files you touched. You'll be asked to agree before your first merge. See
  `CONTRIBUTING.md` § "Licensing of contributions".

---

## Reporting back

Give a short verdict table, then the details:

```
Gate                       Result
make vet                   PASS
make build                 PASS
make test                  FAIL — internal/order: TestCreditLimit (see below)
make fe-typecheck          PASS
make fe-lint               PASS
make fe-test               PASS
make fe-build              PASS
make license-check         FAIL — backend/pkg/apps/foo.go has Commons, needs Connector
make vuln (advisory)       SKIPPED — not a merge gate
secrets / binaries         clean
PR target                  staging
```

Then, for each failure: the exact command, the exact error, and the smallest fix.

**Never report a gate as passing if you did not run it.** If a gate can't run (no Docker, no
Go toolchain, `reuse` not installed), say `SKIPPED — <reason>` and state what CI will do with
it.
