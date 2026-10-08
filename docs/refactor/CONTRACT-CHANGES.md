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
| R1-11 | POST and PUT routes carrying an idempotency key (the server-wide middleware) | Only `X-Idempotency-Key` was read; the stored response lived in an in-process map, so a replay worked only inside one process and a retry after a restart re-ran the handler and repeated the write | `Idempotency-Key` is the canonical header and `X-Idempotency-Key` stays an alias addressing the same claim; claims and stored responses live in the `idempotency_keys` table (migration 087), so a replay after a restart or on another instance returns the stored response, and a replay carries `Idempotency-Replayed: true` |
| R1-11 | POST and PUT routes carrying an idempotency key | A second request with the same key while the first was in flight re-ran the handler (nothing coordinated concurrent duplicates), and a key reused with a different request silently re-ran too | A concurrent duplicate gets `409` with an error envelope naming the idempotency key while the first is in progress, and a key reused with a different request (method, path or body) gets `422` |
| R1-11 | POST and PUT routes carrying an idempotency key | Keys were effectively global: the middleware ran before auth, so all callers shared one anonymous namespace and one caller's stored response could be replayed to another caller who reused the key | Keys are scoped to the authenticated principal (the JWT subject, portal claims, or an integration key's tenant; the fixed `dev` principal under `AUTH_MODE=dev`); a caller without an identity outside dev mode passes through uncached |
| R1-11 | POST and PUT routes carrying an idempotency key whose body exceeds the 10 MB request size limit | The middleware never read the body; the handler's read failed against the limit and the handler answered | The middleware reads the body for the fingerprint inside that same limit and answers `413` with the error envelope itself, before any claim is made |
| R1-11 | POST and PUT routes carrying an idempotency key while the database is unreachable | No such case existed (the cache was in memory) | The request is served uncached (fail open); nothing is claimed or replayed and a warning is logged |
