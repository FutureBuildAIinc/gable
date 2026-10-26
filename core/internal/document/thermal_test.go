// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/platform/httpx"
)

// sampleReceipt builds a plain receipt the tests build on: a counter sale
// with three lines, a cash tender and a card tender, no balance, no
// signature. Callers modify the copy before passing it to the renderer.
func sampleReceipt() ThermalReceipt {
	q2, _ := httpx.ParseQuantity("2")
	q1, _ := httpx.ParseQuantity("1")
	q5, _ := httpx.ParseQuantity("5")
	return ThermalReceipt{
		SaleNumber:    "SO-000099",
		SoldAt:        time.Date(2026, 6, 30, 14, 5, 0, 0, time.UTC),
		Cashier:       "Avi",
		Customer:      "Acme Building Co",
		AccountNumber: "C-77",
		PONumber:      "PO-9000",
		JobName:       "Lakeview Patio",
		Lines: []ThermalLine{
			{SKU: "2X4-8", Description: "SPF Stud", Quantity: q2, UOM: "PCS", UnitPrice: httpx.Price(25000), PriceUOM: "PCS", LineTotal: httpx.Cents(5000)},
			{SKU: "PT-4", Description: "Post", Quantity: q1, UOM: "EA", UnitPrice: httpx.Price(300000), PriceUOM: "EA", LineTotal: httpx.Cents(30000)},
			{SKU: "NAIL-16", Description: "Galv Nail 16d", Quantity: q5, UOM: "KG", UnitPrice: httpx.Price(120000), PriceUOM: "KG", LineTotal: httpx.Cents(60000)},
		},
		SubtotalCents: httpx.Cents(95000),
		TaxCents:      httpx.Cents(12350),
		TaxLabel:      "HST",
		TotalCents:    httpx.Cents(107350),
		Tenders: []ThermalTender{
			{Method: "CASH", AmountCents: httpx.Cents(50000), ChangeCents: httpx.Cents(0)},
			{Method: "CARD", AmountCents: httpx.Cents(60000), LastFour: "4242", ChangeCents: httpx.Cents(0)},
		},
		BalanceCents: httpx.Cents(0),
	}
}

// brandedReceiptService builds a service like the other form tests, with a
// profile and yard so the receipt carries the dealer lockup.
func brandedReceiptService(t *testing.T) *Service {
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
		})
}

// A sale with three lines and two tenders renders and the text holds each
// line, the totals and the tenders. The receipt is a real PDF.
func TestThermalReceiptRendersThreeLinesAndTwoTenders(t *testing.T) {
	svc := brandedReceiptService(t)
	r := sampleReceipt()

	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	for _, want := range []string{
		"SO-000099",
		"Test Yard",
		"555-0100",
		"North Yard",
		"1 Alpha Road",
		"Acme Building Co",
		"C-77",
		"PO: PO-9000",
		"Job: Lakeview Patio",
		"2X4-8 - SPF Stud",
		"PT-4 - Post",
		"NAIL-16 - Galv Nail 16d",
		"Subtotal: $950.00",
		"HST: $123.50",
		"Total: $1,073.50",
		"CASH",
		"CARD  ****4242",
		"$500.00",
		"$600.00",
	} {
		mustContain(t, doc, want)
	}
	// No signature block when the sale is settled in full and no signature
	// was captured.
	mustNotContain(t, doc, "Signed by")
	mustNotContain(t, doc, "Balance due")
}

// A reprint shows the REPRINT banner and the print number; a first print
// shows neither.
func TestThermalReceiptReprintBanner(t *testing.T) {
	svc := brandedReceiptService(t)
	r := sampleReceipt()

	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustNotContain(t, doc, "REPRINT")

	r.Reprint = &ThermalReprint{PrintNumber: 2}
	doc, err = svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "REPRINT")
	mustContain(t, doc, "#2")
}

// A signature renders its name and the seal's first twelve hex characters.
// The signature PNG, when carried, is embedded as an image.
func TestThermalReceiptSignatureRendersNameAndSeal(t *testing.T) {
	svc := brandedReceiptService(t)
	r := sampleReceipt()
	r.Signature = &ThermalSignature{
		Name:       "Jane Doe",
		SignedAt:   time.Date(2026, 6, 30, 14, 5, 0, 0, time.UTC),
		SealPrefix: "abcdef123456",
	}

	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "Signed by Jane Doe")
	mustContain(t, doc, "sealed abcdef123456")
}

// A signature PNG carries through to the PDF.
func TestThermalReceiptSignaturePNGIsEmbedded(t *testing.T) {
	svc := brandedReceiptService(t)
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		img.Set(x, x, color.RGBA{R: 0, A: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	r := sampleReceipt()
	r.Signature = &ThermalSignature{
		Name:       "Jane Doe",
		SignedAt:   time.Date(2026, 6, 30, 14, 5, 0, 0, time.UTC),
		SealPrefix: "abcdef123456",
		PNG:        buf.Bytes(),
	}

	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "/Subtype /Image")
}

// A long description wraps rather than overflowing the printable width.
// The wrap helper splits at word boundaries.
func TestThermalReceiptWrapsLongDescription(t *testing.T) {
	lines := wrapForWidth("Red cedar 4x4 post, sanded, kiln dried, S4S, suitable for outdoor deck framing", 32)
	if len(lines) < 2 {
		t.Fatalf("a 75-char string at 32 chars per line should split into multiple lines, got %d", len(lines))
	}
	for _, l := range lines {
		if len(l) > 32 {
			t.Errorf("wrapped line %q exceeds the budget of 32 characters", l)
		}
	}
	// A short string stays on one line.
	if got := wrapForWidth("Cedar", 50); len(got) != 1 || got[0] != "Cedar" {
		t.Errorf("a short string should stay on one line, got %v", got)
	}
	// An empty string returns one empty element, never zero, so the row
	// builder does not have to special case it.
	if got := wrapForWidth("", 32); len(got) != 1 {
		t.Errorf("an empty string should return a single empty element, got %v", got)
	}
}

// Amounts with grouping render through the package's formatter: digit groups
// of three, two decimal places, the currency sign after the minus.
func TestThermalReceiptRendersGroupedAmounts(t *testing.T) {
	svc := brandedReceiptService(t)
	r := sampleReceipt()
	r.SubtotalCents = httpx.Cents(1284500)
	r.TaxCents = httpx.Cents(167000)
	r.TotalCents = httpx.Cents(1451500)
	r.Lines = []ThermalLine{
		{SKU: "PALLET", Description: "Cedar pallet", Quantity: httpx.Quantity(1), UOM: "EA", UnitPrice: httpx.Price(12845001), PriceUOM: "EA", LineTotal: httpx.Cents(1284500)},
	}
	r.Tenders = []ThermalTender{{Method: "CARD", AmountCents: httpx.Cents(1451500), LastFour: "1111"}}

	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	for _, want := range []string{
		"Subtotal: $12,845.00",
		"HST: $1,670.00",
		"Total: $14,515.00",
		"$14,515.00",
		"$1,284.5001",
	} {
		mustContain(t, doc, want)
	}
}

// An on-account sale draws the balance due and, with no captured signature,
// a blank signature line.
func TestThermalReceiptOnAccountDrawsBalanceAndBlankSignature(t *testing.T) {
	svc := brandedReceiptService(t)
	r := sampleReceipt()
	r.BalanceCents = httpx.Cents(25000)
	r.Tenders = nil

	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "Balance due: $250.00")
	mustContain(t, doc, "Signature:")
}

// The receipt renders even without a profile or a yard, the way every other
// form does.
func TestThermalReceiptWithoutBrandingStillRenders(t *testing.T) {
	svc := NewService(newRepo())
	r := sampleReceipt()
	r.Customer = ""
	r.AccountNumber = ""

	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "SO-000099")
	mustContain(t, doc, "Walk-in")
	mustNotContain(t, doc, "Test Yard")
}
