// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package httpx is the wire contract platform (docs/adr/0001-wire-contract.md):
// the one list envelope, cursor pagination, the one error envelope, field
// validation, the strict query parameter guard, integer money, quantities
// and the one extension rule, revision preconditions, and document
// numbers, that every route converts onto module by module.
//
// It lives at core/internal/platform/httpx (the module path is unchanged by
// the layout move). Nothing in the legacy
// handlers imports it yet: each converting module adopts it in its own
// change, with every wire diff listed in docs/refactor/CONTRACT-CHANGES.md.
package httpx
