// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// These are the two documents that leave the building: the invoice a customer
// is asked to pay, and the pick ticket the yard works from. The money on the
// invoice PDF is the customer-visible rendering of int64 cents, so every
// formatting decision here is a money decision.
//
// The generated PDFs are uncompressed, so the tests assert on the literal text
// the customer sees rather than on the Go values that produced it.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// --- fake product repository --------------------------------------------

type fakeProductRepo struct {
	product.Repository // only GetProduct is used; anything else is a loud nil call
	byID               map[uuid.UUID]*product.Product
	err                error
	lookups            []uuid.UUID
}

func (f *fakeProductRepo) GetProduct(_ context.Context, id uuid.UUID) (*product.Product, error) {
	f.lookups = append(f.lookups, id)
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.byID[id]
	if !ok {
		return nil, errors.New("product not found")
	}
	return p, nil
}

func newRepo(products ...*product.Product) *fakeProductRepo {
	r := &fakeProductRepo{byID: map[uuid.UUID]*product.Product{}}
	for _, p := range products {
		r.byID[p.ID] = p
	}
	return r
}

// assertPDF checks the container is a real PDF and returns its bytes for text
// assertions.
func assertPDF(t *testing.T, doc []byte) []byte {
	t.Helper()
	if len(doc) == 0 {
		t.Fatal("no PDF bytes were produced")
	}
	if !bytes.HasPrefix(doc, []byte("%PDF-")) {
		t.Fatalf("output does not start with the PDF magic: %q", doc[:min(16, len(doc))])
	}
	if !bytes.Contains(doc, []byte("%%EOF")) {
		t.Error("the PDF has no EOF trailer; it is truncated")
	}
	return doc
}

func mustContain(t *testing.T, doc []byte, want string) {
	t.Helper()
	if !bytes.Contains(doc, []byte(want)) {
		t.Errorf("the document does not contain %q", want)
	}
}

func mustNotContain(t *testing.T, doc []byte, unwanted string) {
	t.Helper()
	if bytes.Contains(doc, []byte(unwanted)) {
		t.Errorf("the document unexpectedly contains %q", unwanted)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- invoice PDF ---------------------------------------------------------

// mkInvoice builds an invoice in the wire shape with the given total, number
// and date.
func mkInvoice(totalCents int64, mod func(*invoice.Invoice)) *invoice.Invoice {
	inv := &invoice.Invoice{}
	inv.ID = uuid.New()
	inv.Number = "IN-000001"
	inv.InvoiceDate = "2026-03-04"
	inv.TotalCents = httpx.Cents(totalCents)
	inv.SubtotalCents = httpx.Cents(totalCents)
	if mod != nil {
		mod(inv)
	}
	return inv
}

// mkLine builds a priced product line: quantity, unit and price as the invoice
// stores them, the extension in cents.
func mkLine(sku, desc, qty, uom string, unitPrice int64, totalCents int64) invoice.InvoiceLine {
	q, _ := httpx.ParseQuantity(qty)
	price := httpx.Price(unitPrice)
	total := httpx.Cents(totalCents)
	var l invoice.InvoiceLine
	l.ID = uuid.New()
	l.LineType = salesdoc.LineProduct
	l.SKU, l.Description = &sku, desc
	l.Quantity, l.UOM, l.PriceUOM = &q, &uom, &uom
	l.UnitPrice, l.LineTotal = &price, &total
	return l
}

// CORRECTNESS: the total on the invoice is the customer's payable amount. It is
// stored as int64 cents and must render as dollars with exactly two decimal
// places. Rendering 7388 as $7,388.00 is the exact failure mode CLAUDE.md warns
// about at the cents/dollars boundary.
func TestGenerateInvoicePDF_TotalIsCentsRenderedAsDollars(t *testing.T) {
	tests := []struct {
		name  string
		cents int64
		want  string
	}{
		{"under ten dollars", 738, "TOTAL DUE: $7.38"},
		{"the classic 7388 cents", 7388, "TOTAL DUE: $73.88"},
		{"exact dollars", 10000, "TOTAL DUE: $100.00"},
		{"one cent", 1, "TOTAL DUE: $0.01"},
		{"zero", 0, "TOTAL DUE: $0.00"},
		{"large invoice", 12845000, "TOTAL DUE: $128450.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(newRepo())
			doc, err := svc.GenerateInvoicePDF(context.Background(), mkInvoice(tc.cents, nil), &customer.Customer{Name: "Acme Construction", AccountNumber: "C-1001"})
			if err != nil {
				t.Fatalf("GenerateInvoicePDF: %v", err)
			}
			assertPDF(t, doc)
			mustContain(t, doc, tc.want)
			// The bare cents value must never be printed as if it were
			// dollars. Skipped where the two renderings coincide.
			if asDollars := "TOTAL DUE: $" + itoa(tc.cents) + ".00"; asDollars != tc.want {
				mustNotContain(t, doc, asDollars)
			}
		})
	}
}

// CORRECTNESS: each line renders its unit price and its extension exactly as
// the invoice stores them: the extension is the stored line total, rounded
// once, never recomputed from the quantity in float.
func TestGenerateInvoicePDF_LineArithmetic(t *testing.T) {
	tests := []struct {
		name       string
		qty        string
		unitPrice  int64 // scale 4
		total      int64 // cents
		wantUnit   string
		wantLine   string
		wantQtyStr string
	}{
		{"two at $4.75", "2", 47500, 950, "$4.7500/PCS", "$9.50", "2 PCS"},
		{"one at $128.45", "1", 1284500, 12845, "$128.4500/PCS", "$128.45", "1 PCS"},
		{"fractional quantity", "2.5", 40000, 1000, "$4.0000/PCS", "$10.00", "2.5 PCS"},
		{"a thousand board feet", "1000", 6850000, 68500000, "$685.0000/PCS", "$685000.00", "1000 PCS"},
		{"a sub cent price", "3", 100, 3, "$0.0100/PCS", "$0.03", "3 PCS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(newRepo())
			inv := mkInvoice(tc.total, func(i *invoice.Invoice) {
				i.Lines = []invoice.InvoiceLine{mkLine("2X4-8", "SPF Stud", tc.qty, "PCS", tc.unitPrice, tc.total)}
			})
			doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
			if err != nil {
				t.Fatalf("GenerateInvoicePDF: %v", err)
			}
			assertPDF(t, doc)
			mustContain(t, doc, tc.wantUnit)
			mustContain(t, doc, tc.wantLine)
			mustContain(t, doc, tc.wantQtyStr)
		})
	}
}

// CORRECTNESS: the line identifies the product by SKU and description, because
// that is what the customer matches against their own purchase order. The
// invoice prints its own snapshot of both: a product the catalogue no longer
// has (or a repository outage) cannot change what was billed, and the
// generator needs no product lookup at all.
func TestGenerateInvoicePDF_LinesNameTheProduct(t *testing.T) {
	repo := newRepo() // every lookup would miss: the invoice does not ask
	svc := NewService(repo)

	inv := mkInvoice(20000, func(i *invoice.Invoice) {
		i.Lines = []invoice.InvoiceLine{
			mkLine("2X4-8-SPF", "2x4x8 SPF Stud", "10", "PCS", 47500, 4750),
			mkLine("OSB-716", "7/16 OSB Sheathing", "4", "PCS", 289900, 11596),
		}
	})
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme", AccountNumber: "C-1001"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	assertPDF(t, doc)

	mustContain(t, doc, "2X4-8-SPF - 2x4x8 SPF Stud")
	mustContain(t, doc, "OSB-716 - 7/16 OSB Sheathing")
	mustContain(t, doc, "Acme")
	mustContain(t, doc, "C-1001")
	mustContain(t, doc, "GABLE LBM - INVOICE")
	if len(repo.lookups) != 0 {
		t.Errorf("looked up %d products; the invoice carries its own snapshot", len(repo.lookups))
	}
}

// CORRECTNESS: the invoice shows its number and its business date, not its
// internal id and a timestamp; a text line prints as a note; a void invoice is
// marked void.
func TestGenerateInvoicePDF_NumberDateNotesAndVoid(t *testing.T) {
	svc := NewService(newRepo())
	note := salesdoc.Line{ID: uuid.New(), LineType: salesdoc.LineText, Description: "Leave at the east gate"}
	inv := mkInvoice(100, func(i *invoice.Invoice) {
		i.Number, i.InvoiceDate = "IN-000042", "2026-03-04"
		i.Status = invoice.InvoiceStatusVoid
		i.Lines = []invoice.InvoiceLine{{Line: note}}
	})
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "Invoice #: IN-000042")
	mustContain(t, doc, "2026-03-04")
	mustContain(t, doc, "Leave at the east gate")
	mustContain(t, doc, "INVOICE - VOID")
}

// CORRECTNESS: an invoice with no lines is still a valid document showing the
// total: a customer must not be sent a truncated or empty file.
func TestGenerateInvoicePDF_NoLines(t *testing.T) {
	svc := NewService(newRepo())
	doc, err := svc.GenerateInvoicePDF(context.Background(), mkInvoice(5000, nil), &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "TOTAL DUE: $50.00")
}

// CORRECTNESS: the invoice PDF must not jump from the line extensions straight
// to a tax-inclusive "TOTAL DUE". Without a subtotal row and a tax row the
// lines visibly do not add up to the total. Showing tax separately is also a
// statutory requirement in the GST/HST jurisdictions this product targets.
func TestGenerateInvoicePDF_MustShowSubtotalAndTax(t *testing.T) {
	svc := NewService(newRepo())
	rate := "12"
	inv := mkInvoice(11200, func(i *invoice.Invoice) {
		i.SubtotalCents, i.TaxCents, i.TaxRatePercent = 10000, 1200, &rate
		// Two $50.00 lines, so "$100.00" can only appear if a subtotal row is
		// rendered: it is not a line extension.
		i.Lines = []invoice.InvoiceLine{
			mkLine("2X4-8", "SPF Stud", "50", "PCS", 10000, 5000),
			mkLine("2X4-8", "SPF Stud", "50", "PCS", 10000, 5000),
		}
	})
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "SUBTOTAL: $100.00")
	mustContain(t, doc, "TAX @ 12%: $12.00")
	mustContain(t, doc, "TOTAL DUE: $112.00")
}

// CHARACTERIZATION: the "PAY ONLINE" link is hard-coded to app.gable.com and
// keyed on the invoice UUID. For a self-hosted or white-labelled dealer this
// URL points at someone else's domain; the portal branding config
// (portal.PortalConfig) is not consulted.
func TestGenerateInvoicePDF_PayLinkIsHardCoded(t *testing.T) {
	inv := mkInvoice(100, nil)
	svc := NewService(newRepo())
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	mustContain(t, assertPDF(t, doc), "https://app.gable.com/pay/"+inv.ID.String())
}

// --- pick ticket ---------------------------------------------------------

// CORRECTNESS: the pick ticket tells the yard what to pull. It must carry the
// quantity and the unit of measure — "4" of a product sold by the piece and by
// the thousand board feet are wildly different pulls.
func TestGeneratePickTicketPDF_QuantityAndUOM(t *testing.T) {
	prodID := uuid.New()
	repo := newRepo(&product.Product{ID: prodID, SKU: "2X4-8", Description: "SPF Stud", UOMPrimary: "MBF"})
	svc := NewService(repo)

	prodRef := prodID
	qty := httpx.Quantity(125000)
	uom := "MBF"
	sku := "2X4-8"
	createdAt := httpx.TimestampOf(time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC))
	ord := &order.Order{
		OrderSummary: order.OrderSummary{ID: uuid.New(), CreatedAt: createdAt},
		Lines:        []order.OrderLine{{Line: salesdoc.Line{ProductID: &prodRef, SKU: &sku, Description: "SPF Stud", Quantity: &qty, UOM: &uom, LineType: salesdoc.LineProduct}}},
	}
	doc, err := svc.GeneratePickTicketPDF(context.Background(), ord, &customer.Customer{Name: "Acme Construction"})
	if err != nil {
		t.Fatalf("GeneratePickTicketPDF: %v", err)
	}
	assertPDF(t, doc)

	mustContain(t, doc, "PICK TICKET")
	mustContain(t, doc, "2X4-8 - SPF Stud")
	mustContain(t, doc, "12.5 [MBF]")
	mustContain(t, doc, "Acme Construction")
	mustContain(t, doc, "2026-03-04")
}

// CORRECTNESS: a product with no configured UOM falls back to EA rather than
// printing an empty bracket the picker has to guess at.
func TestGeneratePickTicketPDF_UOMFallback(t *testing.T) {
	known := uuid.New()
	repo := newRepo(&product.Product{ID: known, SKU: "X", Description: "Y"}) // no UOM set
	svc := NewService(repo)

	knownRef := known
	unknownRef := uuid.New()
	q3 := httpx.Quantity(30000)
	q5 := httpx.Quantity(50000)
	ord := &order.Order{
		OrderSummary: order.OrderSummary{ID: uuid.New(), CreatedAt: httpx.TimestampOf(time.Now())},
		Lines: []order.OrderLine{
			// The pick ticket reads the line's own snapshots; a product row
			// the line does not describe is not looked up any more.
			{Line: salesdoc.Line{ProductID: &knownRef, Description: "Known thing", Quantity: &q3, LineType: salesdoc.LineProduct}},
			{Line: salesdoc.Line{ProductID: &unknownRef, Description: "Unknown product", Quantity: &q5, LineType: salesdoc.LineProduct}},
		},
	}
	doc, err := svc.GeneratePickTicketPDF(context.Background(), ord, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GeneratePickTicketPDF: %v", err)
	}
	assertPDF(t, doc)

	mustContain(t, doc, "Unknown product")
	mustContain(t, doc, "5 [EA]")
	// A line with no unit falls back to EA rather than an empty bracket.
	mustContain(t, doc, "3 [EA]")
}

// CORRECTNESS: the pick ticket must not carry pricing. It goes to the yard and
// travels with the load, so unit costs and customer pricing must not be on it.
func TestGeneratePickTicketPDF_CarriesNoPricing(t *testing.T) {
	prodID := uuid.New()
	repo := newRepo(&product.Product{ID: prodID, SKU: "2X4-8", Description: "SPF Stud", UOMPrimary: "EA", BasePrice: 4.75})
	svc := NewService(repo)
	limit := httpx.Cents(2500000)

	prodRef := prodID
	q10 := httpx.Quantity(100000)
	ord := &order.Order{
		OrderSummary: order.OrderSummary{ID: uuid.New(), CreatedAt: httpx.TimestampOf(time.Now()), TotalCents: 128450},
		Lines:        []order.OrderLine{{Line: salesdoc.Line{ProductID: &prodRef, Quantity: &q10, LineType: salesdoc.LineProduct}}},
	}
	doc, err := svc.GeneratePickTicketPDF(context.Background(), ord, &customer.Customer{Name: "Acme", CreditLimitCents: &limit, BalanceCents: 487319})
	if err != nil {
		t.Fatalf("GeneratePickTicketPDF: %v", err)
	}
	assertPDF(t, doc)

	for _, unwanted := range []string{"$", "4.75", "128450", "1284.50", "25000", "4873.19", "TOTAL DUE"} {
		mustNotContain(t, doc, unwanted)
	}
}

// CORRECTNESS: an order with no lines still produces a usable ticket rather
// than an empty file.
func TestGeneratePickTicketPDF_NoLines(t *testing.T) {
	svc := NewService(newRepo())
	ord := &order.Order{OrderSummary: order.OrderSummary{ID: uuid.New(), CreatedAt: httpx.TimestampOf(time.Now())}}

	doc, err := svc.GeneratePickTicketPDF(context.Background(), ord, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GeneratePickTicketPDF: %v", err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "PICK TICKET")
	mustContain(t, doc, "Qty to Pick")
}

// CHARACTERIZATION: the pick ticket's "Job" field is hard-coded to "N/A". The
// order carries no job reference on the struct the generator is given, so a
// jobsite name can never appear on the ticket the driver takes to the site.
func TestGeneratePickTicketPDF_JobIsAlwaysNA(t *testing.T) {
	svc := NewService(newRepo())
	ord := &order.Order{OrderSummary: order.OrderSummary{ID: uuid.New(), CreatedAt: httpx.TimestampOf(time.Now())}}

	doc, err := svc.GeneratePickTicketPDF(context.Background(), ord, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GeneratePickTicketPDF: %v", err)
	}
	mustContain(t, assertPDF(t, doc), "Job: N/A")
}

// --- determinism ---------------------------------------------------------

// CORRECTNESS: the same invoice must render the same document. A generator
// whose output varies run to run cannot be cached, diffed, or trusted when a
// customer disputes what they were sent.
func TestGenerateInvoicePDF_IsDeterministicApartFromMetadata(t *testing.T) {
	svc := NewService(newRepo())
	inv := mkInvoice(950, func(i *invoice.Invoice) {
		i.Lines = []invoice.InvoiceLine{mkLine("2X4-8", "SPF Stud", "2", "PCS", 47500, 950)}
	})
	cust := &customer.Customer{Name: "Acme", AccountNumber: "C-1"}

	first, err := svc.GenerateInvoicePDF(context.Background(), inv, cust)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.GenerateInvoicePDF(context.Background(), inv, cust)
	if err != nil {
		t.Fatal(err)
	}

	// The PDF trailer carries a creation timestamp, so the whole byte stream
	// is not expected to match; the rendered content must.
	for _, want := range []string{"TOTAL DUE: $9.50", "2X4-8 - SPF Stud", "$4.7500/PCS", "2026-03-04"} {
		mustContain(t, first, want)
		mustContain(t, second, want)
	}
	if len(first) != len(second) {
		t.Errorf("document length varied between runs: %d vs %d", len(first), len(second))
	}
}

// itoa avoids pulling strconv in just for the negative-assertion helper.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
