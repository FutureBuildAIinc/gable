// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

// The document a counter sale leaves behind (CP-14).
//
// A sale that took the money leaves a receipt: it is headed RECEIPT, it says
// PAID, it names the tenders that paid it, and it carries neither a total
// due nor a remit to, because there is nothing left to remit. A sale that
// went on account, or one the books have not settled, keeps its invoice.

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// fakeSalePayment is the money a counter sale took, handed to the document
// service in place of the counter.
type fakeSalePayment struct{ pay SalePayment }

func (f fakeSalePayment) InvoicePayment(context.Context, uuid.UUID) (SalePayment, error) {
	return f.pay, nil
}

// paidCashSale is a counter sale that took the money: one cash tender.
func paidCashSale() SalePayment {
	return SalePayment{
		Tenders: []ThermalTender{{Method: "CASH", AmountCents: httpx.Cents(5000), ChangeCents: httpx.Cents(100)}},
	}
}

// settledInvoice is the invoice of a counter sale the books have settled.
func settledInvoice(totalCents int64) *invoice.Invoice {
	return mkInvoice(totalCents, func(i *invoice.Invoice) {
		i.Status = invoice.InvoiceStatusPaid
	})
}

// RULE: the default document of a paid counter cash sale is a receipt: it is
// headed RECEIPT, it says PAID, it names the tenders that paid it and the
// change, and it carries no total due and no remit to.
func TestTheDefaultDocumentOfAPaidCashSaleIsAReceipt(t *testing.T) {
	svc := styledService("en-CA").WithInvoicePayments(fakeSalePayment{pay: paidCashSale()})
	inv := settledInvoice(4900)

	kind := svc.SaleDocumentKind(context.Background(), inv)
	if kind != DocumentReceipt {
		t.Fatalf("the document kind = %v, want the receipt", kind)
	}
	doc, err := svc.GenerateDocumentPDF(context.Background(), inv, &customer.Customer{Name: "Acme"}, kind)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	for _, want := range []string{"RECEIPT", "PAID", "CASH: $50.00", "Change: $1.00", "TOTAL: $49.00"} {
		mustContain(t, doc, want)
	}
	mustNotContain(t, doc, "TOTAL DUE")
	mustNotContain(t, doc, "REMIT TO")
}

// RULE: a sale that went on account keeps its invoice: it is headed INVOICE,
// it asks for the total due and it names the remit to.
func TestAnAccountSaleKeepsItsInvoice(t *testing.T) {
	pay := paidCashSale()
	pay.AccountCents = 4900
	svc := styledService("en-CA").WithInvoicePayments(fakeSalePayment{pay: pay})
	inv := settledInvoice(4900)

	kind := svc.SaleDocumentKind(context.Background(), inv)
	if kind != DocumentInvoice {
		t.Fatalf("the document kind = %v, want the invoice", kind)
	}
	doc, err := svc.GenerateDocumentPDF(context.Background(), inv, &customer.Customer{Name: "Acme"}, kind)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	for _, want := range []string{"INVOICE", "TOTAL DUE: $49.00", "REMIT TO:"} {
		mustContain(t, doc, want)
	}
	mustNotContain(t, doc, "RECEIPT")
}

// RULE: an invoice the books have not settled keeps its invoice, even when
// the sale took the money: an unsettled sale is still owed.
func TestAnUnsettledSaleKeepsItsInvoice(t *testing.T) {
	svc := styledService("en-CA").WithInvoicePayments(fakeSalePayment{pay: paidCashSale()})
	inv := mkInvoice(4900, func(i *invoice.Invoice) { i.Status = invoice.InvoiceStatusUnpaid })

	if kind := svc.SaleDocumentKind(context.Background(), inv); kind != DocumentInvoice {
		t.Fatalf("the document kind = %v, want the invoice", kind)
	}
}

// RULE: an invoice that is not a counter sale at all keeps its invoice: it
// has no tenders, so it is a bill.
func TestAnInvoiceThatIsNotACounterSaleKeepsItsInvoice(t *testing.T) {
	svc := styledService("en-CA").WithInvoicePayments(fakeSalePayment{})
	inv := settledInvoice(4900)

	if kind := svc.SaleDocumentKind(context.Background(), inv); kind != DocumentInvoice {
		t.Fatalf("the document kind = %v, want the invoice", kind)
	}
}

// RULE: a rounded cash sale's invoice and its receipt both carry the
// rounding the branch's rule made, with its direction, and the change that
// was given back. The stored total does not move.
func TestARoundedSaleCarriesTheRoundingAndTheChange(t *testing.T) {
	pay := paidCashSale()
	pay.RoundingCents = httpx.Cents(2)
	pay.ChangeCents = httpx.Cents(100)
	svc := styledService("en-CA").WithInvoicePayments(fakeSalePayment{pay: pay})
	inv := settledInvoice(4900)
	for _, kind := range []DocumentKind{DocumentInvoice, DocumentReceipt} {
		doc, err := svc.GenerateDocumentPDF(context.Background(), inv, &customer.Customer{Name: "Acme"}, kind)
		if err != nil {
			t.Fatal(err)
		}
		assertPDF(t, doc)
		mustContain(t, doc, "Cash rounding +$0.02")
		mustContain(t, doc, "Change: $1.00")
		// the total is the one the sale stored, not the cash the drawer took
		mustContain(t, doc, "$49.00")
	}
}

// RULE: a sale the branch's rule did not touch carries no rounding line.
func TestAnUnroundedSaleCarriesNoRoundingLine(t *testing.T) {
	svc := styledService("en-CA").WithInvoicePayments(fakeSalePayment{pay: SalePayment{
		Tenders: []ThermalTender{{Method: "CASH", AmountCents: httpx.Cents(4900)}},
	}})
	inv := settledInvoice(4900)
	doc, err := svc.GenerateDocumentPDF(context.Background(), inv, &customer.Customer{Name: "Acme"}, DocumentReceipt)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustNotContain(t, doc, "Cash rounding")
	mustContain(t, doc, "CASH: $49.00")
}

// RULE: a round-down names the direction it went: the drawer kept a cent
// less than the sale needed.
func TestARoundDownNamesItsDirection(t *testing.T) {
	pay := paidCashSale()
	pay.RoundingCents = httpx.Cents(-1)
	svc := styledService("en-CA").WithInvoicePayments(fakeSalePayment{pay: pay})
	inv := settledInvoice(4900)
	doc, err := svc.GenerateDocumentPDF(context.Background(), inv, &customer.Customer{Name: "Acme"}, DocumentReceipt)
	if err != nil {
		t.Fatal(err)
	}
	assertPDF(t, doc)
	mustContain(t, doc, "Cash rounding -$0.01")
}
