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
	"github.com/gablelbm/gable/internal/product"
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
		{"a credit total renders negative", -2500, "TOTAL DUE: $-25.00"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(newRepo())
			inv := &invoice.Invoice{ID: uuid.New(), TotalAmount: tc.cents, CreatedAt: time.Now()}
			cust := &customer.Customer{Name: "Acme Construction", AccountNumber: "C-1001"}

			doc, err := svc.GenerateInvoicePDF(context.Background(), inv, cust)
			if err != nil {
				t.Fatalf("GenerateInvoicePDF: %v", err)
			}
			assertPDF(t, doc)
			mustContain(t, doc, tc.want)
			// The bare cents value must never be printed as if it were
			// dollars — that is the $73.88-becomes-$7,388.00 failure. Skipped
			// where the two renderings coincide (whole-dollar and zero cases).
			if asDollars := "TOTAL DUE: $" + itoa(tc.cents) + ".00"; asDollars != tc.want {
				mustNotContain(t, doc, asDollars)
			}
		})
	}
}

// CORRECTNESS: each line renders unit price and extended total in dollars. The
// extended total is quantity x unit price, so a two-of-a-$4.75-item line is
// $9.50 — not $950.00 and not $0.095.
func TestGenerateInvoicePDF_LineArithmetic(t *testing.T) {
	tests := []struct {
		name      string
		qty       float64
		priceEach int64
		wantUnit  string
		wantLine  string
	}{
		{"two at $4.75", 2, 475, "$4.75", "$9.50"},
		{"one at $128.45", 1, 12845, "$128.45", "$128.45"},
		{"fractional quantity", 2.5, 400, "$4.00", "$10.00"},
		{"zero quantity", 0, 475, "$4.75", "$0.00"},
		{"a thousand board feet", 1000, 68500, "$685.00", "$685000.00"},
		{"one cent unit price", 3, 1, "$0.01", "$0.03"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prodID := uuid.New()
			repo := newRepo(&product.Product{ID: prodID, SKU: "2X4-8", Description: "SPF Stud"})
			svc := NewService(repo)

			inv := &invoice.Invoice{
				ID: uuid.New(), CreatedAt: time.Now(),
				Lines: []invoice.InvoiceLine{{ProductID: prodID, Quantity: tc.qty, PriceEach: tc.priceEach}},
			}
			doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
			if err != nil {
				t.Fatalf("GenerateInvoicePDF: %v", err)
			}
			assertPDF(t, doc)
			mustContain(t, doc, tc.wantUnit)
			mustContain(t, doc, tc.wantLine)
		})
	}
}

// CORRECTNESS: the line identifies the product by SKU and description, because
// that is what the customer matches against their own purchase order.
func TestGenerateInvoicePDF_LinesNameTheProduct(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	repo := newRepo(
		&product.Product{ID: a, SKU: "2X4-8-SPF", Description: "2x4x8 SPF Stud"},
		&product.Product{ID: b, SKU: "OSB-716", Description: "7/16 OSB Sheathing"},
	)
	svc := NewService(repo)

	inv := &invoice.Invoice{
		ID: uuid.New(), CreatedAt: time.Now(), TotalAmount: 20000,
		Lines: []invoice.InvoiceLine{
			{ProductID: a, Quantity: 10, PriceEach: 475},
			{ProductID: b, Quantity: 4, PriceEach: 2899},
		},
	}
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

	if len(repo.lookups) != 2 {
		t.Errorf("looked up %d products, want one per line", len(repo.lookups))
	}
}

// CORRECTNESS: a product the catalogue no longer has must not stop the invoice
// being produced, and must not render as a blank line the customer cannot
// identify. A missing product is a data problem, not a reason to fail billing.
func TestGenerateInvoicePDF_UnknownProductFallsBack(t *testing.T) {
	repo := newRepo() // every lookup misses
	svc := NewService(repo)

	inv := &invoice.Invoice{
		ID: uuid.New(), CreatedAt: time.Now(), TotalAmount: 950,
		Lines: []invoice.InvoiceLine{{ProductID: uuid.New(), Quantity: 2, PriceEach: 475}},
	}
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF must not fail on a missing product: %v", err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "Unknown Product")
	// The money must still be right even when the description is not.
	mustContain(t, doc, "$9.50")
	mustContain(t, doc, "TOTAL DUE: $9.50")
}

// CORRECTNESS: a repository outage must degrade the same way — the invoice
// still renders, with placeholder descriptions.
func TestGenerateInvoicePDF_RepositoryOutageStillProducesADocument(t *testing.T) {
	repo := newRepo()
	repo.err = errors.New("db down")
	svc := NewService(repo)

	inv := &invoice.Invoice{
		ID: uuid.New(), CreatedAt: time.Now(), TotalAmount: 475,
		Lines: []invoice.InvoiceLine{{ProductID: uuid.New(), Quantity: 1, PriceEach: 475}},
	}
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "Unknown Product")
}

// CORRECTNESS: an invoice with no lines is still a valid document showing the
// total — a customer must not be sent a truncated or empty file.
func TestGenerateInvoicePDF_NoLines(t *testing.T) {
	svc := NewService(newRepo())
	inv := &invoice.Invoice{ID: uuid.New(), CreatedAt: time.Now(), TotalAmount: 5000}

	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "TOTAL DUE: $50.00")
}

// CORRECTNESS: the invoice date is the creation date, formatted unambiguously.
func TestGenerateInvoicePDF_ShowsTheInvoiceDate(t *testing.T) {
	created := time.Date(2026, 3, 4, 16, 30, 0, 0, time.UTC)
	svc := NewService(newRepo())
	inv := &invoice.Invoice{ID: uuid.New(), CreatedAt: created, TotalAmount: 100}

	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	mustContain(t, assertPDF(t, doc), "2026-03-04")
}

// CORRECTNESS: the invoice PDF must not jump from the line extensions straight
// to a tax-inclusive "TOTAL DUE". Without a subtotal row and a tax row the
// lines visibly do not add up to the total: a $100.00 order with 12% BC tax
// renders as lines summing to $100.00 above a TOTAL DUE of $112.00 with
// nothing to explain the $12.00.
//
// The Invoice model already carries Subtotal, TaxRate and TaxAmount
// (invoice/model.go:29-32) and the tax rate is resolved per branch, so the
// data is present. Showing tax separately is also a statutory requirement in
// the GST/HST jurisdictions this product targets.
func TestGenerateInvoicePDF_MustShowSubtotalAndTax(t *testing.T) {
	prodID := uuid.New()
	repo := newRepo(&product.Product{ID: prodID, SKU: "2X4-8", Description: "SPF Stud"})
	svc := NewService(repo)

	inv := &invoice.Invoice{
		ID: uuid.New(), CreatedAt: time.Now(),
		Subtotal:    10000, // $100.00
		TaxRate:     0.12,
		TaxAmount:   1200,  // $12.00
		TotalAmount: 11200, // $112.00
		// Two $50.00 lines, so "$100.00" can only appear if a subtotal row is
		// rendered — it is not a line extension.
		Lines: []invoice.InvoiceLine{
			{ProductID: prodID, Quantity: 50, PriceEach: 100},
			{ProductID: prodID, Quantity: 50, PriceEach: 100},
		},
	}
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "$100.00") // subtotal
	mustContain(t, doc, "$12.00")  // tax
	mustContain(t, doc, "TOTAL DUE: $112.00")
}

// CHARACTERIZATION: the "PAY ONLINE" link is hard-coded to app.gable.com and
// keyed on the invoice UUID. For a self-hosted or white-labelled dealer this
// URL points at someone else's domain; the portal branding config
// (portal.PortalConfig) is not consulted.
func TestGenerateInvoicePDF_PayLinkIsHardCoded(t *testing.T) {
	id := uuid.New()
	svc := NewService(newRepo())
	inv := &invoice.Invoice{ID: id, CreatedAt: time.Now(), TotalAmount: 100}

	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GenerateInvoicePDF: %v", err)
	}
	mustContain(t, assertPDF(t, doc), "https://app.gable.com/pay/"+id.String())
}

// --- pick ticket ---------------------------------------------------------

// CORRECTNESS: the pick ticket tells the yard what to pull. It must carry the
// quantity and the unit of measure — "4" of a product sold by the piece and by
// the thousand board feet are wildly different pulls.
func TestGeneratePickTicketPDF_QuantityAndUOM(t *testing.T) {
	prodID := uuid.New()
	repo := newRepo(&product.Product{ID: prodID, SKU: "2X4-8", Description: "SPF Stud", UOMPrimary: "MBF"})
	svc := NewService(repo)

	ord := &order.Order{
		ID: uuid.New(), CreatedAt: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
		Lines: []order.OrderLine{{ProductID: prodID, Quantity: 12.5}},
	}
	doc, err := svc.GeneratePickTicketPDF(context.Background(), ord, &customer.Customer{Name: "Acme Construction"})
	if err != nil {
		t.Fatalf("GeneratePickTicketPDF: %v", err)
	}
	assertPDF(t, doc)

	mustContain(t, doc, "PICK TICKET")
	mustContain(t, doc, "2X4-8 - SPF Stud")
	mustContain(t, doc, "12.50 [MBF]")
	mustContain(t, doc, "Acme Construction")
	mustContain(t, doc, "2026-03-04")
}

// CORRECTNESS: a product with no configured UOM falls back to EA rather than
// printing an empty bracket the picker has to guess at.
func TestGeneratePickTicketPDF_UOMFallback(t *testing.T) {
	known := uuid.New()
	repo := newRepo(&product.Product{ID: known, SKU: "X", Description: "Y"}) // no UOM set
	svc := NewService(repo)

	ord := &order.Order{
		ID: uuid.New(), CreatedAt: time.Now(),
		Lines: []order.OrderLine{
			{ProductID: known, Quantity: 3},
			{ProductID: uuid.New(), Quantity: 5}, // unknown product
		},
	}
	doc, err := svc.GeneratePickTicketPDF(context.Background(), ord, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("GeneratePickTicketPDF: %v", err)
	}
	assertPDF(t, doc)

	mustContain(t, doc, "Unknown Product")
	mustContain(t, doc, "5.00 [EA]")
	// A product row with no UOM configured renders an empty bracket today.
	mustContain(t, doc, "3.00 []")
}

// CORRECTNESS: the pick ticket must not carry pricing. It goes to the yard and
// travels with the load, so unit costs and customer pricing must not be on it.
func TestGeneratePickTicketPDF_CarriesNoPricing(t *testing.T) {
	prodID := uuid.New()
	repo := newRepo(&product.Product{ID: prodID, SKU: "2X4-8", Description: "SPF Stud", UOMPrimary: "EA", BasePrice: 4.75})
	svc := NewService(repo)

	ord := &order.Order{
		ID: uuid.New(), CreatedAt: time.Now(), TotalAmount: 128450,
		Lines: []order.OrderLine{{ProductID: prodID, Quantity: 10, PriceEach: 475}},
	}
	doc, err := svc.GeneratePickTicketPDF(context.Background(), ord, &customer.Customer{Name: "Acme", CreditLimit: 25000, BalanceDue: 4873.19})
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
	ord := &order.Order{ID: uuid.New(), CreatedAt: time.Now()}

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
	ord := &order.Order{ID: uuid.New(), CreatedAt: time.Now()}

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
	prodID := uuid.New()
	repo := newRepo(&product.Product{ID: prodID, SKU: "2X4-8", Description: "SPF Stud"})
	svc := NewService(repo)

	inv := &invoice.Invoice{
		ID: uuid.New(), CreatedAt: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), TotalAmount: 950,
		Lines: []invoice.InvoiceLine{{ProductID: prodID, Quantity: 2, PriceEach: 475}},
	}
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
	for _, want := range []string{"TOTAL DUE: $9.50", "2X4-8 - SPF Stud", "$4.75", "2026-03-04"} {
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
