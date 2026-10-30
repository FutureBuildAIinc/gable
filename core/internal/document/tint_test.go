// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

// The tint block (row CP-16) on the receipt: the colour code and name, the
// base, the container size and the colorant amounts print under the line,
// because the receipt is the only copy of the formula the customer takes
// away. A line with no tint prints no tint block.

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	tint "github.com/gablelbm/gable/internal/tint"
)

// tintedReceipt is one paint line with a tint of two colorants.
func tintedReceipt() ThermalReceipt {
	q1, _ := httpx.ParseQuantity("1")
	r := sampleReceipt()
	r.Lines = []ThermalLine{{
		SKU: "PT-INT", Description: "Interior Eggshell", Quantity: q1, UOM: "GAL",
		UnitPrice: httpx.Price(7499), PriceUOM: "GAL", LineTotal: httpx.Cents(7499),
		Tint: &ThermalTint{
			ColorCode: "HC-188", ColorName: "Essex Green", BaseCode: "DEEP",
			ContainerSizeCode: "GAL", TintChargeCents: httpx.Cents(250),
			Components: []ThermalTintComponent{
				{Code: "B1", Name: "Black", Amount: "1 OZ + 12/32"},
				{Code: "W1", Name: "White", Amount: "2/384"},
			},
		},
	}}
	return r
}

func TestThermalReceiptPrintsTheTintBlock(t *testing.T) {
	svc := brandedReceiptService(t)
	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, tintedReceipt())
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	for _, want := range []string{
		"Tint: Essex Green",
		"HC-188",
		"Base: DEEP GAL",
		"B1 Black  1 OZ + 12/32",
		"W1 White  2/384",
		"includes tint charge $2.50",
	} {
		mustContain(t, doc, want)
	}
}

// A rescaled tint says so, because a rescale is an approximation the counter
// must be able to check.
func TestThermalReceiptMarksARescaledTint(t *testing.T) {
	svc := brandedReceiptService(t)
	r := tintedReceipt()
	r.Lines[0].Tint.Rescaled = true
	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, doc, "Amounts rescaled to this size: check them before mixing")
}

// A receipt for a line with no tint is unchanged: no tint block appears.
func TestThermalReceiptWithoutTintPrintsNoTintBlock(t *testing.T) {
	svc := brandedReceiptService(t)
	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, sampleReceipt())
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustNotContain(t, doc, "Tint:")
	mustNotContain(t, doc, "Tint charge")
}

// The tint block wraps to the thermal width: a long colour name or colorant
// name never runs off the paper.
func TestThermalTintBlockWraps(t *testing.T) {
	svc := brandedReceiptService(t)
	r := tintedReceipt()
	r.Lines[0].Tint.ColorName = "An Extremely Long Colour Name That A Dealer Might Really Have"
	r.Lines[0].Tint.Components = []ThermalTintComponent{{
		Code: "Y3", Name: "Deep Gold With A Very Long Name Indeed For Wrapping",
		Amount: strings.Repeat("9 OZ + ", 12) + "31/32",
	}}
	doc, err := svc.GenerateThermalReceiptPDF(context.Background(), yardNorth, r)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	// the long words are hard broken by the wrapper, so the pieces print
	mustContain(t, doc, "An Extremely Long Colour")
	mustContain(t, doc, "Deep Gold With A Very")
}

// tintedInvoiceLine is one painted invoice line with a tint of two
// colorants.
func tintedInvoiceLine() invoice.InvoiceLine {
	line := mkLine("PT-INT", "Interior Eggshell", "1", "GAL", 749900, 7499)
	line.Tint = &salesdoc.LineTint{
		ColorCode: "HC-188", ColorName: "Essex Green", BaseCode: "DEEP",
		ContainerSizeCode: "GAL",
		TintChargeCents:   &[]httpx.Cents{250}[0],
		Components: []salesdoc.TintComponent{
			{ColorantCode: "B1", ColorantName: "Black",
				Amount: tint.Amount{WholeOZ: 1, Coarse: 12, Denominator: 384}},
			{ColorantCode: "W1", ColorantName: "White",
				Amount: tint.Amount{Fine: 2, Denominator: 384}},
		},
	}
	return line
}

// The invoice prints the same tint block under its line (row CP-16), so the
// customer can reorder the colour from the invoice alone.
func TestInvoicePDFPrintsTheTintBlock(t *testing.T) {
	svc := brandedReceiptService(t)
	line := tintedInvoiceLine()
	inv := mkInvoice(7499, func(i *invoice.Invoice) { i.Lines = []invoice.InvoiceLine{line} })
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme Construction", AccountNumber: "C-1001"})
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	for _, want := range []string{
		"Tint: Essex Green",
		"base DEEP  size GAL",
		"B1 Black  1 OZ + 12/32",
		"W1 White  2/384",
		"includes tint charge $2.50",
	} {
		mustContain(t, doc, want)
	}
	// a line without a tint prints no block
	plain := mkLine("PT-4", "Post", "1", "EA", 3000000, 30000)
	inv2 := mkInvoice(30000, func(i *invoice.Invoice) { i.Lines = []invoice.InvoiceLine{plain} })
	doc2, err := svc.GenerateInvoicePDF(context.Background(), inv2, &customer.Customer{Name: "Acme Construction", AccountNumber: "C-1001"})
	if err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, doc2, "Tint:")
}

// RULE: the tint charge sits inside the unit price, so the line says so: it
// names the charge as part of the price, not as a second charge to pay.
func TestTheTintChargeSaysItIsInsideTheUnitPrice(t *testing.T) {
	svc := brandedReceiptService(t)
	line := tintedInvoiceLine()
	inv := mkInvoice(7499, func(i *invoice.Invoice) { i.Lines = []invoice.InvoiceLine{line} })
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme Construction"})
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "includes tint charge $2.50")
	// the old wording read as a charge to pay on top of the price
	mustNotContain(t, doc, "Tint charge:")
}

// RULE: a discounted line says the charge it names is the undiscounted one:
// the discount came off the very price the charge sat in, so the charge as
// paid is smaller.
func TestADiscountedLineSaysTheTintChargeIsTheUndiscountedOne(t *testing.T) {
	svc := brandedReceiptService(t)
	line := tintedInvoiceLine()
	ten, err := httpx.ParseQuantity("10")
	if err != nil {
		t.Fatal(err)
	}
	line.DiscountPercent = &ten
	inv := mkInvoice(6749, func(i *invoice.Invoice) { i.Lines = []invoice.InvoiceLine{line} })
	doc, err := svc.GenerateInvoicePDF(context.Background(), inv, &customer.Customer{Name: "Acme Construction"})
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "includes tint charge $2.50 before discount")
}
