// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"math/rand"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// RULE (ADR 0005 2.4 applied to credits): the share of a line's amount for r of
// the billed units telescopes: pieces taken in any order and size, each as the
// difference of two cumulative shares, sum to the whole to the cent, and never
// run backwards.
func TestSharePiecesSumToTheWhole(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 500; trial++ {
		whole := int64(rng.Intn(100000) + 1)      // cents
		billed := int64(rng.Intn(200000) + 10000) // scale 4
		var done, got int64                       // quantity credited so far, cents credited so far
		for done < billed {
			piece := int64(rng.Intn(int(billed-done))) + 1
			cum := share(whole, done+piece, billed)
			if cum < got {
				t.Fatalf("whole %d billed %d: the cumulative share ran backwards (%d after %d)", whole, billed, cum, got)
			}
			got, done = cum, done+piece
		}
		if got != whole {
			t.Fatalf("whole %d billed %d: the pieces sum to %d", whole, billed, got)
		}
	}
	if share(1000, 0, 10) != 0 || share(1000, 10, 10) != 1000 || share(-1000, 5, 10) != -500 {
		t.Error("share edge cases")
	}
}

func creditLineOf(total int64) InvoiceLine {
	qty := httpx.Quantity(100000) // 10
	one := salesdoc.One
	price := httpx.Price(55000)
	lt := httpx.Cents(total)
	uom := "PCS"
	var l InvoiceLine
	l.ID = uuid.New()
	l.LineType = salesdoc.LineProduct
	l.Quantity, l.UOM, l.PriceUOM, l.UOMQty, l.PriceUOMQty = &qty, &uom, &uom, &one, &one
	l.UnitPrice, l.LineTotal, l.Taxable = &price, &lt, true
	l.Description = "line"
	return l
}

// RULE: a credit line returning part of an invoice line takes its amount from
// the line's charged total in proportion, the earlier credits telescoped, and a
// line cannot credit more than it billed.
func TestCreditFromInvoiceLine(t *testing.T) {
	src := creditLineOf(5500)
	d := &CreditLineInput{InvoiceLineID: &src.ID, Quantity: -30000} // 3 of 10
	cl, err := creditFromInvoiceLine("lines[0]", d, &src, Credited{}, Credited{})
	if err != nil {
		t.Fatal(err)
	}
	if got := int64(*cl.LineTotal); got != -1650 || *cl.Quantity != -30000 {
		t.Errorf("3 of 10 on 5500 = %d (%v), want -1650", got, *cl.Quantity)
	}
	// 3 earlier credited (1650), 3 more: 3300 cumulative, a piece of 1650
	cl, err = creditFromInvoiceLine("lines[0]", d, &src, Credited{Quantity: 30000, TotalCents: 1650}, Credited{Quantity: 30000})
	if err != nil || int64(*cl.LineTotal) != -1650 {
		t.Fatalf("second piece = %v %v", cl.LineTotal, err)
	}
	// the last 4 take the remainder to the cent
	last := &CreditLineInput{InvoiceLineID: &src.ID, Quantity: -40000}
	cl, err = creditFromInvoiceLine("lines[0]", last, &src, Credited{Quantity: 60000, TotalCents: 3300}, Credited{Quantity: 60000})
	if err != nil || int64(*cl.LineTotal) != -2200 {
		t.Fatalf("last piece = %v %v", cl.LineTotal, err)
	}
	// over the line: exceeds_billed, counting the drafts
	_, err = creditFromInvoiceLine("lines[0]", d, &src, Credited{Quantity: 60000, TotalCents: 3300}, Credited{Quantity: 80000})
	if err == nil {
		t.Fatal("crediting 3 with 8 already counted of 10 was accepted")
	}
	// restock without a product is refused
	rs := &CreditLineInput{InvoiceLineID: &src.ID, Quantity: -10000, Restock: true}
	if _, err = creditFromInvoiceLine("lines[0]", rs, &src, Credited{}, Credited{}); err == nil {
		t.Error("restocking a non stock line was accepted")
	}
}

// RULE (ADR 0005 3): the cost that comes back on a restocked line is the
// invoice line's own cost in proportion, never more than that line relieved.
func TestRestockCostComesBackAtTheOriginalCost(t *testing.T) {
	src := creditLineOf(5500)
	pid := uuid.New()
	src.ProductID = &pid
	src.CostCents = 3250
	unit := httpx.Price(32500)
	src.UnitCost = &unit
	d := &CreditLineInput{InvoiceLineID: &src.ID, Quantity: -40000, Restock: true}
	cl, err := creditFromInvoiceLine("lines[0]", d, &src, Credited{}, Credited{})
	if err != nil || int64(cl.CostCents) != -1300 || !cl.Restock {
		t.Fatalf("restock cost = %v %v, want -1300", cl.CostCents, err)
	}
	// 9 earlier restocked at a cost of 3250 less 130: only what is left comes back
	big := &CreditLineInput{InvoiceLineID: &src.ID, Quantity: -10000, Restock: true}
	cl, err = creditFromInvoiceLine("lines[0]", big, &src, Credited{Quantity: 90000, TotalCents: 4950, CostCents: 3200, RestockQuantity: 90000}, Credited{Quantity: 90000})
	if err != nil || int64(cl.CostCents) != -50 {
		t.Errorf("last restock = %v %v, want the remaining -50 not -325", cl.CostCents, err)
	}
}
