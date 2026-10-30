// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"database/sql"
	"log"
)

// seedQECommodities marks the lumber subtree's seeded products as commodities,
// the backfill migration 081 runs on an install that already had products. A
// migration cannot do it for the seed: migrations run before the seed, on an
// empty products table, so the flag stays false on every product this seed
// inserts. A quote send only snapshots a price escalator for a commodity
// line, so without this the price exposure gate never arms on a fresh seed
// (QE-07). Called from Run after every product insert, so it covers both the
// base seed and the opt in catalogue import.
func seedQECommodities(db *sql.DB) {
	if _, err := db.Exec(`
		UPDATE products p
		   SET is_commodity = TRUE
		  FROM product_categories pc
		 WHERE p.category_id = pc.id
		   AND pc.path <@ 'lumber'::ltree
		   AND p.is_commodity = FALSE`); err != nil {
		log.Printf("Seed: marking lumber products commodity: %v", err)
	}
}
