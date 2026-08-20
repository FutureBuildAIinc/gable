# License Map

GableLBM / OpenLBM is licensed **per component**. Each path prefix below is
governed by the OpenLBM Standard license named in its row. The full text of
each license lives in [`LICENSES/`](LICENSES/), every source file additionally
carries a matching `SPDX-License-Identifier` header, and
[`REUSE.toml`](REUSE.toml) encodes the same mapping in machine-readable form.

| Path prefix | SPDX license identifier | License text |
|---|---|---|
| `backend/internal/` | `LicenseRef-OpenLBM-Commons-1.0` | [LICENSES/LicenseRef-OpenLBM-Commons-1.0.txt](LICENSES/LicenseRef-OpenLBM-Commons-1.0.txt) |
| `backend/pkg/` *(except `backend/pkg/apps/`)* | `LicenseRef-OpenLBM-Commons-1.0` | [LICENSES/LicenseRef-OpenLBM-Commons-1.0.txt](LICENSES/LicenseRef-OpenLBM-Commons-1.0.txt) |
| `backend/cmd/` | `LicenseRef-OpenLBM-Commons-1.0` | [LICENSES/LicenseRef-OpenLBM-Commons-1.0.txt](LICENSES/LicenseRef-OpenLBM-Commons-1.0.txt) |
| `backend/migrations/` | `LicenseRef-OpenLBM-Commons-1.0` | [LICENSES/LicenseRef-OpenLBM-Commons-1.0.txt](LICENSES/LicenseRef-OpenLBM-Commons-1.0.txt) |
| `backend/pkg/apps/` | `LicenseRef-OpenLBM-Connector-1.0` | [LICENSES/LicenseRef-OpenLBM-Connector-1.0.txt](LICENSES/LicenseRef-OpenLBM-Connector-1.0.txt) |
| `app/` | `LicenseRef-OpenLBM-Surface-1.0` | [LICENSES/LicenseRef-OpenLBM-Surface-1.0.txt](LICENSES/LicenseRef-OpenLBM-Surface-1.0.txt) |
| `docs/` | `LicenseRef-OpenLBM-Docs-1.0` | [LICENSES/LicenseRef-OpenLBM-Docs-1.0.txt](LICENSES/LicenseRef-OpenLBM-Docs-1.0.txt) |

## Precedence

`backend/pkg/apps/` — the installable-apps **connector seam** — is carved out
of the `backend/pkg/` Commons default and licensed under the more permissive
**Connector** license so third parties can plug into the commons without
copyleft crossing the boundary. The most specific path wins: a file under
`backend/pkg/apps/` is Connector-licensed; anything else under `backend/pkg/`
is Commons-licensed.

## Also in `LICENSES/`

These are referenced by the project but are **not** directory-scoped, so they
do not appear in the table above:

- `LicenseRef-OpenLBM-Community-Source-1.0` — the Community Source License,
  applied per-work (by version notice), not per-directory.
- `LicenseRef-OpenLBM-Trademark` — a brand-use policy, not a code license.

## Status

The OpenLBM license texts under `LICENSES/` are **effective**. They are copies
of the published Standard, which is canonical and lives at
<https://github.com/FutureBuildAIinc/openlbm>. Where a copy here and the
published Standard ever disagree, the published Standard governs — the copies
are shipped so the repository is self-contained and REUSE-compliant offline,
not as a second source of truth.
