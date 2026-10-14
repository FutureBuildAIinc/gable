// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
)

// demoLogo draws the demo dealer's invented logo: a green block carrying a
// lighter stripe, so a printed form visibly shows an image. It is generated,
// not shipped, so the repository carries no binary.
func demoLogo() []byte {
	const w, h = 160, 48
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			c := color.RGBA{R: 22, G: 101, B: 52, A: 255}
			if y >= 20 && y < 28 {
				c = color.RGBA{R: 134, G: 239, B: 172, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil
	}
	return b.Bytes()
}

// seedDealerProfile sets the demo's invented dealer profile (migration 115's
// single row) and logo. It writes only a profile that is still blank, so a
// profile an operator has filled in is never overwritten by a re-seed.
func seedDealerProfile(db *sql.DB) {
	res, err := db.Exec(`
		UPDATE dealer_profile
		   SET legal_name = 'Gable Lumber & Supply Ltd',
		       trading_name = 'Gable Lumber & Supply',
		       phone = '250-555-1000',
		       email = 'accounts@gablelumber.example',
		       website = 'https://gablelumber.example',
		       remit_to = 'Gable Lumber & Supply, Accounts Receivable' || E'\n' || 'PO Box 4100, Kelowna BC V1Y 7R7',
		       logo = $1, logo_content_type = 'image/png',
		       revision = revision + 1, updated_at = NOW()
		 WHERE id = 1 AND legal_name = '' AND trading_name = '' AND logo IS NULL`, demoLogo())
	if err != nil {
		log.Printf("seed: dealer profile: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		fmt.Println("Seed: Dealer Profile (invented) and logo")
	}
}
