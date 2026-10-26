// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
	"github.com/google/uuid"
)

type offlineArithmeticFixture struct {
	Extensions []struct {
		Name             string  `json:"name"`
		Qty              string  `json:"qty"`
		UOM              string  `json:"uom"`
		PriceUOM         string  `json:"price_uom"`
		UOMQty           string  `json:"uom_qty"`
		PriceUOMQty      string  `json:"price_uom_qty"`
		UnitPriceTenThou int64   `json:"unit_price_ten_thousandths"`
		PriceKey         string  `json:"price_key"`
		PriceSource      string  `json:"price_source"`
		PriceBasis       *string `json:"price_basis"`
		ExpectedCents    int64   `json:"expected_cents"`
	} `json:"extensions"`
	Taxes []struct {
		Name             string `json:"name"`
		TaxableCents     int64  `json:"taxable_cents"`
		Rate             string `json:"rate"`
		RateScaled       int64  `json:"rate_scaled"`
		ExpectedTaxCents int64  `json:"expected_tax_cents"`
	} `json:"taxes"`
	StockConversions []struct {
		Name             string  `json:"name"`
		Qty              string  `json:"qty"`
		UOM              string  `json:"uom"`
		StockUOM         string  `json:"stock_uom"`
		UnitQty          string  `json:"unit_qty"`
		StockQty         string  `json:"stock_qty"`
		Refused          bool    `json:"refused"`
		ExpectedStockQty *string `json:"expected_stock_qty"`
		NearestBelow     *string `json:"nearest_below"`
		NearestAbove     *string `json:"nearest_above"`
		ExpectedError    *string `json:"expected_error"`
	} `json:"stock_conversions"`
	StockRestatements []struct {
		Name             string `json:"name"`
		LineIndex        int    `json:"line_index"`
		RecordedStockUOM string `json:"recorded_stock_uom"`
		RecordedStockQty string `json:"recorded_stock_qty"`
		CurrentStockUOM  string `json:"current_stock_uom"`
		UnitSet          []struct {
			UOM      string `json:"uom"`
			UnitQty  string `json:"unit_qty"`
			StockQty string `json:"stock_qty"`
			Sell     bool   `json:"sell"`
			Price    bool   `json:"price"`
		} `json:"unit_set"`
		Refused          bool    `json:"refused"`
		ExpectedStockUOM *string `json:"expected_stock_uom"`
		ExpectedStockQty *string `json:"expected_stock_qty"`
		ExpectedCode     *string `json:"expected_code"`
	} `json:"stock_restatements"`
	Tallies []struct {
		Name        string `json:"name"`
		ThicknessIn string `json:"thickness_in"`
		WidthIn     string `json:"width_in"`
		Rows        []struct {
			Pieces   int    `json:"pieces"`
			LengthFT string `json:"length_ft"`
		} `json:"rows"`
		ExpectedLinearFeet string `json:"expected_linear_feet"`
		ExpectedBoardFeet  string `json:"expected_board_feet"`
	} `json:"tallies"`
	Tenders []struct {
		Name                  string          `json:"name"`
		TotalCents            int64           `json:"total_cents"`
		Tenders               []OfflineTender `json:"tenders"`
		Refused               bool            `json:"refused"`
		ExpectedTenderedCents int64           `json:"expected_tendered_cents"`
		ExpectedCashCents     int64           `json:"expected_cash_cents"`
		ExpectedAccountCents  int64           `json:"expected_account_cents"`
		ExpectedChangeCents   int64           `json:"expected_change_cents"`
		ExpectedCode          *string         `json:"expected_code"`
	} `json:"tenders"`
}

// RULE (ADR 0011 section 7): the Go salesdoc arithmetic and the Rust offline
// core arithmetic run the same shared fixture (testdata/offline-arithmetic.json),
// covering extension and tax rounding on both sides of a half, a line in a unit
// other than its row's price unit, a contract price in its own unit, a cost
// derived level in the unit it derives from, exact and refused stock
// conversions, stock unit restatements (converted when the old unit remains in
// the current set, refused when gone), tally linear and board feet, and tender
// sums and change.
func TestOfflineArithmeticFixture(t *testing.T) {
	rawBytes, err := os.ReadFile("testdata/offline-arithmetic.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fix offlineArithmeticFixture
	if err := json.Unmarshal(rawBytes, &fix); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	for _, tc := range fix.Extensions {
		t.Run("extend/"+tc.Name, func(t *testing.T) {
			qty := mustQ(t, tc.Qty)
			uq := mustQ(t, tc.UOMQty)
			pq := mustQ(t, tc.PriceUOMQty)
			price := httpx.Price(tc.UnitPriceTenThou)
			uom := tc.UOM
			priceUOM := tc.PriceUOM
			line := Line{
				ID:          uuid.New(),
				LineType:    LineProduct,
				Quantity:    &qty,
				UOM:         &uom,
				PriceUOM:    &priceUOM,
				UOMQty:      &uq,
				PriceUOMQty: &pq,
				UnitPrice:   &price,
				PriceSource: PriceSource(tc.PriceSource),
				PriceBasis:  tc.PriceBasis,
			}
			if err := ExtendLine(&line); err != nil {
				t.Fatalf("ExtendLine: %v", err)
			}
			if line.LineTotal == nil || int64(*line.LineTotal) != tc.ExpectedCents {
				t.Errorf("line total = %v, want %d", line.LineTotal, tc.ExpectedCents)
			}
		})
	}

	for _, tc := range fix.Taxes {
		t.Run("tax/"+tc.Name, func(t *testing.T) {
			scaled, ok, err := ParseTaxRate(tc.Rate)
			if err != nil || !ok {
				t.Fatalf("ParseTaxRate(%q): ok=%v err=%v", tc.Rate, ok, err)
			}
			if scaled != tc.RateScaled {
				t.Fatalf("scaled = %d, want %d", scaled, tc.RateScaled)
			}
			got := TaxAt(httpx.Cents(tc.TaxableCents), scaled)
			if int64(got) != tc.ExpectedTaxCents {
				t.Errorf("TaxAt(%d, %d) = %d, want %d", tc.TaxableCents, scaled, got, tc.ExpectedTaxCents)
			}
		})
	}

	for _, tc := range fix.StockConversions {
		t.Run("stock_conversion/"+tc.Name, func(t *testing.T) {
			qty := mustQ(t, tc.Qty)
			uq := mustQ(t, tc.UnitQty)
			sq := mustQ(t, tc.StockQty)
			got, err := units.ConvertStock(qty, tc.UOM, tc.StockUOM, units.Pair{A: uq, B: sq})
			if tc.Refused {
				if err == nil {
					t.Fatalf("ConvertStock succeeded with %s, want refusal", got.WireString())
				}
				var inexact *units.InexactError
				if !errors.As(err, &inexact) {
					t.Fatalf("err = %T (%v), want *units.InexactError", err, err)
				}
				if tc.NearestBelow != nil && inexact.Below.WireString() != *tc.NearestBelow {
					t.Errorf("below = %s, want %s", inexact.Below.WireString(), *tc.NearestBelow)
				}
				if tc.NearestAbove != nil && inexact.Above.WireString() != *tc.NearestAbove {
					t.Errorf("above = %s, want %s", inexact.Above.WireString(), *tc.NearestAbove)
				}
				if tc.ExpectedError != nil && inexact.Error() != *tc.ExpectedError {
					t.Errorf("error = %q, want %q", inexact.Error(), *tc.ExpectedError)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConvertStock: %v", err)
			}
			if tc.ExpectedStockQty == nil || got.WireString() != *tc.ExpectedStockQty {
				t.Errorf("stock qty = %s, want %v", got.WireString(), tc.ExpectedStockQty)
			}
		})
	}

	for _, tc := range fix.StockRestatements {
		t.Run("stock_restatement/"+tc.Name, func(t *testing.T) {
			recUOM := tc.RecordedStockUOM
			recQty := mustQ(t, tc.RecordedStockQty)
			line := &Line{
				ID:            uuid.New(),
				LineType:      LineProduct,
				StockUOM:      &recUOM,
				StockQuantity: &recQty,
			}
			set := &ProductUnitSet{StockUOM: tc.CurrentStockUOM}
			for _, r := range tc.UnitSet {
				set.Rows = append(set.Rows, UnitSetRow{
					UOM:      r.UOM,
					UnitQty:  mustQ(t, r.UnitQty),
					StockQty: mustQ(t, r.StockQty),
					Sell:     r.Sell,
					Price:    r.Price,
				})
			}
			uom, qty, err := ResolveStockMove(tc.LineIndex, line, tc.CurrentStockUOM, set)
			if tc.Refused {
				if err == nil {
					t.Fatalf("ResolveStockMove succeeded (%s %s), want refusal", qty.WireString(), uom)
				}
				var hx *httpx.Error
				if !errors.As(err, &hx) || len(hx.Details) == 0 {
					t.Fatalf("err = %T (%v), want *httpx.Error with details", err, err)
				}
				if tc.ExpectedCode != nil && hx.Details[0].Code != *tc.ExpectedCode {
					t.Errorf("code = %q, want %q", hx.Details[0].Code, *tc.ExpectedCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveStockMove: %v", err)
			}
			if tc.ExpectedStockUOM == nil || uom != *tc.ExpectedStockUOM {
				t.Errorf("stock uom = %q, want %v", uom, tc.ExpectedStockUOM)
			}
			if tc.ExpectedStockQty == nil || qty.WireString() != *tc.ExpectedStockQty {
				t.Errorf("stock qty = %s, want %v", qty.WireString(), tc.ExpectedStockQty)
			}
		})
	}

	for _, tc := range fix.Tallies {
		t.Run("tally/"+tc.Name, func(t *testing.T) {
			thick := mustQ(t, tc.ThicknessIn)
			width := mustQ(t, tc.WidthIn)
			tally := &Tally{
				ThicknessIn: &thick,
				WidthIn:     &width,
			}
			for _, r := range tc.Rows {
				tally.Rows = append(tally.Rows, TallyRow{
					Pieces:   r.Pieces,
					LengthFT: mustQ(t, r.LengthFT),
				})
			}
			lines := []Line{{ID: uuid.New(), LineType: LineProduct, Tally: tally}}
			CompleteTallies(lines)
			got := lines[0].Tally
			if got == nil {
				t.Fatal("CompleteTallies cleared the tally")
			}
			if got.LinearFeet.WireString() != tc.ExpectedLinearFeet {
				t.Errorf("linear_feet = %s, want %s", got.LinearFeet.WireString(), tc.ExpectedLinearFeet)
			}
			if got.BoardFeet == nil || got.BoardFeet.WireString() != tc.ExpectedBoardFeet {
				t.Errorf("board_feet = %v, want %s", got.BoardFeet, tc.ExpectedBoardFeet)
			}
		})
	}

	for _, tc := range fix.Tenders {
		t.Run("tender/"+tc.Name, func(t *testing.T) {
			res, err := SettleOfflineTenders(httpx.Cents(tc.TotalCents), tc.Tenders)
			if tc.Refused {
				if err == nil {
					t.Fatalf("SettleOfflineTenders succeeded (%+v), want refusal", res)
				}
				if tc.ExpectedCode != nil && res.RefusalCode != *tc.ExpectedCode {
					t.Errorf("refusal code = %q, want %q", res.RefusalCode, *tc.ExpectedCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("SettleOfflineTenders: %v", err)
			}
			if int64(res.TenderedCents) != tc.ExpectedTenderedCents {
				t.Errorf("tendered = %d, want %d", res.TenderedCents, tc.ExpectedTenderedCents)
			}
			if int64(res.CashCents) != tc.ExpectedCashCents {
				t.Errorf("cash = %d, want %d", res.CashCents, tc.ExpectedCashCents)
			}
			if int64(res.AccountCents) != tc.ExpectedAccountCents {
				t.Errorf("account = %d, want %d", res.AccountCents, tc.ExpectedAccountCents)
			}
			if int64(res.ChangeCents) != tc.ExpectedChangeCents {
				t.Errorf("change = %d, want %d", res.ChangeCents, tc.ExpectedChangeCents)
			}
		})
	}
}
