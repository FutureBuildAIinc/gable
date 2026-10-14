// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gablelbm/gable/internal/platform/pictures"
)

// pictureDir is where the desk serves the catalogue illustrations from
// (web/apps/desk/public/catalog-illustrations).
const pictureDir = "/catalog-illustrations/"

// productImageSetting is the system_settings key the counter reads to show
// product pictures; the demo import turns it on, the base seed leaves it off.
const productImageSetting = pictures.Setting

// picturePath is the picture path a catalogue image family resolves to.
func picturePath(family string) string { return pictureDir + family + ".svg" }

// baseProductFamilies gives the base seed's counter products an illustration
// where one fits, by category and description. Products not listed (caulk and
// the like) have no picture and the tiles show their placeholder.
var baseProductFamilies = map[string]string{
	// framing lumber
	"LUM-248-PREM": "spf_framing_lumber", "LUM-2410-PREM": "spf_framing_lumber",
	"LUM-2412-PREM": "spf_framing_lumber", "LUM-2492-STUD": "spf_framing_lumber",
	"LUM-2610-PREM": "spf_framing_lumber", "LUM-2612-PREM": "spf_framing_lumber",
	"LUM-2616-PREM": "spf_framing_lumber", "LUMB14RLN3+": "spf_framing_lumber",
	"LUMB24RLN3": "spf_framing_lumber", "LUMBUT24RL": "spf_framing_lumber",
	"LUM-2810-NO2": "hem_fir_framing_lumber", "LUM-2816-NO2": "hem_fir_framing_lumber",
	"LUM-21012-NO2": "hem_fir_framing_lumber", "LUM-21216-NO2": "hem_fir_framing_lumber",
	// posts and fence boards
	"LUM-448-PT": "pt_4x4_post", "LUM-4410-PT": "pt_4x4_post", "LUM-6612-PT": "pt_6x6_post",
	"LUM-448-WRC": "cedar_post", "LUM-166-WRC": "cedar_fence_board",
	// sheet goods
	"PLY-34-CDX": "plywood_sheathing_sheet", "PLY-12-CDX": "plywood_sheathing_sheet",
	"OSB-34-TG": "osb_sheathing_sheet", "OSB-12": "osb_sheathing_sheet",
	"DW-12-REG": "drywall_sheet_stack", "DW-58-FC": "drywall_sheet_stack",
	// hardware
	"NAIL-16D-50": "common_nail_box", "NAIL-10D-50": "common_nail_box", "RF-NAIL-125": "galvanized_nail_box",
	"SCR-DECK-3-5": "deck_screw_box",
	"HANGER-26":    "joist_hanger", "HANGER-28": "joist_hanger", "HANGER-210": "joist_hanger",
	"SIMP-LUS28": "joist_hanger", "TIE-H1": "framing_connector",
	// roofing
	"RF-ARCH-30": "asphalt_shingle_bundle", "RF-SH-BLK": "asphalt_shingle_bundle",
	"RF-SH-WW": "asphalt_shingle_bundle", "RF-START": "asphalt_shingle_bundle",
	"RF-FELT-15": "roof_underlayment_roll", "RF-ICE-65": "roof_underlayment_roll", "RF-ICE-WTR": "roof_underlayment_roll",
	"RF-DRIP-10": "roof_flashing_piece", "RF-EDGE-WHT": "roof_flashing_piece",
	"RF-FLASH-STEP": "roof_flashing_piece", "RF-FLASH-PIPE": "roof_flashing_piece",
	"RF-RIDGE-4": "roof_vent_piece",
	// insulation
	"INS-R13-15": "fiberglass_insulation_batt", "INS-R19-15": "fiberglass_insulation_batt",
	"INS-R30-24": "fiberglass_insulation_batt",
	// millwork
	"DR-INT-3080-6P": "interior_door_slab", "DR-INT-3280-6P": "interior_door_slab",
	"DR-INT-3680-6P": "interior_door_slab", "DR-EXT-3680-STL": "exterior_door_unit",
	"MLD-BASE-MDF": "mdf_moulding_stack", "MLD-CASE-MDF": "mdf_moulding_stack",
	// cornice and exterior trim
	"CORN2006": "roof_flashing_piece", "CORN2009": "roof_flashing_piece",
	"CORNCTRM412+": "fibre_cement_panel", "CORNCTRMG12+": "fibre_cement_panel",
	"CORNHMOLD14": "vinyl_siding_panel", "CORNPOLY18": "vapour_barrier_roll",
	"CORNSFT1212": "fibre_cement_soffit", "CORNSFT1612": "fibre_cement_soffit",
	"CORNSFT1612V": "fibre_cement_soffit", "CORNSFT2408V": "fibre_cement_soffit",
	"CORNSHTGR49": "osb_sheathing_sheet", "CORNTAPE": "house_wrap_roll",
}

// applyProductPictures writes the illustration path onto the base seed's
// counter products and turns the picture setting on. It runs inside the demo
// import only, so the base seed alone leaves the setting off and every
// base product without a picture. A second run writes the same values.
func applyProductPictures(ctx context.Context, db *sql.DB) error {
	for sku, family := range baseProductFamilies {
		if _, err := db.ExecContext(ctx, `UPDATE products SET image_url = $2 WHERE sku = $1`, sku, picturePath(family)); err != nil {
			return fmt.Errorf("picture for %s: %w", sku, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO system_settings (key, value) VALUES ($1, 'on')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, productImageSetting); err != nil {
		return fmt.Errorf("the picture setting: %w", err)
	}
	return nil
}
