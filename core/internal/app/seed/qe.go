// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

// The quotes and estimates section of the demo seed (QE). Every QE seed row
// lives here and nowhere else, so the section's slices stay one file's to
// move. The rows are reference data the seed owns by natural key: an upsert
// re-running the seed adopts a name or price edit, and a row an operator
// changed elsewhere is never the seed's to move.

import (
	"database/sql"
	"fmt"
	"log"
	"math"

	"github.com/google/uuid"
)

// demoKitComponent is one row of a seeded kit's definition: how many of a
// seeded SKU one kit holds, in the component's own stocking unit.
type demoKitComponent struct {
	SKU      string
	Quantity float64
}

// demoKit is one seeded kit product. BasePrice is what the kit sells for as
// a whole; its cost is what its components cost, which a quote's margin
// reads (QE-05). TargetMargin is the percent that price leaves over the
// parts' cost, the target margin every catalogue row carries.
type demoKit struct {
	SKU, Description, Category, Vendor string
	BasePrice                          float64
	TargetMargin                       float64
	Components                         []demoKitComponent
}

// demoKits: two plain building material kits a dealer quotes and sells
// whole, made of the seed's own catalog (the framing kit's parts cost
// 62.40 against its 89.00 price, so it targets the 29.89 percent that
// leaves; the deck section's 71.00 against 129.00, so 44.96), so the
// demo's quotes carry kits whose margin reads from real component costs.
var demoKits = []demoKit{
	{
		SKU:          "KIT-FRAME-8",
		Description:  "8 ft Wall Section Framing Kit (12 studs, 2 sheets)",
		Category:     "Lumber",
		Vendor:       "Larkmere Building Products",
		BasePrice:    89.00,
		TargetMargin: 29.89,
		Components: []demoKitComponent{
			{"LUM-2492-STUD", 12},
			{"OSB-12", 2},
		},
	},
	{
		SKU:          "KIT-DECK-10",
		Description:  "10 ft Deck Section Kit (joists, decking, hardware)",
		Category:     "Lumber",
		Vendor:       "Larkmere Building Products",
		BasePrice:    129.00,
		TargetMargin: 44.96,
		Components: []demoKitComponent{
			{"LUM-2810-NO2", 3},
			{"LUM-2612-PREM", 5},
			{"HANGER-28", 10},
		},
	},
}

// seedQuoteKits upserts the section's kit products and their component
// definitions, so the demo's quotes and orders have kits to build and
// explode. A kit's stock is its components': the kit row itself is never
// stocked, and a component must already be in the seed's catalog (the
// skuToID map the product section filled).
//
// A kit carries the target margin and the commission every catalogue row
// carries: the margin is what its price leaves over the parts' cost, the
// cost a quote's margin reads, and the commission follows the demo's house
// rule, a quarter of the margin (the import's own rule).
func seedQuoteKits(db *sql.DB, skuToID map[string]uuid.UUID) {
	for _, kit := range demoKits {
		margin := kit.TargetMargin
		commission := math.Round(margin*0.25*100) / 100
		var kitID uuid.UUID
		err := db.QueryRow(`
			INSERT INTO products (sku, description, uom_primary, base_price, category, vendor, is_kit, taxable,
				target_margin, commission_rate)
			VALUES ($1, $2, 'EA', $3, $4, $5, TRUE, TRUE, $6, $7)
			ON CONFLICT (sku) DO UPDATE SET
				description = EXCLUDED.description, base_price = EXCLUDED.base_price,
				category = EXCLUDED.category, vendor = EXCLUDED.vendor, is_kit = TRUE,
				target_margin = EXCLUDED.target_margin, commission_rate = EXCLUDED.commission_rate
			RETURNING id`, kit.SKU, kit.Description, kit.BasePrice, kit.Category, kit.Vendor, margin, commission).Scan(&kitID)
		if err != nil {
			log.Printf("QE seed kit %s: %v", kit.SKU, err)
			continue
		}
		for i, comp := range kit.Components {
			compID, ok := skuToID[comp.SKU]
			if !ok {
				log.Printf("QE seed kit %s: component %s is not in the seeded catalog", kit.SKU, comp.SKU)
				continue
			}
			if _, err := db.Exec(`
				INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (kit_product_id, component_product_id) DO UPDATE SET
					quantity = EXCLUDED.quantity, position = EXCLUDED.position`,
				kitID, compID, comp.Quantity, i); err != nil {
				log.Printf("QE seed kit %s component %s: %v", kit.SKU, comp.SKU, err)
			}
		}
	}
	fmt.Printf("Seed: QE kits (%d)\n", len(demoKits))
}
