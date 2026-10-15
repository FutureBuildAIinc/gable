// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// pageOf is the content of the page that holds the first occurrence of want:
// the document between the stream ends around it. A page's text is one stream.
func pageOf(doc []byte, want string) []byte {
	at := bytes.Index(doc, []byte(want))
	if at < 0 {
		return nil
	}
	start := bytes.LastIndex(doc[:at], []byte("endstream"))
	if start < 0 {
		start = 0
	}
	end := bytes.Index(doc[at:], []byte("endstream"))
	if end < 0 {
		return doc[start:]
	}
	return doc[start : at+end]
}

// However many lines a document has, its totals never split across a page and
// the foot (the legal name, and the remit to) never stands alone on a page:
// the page that holds the foot also holds the last of the document's content.
func TestTotalsNeverSplitAndTheFootNeverStandsAlone(t *testing.T) {
	cust := &customer.Customer{Name: "Acme", AccountNumber: "C-1"}
	qty := httpx.Quantity(10000)
	uom, sku := "EA", "PT-4"
	svc := styledService("en-US")

	for n := 0; n <= 70; n++ {
		t.Run(fmt.Sprintf("%d lines", n), func(t *testing.T) {
			// invoice: the three total rows together, then the foot with them
			inv := mkInvoice(100, func(i *invoice.Invoice) {
				for k := 0; k < n; k++ {
					i.Lines = append(i.Lines, mkLine("A", "x", "1", "EA", 100, 100))
				}
			})
			doc, err := svc.GenerateInvoicePDF(context.Background(), inv, cust)
			if err != nil {
				t.Fatal(err)
			}
			page := pageOf(doc, "SUBTOTAL:")
			for _, want := range []string{"SUBTOTAL:", "TAX", "TOTAL DUE:", "Legal name:", "REMIT TO:"} {
				if !bytes.Contains(page, []byte(want)) {
					t.Errorf("invoice: %q is not on the page that holds SUBTOTAL", want)
				}
			}

			// quote: the total with the foot
			ql := make([]quote.QuoteLine, n)
			for k := range ql {
				ql[k] = quote.QuoteLine{SKU: "PT-4", Description: "Post", Quantity: httpx.Quantity(10000), UOM: product.UOM("EA"), PriceUOM: "EA", UnitPrice: httpx.Price(10000), LineTotal: httpx.Cents(100)}
			}
			qdoc, err := svc.GenerateQuotePDF(context.Background(), &quote.Quote{QuoteSummary: quote.QuoteSummary{ID: uuid.New(), Number: "QT-1",
				TotalCents: 100, FreightCents: 500, CreatedAt: httpx.TimestampOf(time.Now())}, Lines: ql}, cust)
			if err != nil {
				t.Fatal(err)
			}
			page = pageOf(qdoc, "Legal name:")
			for _, want := range []string{"FREIGHT:", "TOTAL:"} {
				if !bytes.Contains(page, []byte(want)) {
					t.Errorf("quote: the foot's page does not hold %q", want)
				}
			}

			// purchase order: the total with the foot
			pl := make([]purchase_order.PurchaseOrderLine, n)
			for k := range pl {
				pl[k] = purchase_order.PurchaseOrderLine{Description: "Pallet", Quantity: 1, Cost: 1}
			}
			pdoc, err := svc.GeneratePurchaseOrderPDF(context.Background(), &purchase_order.PurchaseOrder{ID: uuid.New(), Number: "PO-1",
				VendorName: "Mill", CreatedAt: time.Now(), Lines: pl})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(pageOf(pdoc, "Legal name:"), []byte("TOTAL:")) {
				t.Errorf("purchase order: the foot's page does not hold the total")
			}

			// pick ticket: the last line with the foot
			ol := make([]order.OrderLine, n+1)
			for k := range ol {
				ol[k] = order.OrderLine{Line: salesdoc.Line{SKU: &sku, Description: fmt.Sprintf("Post %d", k), Quantity: &qty, UOM: &uom, LineType: salesdoc.LineProduct}}
			}
			kdoc, err := svc.GeneratePickTicketPDF(context.Background(), &order.Order{OrderSummary: order.OrderSummary{ID: uuid.New(), Number: "SO-1",
				CreatedAt: httpx.TimestampOf(time.Now())}, Lines: ol}, cust)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(pageOf(kdoc, "Legal name:"), []byte(fmt.Sprintf("Post %d", n))) {
				t.Errorf("pick ticket: the foot's page does not hold the last line")
			}

			// statement: the last row (the aging total) with the foot
			sl := make([]account.StatementLine, n)
			for k := range sl {
				sl[k] = account.StatementLine{Date: "2026-06-30", Type: "INVOICE", Description: "Invoice", AmountCents: 100, BalanceAfterCents: 100}
			}
			st := &account.Statement{CustomerID: uuid.New(), From: "2026-06-01", To: "2026-06-30",
				Currencies: []account.StatementCurrency{{Currency: "CAD", Lines: sl, ClosingBalanceCents: 100}}}
			aging := &account.AgingSummary{AsOf: "2026-06-30", Basis: "due_date", Totals: []account.AgingTotal{{Currency: "CAD", CurrentCents: 100, TotalCents: 100}}}
			sdoc, err := styledService("en-US").WithStatements(&fakeStatements{st: st, aging: aging}).GenerateStatementPDF(context.Background(), st, cust)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(pageOf(sdoc, "Legal name:"), []byte("Total:")) {
				t.Errorf("statement: the foot's page does not hold the aging total")
			}
		})
	}
}
