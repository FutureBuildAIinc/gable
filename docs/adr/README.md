# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

# Architecture decision records

Decisions that shape Gable's architecture, one file each, numbered in the
order they were taken. The App Shape this repository follows (`manifest.yaml`,
checked by `scripts/check-shape.sh`) is defined by the FutureBuild platform's
ADR-011, which lives in the FutureBuild infra repository, not here.

| ADR | Title | Item |
|---|---|---|
| [0001](0001-wire-contract.md) | The wire contract for every route | R1-6 |
| [0002](0002-machine-keys.md) | Scoped machine keys | R1-13 |
| [0003](0003-events-outbox.md) | The transactional outbox and the events read API | R1-12 |
| 0004 | Web session custody (arrives with R1-8) | R1-8 |

## Writing one

- Name the file `NNNN-short-title.md` with the next free number, and add a row
  to the table above in the same change.
- Start with the two SPDX header lines of a neighbouring ADR, then the title
  `# ADR NNNN: <title>`, then `## Status`, `## Context`, `## Decision` and
  `## Consequences`.
- An accepted ADR is not edited to change its decision. A later ADR supersedes
  it, and both say so in their Status.
- No calendar dates or durations; sizes in dev hour equivalents.
