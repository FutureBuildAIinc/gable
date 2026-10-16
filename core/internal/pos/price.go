// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// WalkInPriceCentsSQL is the counter's walk in price of a product in cents,
// as an expression over the products table aliased p. The counter's search
// and catalogue read it, and the Market check reads it too, so the two never
// drift apart.
const WalkInPriceCentsSQL = `COALESCE(ROUND(p.base_price * 100)::bigint, 0)`
