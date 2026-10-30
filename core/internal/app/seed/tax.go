// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

// CP-14 A1: the demo dealer is a British Columbia one, so its documents carry
// GST and PST as their own lines. The seed writes the BC province zone the
// branches' own pickups and the BC deliveries answer from, and prices the
// documents it writes itself (the historical invoices) part by part, exactly
// as the product does, freezing the parts with their cents.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/google/uuid"
)

// bcZone is the seeded BC zone, set by seedBCZone before any document is
// written; nil when the zone could not be written.
var bcZone *taxzone.Snapshot

// bcZoneFrom is the zone's first effective day: earlier than any document
// the seed writes.
const bcZoneFrom = "2000-01-01"

// demoGSTHSTNumber is the demo dealer's GST/HST number: invented, in CRA's
// shape (nine digit business number, RT, four digit reference), and no real
// registrant's. It is demo data only.
const demoGSTHSTNumber = "123456789RT0001"

// seedBCZone writes the BC province zone once (GST 5, PST 7, sourcing BOTH,
// data source seed) and keeps it for the documents the seed prices. Rates for
// other provinces are dealer data entered on the tax zones screen, never
// seeded.
func seedBCZone(db *sql.DB) {
	var id uuid.UUID
	err := db.QueryRow(`SELECT id FROM tax_zones WHERE country = 'CA' AND zone_kind = 'PROVINCE'
		AND province_code = 'BC' AND data_source = 'seed' ORDER BY created_at LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		id = uuid.New()
		if _, err = db.Exec(`INSERT INTO tax_zones (id, country, jurisdiction_code, jurisdiction_type, jurisdiction_name,
				province_code, zone_kind, sourcing, effective_from, data_source, data_version)
			VALUES ($1, 'CA', 'BC', 'PROV', 'British Columbia', 'BC', 'PROVINCE', 'BOTH', $2::date, 'seed', 'demo')`,
			id, bcZoneFrom); err == nil {
			_, err = db.Exec(`INSERT INTO tax_zone_rates (zone_id, part_name, rate, effective_from)
				VALUES ($1, 'GOODS_TAX', 0.050000, $2::date), ($1, 'PROVINCIAL_SALES_TAX', 0.070000, $2::date)
				ON CONFLICT (zone_id, part_name, effective_from) DO NOTHING`, id, bcZoneFrom)
		}
	}
	if err != nil {
		log.Printf("Seed: BC tax zone: %v", err)
		return
	}
	bcZone = &taxzone.Snapshot{ZoneID: id, CombinedRate: "0.120000", DataSource: "seed demo", Country: "CA", Province: "BC",
		Parts: []taxzone.Part{{Name: taxzone.PartGoodsTax, Rate: "0.050000"}, {Name: taxzone.PartProvincialSales, Rate: "0.070000"}}}
	fmt.Println("Seed: BC tax zone (GST 5, PST 7)")
}

// seedDemoGSTHSTNumber puts the invented number on the demo dealer's
// profile, only while none is set and the dealer is not marked not
// registered, so an operator's own number or switch is never overwritten.
func seedDemoGSTHSTNumber(db *sql.DB) {
	if _, err := db.Exec(`UPDATE dealer_profile SET gst_hst_number = $1, updated_at = NOW()
		WHERE id = 1 AND gst_hst_number = '' AND NOT gst_hst_not_registered AND trading_name = 'Gable Building Supply'`,
		demoGSTHSTNumber); err != nil {
		log.Printf("Seed: demo GST/HST number: %v", err)
	}
}

// bcTax prices a seeded BC document's taxable base part by part, each part
// rounded on its own (taxzone.Snapshot.Price, the product's own rule), and
// answers the tax and the frozen parts for tax_rate_parts. With no zone it
// falls back to the combined 12 percent rounded once and freezes nothing.
func bcTax(taxableCents int64) (int64, []byte) {
	if bcZone == nil {
		return (taxableCents*12 + 50) / 100, nil
	}
	snap := *bcZone
	snap.Parts = append([]taxzone.Part(nil), bcZone.Parts...)
	sum, err := snap.Price(httpx.Cents(taxableCents))
	if err != nil {
		return (taxableCents*12 + 50) / 100, nil
	}
	raw, err := json.Marshal(&snap)
	if err != nil {
		return int64(sum), nil
	}
	return int64(sum), raw
}

// bcZoneID is the seeded zone's id for a document's tax_zone_id, nil when
// none was written.
func bcZoneID() *uuid.UUID {
	if bcZone == nil {
		return nil
	}
	id := bcZone.ZoneID
	return &id
}

// nullJSON answers raw as a JSON text argument, or nil for no parts.
func nullJSON(raw []byte) any {
	if raw == nil {
		return nil
	}
	return string(raw)
}
