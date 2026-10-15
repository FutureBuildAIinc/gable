// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// Every document the module makes reads its amounts with digit groups and its
// dates as words in the dealer's locale (G1 notes N14 and N15). The documents
// are the invoice (which is also the counter's receipt: the receipt routes
// print the sale's invoice through GenerateInvoicePDF), the quote, the
// purchase order, the pick ticket and the statement.

func styledService(locale string) *Service {
	return NewService(newRepo()).WithProfile(fakeProfiles{profile: &dealerprofile.Profile{
		LegalName: "Granite Yard Holdings Ltd", TradingName: "Granite Yard", RemitTo: "Remit Dept, Testtown", Locale: locale,
	}})
}

func TestInvoiceAndReceiptReadGroupedAmountsAndWordDates(t *testing.T) {
	svc := styledService("en-CA")
	inv := mkInvoice(392997, func(i *invoice.Invoice) {
		i.InvoiceDate = "2026-06-30"
		i.Lines = []invoice.InvoiceLine{mkLine("PVR-1", "Paver", "1000", "PCS", 39299700, 392997)}
		i.TaxCents = 24997
		i.SubtotalCents = 368000
	})
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Date: Jun 30, 2026", "TOTAL DUE: $3,929.97", "SUBTOTAL: $3,680.00", "$3,929.9700/PCS", "$3,929.97"} {
		mustContain(t, doc, want)
	}
	for _, unwanted := range []string{"2026-06-30", "$3929.97", "$3680.00"} {
		mustNotContain(t, doc, unwanted)
	}
}

func TestQuoteReadsGroupedAmountsAndWordDates(t *testing.T) {
	svc := styledService("en-US")
	qty, _ := httpx.ParseQuantity("1")
	exp := httpx.TimestampOf(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC))
	q := &quote.Quote{QuoteSummary: quote.QuoteSummary{
		ID: uuid.New(), Number: "QT-000005", TotalCents: 392997 + 125000, FreightCents: 125000,
		CreatedAt: httpx.TimestampOf(time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)), ExpiresAt: &exp,
	}, Lines: []quote.QuoteLine{{SKU: "PT-4", Description: "Post", Quantity: qty, UOM: product.UOM("EA"), PriceUOM: "EA", UnitPrice: httpx.Price(39299700), LineTotal: httpx.Cents(392997)}}}
	doc, err := svc.GenerateQuotePDF(context.Background(), q, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Date: Jun 30, 2026", "Valid until: Jul 31, 2026", "FREIGHT: $1,250.00", "TOTAL: $5,179.97", "$3,929.97"} {
		mustContain(t, doc, want)
	}
	for _, unwanted := range []string{"2026-06-30", "2026-07-31", "$1250.00", "$5179.97", "$3929.97"} {
		mustNotContain(t, doc, unwanted)
	}
}

func TestPurchaseOrderReadsGroupedAmountsAndWordDates(t *testing.T) {
	svc := styledService("en-US")
	po := &purchase_order.PurchaseOrder{
		ID: uuid.New(), Number: "PO-000042", VendorName: "Mill Supply Co", Status: "SENT",
		CreatedAt: time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
		Lines:     []purchase_order.PurchaseOrderLine{{Description: "Pallet", Quantity: 2, Cost: 1964.985}},
	}
	doc, err := svc.GeneratePurchaseOrderPDF(context.Background(), po)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Date: Jun 30, 2026", "TOTAL: $3,929.97"} {
		mustContain(t, doc, want)
	}
	for _, unwanted := range []string{"2026-06-30", "$3929.97"} {
		mustNotContain(t, doc, unwanted)
	}
}

func TestPickTicketReadsItsDateAsWords(t *testing.T) {
	svc := styledService("en-GB")
	qty := httpx.Quantity(10000)
	uom, sku := "EA", "PT-4"
	o := &order.Order{OrderSummary: order.OrderSummary{ID: uuid.New(), Number: "SO-000001",
		CreatedAt: httpx.TimestampOf(time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC))},
		Lines: []order.OrderLine{{Line: salesdoc.Line{SKU: &sku, Description: "Post", Quantity: &qty, UOM: &uom, LineType: salesdoc.LineProduct}}}}
	doc, err := svc.GeneratePickTicketPDF(context.Background(), o, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "Date: 30 Jun 2026")
	mustNotContain(t, doc, "2026-06-30")
}

func TestStatementReadsGroupedAmountsAndWordDates(t *testing.T) {
	cust := &customer.Customer{Name: "Acme", AccountNumber: "C-9"}
	due := "2026-07-15"
	st := &account.Statement{
		CustomerID: uuid.New(), CustomerName: "Acme", From: "2026-06-01", To: "2026-06-30",
		Currencies: []account.StatementCurrency{{
			Currency: "CAD", OpeningBalanceCents: -248763, ClosingBalanceCents: 392997, NetBalanceCents: 392997 - 125000,
			Lines: []account.StatementLine{
				{Date: "2026-06-30", Type: "INVOICE", Description: "Invoice IN-000007", AmountCents: 641760, BalanceAfterCents: 392997},
			},
			UnappliedPayments: []account.UnappliedPayment{{ID: uuid.New(), Number: "PAY-000009", ReceivedOn: "2026-06-12", UnappliedCents: 125000}},
			UnappliedCents:    125000,
			OpenDocuments:     []account.OpenDocument{{Kind: "invoice", Number: "IN-000007", Date: "2026-06-30", DueDate: &due, TotalCents: 392997, OpenCents: 392997}},
		}},
	}
	aging := &account.AgingSummary{AsOf: "2026-06-30", Basis: "due_date", Totals: []account.AgingTotal{{
		Currency: "CAD", CurrentCents: 392997, TotalCents: 392997 - 125000, UnappliedCents: -125000}}}
	svc := styledService("en-CA").WithStatements(&fakeStatements{st: st, aging: aging})
	doc, err := svc.GenerateStatementPDF(context.Background(), st, cust)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"From: Jun 1, 2026", "To: Jun 30, 2026", "Opening balance: -$2,487.63", "Closing balance: $3,929.97",
		"Aging as of Jun 30, 2026", "due Jul 15, 2026", "Net balance: $2,679.97", "Jun 12, 2026", "-$1,250.00"} {
		mustContain(t, doc, want)
	}
	for _, unwanted := range []string{"2026-06-30", "2026-06-01", "2026-07-15", "$3929.97", "-$2487.63"} {
		mustNotContain(t, doc, unwanted)
	}
}
