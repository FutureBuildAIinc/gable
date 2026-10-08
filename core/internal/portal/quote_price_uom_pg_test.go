// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"testing"
)

// RULE (ruling on the review of R1-15): the portal quote line shows the unit
// its unit_price is quoted per. Without it a contractor reads 500.00 per MBF
// as 500.00 per piece.
func TestPortalQuoteLine_CarriesThePriceUnit(t *testing.T) {
	tt := setupTenants(t)
	ctx := context.Background()
	if _, err := tt.db.Pool.Exec(ctx,
		`UPDATE quote_lines SET price_uom = 'MBF', uom_qty = 187.5, price_uom_qty = 1 WHERE quote_id = $1`, tt.aQuote); err != nil {
		t.Fatal(err)
	}

	q, err := NewRepository(tt.db).GetPortalQuote(ctx, tt.aQuote, tt.aCustomer)
	if err != nil {
		t.Fatalf("GetPortalQuote: %v", err)
	}
	if len(q.Lines) != 1 || q.Lines[0].UOM != "EA" || q.Lines[0].PriceUOM != "MBF" {
		t.Fatalf("line = %+v, want uom EA and price_uom MBF", q.Lines)
	}
}
