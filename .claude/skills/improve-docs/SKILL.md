---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
name: improve-docs
description: Fix documentation in the Gable repo that is wrong, stale, or missing — verify it against the actual code, check every link and command still works, keep the right SPDX header, and open a PR against staging. Use when the user says "this doc is wrong", "the README is out of date", "the setup instructions don't work", "fix a typo", "document this", "the docs say X but the code does Y", "improve the docs", "/fix-doc", or hits an instruction in the repo that failed when they followed it.
---

# improve-docs — make the docs match the code

Documentation in this repo is checked against reality, not vibes. A doc fix is only done when
you have **run the thing the doc describes**.

Docs contributions are one of the best entry points to the project: no Go, no TypeScript,
and immediately useful to the next person.

---

## 1 · Find the target

The docs surface:

| Location | What's there |
|---|---|
| `README.md` | The project pitch, stack table, quickstart, licensing, contributing pointers |
| `CONTRIBUTING.md` | Build & run, branch model, PR workflow, pre-flight, CLA |
| `CLAUDE.md` | Stack, conventions, gotchas, money conventions, backlog. The deepest doc. |
| `SECURITY.md` | Private disclosure, supported branches, the `AUTH_MODE=dev` warning |
| `LICENSE-MAP.md` | Directory → license mapping |
| `CODE_OF_CONDUCT.md` | Contributor Covenant |
| `NOTICE`, `THIRD-PARTY-NOTICES.md` | Dependency and map-data attribution |
| `docs/architecture.md` | Module boundaries as built, API surface, hosting |
| `docs/modularization-blueprint.md` | Installable-apps design, conversion recipe, phases |
| `docs/design-system.md` | Colours, typography, component patterns |
| `docs/database-erd.md` | Schema and ERD |
| `docs/production-external-services-roadmap.md` | Self-hosting decisions for AI + routing |
| `.do/README.md` | Deploy notes for the Digital Ocean examples |
| `.github/ISSUE_TEMPLATE/`, `.github/PULL_REQUEST_TEMPLATE.md` | Contributor templates |
| `app/README.md` | Frontend-specific notes |

If they don't know where it lives:

```bash
grep -rn "the phrase they remember" --include='*.md' . | grep -v node_modules
```

**Out of scope for this skill** (do not edit): anything under `.github/`, `CONTRIBUTING.md`,
and `REUSE.toml` are owned elsewhere in this repo's workflow. If the fix belongs there, write
it up as an issue instead — use **`report-an-issue`**.

## 2 · Verify against reality — this is the actual work

Never "fix" a doc by making it read better. Fix it by making it **true**.

**If it's a command**, run it:

```bash
make up && make migrate && DEMO_SEED=1 make seed
cd backend && go run ./cmd/server
cd app && npm install && npm run dev
```

If a documented command fails, that failure *is* the bug and the corrected command is the
fix. Paste the real error in the PR.

**If it's a path or filename**, check it exists:

```bash
ls backend/internal/order/ app/src/lib/utils.ts docs/architecture.md
```

**If it's a claim about behaviour**, find it in the code:

```bash
grep -rn "AUTH_MODE" backend/internal/config/config.go backend/pkg/middleware/auth.go
grep -n "formatCents" app/src/lib/utils.ts
grep -n "DefaultTaxRate" backend/internal/invoice/*.go
```

**If it's a port, a version, or a default**, check the source of truth:

- Postgres port → `docker-compose.yml` and `backend/internal/config/config.go` (it's **5434**
  locally, not 5432)
- Go version → `backend/go.mod` and `.github/workflows/ci.yml`
- Node version → `.github/workflows/ci.yml`
- npm scripts → `app/package.json`
- Branch names → `CONTRIBUTING.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `.github/workflows/ci.yml`

### Known drift worth checking for

These are real inconsistencies that a docs contributor can legitimately fix:

- **Aspirational vs as-built.** `docs/architecture.md` §4.2 describes NATS events that are
  **not implemented** — no NATS client is imported in Go code. Anything describing the event
  bus as working is wrong. `docs/architecture.md` labels its forward-looking sections; if you
  find one that isn't labelled, label it.
- **Money conventions.** `CLAUDE.md` says money-as-cents is *the target, not current reality*
  — ERP orders/invoices and `account` use `int64` cents while portal, quotes, and DailyTill
  use `float64` dollars. Any doc that states "money is cents" flatly is overstating it.
- **Branch target.** `CONTRIBUTING.md`, the PR template, and `README.md` all say contributions
  target **`staging`**. `CLAUDE.md`'s branch table still says "Community PRs target
  [`community`]". That's stale — but `CLAUDE.md` is a high-traffic file, so fix it in its own
  small PR with the evidence, don't bundle it.
- **Coverage claims.** Check them against reality rather than repeating them:
  `find app/src -name '*.test.ts' | wc -l` and, for the backend,
  `for d in backend/internal/*/; do [ -z "$(ls $d*_test.go 2>/dev/null)" ] && echo "$d"; done`.
- **Gate lists.** Any doc listing the pre-flight commands must match the `Makefile` and
  `.github/workflows/ci.yml`. `make help` prints the current set. Note that `govulncheck` is
  an **advisory** job (`continue-on-error`, plus a nightly run), not a merge gate — a doc
  calling it a blocking check is wrong.

## 3 · Write the fix

- **Match the surrounding voice.** These docs are direct, second person, short paragraphs,
  tables for anything enumerable. Don't switch to marketing register.
- **Prefer deleting a wrong sentence to hedging it.** A stale claim removed is a real
  improvement.
- **Say "as built" or "planned"** whenever a doc describes something that doesn't exist yet.
- **Use real, runnable examples** — with the demo dataset (`DEMO_SEED=1 make seed` → "Gable Lumber &
  Supply"), not invented record IDs.
- **Keep line width consistent** with the file you're editing (most of these wrap around 80).
- **Don't restructure a whole document** in a typo PR. One concern per PR.

## 4 · Check the links

Every relative link must resolve, and every anchor must exist:

```bash
# extract markdown links from the files you changed and test the local ones
grep -oE '\]\([^)#][^)]*\)' <file>.md | tr -d '](' | while read -r l; do
  case "$l" in
    http*) echo "external (check by hand): $l" ;;
    *) [ -e "$(dirname <file>.md)/${l%%#*}" ] && echo "ok:      $l" || echo "BROKEN:  $l" ;;
  esac
done
```

For external links, actually open them. In particular the OpenLBM Standard link
(<https://github.com/FutureBuildAIinc/openlbm>) appears in `README.md`, `CONTRIBUTING.md`,
and `LICENSE-MAP.md` — if it moves, all three change.

## 5 · SPDX header

Docs in `docs/` are `LicenseRef-OpenLBM-Docs-1.0` per
[`LICENSE-MAP.md`](../../../LICENSE-MAP.md). **New** markdown files you create must carry the
header themselves, as an HTML comment at the very top:

<!-- REUSE-IgnoreStart -->

```markdown
<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->
```

<!-- REUSE-IgnoreEnd -->

Existing files under `docs/` are currently covered by the `docs/**` annotation in
`REUSE.toml` rather than per-file headers — **do not strip or "fix" that**, and do not edit
`REUSE.toml` (it's owned elsewhere). Add headers to files you create; leave existing files'
header situation as you found it unless the fix is specifically about licensing metadata.

If `reuse` is installed, verify:

```bash
reuse lint
```

## 6 · Open the PR

```bash
git fetch origin
git switch -c docs/fix-postgres-port origin/staging
git add docs/architecture.md
git commit -m "docs: correct local Postgres port to 5434 (matches docker-compose)"
```

> If `git switch` fails with `invalid reference: origin/staging`, this clone doesn't have the
> `staging` branch — branch from `origin/main` and still open the PR **against `staging`**
> on GitHub. Maintainers fast-forward `staging → main`.

Fill in `.github/PULL_REQUEST_TEMPLATE.md`, ticking **Documentation** under type of change.
In the summary, say:

- What was wrong (quote the old text).
- What it should say.
- **How you verified it** — the command you ran and its output. This is the part that gets
  docs PRs merged quickly.

Then run **`check-my-contribution`** for the SPDX and PR-target checks.

---

## Ground rules

- **Verify before you edit.** Run the command, check the path, read the code.
- **Never document something you haven't confirmed exists.** A confident wrong doc is worse
  than a gap.
- **Don't touch `.github/`, `CONTRIBUTING.md`, or `REUSE.toml`** — file an issue instead.
- **One concern per PR.** Typos separately from restructures.
- **No secrets, no real hostnames, no real customer data** in examples. Use placeholders and
  the seeded demo fixtures.
- **PRs target `staging`.**
