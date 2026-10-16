// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/google/uuid"
)

// The invoice and the quote print the unit price the same way, with the line
// total (exact cents) untouched beside it.
func TestInvoiceAndQuotePrintUnitPricesInTwoPlaces(t *testing.T) {
	svc := styledService("en-US")
	cust := &customer.Customer{Name: "Acme"}
	prices := []struct {
		price httpx.Price
		total httpx.Cents
		unit  string
		line  string
	}{
		{60000, 600, "$6.00/PCS", "$6.00"},
		{14025, 1403, "$1.4025/PCS", "$14.03"},
		{12345678, 123457, "$1,234.5678/PCS", "$1,234.57"},
	}
	for _, p := range prices {
		inv := mkInvoice(int64(p.total), func(i *invoice.Invoice) {
			i.Lines = []invoice.InvoiceLine{mkLine("A", "Item", "1", "PCS", int64(p.price), int64(p.total))}
		})
		doc, err := svc.GenerateInvoicePDF(context.Background(), inv, cust)
		if err != nil {
			t.Fatal(err)
		}
		mustContain(t, doc, p.unit)
		mustNotContain(t, doc, p.unit+"0") // never a padded fifth look
		if p.price%100 == 0 {
			mustNotContain(t, doc, "$"+itoa(int64(p.price)/10000)+".0000")
		}

		qty, _ := httpx.ParseQuantity("1")
		q := &quote.Quote{QuoteSummary: quote.QuoteSummary{ID: uuid.New(), Number: "QT-1", TotalCents: p.total},
			Lines: []quote.QuoteLine{{SKU: "A", Description: "Item", Quantity: qty, UOM: product.UOM("PCS"), PriceUOM: "PCS", UnitPrice: p.price, LineTotal: p.total}}}
		qdoc, err := svc.GenerateQuotePDF(context.Background(), q, cust)
		if err != nil {
			t.Fatal(err)
		}
		mustContain(t, qdoc, p.unit)
		mustContain(t, qdoc, p.line)
	}
}
