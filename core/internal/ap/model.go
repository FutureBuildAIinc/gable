// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap

import (
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Status is the vendor invoice lifecycle on the wire: lowercase snake_case
// (ADR 0001 section 6). The storage vocabulary stays uppercase; the mapping
// happens at this boundary.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusPartial  Status = "partial"
	StatusPaid     Status = "paid"
	StatusVoided   Status = "voided"
)

// ParseStatus accepts only the lowercase spelling; anything else is not a
// status this module knows.
func ParseStatus(raw string) (Status, bool) {
	switch Status(raw) {
	case StatusPending, StatusApproved, StatusPartial, StatusPaid, StatusVoided:
		return Status(raw), true
	}
	return "", false
}

// storageStatus maps a wire status to the column's uppercase vocabulary.
func storageStatus(s Status) string { return strings.ToUpper(string(s)) }

// Status renders the status for a message.
func (s Status) Status() string { return string(s) }

// wireStatus maps a stored status to the wire's lowercase one; an unknown
// stored value (a row written by hand) reads as pending.
func wireStatus(stored string) Status {
	if s, ok := ParseStatus(strings.ToLower(stored)); ok {
		return s
	}
	return StatusPending
}

// transitions is the lifecycle: which edge each status allows.
var transitions = map[Status][]Status{
	StatusPending:  {StatusApproved, StatusVoided},
	StatusApproved: {StatusVoided},
	// partial and paid carry payments; a partial bill cannot be voided while
	// money has left (the transition's guard refuses it), and a paid one is
	// closed.
	StatusPartial: {},
	StatusPaid:    {},
	StatusVoided:  {},
}

func (s Status) allows(to Status) bool {
	for _, t := range transitions[s] {
		if t == to {
			return true
		}
	}
	return false
}

// PaymentMethod for AP payments to vendors. The payment routes keep their
// uppercase wire vocabulary until their own conversion; this is the storage
// set.
type PaymentMethod string

const (
	PaymentMethodCheck PaymentMethod = "CHECK"
	PaymentMethodACH   PaymentMethod = "ACH"
	PaymentMethodWire  PaymentMethod = "WIRE"
)

// Summary is one vendor invoice's header, the shape of both the single read
// and the list item (ADR 0001: the list item is the smaller struct the full
// document embeds, so the two cannot drift).
type Summary struct {
	ID                  uuid.UUID        `json:"id"`
	Number              string           `json:"number"`
	VendorID            uuid.UUID        `json:"vendor_id"`
	VendorName          string           `json:"vendor_name"`
	BranchID            uuid.UUID        `json:"branch_id"`
	VendorInvoiceNumber string           `json:"vendor_invoice_number"`
	Currency            string           `json:"currency"`
	InvoiceDate         string           `json:"invoice_date"` // YYYY-MM-DD
	DueDate             string           `json:"due_date"`     // YYYY-MM-DD
	POID                *uuid.UUID       `json:"po_id"`
	SubtotalCents       httpx.Cents      `json:"subtotal_cents"`
	TaxCents            httpx.Cents      `json:"tax_cents"`
	TotalCents          httpx.Cents      `json:"total_cents"`
	AmountPaidCents     httpx.Cents      `json:"amount_paid_cents"`
	AmountOpenCents     httpx.Cents      `json:"amount_open_cents"`
	Status              Status           `json:"status"`
	ApprovedBy          *uuid.UUID       `json:"approved_by"`
	ApprovedAt          *httpx.Timestamp `json:"approved_at"`
	Notes               string           `json:"notes"`
	Revision            int64            `json:"revision"`
	GLEntryID           *uuid.UUID       `json:"gl_entry_id"`
	CreatedAt           httpx.Timestamp  `json:"created_at"`
}

// Invoice is a vendor bill: the summary plus its lines. Gable's own number is
// number (AP-, gapped sequence); the vendor's own number is
// vendor_invoice_number (ADR 0008 7.4).
type Invoice struct {
	Summary
	Lines []InvoiceLine `json:"lines"`
}

// InvoiceLine is one line of a vendor bill. The extension is priced through
// httpx.Extend with the pair 1 and 1: AP lines carry no conversion pair
// until the purchase line link carries units (ADR 0008 sections 1 and 7.3).
type InvoiceLine struct {
	ID                      uuid.UUID       `json:"id"`
	Position                int             `json:"position"`
	Description             string          `json:"description"`
	Quantity                httpx.Quantity  `json:"quantity"`
	UnitPriceTenThousandths httpx.Price     `json:"unit_price_ten_thousandths"`
	LineTotalCents          httpx.Cents     `json:"line_total_cents"`
	GLAccountID             *uuid.UUID      `json:"gl_account_id"`
	PurchaseOrderLineID     *uuid.UUID      `json:"purchase_order_line_id"`
	ProductID               *uuid.UUID      `json:"product_id"`
	POFreightChargeID       *uuid.UUID      `json:"po_freight_charge_id"`
	CreatedAt               httpx.Timestamp `json:"created_at"`
}

// APPayment is a payment made to a vendor. The payment routes keep their
// shape until their own conversion (ADR 0008 section 11); amount is cents.
type APPayment struct {
	ID          uuid.UUID     `json:"id" db:"id"`
	VendorID    uuid.UUID     `json:"vendor_id" db:"vendor_id"`
	VendorName  string        `json:"vendor_name,omitempty" db:"vendor_name"`
	BatchID     *uuid.UUID    `json:"batch_id,omitempty" db:"batch_id"`
	Amount      int64         `json:"amount" db:"amount"` // Cents
	Method      PaymentMethod `json:"method" db:"method"`
	CheckNumber string        `json:"check_number,omitempty" db:"check_number"`
	Reference   string        `json:"reference,omitempty" db:"reference"`
	PaymentDate string        `json:"payment_date" db:"payment_date"` // YYYY-MM-DD
	Status      string        `json:"status" db:"status"`
	CreatedAt   time.Time     `json:"created_at" db:"created_at"`
}

// APPaymentApplication links a payment to specific vendor invoices.
type APPaymentApplication struct {
	ID        uuid.UUID `json:"id" db:"id"`
	PaymentID uuid.UUID `json:"payment_id" db:"payment_id"`
	InvoiceID uuid.UUID `json:"invoice_id" db:"invoice_id"`
	Amount    int64     `json:"amount" db:"amount"` // Cents
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

// APAgingSummary represents aging buckets for a vendor.
type APAgingSummary struct {
	VendorID   uuid.UUID `json:"vendor_id"`
	VendorName string    `json:"vendor_name"`
	Current    int64     `json:"current"` // Cents
	Past30     int64     `json:"past_30"` // Cents
	Past60     int64     `json:"past_60"` // Cents
	Past90     int64     `json:"past_90"` // Cents
	Total      int64     `json:"total"`   // Cents
}
