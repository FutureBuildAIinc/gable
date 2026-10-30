// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"database/sql"
	"fmt"
	"log"
)

// The QE section's rows: the option catalog a configured item is priced
// from.
//
// The file is qe.go because the Quotes and Estimates section (QE) owns it.
// QE-05 lands its own rows in this same file and the lead merges the two,
// so every QE row is written here and the seed's entry point calls
// seedQE once.
//
// The catalog is what the server prices a configuration from (QE-06): a
// configured item's unit price is the sum over the options its
// configuration selects of that option's base plus that option's
// adjustment, and its unit cost is the same sum over the cost columns.
//
// The rule of the seed is one base per configuration: the option that names
// the item carries the base (a door's door type, a board's species) and
// every other option carries adjustments only. The door's size is in the
// configuration, the SKU and the description, and does not move the price:
// the adopted decision put the door's base price on the door type row.
//
// The money is in cents, the unit the option catalog stores and the wire
// carries. No float ever carries it.

import "time"

// qeOption is one row of the option catalog.
type qeOption struct {
	Category             string
	Name                 string
	BasePriceCents       int64
	BaseCostCents        int64
	PriceAdjustmentCents int64
	CostAdjustmentCents  int64
}

// qeDoorOptions is the door's catalog: the door type carries the base price
// and the base cost of the door, and the other categories adjust it.
func qeDoorOptions() []qeOption {
	return []qeOption{
		// The door type: the base of the door.
		{Category: "door_type", Name: "Panel Door", BasePriceCents: 25000, BaseCostCents: 15000},
		{Category: "door_type", Name: "Flush Door", BasePriceCents: 18000, BaseCostCents: 11000},
		{Category: "door_type", Name: "Glass Panel Door", BasePriceCents: 32000, BaseCostCents: 20000},
		{Category: "door_type", Name: "Fire Door", BasePriceCents: 45000, BaseCostCents: 30000},

		// The material the door is made of.
		{Category: "material", Name: "Pine", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "material", Name: "Oak", PriceAdjustmentCents: 8000, CostAdjustmentCents: 4000},
		{Category: "material", Name: "Mahogany", PriceAdjustmentCents: 15000, CostAdjustmentCents: 8000},
		{Category: "material", Name: "Maple", PriceAdjustmentCents: 6000, CostAdjustmentCents: 3000},
		{Category: "material", Name: "MDF", PriceAdjustmentCents: -5000, CostAdjustmentCents: -2000},

		// The glass the door carries. A door with no glass names no glass
		// option at all, so the choice is the absence of a row.
		{Category: "glass", Name: "Clear", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "glass", Name: "Frosted", PriceAdjustmentCents: 4500, CostAdjustmentCents: 2000},
		{Category: "glass", Name: "Laminated", PriceAdjustmentCents: 7500, CostAdjustmentCents: 3500},

		// The depth of the jamb the door sits in.
		{Category: "jamb_depth", Name: "4-1/2\"", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "jamb_depth", Name: "5-1/2\"", PriceAdjustmentCents: 1500, CostAdjustmentCents: 700},
		{Category: "jamb_depth", Name: "6-1/2\"", PriceAdjustmentCents: 3000, CostAdjustmentCents: 1500},
		{Category: "jamb_depth", Name: "7\"", PriceAdjustmentCents: 4500, CostAdjustmentCents: 2200},

		// The handing: which way the door swings.
		{Category: "handing", Name: "Left", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "handing", Name: "Right", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "handing", Name: "Left and Right", PriceAdjustmentCents: 2500, CostAdjustmentCents: 1200},

		// The finish of the hinges.
		{Category: "hinge_finish", Name: "Brass", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "hinge_finish", Name: "Satin Nickel", PriceAdjustmentCents: 800, CostAdjustmentCents: 400},
		{Category: "hinge_finish", Name: "Black", PriceAdjustmentCents: 600, CostAdjustmentCents: 300},
		{Category: "hinge_finish", Name: "Stainless Steel", PriceAdjustmentCents: 1200, CostAdjustmentCents: 600},

		// The frame the door hangs in.
		{Category: "frame_type", Name: "Wood", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "frame_type", Name: "Steel", PriceAdjustmentCents: 6000, CostAdjustmentCents: 3500},
		{Category: "frame_type", Name: "Fiberboard", PriceAdjustmentCents: -2000, CostAdjustmentCents: -1000},
	}
}

// qeLumberOptions is the lumber catalog: the species carries the base price
// and the base cost of the board, and the grade, the treatment and the
// dimensions adjust it.
func qeLumberOptions() []qeOption {
	opts := []qeOption{
		// The species: the base of the board.
		{Category: "species", Name: "SYP", BasePriceCents: 800, BaseCostCents: 500},
		{Category: "species", Name: "Douglas Fir", BasePriceCents: 1400, BaseCostCents: 850},
		{Category: "species", Name: "Cedar", BasePriceCents: 2200, BaseCostCents: 1400},
		{Category: "species", Name: "Hem-Fir", BasePriceCents: 1100, BaseCostCents: 700},
		{Category: "species", Name: "SPF", BasePriceCents: 600, BaseCostCents: 380},

		// The grade.
		{Category: "grade", Name: "#1", PriceAdjustmentCents: 400, CostAdjustmentCents: 200},
		{Category: "grade", Name: "#2", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "grade", Name: "#3", PriceAdjustmentCents: -200, CostAdjustmentCents: -100},
		{Category: "grade", Name: "Stud", PriceAdjustmentCents: 150, CostAdjustmentCents: 80},
		{Category: "grade", Name: "Select Structural", PriceAdjustmentCents: 600, CostAdjustmentCents: 300},
		{Category: "grade", Name: "Clear", PriceAdjustmentCents: 800, CostAdjustmentCents: 400},
		{Category: "grade", Name: "STK", PriceAdjustmentCents: 250, CostAdjustmentCents: 120},
		{Category: "grade", Name: "Structural", PriceAdjustmentCents: 350, CostAdjustmentCents: 180},
		{Category: "grade", Name: "Appearance", PriceAdjustmentCents: 500, CostAdjustmentCents: 250},

		// The treatment. "None" is the absence of a treatment, and it is a
		// row so a configuration that names it prices to the same sum.
		{Category: "treatment", Name: "None", PriceAdjustmentCents: 0, CostAdjustmentCents: 0},
		{Category: "treatment", Name: "Treatable", PriceAdjustmentCents: 900, CostAdjustmentCents: 450},
		{Category: "treatment", Name: "Fire Retardant", PriceAdjustmentCents: 2500, CostAdjustmentCents: 1200},
		{Category: "treatment", Name: "Borate", PriceAdjustmentCents: 1500, CostAdjustmentCents: 700},
	}
	return append(opts, qeDimensionOptions()...)
}

// qeDimensionOptions is the board's cross section and length: one row per
// combination the configurator offers, each one an adjustment over the
// species' base price.
func qeDimensionOptions() []qeOption {
	// The cross section: what the board's face is worth over a 2x4.
	sizes := []struct {
		Name       string
		PriceCents int64
		CostCents  int64
	}{
		{"2x4", 0, 0},
		{"2x6", 350, 200},
		{"2x8", 800, 450},
		{"2x10", 1400, 800},
		{"2x12", 2100, 1200},
		{"4x4", 2600, 1500},
		{"4x6", 3400, 1950},
		{"6x6", 4800, 2750},
		{"1x4", -150, -80},
		{"1x6", 100, 60},
		{"1x8", 300, 170},
		{"1x10", 550, 310},
		{"1x12", 850, 480},
	}
	// The length: what the extra foot is worth over eight feet.
	lengths := []struct {
		Name       string
		PriceCents int64
		CostCents  int64
	}{
		{"8", 0, 0},
		{"10", 250, 150},
		{"12", 550, 320},
		{"14", 900, 520},
		{"16", 1300, 750},
		{"20", 2200, 1300},
	}
	out := make([]qeOption, 0, len(sizes)*len(lengths))
	for _, s := range sizes {
		for _, l := range lengths {
			out = append(out, qeOption{
				Category:             "dimensions",
				Name:                 s.Name + "-" + l.Name,
				PriceAdjustmentCents: s.PriceCents + l.PriceCents,
				CostAdjustmentCents:  s.CostCents + l.CostCents,
			})
		}
	}
	return out
}

// qeCatalog is the whole catalog the seed writes.
func qeCatalog() []qeOption {
	return append(qeDoorOptions(), qeLumberOptions()...)
}

// seedQE writes the option catalog a configured item is priced from.
//
// The rows it writes are replaced, not added: the seed owns these (category,
// name) pairs, so a re-seed repairs the money rather than adding a second
// copy of every option. A row the dealer added under another name is left
// alone.
//
// The seed writes the money in cents as the columns store it, through
// parameters, never through a float.
func seedQE(db *sql.DB) {
	rows := qeCatalog()
	if len(rows) == 0 {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		log.Printf("Seed: the configured item's option catalog: %v", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	for _, o := range rows {
		// The seed owns these rows: they are replaced, so a re-seed never
		// leaves two options of one name behind.
		if _, err := tx.Exec(`DELETE FROM millwork_options WHERE category = $1 AND name = $2`,
			o.Category, o.Name); err != nil {
			log.Printf("Seed: the configured item's option catalog: %v", err)
			return
		}
		if _, err := tx.Exec(`
			INSERT INTO millwork_options (
				category, name, price_adjustment,
				base_price_cents, base_cost_cents, cost_adjustment_cents,
				attributes, created_at, updated_at
			) VALUES ($1, $2, $3::numeric, $4, $5, $6, '{}'::jsonb, $7, $7)`,
			o.Category, o.Name, qeCents(o.PriceAdjustmentCents),
			o.BasePriceCents, o.BaseCostCents, o.CostAdjustmentCents,
			time.Now().UTC()); err != nil {
			log.Printf("Seed: the configured item's option catalog: %v", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("Seed: the configured item's option catalog: %v", err)
		return
	}
	fmt.Printf("Seed: the configured item's option catalog: %d options\n", len(rows))
}

// qeCents renders a cents amount as the decimal the NUMERIC column takes,
// exactly, without a float.
func qeCents(cents int64) string {
	sign := ""
	n := cents
	if n < 0 {
		sign, n = "-", -n
	}
	return fmt.Sprintf("%s%d.%02d", sign, n/100, n%100)
}
