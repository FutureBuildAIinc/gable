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
| R1-14 | `audit_log` table / every audited mutation (order confirm, cancel, fulfil; payment process, refund; invoice create; deposit record, apply; POS complete, void, return) | Audit rows were written by a goroutine through the pool after the mutation, so a rolled back mutation could still leave an audit row and a crash could lose one | Audit rows are written synchronously inside the mutation's transaction: a rolled back mutation leaves no row, a committed one leaves exactly one, and a failed audit write fails the mutation. Rows also carry the actor's kind (`user`, `key`, `agent`, `anonymous`), the actor's id (always the resolved actor; `user_id` keeps the legacy explicit attribution, and stays NULL when a machine key made the write with no explicit attribution, so key ids appear only in `actor_id`; an empty attribution of any kind is stored as NULL, the value the 088 backfill already writes for such rows), and for an agent the `X-Acting-As` marker and `X-Agent-Tool` tool name. The table is indexed on `(actor_kind, actor_id)` |
| R1-14 | `pkg/audit` `Log` seam | Fire-and-forget, no error return | Synchronous; returns an error (callers inside a transaction propagate it, callers without one log it and continue). With no transaction in the context the write runs under `context.WithoutCancel` plus a 5s timeout, so a client disconnect after the commit no longer drops the row for a committed mutation |
| R1-14 | Requests carrying `X-Acting-As` and `X-Agent-Tool` | Headers ignored; agent mutations were indistinguishable from the user's own | The marker (recorded lowercased) and tool name are recorded on the audit row. Each value is capped at 128 bytes of printable ASCII; a longer or non-printable value is refused with a 400 `validation_failed` naming the header, in the wire ADR's error envelope shape. The marker never grants anything; it only records |
| R1-14 | CORS preflight (`Access-Control-Allow-Headers`) | `X-Acting-As` and `X-Agent-Tool` not allowed, so a cross-origin agentic UI failed preflight | Both headers join the allowed list |
| R1-14 | Audit rows with no identity (dev mode, background jobs) | Labelled `kind='user'` with an empty actor id | Labelled `kind='anonymous'` with a null actor id; the 088 backfill labels userless rows (null or empty `user_id`) the same way |
| R1-14 | POS `POST /api/v1/pos/transactions/{id}/void` (audit side only) | Voiding an already-voided transaction wrote a second `pos.transaction.voided` audit row | Only a void that actually changes state writes an audit row |
| R1-14 | card payments (`ProcessCardPayment`, the gateway-charged path) | Card payments wrote no audit row at all, so card money movement was invisible to the financial audit trail while cash payments were audited | A committed card payment writes one `payment.processed` audit row inside the transaction that records the payment (the gateway charge still runs outside it, before it), so the row shares the payment's fate |
