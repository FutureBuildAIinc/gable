<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Refactor status

One row per item merged into `refactor/v1`: its pull request, its review rounds (each round names the reviewer's
model; "second" is the independent second review the money, security and data classes require), the pull request's
head when it merged, and the merge commit. The lead keeps this file current at each cycle's close.

## Cycle 1: the foundation

| Item | What | Pull request | Review rounds | Head | Merge |
|---|---|---|---|---|---|
| R1-0 | Baseline at 8361f23b | #2 | 1: Sonnet clean | `6ba72fb` | `c8b7c6f` |
| R1-1 | Characterisation goldens | #6 | 2: Sonnet changes; Sonnet changes; fixes verified by the lead | `7442170` | `fcb6a2e` |
| R1-1b | Goldens for every route | #26 | 1: Sonnet changes; fix round and a resync onto #25 and #21 verified by the lead | `55a913c` | `efe59ab` |
| R1-2 | Route census | #4 | 2: Sonnet changes; Sonnet changes; fixes probed by the lead | `d360e59` | `2be1a61` |
| R1-3 | Layout move (`backend` to `core`, `app` to `web/apps/desk`) | #9 | 1: Sonnet clean; lead sync merge | `95fdf38` | `398359f` |
| R1-4 | One binary with roles | #11 | 1: Sonnet changes; fixes verified by the lead | `a5f471a` | `c9afdd9` |
| R1-5 | Manifest, AGENTS.md, App Shape check | #22 | 1: Sonnet changes; fixes and a resync after #20 by the lead | `0b1bf14` | `512b5e5` |
| R1-6 | Wire contract ADR 0001 and platform httpx | #3 | 4: Sonnet changes and Opus design changes; Opus changes; Opus changes; fixes verified by the lead | `63a7e40` | `f4e27a5` |
| R1-7a | OpenAPI tooling and the first fragments | #12 | 1: Sonnet changes; fixes verified by the lead | `2defd76` | `7305c2a` |
| R1-7b | Contract conformance against the goldens | #18 | 1: Sonnet changes (every mutation probe caught); fixes verified by the lead | `6dd44b2` | `cc28a0f` |
| R1-7c | Contract: portal and the non module routes | #16 | 1: Sonnet changes; two fix rounds verified by the lead | `2856cb4` | `ac42875` |
| R1-7ct | JSON responses declare application/json | #15 | 1: Sonnet clean | `0f48da9` | `8fd57a1` |
| R1-7d | Contract: sales, inventory and delivery modules | #17 | 2: Sonnet changes; Sonnet changes (one finding fixed by the lead) | `140bc03` | `8b482d6` |
| R1-7e | Contract: pricing, purchasing, AP, GL, bank reconciliation | #19 | 1: Sonnet changes; fixes verified and resynced by the lead | `5fdf7b3` | `3d97375` |
| R1-7f | Contract: admin, reporting and the rest; one error envelope rule; pending list empty | #21 | 1: Sonnet changes; fixes verified by the lead | `3152e16` | `594b161` |
| R1-8 | Web workspace, front door, desk as a micro app | #20, #31 | 2: Sonnet changes; Sonnet code and visual changes; lead visual check; the last fix round merged as #31 | `8637a10`, `a2f6d93` | `91106c0`, `3ffa504` |
| R1-9 | Tauri shell for the front door | #24 | 1: Sonnet clean | `4aa5400` | `1defbd7` |
| R1-10 | Local stack and make smoke | #30 | 1: Sonnet changes; fixes verified by the lead | `1136187` | `d267bd0` |
| R1-11 | Idempotency in Postgres | #5 | 3: Sonnet changes; Sonnet clean; second (GLM-5.3) clean | `3db34ef` | `cc7be2d` |
| R1-12 | Transactional outbox and the events read API | #14 | 3: Opus design and code changes; Opus changes; second (MiniMax M3) clean | `5e7227d` | `e9c143c` |
| R1-12b | Events feed contract and outbox retention | #25 | 2: Sonnet changes; second (MiniMax M3) clean | `29dffb3` | `4534a2f` |
| R1-13 | Scoped machine keys | #10 | 2: Sonnet changes; second (GLM-5.3) changes; serve wiring committed and probed by the lead | `078234f` | `f43c190` |
| R1-14 | Audit in the transaction with the actor's kind | #7 | 3: Sonnet changes; second (GLM-5.3) clean; Sonnet clean | `b4b4551` | `dea6374` |
| R1-15 | Quotes on the new contract, and the module recipe | #27 | 3: Opus design and code changes; second (MiniMax M3) clean; Opus approve | `7876ed3` | `6e32a52` |
| R1-sec | Rate limiter trusts forwarding headers only from configured proxies | #23 | 2: Sonnet changes; second (MiniMax M3) clean | `0fe5a5e` | `5f7682b` |
| R1-deps | golang.org/x/text (GO-2026-6629) | #8 | 1: Sonnet clean | `92655ba` | `6188fd6` |
| R1-deps | The desk's npm advisories | #13 | 1: Sonnet changes; fixes verified by the lead | `045b91c` | `463e17f` |
| R1-flake | clockwindow golden drift; dashboard list tiebreaks | #28 | 1: Sonnet changes (documentation); fixed by the lead | `a008038` | `ede839c` |

## Cycle 2: the sales and money core

| Item | What | Pull request | Review rounds | Head | Merge |
|---|---|---|---|---|---|
| C2-0 | Design: ADR 0005 | #29 | 3: Opus changes three times; round 3 verified by the lead | `ea30fec` | `8c55b2a` |
