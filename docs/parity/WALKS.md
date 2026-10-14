<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Parity walks

Short scripts a person can follow on the demo seed to see each MUST row of the
[parity matrix](LBM-PARITY-MATRIX.md) working across more than one yard. Each item of the matrix's plan adds its
walk here when it merges; a row is not done until its walk passes.

## Before any walk

1. Start the whole stack: `make up` (Postgres, migrate and seed, `core serve`, `core worker` and the web image), then
   open <http://127.0.0.1:8080>. `make smoke` checks it end to end; `make down` removes it and its data.
2. The demo seed is invented data. It carries three yards, the branches `KEL-MAIN`, `WK` and `LK`, each a top level
   `BRANCH` location with its own address and tax rate.
3. The stack runs with `AUTH_MODE=dev`, which gives every request full reach. A walk that depends on who the person
   is (a yard's cashier, a finance user granted one yard) says how to act as that person; until sign in lands (the
   plan's shell track, S9 and S13) those walks run through the API with a test token, as each walk states.
4. Each walk names the rows it proves, its steps, and what each step must show. A step that fails is a bug against
   the row, filed with the walk's name.

## The walks

None yet: the first land with M0a, M0b and M1 (the plan's phases 0 and 1), which turn on multi yard operation in the
demo and give each yard its own people, registers and stock.
