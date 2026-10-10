// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// TestUnitPairWire is the exit test's line (ADR 0006 section 9.3): the round
// trip of TestEveryDefinedPairRoundTrips run over the wire, on a fixture
// catalogue that covers each derivation rule, for every ordered pair (A, B)
// of a product's set: the pair GET /products/{id}/units answers resolves a
// quote line per pair to the same conversion, the pair for (A, B) is the
// swap of the pair for (B, A), and a sample of quantities converted A to B
// and back returns itself whenever the forward result is exact.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/units"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// pairWireFixture serves the product and quote routes over one database: the
// unit set routes answer the set, the quote routes answer a line per pair.
type pairWireFixture struct {
	*kitFixture
	quotes *httptest.Server
}

func newPairWireFixture(t *testing.T) *pairWireFixture {
	t.Helper()
	db := testutil.RequireDB(t)
	f := &pairWireFixture{kitFixture: newKitFixture(t, nil, db)}
	quoteSvc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	mux := http.NewServeMux()
	quote.NewHandler(quoteSvc).RegisterRoutes(mux)
	f.quotes = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(f.quotes.Close)
	return f
}

// pairQuoteLine creates a one line quote between two units of the product
// and answers the stored line from the columns.
func (f *pairWireFixture) pairQuoteLine(t *testing.T, productID, customerID, uom, priceUOM string) (string, string) {
	t.Helper()
	body := map[string]any{
		"customer_id": customerID, "delivery_type": "pickup",
		"lines": []any{map[string]any{
			"product_id": productID, "quantity": "1", "uom": uom,
			"price_uom": priceUOM, "unit_price_ten_thousandths": 10000,
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", f.quotes.URL+"/api/v1/quotes", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	httpRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer httpRes.Body.Close()
	data, _ := io.ReadAll(httpRes.Body)
	if httpRes.StatusCode != http.StatusCreated {
		t.Fatalf("a %s line priced per %s = %d: %s", uom, priceUOM, httpRes.StatusCode, data)
	}
	var created struct {
		Lines []struct {
			UOMQty      string `json:"uom_qty"`
			PriceUOMQty string `json:"price_uom_qty"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(data, &created); err != nil || len(created.Lines) != 1 {
		t.Fatalf("the created quote does not read back: %v (%s)", err, data)
	}
	return created.Lines[0].UOMQty, created.Lines[0].PriceUOMQty
}

func TestUnitPairWire(t *testing.T) {
	f := newPairWireFixture(t)
	ctx := context.Background()

	// A customer for the quote creates.
	var customerID uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Pair Wire Co', $2,
			(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))
		RETURNING id`, uuid.New(), "PW-"+uuid.NewString()[:8]).Scan(&customerID); err != nil {
		t.Fatal(err)
	}

	// The fixture catalogue: the 2x4x8, whose set covers every derivation
	// rule (the piece through the fixed length, the standard size, the cross
	// section through the piece and through LF).
	fixed, rev := f.createBoard(map[string]any{
		"board_thickness_in": "2", "board_width_in": "4", "board_length_ft": "8",
	})
	res := f.putUnits(fixed, unitsBody(rev, "PCS", "PCS", "PCS", "MBF", []any{
		unitRow("PCS", true, true, true),
		unitRow("LF", true, false, false),
		unitRow("BF", false, false, true),
		unitRow("MBF", false, true, true),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("the 2x4x8 set = %d: %s", res.status, res.raw)
	}

	// The set off the wire, as pairs with their use flags.
	got := f.do("GET", "/api/v1/products/"+fixed+"/units", nil)
	if got.status != http.StatusOK {
		t.Fatalf("GET the set = %d: %s", got.status, got.raw)
	}
	type setRow struct {
		pair  units.Pair
		sell  bool
		price bool
	}
	rows := map[string]setRow{}
	for _, it := range got.body["units"].([]any) {
		row := it.(map[string]any)
		a, errA := parseWireQty(row["unit_qty"].(string))
		b, errB := parseWireQty(row["stock_qty"].(string))
		if errA != nil || errB != nil {
			t.Fatalf("the set's pair does not parse: %v", row)
		}
		rows[row["uom"].(string)] = setRow{
			pair:  units.Pair{A: a, B: b},
			sell:  row["sell"].(bool),
			price: row["price"].(bool),
		}
	}

	// Every ordered pair (A, B) resolves, and the pair for (A, B) is the
	// swap of the pair for (B, A).
	resolved := map[[2]string]units.Pair{}
	for a, rowA := range rows {
		for b, rowB := range rows {
			if a == b {
				continue
			}
			want, err := units.ResolveLinePair(rowA.pair, rowB.pair)
			if err != nil {
				t.Fatalf("the pair for (%s, %s) does not resolve: %v", a, b, err)
			}
			resolved[[2]string{a, b}] = want
		}
	}
	for a := range rows {
		for b := range rows {
			if a == b {
				continue
			}
			if !resolved[[2]string{a, b}].SameRatio(resolved[[2]string{b, a}].Swap()) {
				t.Errorf("the pair for (%s, %s) is not the swap of the pair for (%s, %s)", a, b, b, a)
			}
		}
	}

	// A quote line per pair the wire admits (a sale unit priced per a price
	// unit of the set, section 7.4): the stored pair is the resolution of
	// the two rows.
	for a, rowA := range rows {
		if !rowA.sell {
			continue
		}
		for b, rowB := range rows {
			if a == b || !rowB.price {
				continue
			}
			want := resolved[[2]string{a, b}]
			uomQty, priceUOMQty := f.pairQuoteLine(t, fixed, customerID.String(), a, b)
			got := units.Pair{A: mustQty(t, uomQty), B: mustQty(t, priceUOMQty)}
			if !got.SameRatio(want) {
				t.Errorf("the line pair for (%s, %s) is (%s, %s); want the resolution (%s, %s)",
					a, b, uomQty, priceUOMQty, want.A.WireString(), want.B.WireString())
			}
		}
	}

	// A sample of scale 4 quantities converted A to B and back returns
	// itself whenever the forward result is exact.
	sample := []httpx.Quantity{1, 7, 10_000, 280_000, 1_234_000, 10_000_000}
	for a, rowA := range rows {
		for b, rowB := range rows {
			if a == b {
				continue
			}
			for _, q := range sample {
				forward, err := units.Convert(q, a, b, rowA.pair, rowB.pair)
				if err != nil {
					continue // refused inexact: the exact rationals agree by R3
				}
				back, err := units.Convert(forward, b, a, rowB.pair, rowA.pair)
				if err != nil {
					t.Errorf("%s of %s converts into %s but not back: %v", wire(q), a, b, err)
					continue
				}
				if back != q {
					t.Errorf("%s of %s to %s and back is %s", wire(q), a, b, wire(back))
				}
			}
		}
	}
}

// parseWireQty reads a decimal string into the platform's exact quantity.
func parseWireQty(s string) (httpx.Quantity, error) {
	return httpx.ParseQuantity(s)
}

func mustQty(t *testing.T, s string) httpx.Quantity {
	t.Helper()
	q, err := parseWireQty(s)
	if err != nil {
		t.Fatalf("the quantity %s does not parse: %v", s, err)
	}
	return q
}

func wire(q httpx.Quantity) string { return q.WireString() }
