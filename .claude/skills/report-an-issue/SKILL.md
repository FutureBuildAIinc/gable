---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
name: report-an-issue
description: Turn "something is broken in Gable" into a properly scoped, filed bug report or feature request. Use when the user says something like "the delivery screen shows the wrong total", "the order total is off by 100x", "this page is broken", "I found a bug", "the yard app won't scan", "prices are wrong on the portal", "I want to report a problem", "file an issue", "how do I report this", or describes any behaviour in the ERP, POS, portal, driver, or yard surfaces that surprised them. Also handles routing security problems privately to SECURITY.md instead of a public issue.
---

# report-an-issue — from "that looks wrong" to a filed, actionable issue

You are helping someone report a problem in Gable. **They may not be a programmer.** A
lumber-yard counter person, a dispatcher, a driver, or a docs writer is exactly who this
skill is for. Your job is to do the technical work *for* them so the issue a maintainer
receives is complete, reproducible, and correctly routed.

Never make them guess at a version number, a file path, or a module name. Go find it.

---

## 0 · Stop: is this a security problem?

**Do this check first, before anything else.** If the report involves any of the
following, it does **not** go in a public issue:

- Seeing data belonging to a customer, branch, or user that shouldn't be visible
- Getting into a page or an action without logging in, or with the wrong role
- A password, API key, token, or credential appearing anywhere (a log, a URL, a screen)
- Being able to change money, invoices, payments, or the GL in a way that looks unauthorised
- An internet-reachable Gable instance where nobody is asked to log in

If any of those apply, say so plainly and route them to
[`SECURITY.md`](../../../SECURITY.md):

> This looks like a security issue, so it should **not** go in a public issue — a public
> report tells every operator running Gable about the hole before a fix exists.
> Report it privately instead: open the repository's **Security** tab →
> **Report a vulnerability** (a private advisory only you and the maintainers can see),
> or email **security@futurebuild.ai**. `SECURITY.md` in this repo has the full process
> and what to include.

Then help them write the *private* report using the same evidence-gathering below. Stop
here — do not draft a public issue.

**The one exception people trip over:** the demo and staging deployments run with
`AUTH_MODE=dev`, which intentionally disables login and treats everyone as the seeded
`demo@gable.com` admin. On a deployment that is *deliberately* a demo or staging sandbox
that is expected and safe (the data is fake). It is only a security issue if you find it on
a host that is meant to be real. See `SECURITY.md` § "`AUTH_MODE=dev` must never reach production".

---

## 1 · Get the story in their words

Ask for, and write down verbatim:

1. **What screen were you on?** ("the delivery screen", "order detail", "the POS till")
2. **What did you do?** ("clicked Confirm", "scanned a bundle tag", "opened order 1042")
3. **What did you expect?** ("total should be $738.87")
4. **What actually happened?** ("it said $73,887.00")
5. **Does it happen every time,** or did it happen once?

Do not editorialise. "The total was wrong" is the report; your theory about *why* goes in
a separate section clearly marked as a guess.

---

## 2 · Find the screen in the code (so the maintainer doesn't have to)

Map the human description to a real path. The route table is
[`app/src/routes.ts`](../../../app/src/routes.ts) — grep it for the URL they were on:

```bash
grep -n "delivery" app/src/routes.ts
```

The surface trees are:

| They said | URL prefix | Lives under |
|---|---|---|
| "the ERP", "the main app", the desktop screens | `/erp/*` | `app/src/pages/` |
| "the customer portal", "the contractor site" | `/portal/*` | `app/src/pages/portal/` |
| "the driver app", "on my phone in the truck" | `/driver/*` | `app/src/pages/driver/` |
| "the yard", "the warehouse scanner" | `/yard/*` | `app/src/pages/yard/` |
| "the till", "the front counter", "POS" | `/pos` | `app/src/pages/pos/` |

If the problem is in data rather than display, the backend module is under
`backend/internal/<module>/` — there are 41 of them. `ls backend/internal/` and pick the
obvious one (`order`, `invoice`, `delivery`, `inventory`, `pricing`, `payment`, `pos`…).

Record the **directory**, not just a guess: the issue template asks for it, and Gable is
licensed per component so naming the directory routes the report.

---

## 3 · Check it against the known landmines *before* filing

A large share of "bugs" in Gable are one of these. Check each one and say in the issue
which you ruled out — that alone makes the report better than most.

### a. Money rendered 100× too big or too small

**The single most common real bug in this repo.** ERP API money fields are `int64` **cents**;
portal API money fields are `float64` **dollars**. If an ERP page calls `.toFixed(2)`
directly on a money field, `$738.87` renders as `$73,887.00` — and the reverse if a portal
page runs a cents helper over dollars.

Check the page:

```bash
grep -n "toFixed\|formatCents" app/src/pages/<the-page>.ts
```

- ERP pages (`/erp/*`, `/pos`, `/yard/*`, `/driver/*`) **must** use `formatCents()` from
  [`app/src/lib/utils.ts`](../../../app/src/lib/utils.ts).
- Portal pages (`/portal/*`) already receive dollars and must **not** use `formatCents()`.

If the wrong total is off by exactly 100×, you have found the bug and can say so precisely:
*"`app/src/pages/X.ts:NN` renders an ERP cents field with `.toFixed(2)` instead of
`formatCents()`."* That is a one-line fix and a maintainer will love you.

Also possible without being a display bug: money conventions genuinely differ per module
(ERP orders/invoices and the `account` module use cents; portal, quotes, and DailyTill use
float dollars). `CLAUDE.md` § "Money convention is not uniform across modules" has the table.

### b. Demo data vanished / numbers reset

`backend/cmd/seed/main.go` **truncates all transactional data** (orders, invoices, quotes,
deliveries, payments, GL entries, POs, POS/CRM rows) at the start of every run, and the seed
runs on every demo/staging deploy. Orders you created on demo yesterday are *supposed* to be
gone today. Reference data (customers, products, vendors, locations) is upserted and survives.

That is not a bug. It **is** a bug if reference data you edited got reverted — that means an
`ON CONFLICT DO NOTHING` where an upsert was needed.

### c. Everyone appears to be an admin

Expected on demo/staging (`AUTH_MODE=dev`). See §0.

### d. A generated product image never appears

`pim.GenerateImage` is **asynchronous**: it returns `202` immediately and writes a `pim_media`
row with `status:"generating"`, then finalises in the background while the frontend polls.
Waiting ~30 seconds is normal. It's only a bug if the row is still `generating` minutes later,
or the UI never polls.

### e. A map pin or route is in the wrong place

Routing uses OpenRouteService, which orders coordinates `[lng, lat]` — the **reverse** of
Google. A pin in the wrong hemisphere is almost always a swapped pair. Also: if no
OpenRouteService key is configured in **Tech Admin → Routing**, routing returns mock data by
design. Check whether a key is set before reporting bad routes.

### f. Tax is wrong on an invoice

The rate comes from the invoice's **branch** (`locations.default_tax_rate` — e.g. 0.12 in BC),
resolved by `invoice.CreateInvoice` via `repo.GetBranchTaxRate`. `invoice.DefaultTaxRate =
0.0825` is only the fallback when the branch has no configured rate. "Tax is 8.25% when it
should be 12%" usually means the branch's rate is unset or the invoice wasn't stamped with a
`branch_id`, not that the tax code is broken.

### g. An AI feature does nothing

AI degrades gracefully by design when no OpenRouter key is configured (**Tech Admin → AI**).
Silence is expected; a hard crash is not.

### h. Is it already reported?

Search open **and** closed issues for the key phrase before filing. Say in the issue what you
searched for.

---

## 4 · Try to reproduce it, and capture the environment

If they are on a laptop with the repo, reproduce it locally so the report is grounded:

```bash
make up                                    # Postgres on :5434
make migrate
DEMO_SEED=1 make seed                      # demo dataset (gate is required)
cd backend && go run ./cmd/server          # API on :8080
cd app && npm install && npm run dev       # SPA on :5173
```

Then open <http://localhost:5173> and walk their steps.

If they are only describing what they saw on demo/staging, that is still a valid report —
say so explicitly ("reproduced on the demo deployment, not verified locally" — and name the
host) rather than implying a local repro that didn't happen.

**Collect the environment facts yourself** — run these and paste the real output:

```bash
git rev-parse --short HEAD && git rev-parse --abbrev-ref HEAD
go version
node -v
docker exec gable_postgres psql -U gable_user -d gable_db -c 'SELECT version();'
```

Also grab, if relevant:

- **Backend logs** from the terminal running `go run ./cmd/server`
- **Browser console** — F12 → Console tab, and the Network tab entry for the failing request
- **A screenshot** of the wrong screen

**Redact before pasting.** Strip anything that looks like a key, token, password, or a real
customer name/address. Demo data is fake and safe; a screenshot from a live yard is not.

---

## 5 · Fill in the repo's actual issue template

Use the real templates in this repo — do not invent a format:

- Bug: [`.github/ISSUE_TEMPLATE/bug_report.md`](../../../.github/ISSUE_TEMPLATE/bug_report.md)
- Feature / "it should do X": [`.github/ISSUE_TEMPLATE/feature_request.md`](../../../.github/ISSUE_TEMPLATE/feature_request.md)

Read the template file and fill **every** heading. Write the filled-in issue out for the
person to copy, or offer to open it with `gh issue create` if they have the GitHub CLI:

```bash
gh issue create --repo FutureBuildAIinc/gable \
  --title "[Bug] Delivery detail renders order total 100x too high" \
  --label bug --body-file /tmp/issue.md
```

Rules for the body:

- **Title**: what is wrong and where, in one line. `[Bug] ` prefix (the template sets it).
  Not "delivery broken" — "[Bug] Delivery detail renders order total 100× too high".
- **Affected area**: the real directory (`app/src/pages/...`, `backend/internal/delivery`).
- **Steps to reproduce**: numbered, starting from a fresh `DEMO_SEED=1 make seed`, with the specific
  record they used ("open order `SO-1042` from the seeded Gable Lumber & Supply data").
- **Expected vs actual**: exact numbers and exact error text.
- **Environment**: the real output from §4, including the `AUTH_MODE` value.
- **Additional context**: what you ruled out in §3, and — clearly labelled as a hypothesis —
  where you think it lives. Never assert a cause you haven't verified.

If it is a **feature request** rather than a defect, and the person is describing how their
yard actually works, stop and use the **`describe-a-workflow`** skill instead. It produces a
far more useful artefact than a feature request: a structured spec with acceptance criteria
an engineer can build straight from.

---

## 6 · Offer the next step honestly

Close by telling them what happens next and what they *could* do:

- If the fix is a one-liner you already located, offer to make it — then hand off to
  **`check-my-contribution`** to run the pre-flight before opening the PR (which targets
  **`staging`**, not `main`).
- If it needs a maintainer decision, say so. A well-scoped issue is a complete contribution;
  they do not owe anyone a patch.

---

## Ground rules

- **Security bugs never go in public issues.** §0 is not optional.
- **Never paste a secret, key, password, or real customer data** into an issue. Redact first.
- **Don't assert a root cause you haven't checked.** Label guesses as guesses.
- **Don't file a duplicate.** Search first, and say what you searched.
- **Don't file "it's slow" or "it's confusing" without specifics.** Get a number, a record ID,
  or a screenshot.
