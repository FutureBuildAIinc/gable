---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
name: describe-a-workflow
description: Capture how a real lumberyard job actually works — quoting, will-call/pickup, dispatch and delivery, purchase orders and receiving, yard picking, credit holds, returns, cash till — as a buildable spec with acceptance criteria. Use when someone says "here's how we actually do will-call", "Gable doesn't handle our dispatch process", "let me explain how quoting works at our yard", "I want to describe a workflow", "can I contribute without coding", "I'm not a developer but I know how this should work", "spec out a feature", or when a feature request turns out to be a whole operational process. This is how a dealer with no coding ability makes a genuinely valuable contribution.
---

# describe-a-workflow — turn yard knowledge into something an engineer can build

The scarcest input in this project is not code. It is somebody who has actually run a
counter, dispatched a boom truck, or chased a will-call ticket, telling us **exactly** how
the job works — including the parts that are messy, regional, or "we just know".

You are interviewing that person and producing a spec. They should never have to open a
code editor. Your output is a document good enough that an engineer can build from it
without a second conversation.

**Do not let them write code. Do not write code for them.** The deliverable is the spec.

---

## 0 · Orient yourself first (don't make them explain what we already have)

Before the interview, spend a few minutes finding out what Gable does today, so you can ask
"what's different about yours?" instead of "how does quoting work?".

```bash
ls backend/internal/                      # 41 domain modules — find the closest one
ls app/src/pages/                          # the screens that exist today
grep -rn "status" backend/internal/order/model.go | head -30
```

Useful orientation reads:

- [`docs/architecture.md`](../../../docs/architecture.md) § 3 "Module Boundaries (as built)"
- [`CLAUDE.md`](../../../CLAUDE.md) § "Tier 1 Backlog" — the current candidate work items
  (will-call, pick lists, credit holds are all live candidates; if their workflow matches one,
  say so and reference it)
- The relevant module's `model.go` for the statuses and fields that already exist

Two known facts worth having in hand:

- Orders today flow `DRAFT → CONFIRMED → FULFILLED`, plus `ON_HOLD` (a valid `orders.status`
  as of migration 071). There is **no** pickup/will-call path and **no** `PICKED` step.
- Fulfilling an order posts one tax-inclusive invoice plus a balanced
  `DR Accounts Receivable / CR Sales Revenue` GL entry and an AR subledger debit, in one
  transaction. Anything that creates or cancels a sale has to respect that.

Open the interview by telling them, in one or two sentences, what Gable does today. Then ask
what's different.

---

## 1 · Interview: get the real process, not the tidy version

Work through these. Ask one at a time, in plain language. Follow the tangents — the tangents
are where the requirements live.

**The job**
1. What is the task called at your yard? What do other yards call it?
2. Who does it — counter, dispatcher, yard lead, driver, AP clerk, owner?
3. When does it start? What triggers it?
4. When is it *done*? How do you know?

**The steps**
5. Walk me through it, start to finish, as if I'm shadowing you. Every step.
6. What paperwork exists? Who signs what? Where does the paper go?
7. What does the customer see or receive at each step?
8. What gets printed, scanned, texted, or phoned?

**The reality**
9. What goes wrong most often?
10. What's the workaround everyone uses?
11. What has to be true before this can happen? (credit OK, stock on hand, truck available…)
12. What happens if it's *not* true? Who can override, and does anybody record that?
13. What changes for a big commercial customer versus a cash walk-in?
14. What changes at month-end, or in the busy season?

**The numbers**
15. How many of these a day? At what time of day?
16. What does it cost you when it goes wrong?
17. Is there a number somebody looks at to know this is going well?

**The edges**
18. What happens when it's cancelled halfway through?
19. What happens when the customer changes their mind after step N?
20. What happens with a partial — half the order picked, half the load delivered?
21. Is any of this different in your province/state (tax, permits, hours-of-service)?

Ask about **money** and **inventory** explicitly at every step: does this step move stock,
create a charge, take a payment, or change what the customer owes? Those are the steps that
need the most care.

---

## 2 · Write the spec

Create the file at `docs/workflows/<workflow-name>.md` — for example
`docs/workflows/will-call-pickup.md`. Use this exact skeleton. It mirrors the house spec
format used across the Gable program, including the **acceptance-criteria triad**.

<!-- REUSE-IgnoreStart -->

````markdown
<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Workflow: <Name>

**Contributed by:** <name / role / yard type — "counter lead, 3-location independent, BC">
**Date:** <yyyy-mm-dd>
**Closest existing module:** `backend/internal/<module>` (or "none — greenfield")
**Related backlog item:** <CLAUDE.md Tier 1 item, or "none">

## 1 · What this is, in one paragraph
Plain language. No jargon a maintainer in another country wouldn't know. Define the local
terms you do use ("will-call = customer picks up at the yard rather than us delivering").

## 2 · Who does it
| Role | What they do in this workflow |
|---|---|
| Counter sales | … |
| Yard lead | … |

## 3 · Trigger and completion
- **Starts when:** …
- **Ends when:** …
- **Volume:** ~N per day, peaking <when>

## 4 · The happy path
Numbered steps. For each: **who** does it, **what they see**, **what the system must record**,
and whether the step **moves inventory**, **creates a charge**, or **takes a payment**.

1. …
2. …

## 5 · States
The states this thing moves through, and every legal transition. Draw the illegal ones too —
those become tests.

| From | Event | To | Notes |
|---|---|---|---|
| `NEW` | counter confirms | `READY_FOR_PICKUP` | reserves stock |

## 6 · Rules and gates
- Preconditions that must hold (credit, stock, hours, licence).
- What happens when one fails.
- Who can override, and what gets written to the audit log.

## 7 · Data this needs
Fields, in business terms — an engineer maps them to columns. Flag anything that is:
- **money** (say whether it's tax-inclusive, and in what currency)
- **a physical quantity** (say the unit of measure — the schema pairs every quantity with a UOM)
- **a date/time** (say the timezone and whether it's a promise or a record)

## 8 · What the customer sees
Notifications, documents, signatures, the portal view. Quote the actual wording where it
matters legally.

## 9 · Edge cases
The list from interview questions 18–21. Each one gets an expected outcome, not a shrug.

## 10 · Reporting
What number tells the owner this is working.

## 11 · Acceptance criteria

### Technical — what an automated test asserts
- [ ] <e.g. "POST /api/v1/will-call-tickets on a CONFIRMED order → 201 and the order moves to
      READY_FOR_PICKUP; the full state matrix is table-tested">
- [ ] <e.g. "Pickup on an already-picked-up ticket → 409 and writes nothing (row count unchanged)">
- [ ] <edge case → expected result>

### PRR (production readiness) — the non-functional bar
- [ ] **Auth + scoping** — who may call this; branch/tenant-scoped; does not rely on `AUTH_MODE=dev`
- [ ] **Money/data integrity** — single writer, one transaction, no partial writes
- [ ] **Migrations additive + reversible** — forward and down; idempotent
- [ ] **No secrets committed**
- [ ] **Boot wiring** — registered via `RegisterRoutes` in `backend/cmd/server/main.go`
- [ ] **Rollback plan**
- [ ] **Observability** — what gets logged/measured; CI actually runs the new tests

### User-driven — the numbered walkthrough a yard person runs to validate it
1. Navigate to … → expect …
2. Do … → expect …
3. Verify … → expect …

## 12 · Open questions for maintainers
Things you genuinely don't know, or where yards differ and somebody has to choose.

## 13 · How other systems do it
If you've run BisTrack, Spruce, or Agility: what did they call this, and what did they get
right or wrong? Describe behaviour only — **do not paste their screens, schemas, API specs,
or documentation** into this repo.
````

<!-- REUSE-IgnoreEnd -->

---

## 3 · Quality bar — check your own spec before filing

The spec is ready when **all three** kinds of acceptance criteria are written and each one is
a **testable statement**. That's the house rule; a spec without all three is not buildable.

Then check:

- [ ] Someone who has never been in a lumberyard could follow §4 without asking a question.
- [ ] Every step that moves money or stock says so explicitly.
- [ ] Every state transition table row has an expected outcome, including the illegal ones.
- [ ] Every quantity names its unit of measure.
- [ ] Every money field says tax-inclusive or tax-exclusive.
- [ ] The user-driven walkthrough in §11 is numbered, concrete, and runnable on demo data.
- [ ] Open questions are listed rather than silently decided.
- [ ] No competitor documentation, schemas, or API specs are pasted in (§13 rule).

---

## 4 · File it

Two routes, and it's fine to do both:

**A. Open a feature request that links the spec.** Use
[`.github/ISSUE_TEMPLATE/feature_request.md`](../../../.github/ISSUE_TEMPLATE/feature_request.md).
In "Proposed solution", summarise in three sentences and link the spec file. In "Affected
area", name the module directory.

**B. Open a PR that adds the spec to `docs/workflows/`.** This is the higher-value version —
the spec lands in the repo where the next contributor finds it.

```bash
git fetch origin
git switch -c docs/workflow-will-call origin/staging
mkdir -p docs/workflows
# write docs/workflows/will-call-pickup.md with the SPDX header shown above
git add docs/workflows/will-call-pickup.md
git commit -m "docs: capture will-call / pickup workflow spec"
```

> If `git switch` fails with `invalid reference: origin/staging`, this clone doesn't have the
> `staging` branch yet — run `git fetch origin` again, or branch from `origin/main` and still
> open the PR **against `staging`** on GitHub.

Then run the **`check-my-contribution`** skill before pushing (it verifies the SPDX header
matches the directory and that the PR targets `staging`).

---

## Ground rules

- **You are the scribe, not the architect.** Their process is the requirement. If you think a
  step is wrong, put it in §12 as an open question — don't quietly redesign their yard.
- **Never invent a step.** If you don't know, ask. If they don't know, write "unknown — needs
  a second interviewee".
- **Name the units and the tax basis.** "500 board feet, tax-exclusive" is a spec. "the total"
  is not.
- **No competitor IP.** Describe behaviour from memory; never paste BisTrack/Spruce/Agility
  documents, schemas, or API specifications into this repo.
- **No real customer data.** Use the seeded Gable Lumber & Supply fixtures for examples.
- **Regional differences are features, not noise.** Write them down.
