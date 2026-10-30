// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

// CP-14 A1 on paper. A Canadian invoice and 80mm receipt print GST and PST as
// their own lines from the parts the sale froze (BC's PSTR s. 85(1)), at any
// total, and the dealer's GST/HST number under the dealer block; a plain
// notice stands in for a number not on file (item 364), and a dealer marked
// not registered prints none. A US document prints its one tax line and no
// registration line, as before.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/google/uuid"
)

func bcParts(t *testing.T, gst, pst int64) []byte {
	t.Helper()
	raw, err := json.Marshal(&taxzone.Snapshot{ZoneID: uuid.New(), CombinedRate: "0.120000", Country: "CA", Province: "BC",
		Parts: []taxzone.Part{{Name: taxzone.PartGoodsTax, Rate: "0.050000", Cents: &gst},
			{Name: taxzone.PartProvincialSales, Rate: "0.070000", Cents: &pst}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func taxService(country, number string, notRegistered bool) (*Service, uuid.UUID) {
	branch := uuid.New()
	return NewService(newRepo()).
		WithProfile(fakeProfiles{profile: &dealerprofile.Profile{LegalName: "Test Yard Supply Ltd", TradingName: "Test Yard",
			GSTHSTNumber: number, GSTHSTNotRegistered: notRegistered}}).
		WithYards(fakeYards{branch: {Name: "Main Yard", Address: "1 Alpha Road", City: "Kelowna", State: "BC", Zip: "V1X 7K2", Country: country}}), branch
}

func bcInvoice(t *testing.T, branch uuid.UUID, subtotal, gst, pst int64) *invoice.Invoice {
	pct := "12"
	return mkInvoice(subtotal+gst+pst, func(inv *invoice.Invoice) {
		inv.BranchID = branch
		inv.SubtotalCents = httpx.Cents(subtotal)
		inv.TaxCents = httpx.Cents(gst + pst)
		inv.TaxRatePercent = &pct
		inv.TaxRateParts = bcParts(t, gst, pst)
		inv.Lines = []invoice.InvoiceLine{mkLine("2X4-8", "SPF Stud", "6", "PCS", 55000, subtotal)}
	})
}

var cust = &customer.Customer{Name: "Acme Construction", AccountNumber: "C-1001"}

func TestCanadianInvoicePrintsGSTAndPSTAndTheNumber(t *testing.T) {
	svc, branch := taxService("CA", "123456789RT0001", false)
	doc, err := svc.GenerateInvoicePDF(context.Background(), bcInvoice(t, branch, 3300, 165, 231), cust)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "GST 5%: $1.65")
	mustContain(t, doc, "PST 7%: $2.31")
	mustContain(t, doc, "GST/HST No. 123456789 RT0001")
	mustNotContain(t, doc, "TAX @ 12%")
}

func TestCanadianSaleUnderOneHundredDollarsStillPrintsBoth(t *testing.T) {
	// $12.50: the simplification of item 363 prints GST and the number at
	// every total, not only from $100.
	svc, branch := taxService("CA", "123456789RT0001", false)
	doc, err := svc.GenerateInvoicePDF(context.Background(), bcInvoice(t, branch, 1250, 63, 88), cust)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "GST 5%: $0.63")
	mustContain(t, doc, "PST 7%: $0.88")
	mustContain(t, doc, "GST/HST No. 123456789 RT0001")
}

func TestCanadianInvoiceWithNoNumberPrintsTheNotice(t *testing.T) {
	svc, branch := taxService("CA", "", false)
	doc, err := svc.GenerateInvoicePDF(context.Background(), bcInvoice(t, branch, 3300, 165, 231), cust)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "GST/HST No.: not on file")
}

func TestNotRegisteredDealerPrintsNoGST(t *testing.T) {
	// The switch on: the sale froze only the PST part, and no number line.
	svc, branch := taxService("CA", "", true)
	pst := int64(231)
	raw, _ := json.Marshal(&taxzone.Snapshot{ZoneID: uuid.New(), CombinedRate: "0.070000", Country: "CA", Province: "BC",
		Parts: []taxzone.Part{{Name: taxzone.PartProvincialSales, Rate: "0.070000", Cents: &pst}}})
	inv := bcInvoice(t, branch, 3300, 0, 231)
	inv.TaxRateParts = raw
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, cust)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "PST 7%: $2.31")
	mustNotContain(t, doc, "GST")
}

func TestUSInvoiceKeepsItsOneTaxLine(t *testing.T) {
	svc, branch := taxService("US", "123456789RT0001", false)
	pct := "8.875"
	inv := mkInvoice(3593, func(inv *invoice.Invoice) {
		inv.BranchID = branch
		inv.SubtotalCents, inv.TaxCents, inv.TaxRatePercent = 3300, 293, &pct
	})
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, cust)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "TAX @ 8.875%: $2.93")
	mustNotContain(t, doc, "GST")
}

func TestCanadianThermalPrintsBothLinesAndTheNumber(t *testing.T) {
	svc, branch := taxService("CA", "123456789RT0001", false)
	r := sampleReceipt()
	r.TaxLabel = ""
	r.TaxLines = taxzone.Lines(bcParts(t, 4750, 6650))
	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), branch, r)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "GST 5%: $47.50")
	mustContain(t, doc, "PST 7%: $66.50")
	mustContain(t, doc, "GST/HST No. 123456789 RT0001")
	mustNotContain(t, doc, "Tax: ")
}

func TestUSThermalKeepsItsOneTaxLine(t *testing.T) {
	svc, branch := taxService("US", "123456789RT0001", false)
	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), branch, sampleReceipt())
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "HST: $123.50")
	mustNotContain(t, doc, "GST/HST No.")
}
