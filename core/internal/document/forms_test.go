// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
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

// The forms carry the dealer's identity and the issuing yard's own: the
// dealer's name and logo, the yard's name and address, the remit to on the
// documents that ask for money. The fixed title and the pay link are gone.

type fakeProfiles struct {
	profile *dealerprofile.Profile
	logo    *dealerprofile.Logo
}

func (f fakeProfiles) Get(context.Context) (*dealerprofile.Profile, error) { return f.profile, nil }
func (f fakeProfiles) GetLogo(context.Context) (*dealerprofile.Logo, error) {
	if f.logo == nil {
		return nil, dealerprofile.ErrNotFound
	}
	return f.logo, nil
}

type fakeYards map[uuid.UUID]*Yard

func (f fakeYards) Yard(_ context.Context, id uuid.UUID) (*Yard, error) {
	y, ok := f[id]
	if !ok {
		return nil, errors.New("no such yard")
	}
	return y, nil
}

type fakeJobs map[uuid.UUID]string

func (f fakeJobs) JobName(_ context.Context, id uuid.UUID) (string, error) {
	n, ok := f[id]
	if !ok {
		return "", errors.New("no such job")
	}
	return n, nil
}

type fakeStatements struct {
	st    *account.Statement
	aging *account.AgingSummary
}

// AgingSummary answers the canned aging, and records nothing: the form asks
// for the customer's aging as of the statement's last day.
func (f fakeStatements) AgingSummary(_ context.Context, q account.AgingQuery) (*account.AgingSummary, error) {
	if f.aging == nil {
		return &account.AgingSummary{AsOf: q.AsOf.Format("2006-01-02"), Basis: q.Basis, Totals: []account.AgingTotal{}}, nil
	}
	return f.aging, nil
}

func (fakeStatements) LocalDate(_ context.Context, _ uuid.UUID, at time.Time) (time.Time, error) {
	return at, nil
}
func (fakeStatements) Now() time.Time { return time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC) }

func (f fakeStatements) Statement(context.Context, account.StatementQuery) (*account.Statement, error) {
	return f.st, nil
}

var (
	yardNorth = uuid.New()
	yardSouth = uuid.New()
)

func brandedService(t *testing.T) *Service {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		img.Set(x, x, color.RGBA{R: 255, A: 255})
	}
	var logo bytes.Buffer
	if err := png.Encode(&logo, img); err != nil {
		t.Fatal(err)
	}
	logoURL := dealerprofile.LogoPath
	return NewService(newRepo()).
		WithProfile(fakeProfiles{
			profile: &dealerprofile.Profile{
				LegalName: "Test Yard Supply Ltd", TradingName: "Test Yard",
				Phone: "555-0100", Email: "office@example.test", Website: "https://example.test",
				RemitTo: "Remit Dept, PO Box 77, Testtown", LogoURL: &logoURL,
			},
			logo: &dealerprofile.Logo{Bytes: logo.Bytes(), ContentType: "image/png"},
		}).
		WithYards(fakeYards{
			yardNorth: {Name: "North Yard", Address: "1 Alpha Road", City: "Northtown", State: "NT", Zip: "N1N 1N1", Phone: "555-0111"},
			yardSouth: {Name: "South Yard", Address: "2 Beta Street", City: "Southtown", State: "ST", Zip: "S2S 2S2", Phone: "555-0222"},
		})
}

func invoiceAt(branch uuid.UUID) *invoice.Invoice {
	return mkInvoice(5000, func(i *invoice.Invoice) {
		i.BranchID = branch
		i.Lines = []invoice.InvoiceLine{mkLine("2X4-8", "SPF Stud", "2", "PCS", 25000, 5000)}
	})
}

func TestInvoiceShowsDealerAndTheIssuingYard(t *testing.T) {
	svc := brandedService(t)
	cust := &customer.Customer{Name: "Acme", AccountNumber: "C-1"}

	north, err := svc.GenerateInvoicePDF(context.Background(), invoiceAt(yardNorth), cust)
	if err != nil {
		t.Fatal(err)
	}
	south, err := svc.GenerateInvoicePDF(context.Background(), invoiceAt(yardSouth), cust)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Test Yard", "Test Yard Supply Ltd", "555-0100", "North Yard", "1 Alpha Road", "Northtown", "Remit Dept, PO Box 77, Testtown", "/Subtype /Image"} {
		mustContain(t, north, want)
	}
	mustNotContain(t, north, "South Yard")
	mustNotContain(t, north, "2 Beta Street")
	mustContain(t, south, "South Yard")
	mustContain(t, south, "2 Beta Street")
	mustNotContain(t, south, "1 Alpha Road")
}

func TestInvoiceHasNoFixedTitleOrPayLink(t *testing.T) {
	svc := brandedService(t)
	inv := invoiceAt(yardNorth)
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, doc, "GABLE LBM")
	mustNotContain(t, doc, "app.gable.com")
	mustNotContain(t, doc, "PAY ONLINE")
	mustContain(t, doc, "INVOICE")
	mustContain(t, doc, "TOTAL DUE: $50.00")
}

func TestFormsWithoutProfileStillRender(t *testing.T) {
	svc := NewService(newRepo())
	doc, err := svc.GenerateInvoicePDF(context.Background(), invoiceAt(yardNorth), &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustNotContain(t, doc, "GABLE LBM")
}

func TestPickTicketShowsNumberYardJobAndUnits(t *testing.T) {
	job := uuid.New()
	svc := brandedService(t).WithJobs(fakeJobs{job: "Lakeview Patio"})
	qty, _ := httpx.ParseQuantity("12")
	uom := "BF"
	sku := "CDR-5"
	line := order.OrderLine{Line: salesdoc.Line{ID: uuid.New(), LineType: salesdoc.LineProduct, SKU: &sku, Description: "Cedar", Quantity: &qty, UOM: &uom}}
	o := &order.Order{OrderSummary: order.OrderSummary{
		ID: uuid.New(), Number: "SO-000123", BranchID: yardSouth, JobID: &job,
		CreatedAt: httpx.TimestampOf(time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)),
	}, ShipTo: &salesdoc.ShipToSnapshot{Line1: "9 Site Lane", City: "Lakeview", Region: "LV", PostalCode: "L9L 9L9"}, Lines: []order.OrderLine{line}}

	doc, err := svc.GeneratePickTicketPDF(context.Background(), o, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Order #: SO-000123", "South Yard", "2 Beta Street", "Lakeview Patio", "9 Site Lane", "CDR-5 - Cedar", "12 [BF]"} {
		mustContain(t, doc, want)
	}
	mustNotContain(t, doc, "Job: N/A")
	mustNotContain(t, doc, "Remit Dept")
}

func TestPickTicketWithoutAJobSaysSoPlainly(t *testing.T) {
	svc := brandedService(t)
	o := &order.Order{OrderSummary: order.OrderSummary{ID: uuid.New(), Number: "SO-000124", BranchID: yardNorth, CreatedAt: httpx.TimestampOf(time.Now())}}
	doc, err := svc.GeneratePickTicketPDF(context.Background(), o, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "Job: none")
	mustContain(t, doc, "SO-000124")
}

func TestStatementShowsYardRemitToAndTheAccount(t *testing.T) {
	cust := &customer.Customer{Name: "Acme", AccountNumber: "C-9", PrimaryBranchID: yardSouth}
	st := &account.Statement{
		CustomerID: uuid.New(), CustomerName: "Acme", From: "2026-02-01", To: "2026-02-28",
		Currencies: []account.StatementCurrency{{
			Currency: "CAD", OpeningBalanceCents: 1000, ClosingBalanceCents: 6500,
			Lines: []account.StatementLine{
				{Date: "2026-02-10", Type: "INVOICE", Description: "Invoice IN-000007", AmountCents: 5500, BalanceAfterCents: 6500},
				{Date: "2026-02-12", Type: "PAYMENT", Description: "Payment PAY-000001", AmountCents: -2270, BalanceAfterCents: 4230},
			},
			OpenDocuments: []account.OpenDocument{{Kind: "invoice", Number: "IN-000007", Date: "2026-02-10", TotalCents: 5500, OpenCents: 5500}},
		}},
	}
	svc := brandedService(t).WithStatements(fakeStatements{st: st})
	doc, err := svc.GenerateStatementPDF(context.Background(), st, cust)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"STATEMENT", "South Yard", "2 Beta Street", "Remit Dept, PO Box 77, Testtown", "C-9", "2026-02-01", "Invoice IN-000007", "$10.00", "$65.00", "IN-000007", "-$22.70"} {
		mustContain(t, doc, want)
	}
	mustNotContain(t, doc, "$-")
	mustNotContain(t, doc, "North Yard")
}

// The statement carries the account's aging as of the period's last day: the
// buckets the AR Aging page reads, in the statement's own currency.
func TestStatementCarriesTheAgingAsOfThePeriodsEnd(t *testing.T) {
	cust := &customer.Customer{Name: "Acme", AccountNumber: "C-9", PrimaryBranchID: yardSouth}
	st := &account.Statement{
		CustomerID: uuid.New(), CustomerName: "Acme", From: "2026-02-01", To: "2026-02-28",
		Currencies: []account.StatementCurrency{{Currency: "CAD", OpeningBalanceCents: 0, ClosingBalanceCents: 123456,
			Lines: []account.StatementLine{{Date: "2026-02-10", Type: "INVOICE", Description: "Invoice IN-000007", AmountCents: 123456, BalanceAfterCents: 123456}}}},
	}
	aging := &account.AgingSummary{AsOf: "2026-02-28", Basis: "due_date", Totals: []account.AgingTotal{{
		Currency: "CAD", CurrentCents: 100001, Days1To30: 20002, Days31To60: 3003, Days61To90: 400, Over90: 50, TotalCents: 123456}}}
	svc := brandedService(t).WithStatements(fakeStatements{st: st, aging: aging})
	doc, err := svc.GenerateStatementPDF(context.Background(), st, cust)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Aging as of 2026-02-28", "Current", "1-30", "31-60", "61-90", "Over 90", "$1,000.01", "$200.02", "$30.03", "$4.00", "$0.50"} {
		mustContain(t, doc, want)
	}
}

func TestQuoteForm(t *testing.T) {
	svc := brandedService(t)
	qty, _ := httpx.ParseQuantity("3")
	q := &quote.Quote{QuoteSummary: quote.QuoteSummary{
		ID: uuid.New(), Number: "QT-000005", BranchID: yardNorth, TotalCents: 9000,
		CreatedAt: httpx.TimestampOf(time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)),
	}, Lines: []quote.QuoteLine{{SKU: "PT-4", Description: "Post", Quantity: qty, UOM: product.UOM("EA"), PriceUOM: "EA", UnitPrice: httpx.Price(300000), LineTotal: httpx.Cents(9000)}}}
	doc, err := svc.GenerateQuotePDF(context.Background(), q, &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"QUOTE", "QT-000005", "North Yard", "1 Alpha Road", "PT-4 - Post", "$90.00"} {
		mustContain(t, doc, want)
	}
	mustNotContain(t, doc, "Remit Dept")
}

func TestPurchaseOrderForm(t *testing.T) {
	svc := brandedService(t)
	po := &purchase_order.PurchaseOrder{
		ID: uuid.MustParse("5a0fbd2c-1111-4222-8333-444455556666"), Number: "PO-000042", BranchID: yardSouth, VendorName: "Mill Supply Co", Status: "SENT",
		CreatedAt: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC),
		Lines:     []purchase_order.PurchaseOrderLine{{Description: "2x4x8 SPF", Quantity: 100, Cost: 3.5}},
	}
	doc, err := svc.GeneratePurchaseOrderPDF(context.Background(), po)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PURCHASE ORDER", "PO-000042", "Mill Supply Co", "South Yard", "2 Beta Street", "2x4x8 SPF"} {
		mustContain(t, doc, want)
	}
	mustNotContain(t, doc, "PO-5A0FBD2C")

	// A purchase order with no number yet falls back to the id's first block.
	po.Number = ""
	doc, err = svc.GeneratePurchaseOrderPDF(context.Background(), po)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "PO-5A0FBD2C")
}

// A logo already stored past the dimension cap prints without the image
// rather than allocating its pixels on every form.
func TestOversizedStoredLogoPrintsWithoutTheImage(t *testing.T) {
	svc := brandedService(t)
	var huge bytes.Buffer
	if err := png.Encode(&huge, image.NewGray(image.Rect(0, 0, 8000, 8000))); err != nil {
		t.Fatal(err)
	}
	p := svc.profiles.(fakeProfiles)
	p.logo = &dealerprofile.Logo{Bytes: huge.Bytes(), ContentType: "image/png"}
	svc.profiles = p

	doc, err := svc.GenerateInvoicePDF(context.Background(), invoiceAt(yardNorth), &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "Test Yard")
	mustNotContain(t, doc, "/Subtype /Image")
}

// A stored logo the PDF writer cannot embed (16 bit) prints without the
// image rather than failing the form.
func TestUnembeddableStoredLogoPrintsWithoutTheImage(t *testing.T) {
	svc := brandedService(t)
	var deep bytes.Buffer
	if err := png.Encode(&deep, image.NewNRGBA64(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	p := svc.profiles.(fakeProfiles)
	p.logo = &dealerprofile.Logo{Bytes: deep.Bytes(), ContentType: "image/png"}
	svc.profiles = p

	doc, err := svc.GenerateInvoicePDF(context.Background(), invoiceAt(yardNorth), &customer.Customer{Name: "Acme"})
	if err != nil {
		t.Fatalf("the form failed on a logo it cannot embed: %v", err)
	}
	mustContain(t, doc, "Test Yard")
	mustNotContain(t, doc, "/Subtype /Image")
}
