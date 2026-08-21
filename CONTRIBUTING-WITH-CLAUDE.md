<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Contributing to Gable with Claude Code

**You do not have to be a programmer to contribute to Gable.**

If you run a lumber yard, work a sales counter, dispatch trucks, pick orders, or manage a
dealership, you know things this project needs and can't get anywhere else. This guide shows
you how to turn that knowledge into a real contribution using **Claude Code** — an AI
assistant that works inside this repository and already knows how it's built.

Programmers: this is also the fastest way to get a repo-aware setup. Skip to
[The skills](#the-skills).

---

## What Claude Code is

Claude Code is a command-line tool from Anthropic. You point it at a folder of code, describe
what you want in ordinary English, and it reads the files, runs commands, and makes changes —
showing you each step and asking before it does anything significant.

It is not magic and it is not always right. Think of it as a fast, tireless assistant who has
read the whole codebase but has never worked at your yard. You supply the judgement.

### Install it

1. **Get the repository onto your computer.** You need [Git](https://git-scm.com/downloads).
   ```bash
   git clone https://github.com/FutureBuildAIinc/gable.git
   cd gable
   ```
2. **Install Claude Code.** Follow the official instructions at
   <https://docs.claude.com/en/docs/claude-code/overview>. You will need a Claude account.
3. **Start it, from inside the `gable` folder:**
   ```bash
   claude
   ```

That's it. Type what you want in plain English and press enter.

### The kit loads itself

This repository ships a `.claude/` folder containing everything below. When you start Claude
Code from inside `gable`, it picks that up automatically — the skills, the shortcuts, and
`CLAUDE.md` (a long file of conventions and gotchas that stops it giving you generic advice).

You don't install anything. You don't configure anything. It's already there.

---

## The skills

A **skill** is a set of instructions Claude follows for a particular kind of job. You never
have to name one — describe what you want and the right skill activates. Each also has a
**slash command** shortcut if you prefer.

| Skill | Shortcut | Use it when |
|---|---|---|
| **report-an-issue** | `/file-issue` | Something in Gable behaved wrong and you want it fixed. Works even if all you can say is "the delivery screen showed the wrong total." |
| **describe-a-workflow** | `/describe-workflow` | You know how a yard job really works — will-call, dispatch, quoting, picking, POs — and Gable doesn't do it, or does it wrong. **This is the highest-value non-technical contribution in the project.** |
| **improve-docs** | `/fix-doc` | The documentation is wrong, out of date, or missing. Including "I followed the setup steps and they didn't work." |
| **explain-this-code** | `/newcomer-tour` | You want to understand how something works before you touch it. |
| **check-my-contribution** | `/preflight` | You're about to open a pull request and want to know it will pass. |
| **add-a-test** | `/write-a-test` | You want to add safety to a part of the system that has none. A great first code contribution. |
| **licensing-check** | `/license-of` | "Which license covers this file?" or "Can my company use this?" |

---

## Three worked examples

### 1 · You're a yard operator and the delivery screen showed the wrong total

You don't know Go. You don't know what a pull request is. That's fine.

**Type this:**

> The delivery detail screen showed the order total as $73,887.00 but the order is $738.87.
> I saw it on our demo instance on order SO-1042.

**What happens:** Claude runs the `report-an-issue` skill. It will:

- Check first whether this is a **security** problem (it isn't — but if it were, it would stop
  and send you to [`SECURITY.md`](./SECURITY.md) instead of a public issue).
- Find the actual screen in the code by searching `app/src/routes.ts`.
- Notice your numbers are off by exactly 100× and check for the known money-formatting bug —
  ERP money comes from the API in **cents** and has to be rendered with `formatCents()`, so a
  page using `.toFixed(2)` directly renders `$738.87` as `$73,887.00`.
- Rule out the other usual suspects (demo data resets on redeploy, `AUTH_MODE=dev`, async
  image generation, `[lng,lat]` map ordering, branch tax rates).
- Collect the environment facts — commit SHA, Go version, Node version — by running the
  commands itself, so you don't have to.
- Fill in this repo's real bug template at `.github/ISSUE_TEMPLATE/bug_report.md` and hand you
  the finished issue to post.

The maintainer receives a report naming the exact file and line. That is a complete
contribution. You are done — you don't owe anybody a patch.

### 2 · You're a docs writer and the setup instructions don't work

**Type this:**

> I followed the quickstart in the README and `make migrate` failed with "connection refused
> on port 5432". Can you check whether the docs are right?

**What happens:** the `improve-docs` skill. Claude will actually run the commands rather than
guessing, discover that the local default Postgres port is **5434** (matching
`docker-compose.yml`, not the standard 5432), find every doc that says otherwise, fix them,
verify every link in the files it touched still resolves, make sure the SPDX header is right
for the directory, and open a pull request against **`staging`** with a description that says
exactly how it verified the fix.

### 3 · You're a developer and want to add a test

**Type this:**

> Which backend modules have no tests? Pick a high-value one and write a table-driven test.

**What happens:** the `add-a-test` skill. Claude computes the untested-module list live
(rather than trusting a stale one), recommends by value — invoice tax resolution and
rounding, `account.PostTransaction` as the single AR writer, the portal/ERP dollars-vs-cents
boundary — reads the source, copies the idiom from a module that already tests well
(`backend/internal/tax/service_test.go`), writes a table-driven test that needs no Postgres,
runs it with `-race` the way CI does, and **proves it can fail** by breaking the code once.

Then it applies the house rules: no tautological tests; a characterization test is labelled as
one; and if it finds a real bug it files an issue rather than writing the wrong value into
`want:`.

---

## The ground rules

These apply whether or not you used AI.

**1 · Pull requests target `staging`, never `main`.**
Maintainers fast-forward `staging → main` after review. See
[`CONTRIBUTING.md`](./CONTRIBUTING.md).

**2 · Never commit a secret.**
No API keys, tokens, passwords, connection strings, or `.env` files. If one lands in a commit,
deleting it later doesn't help — it stays in history. Rotate the credential first, then tell a
maintainer. The kit's `.claude/settings.json` blocks reading `.env` files for exactly this
reason.

**3 · The SPDX header must match the directory.**
Gable is licensed per component. Every file carries a licence header determined by where it
lives:

| Directory | Header |
|---|---|
| `backend/internal/`, `backend/pkg/`, `backend/cmd/`, `backend/migrations/` | `LicenseRef-OpenLBM-Commons-1.0` |
| `backend/pkg/apps/` *(the connector seam — most specific path wins)* | `LicenseRef-OpenLBM-Connector-1.0` |
| `app/` | `LicenseRef-OpenLBM-Surface-1.0` |
| `docs/`, `.claude/` | `LicenseRef-OpenLBM-Docs-1.0` |

Full map: [`LICENSE-MAP.md`](./LICENSE-MAP.md). Ask with `/license-of <path>` if unsure.

**4 · Security problems go to [`SECURITY.md`](./SECURITY.md), not a public issue.**
Anything about seeing data you shouldn't, getting past a login, or a leaked credential: use
the repository's **Security** tab → **Report a vulnerability**, or email
**security@futurebuild.ai**. A public issue tells every operator running Gable about the hole
before a fix exists.

**5 · Never paste in competitor material.**
Describe how BisTrack, Spruce, or Agility behave from memory if it's useful. Do not paste
their documentation, schemas, screenshots, or API specifications into this repository.

**6 · Run the pre-flight before you push.**
`/preflight` runs the same gates CI runs. Failing locally is much faster than failing in CI.

**7 · Contributions are licensed under the component licence you touched, via a CLA.**
You'll be asked to agree before your first merge. See
[`CONTRIBUTING.md`](./CONTRIBUTING.md) § "Licensing of contributions".

---

## An honest note about AI-assisted contributions

**AI-assisted contributions are welcome here.** We built this kit on purpose. We would rather
have your operational knowledge filtered through an AI assistant than not have it at all.

But there's a bargain, and it isn't negotiable:

> **You are responsible for what you submit.** Your name goes on the pull request. When a
> reviewer asks "why does this work?", "AI wrote it" is not an answer.

Concretely, before you open a PR:

- **Read every line of the diff.** If you don't understand a change, ask Claude to explain it
  until you do — or drop it.
- **Run it yourself.** Run `/preflight`. Actually start the app and click the thing.
- **Check the facts.** Claude can state a wrong file path or a plausible-sounding command
  with complete confidence. Everything in this kit was verified against the repository when it
  was written, but the repository moves. If a command in a skill doesn't exist any more,
  that's a docs bug — file it with `/file-issue`.
- **Don't submit what you can't defend.** A one-line fix you understand beats a 500-line
  refactor you don't.
- **Say that you used AI** in the PR description. Nobody minds. It helps reviewers know where
  to look hardest.

And the flip side: **a well-written issue or a workflow spec is a complete contribution.** You
do not have to submit code. Describing how will-call actually works at your yard is worth more
to this project than most patches.

---

## Where to go next

| You want to… | Go to |
|---|---|
| Build and run Gable locally | [`README.md`](./README.md) quickstart |
| Understand the branch model, PR workflow, CLA | [`CONTRIBUTING.md`](./CONTRIBUTING.md) |
| Understand the conventions and gotchas | [`CLAUDE.md`](./CLAUDE.md) |
| Understand the architecture | [`docs/architecture.md`](./docs/architecture.md) |
| Report a security problem | [`SECURITY.md`](./SECURITY.md) |
| Know how we treat each other | [`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md) |
| See which licence covers what | [`LICENSE-MAP.md`](./LICENSE-MAP.md) |
