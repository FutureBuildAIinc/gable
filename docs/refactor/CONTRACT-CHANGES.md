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
| R1-11 | POST, PUT and PATCH routes carrying an idempotency key (the server-wide middleware) | Only `X-Idempotency-Key` was read; the stored response lived in an in-process map, so a replay worked only inside one process and a retry after a restart re-ran the handler and repeated the write | `Idempotency-Key` is the canonical header and `X-Idempotency-Key` stays an alias addressing the same claim; claims and stored responses live in the `idempotency_keys` table (migration 087), so a replay after a restart or on another instance returns the stored response, and a replay carries `Idempotency-Replayed: true` |
| R1-11 | POST, PUT and PATCH routes carrying an idempotency key | A second request with the same key while the first was in flight re-ran the handler (nothing coordinated concurrent duplicates), and a key reused with a different request silently re-ran too | A concurrent duplicate gets `409` with the machine code `idempotency_in_progress` while the first is in progress, and a key reused with a different request gets `422` with the machine code `idempotency_key_reused`; both answers use the standard error envelope (code, message, an empty `details` array, the request id in `meta`) |
| R1-11 | POST, PUT and PATCH routes carrying an idempotency key | A `4xx` or `5xx` outcome was never at stake (the in-memory cache stored nothing) | Only `2xx` and `3xx` outcomes are stored and replayed; a `4xx` or `5xx` releases the claim so the client can retry with the same key |
| R1-11 | POST, PUT and PATCH routes carrying an idempotency key | Keys were effectively global: the middleware ran before auth, so all callers shared one anonymous namespace and one caller's stored response could be replayed to another caller who reused the key | Keys are scoped to the caller, one layer per surface: the global middleware (inside JWT auth) covers the ERP API keyed on the JWT subject and skips `/api/portal/v1/` and `/api/integration/`; portal routes carry the layer inside the portal auth chain, keyed on the portal customer and user; integration routes carry it inside the `X-Integration-Key` check, keyed on the caller's `X-Tenant-ID` tenant. Under `AUTH_MODE=dev` a caller with no identity is the fixed `dev` principal, and an integration caller without a tenant gets its own `dev:integration` principal so it never shares the global layer's dev namespace; outside dev mode such a caller passes through uncached |
| R1-11 | POST, PUT and PATCH routes with query flags carrying an idempotency key | The query string was not part of the request fingerprint, so `?dry_run=true` and `?dry_run=false` under one key replayed the first response | The query string, canonicalized by key then value, is part of the fingerprint: a key reused with a different query string gets `422` `idempotency_key_reused`, while reordered spellings of the same query still replay |
| R1-11 | POST, PUT and PATCH routes carrying an idempotency key whose body exceeds the 10 MB request size limit | The middleware never read the body; the handler's read failed against the limit and the handler answered | The middleware reads the body for the fingerprint inside that same limit and answers `413` with the error envelope (code `payload_too_large`) itself, before any claim is made; any other body read failure answers `400` `bad_request` |
| R1-11 | POST, PUT and PATCH routes carrying an `Idempotency-Key` outside 1 to 255 printable ASCII characters | The key was accepted as given (an overlong key travelled to the database and the request was then served uncached through the fail-open path) | `400` `validation_failed` naming the header in the message and in `details`, before any claim is made |
| R1-11 | POST, PUT and PATCH routes carrying an idempotency key whose response is a redirect or carries a `Location` header | Nothing was stored for a `3xx` (the claim was released and a retry re-ran the handler), and a stored `201`'s `Location` was dropped on replay | `2xx` and `3xx` outcomes are stored with status, `Content-Type`, `Location` and body, and replayed with all of them; `Set-Cookie` is never stored, so never replayed. A stored body is capped at 1 MiB: a larger outcome is served to its caller in full, the claim is released and the drop logged, so a retry re-runs the handler |
| R1-11 | POST, PUT and PATCH routes carrying an idempotency key while the database is unreachable | No such case existed (the cache was in memory) | The request is served uncached (fail open); nothing is claimed or replayed and a warning is logged |
