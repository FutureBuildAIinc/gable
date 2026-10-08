<!--
SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
-->

# Contract changes

This file lists every behaviour change made against the goldens recorded at
`8361f23b40c3daf08a64ed4f4c43f518807468e4`, the commit `refactor/v1` was cut
from. The goldens pin current behaviour; a refactor item that changes
behaviour on purpose records the change here, one entry per change, and lands
the entry in the same pull request as the change it describes. An entry names
the route (or seam) touched, the behaviour before, the behaviour after, and
the item that made the change. Refactors that keep behaviour identical do not
get an entry.

| Item | Route / seam | Before | After |
|---|---|---|---|
| R1-14 | `audit_log` table / every audited mutation (order confirm, cancel, fulfil; payment process, refund; invoice create; deposit record, apply; POS complete, void, return) | Audit rows were written by a goroutine through the pool after the mutation, so a rolled back mutation could still leave an audit row and a crash could lose one | Audit rows are written synchronously inside the mutation's transaction: a rolled back mutation leaves no row, a committed one leaves exactly one, and a failed audit write fails the mutation. Rows also carry the actor's kind (`user`, `key`, `agent`), the actor's id, and for an agent the `X-Acting-As` marker and `X-Agent-Tool` tool name |
| R1-14 | `pkg/audit` `Log` seam | Fire-and-forget, no error return | Synchronous; returns an error (callers inside a transaction propagate it, callers without one log it and continue) |
| R1-14 | POS `POST /api/v1/pos/transactions/{id}/void` (audit side only) | Voiding an already-voided transaction wrote a second `pos.transaction.voided` audit row | Only a void that actually changes state writes an audit row |
| R1-14 | Requests carrying `X-Acting-As` and `X-Agent-Tool` | Headers ignored; agent mutations were indistinguishable from the user's own | The marker and tool name are recorded on the audit row. The marker never grants anything; it only records |
