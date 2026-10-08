<!-- SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0 -->
<!-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors -->

# CLAUDE.md

@AGENTS.md

Everything an agent needs to know about this repository is in `AGENTS.md`, imported above: the stack, conventions, pre-flight checks, the money convention and other gotchas, and the backlog. Cite those sections as `AGENTS.md`. This file holds only what is specific to Claude Code.

## Contributor Agent Kit (`.claude/`)

This repo ships a Claude Code kit so contributors — including non-technical ones — get a
repo-aware setup on clone. It is tracked in git (only `.claude/settings.local.json` and
`.claude/**/*.local.json` are ignored).

```
.claude/skills/     report-an-issue · describe-a-workflow · improve-docs ·
                    explain-this-code · check-my-contribution · add-a-test · licensing-check
.claude/commands/   /file-issue · /describe-workflow · /fix-doc · /newcomer-tour ·
                    /preflight · /write-a-test · /license-of
.claude/settings.json   allows the pre-flight commands; denies reading .env and force-push
```

Human-facing onboarding is [`CONTRIBUTING-WITH-CLAUDE.md`](./CONTRIBUTING-WITH-CLAUDE.md).

**Two skills matter most for people who don't write code:**
`report-an-issue` (turns "the delivery screen showed the wrong total" into a filed, correctly
routed report — and diverts security bugs to `SECURITY.md`) and `describe-a-workflow` (turns a
dealer's operational knowledge into a spec with the Technical / PRR / User-driven acceptance
triad, written to `docs/workflows/`).

**Maintaining the kit:** the skills quote real commands and paths from this repo — `make`
targets, CI job names, module layout, `LICENSE-MAP.md` rows. When you change the `Makefile`,
`.github/workflows/ci.yml`, the branch model, or the license map, grep `.claude/` and update
it in the same PR. Files under `.claude/` are `LicenseRef-OpenLBM-Docs-1.0`; the SPDX tags live
inside the YAML frontmatter as `#` comments so the frontmatter still parses, and
`settings.json` uses a `settings.json.license` sidecar. The REUSE job in CI is a merge gate,
so a missing header there fails the build.
