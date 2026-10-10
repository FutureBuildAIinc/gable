// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// InvoiceStatus is the invoice lifecycle in its storage vocabulary (ADR 0005
// section 6.2): UPPERCASE in the database CHECK, lowercase on the wire.
// OVERDUE is not a status: it is the computed is_overdue flag and the
// overdue list filter.
type InvoiceStatus string

const (
	InvoiceStatusUnpaid     InvoiceStatus = "UNPAID"
	InvoiceStatusPartial    InvoiceStatus = "PARTIAL"
	InvoiceStatusPaid       InvoiceStatus = "PAID"
	InvoiceStatusVoid       InvoiceStatus = "VOID"
	InvoiceStatusWrittenOff InvoiceStatus = "WRITTEN_OFF"
)

var invoiceStatusNames = []string{"unpaid", "partial", "paid", "void", "written_off"}

// Status is the wire name.
func (s InvoiceStatus) Status() string { return strings.ToLower(string(s)) }

// MarshalText writes the lowercase wire name.
func (s InvoiceStatus) MarshalText() ([]byte, error) { return []byte(s.Status()), nil }

// ParseStatus maps a lowercase wire name to its status. Any other spelling,
// the legacy uppercase and overdue included, is not a status.
func ParseStatus(name string) (InvoiceStatus, bool) {
	for _, n := range invoiceStatusNames {
		if name == n {
			return InvoiceStatus(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// open reports whether the status counts in a customer's open receivable.
func (s InvoiceStatus) open() bool { return s == InvoiceStatusUnpaid || s == InvoiceStatusPartial }

// OpenInvoiceStatuses is the canonical set of invoice statuses that contribute
// to a customer's outstanding AR balance (ADR 0005 5.3: UNPAID and PARTIAL;
// OVERDUE is gone from storage). The portal, dashboard, reporting and credit
// surfaces read the same set.
const OpenInvoiceStatuses = "'UNPAID','PARTIAL'"

// DeliveryType and Origin are the lowercase wire spellings of the invoice's
// fulfilment and source.
type DeliveryType string

func (d DeliveryType) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(d))), nil }

type Origin string

func (o Origin) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(o))), nil }

// InvoiceSummary is the head of an invoice: a list item, and the summary of
// the full document. Money is integer cents; optional fields are present as
// null (ADR 0001 sections 7 and 12).
type InvoiceSummary struct {
	ID           uuid.UUID     `json:"id"`
	Number       string        `json:"number"`
	BranchID     uuid.UUID     `json:"branch_id"`
	CustomerID   uuid.UUID     `json:"customer_id"`
	CustomerName string        `json:"customer_name"`
	OrderID      *uuid.UUID    `json:"order_id"`
	JobID        *uuid.UUID    `json:"job_id"`
	ShipToID     *uuid.UUID    `json:"ship_to_id"`
	Status       InvoiceStatus `json:"status"`
	Revision     int64         `json:"revision"`
	Currency     string        `json:"currency"`
	Origin       Origin        `json:"origin"`

	DeliveryType DeliveryType `json:"delivery_type"`
	PickedUpBy   *string      `json:"picked_up_by"`
	DeliveryID   *uuid.UUID   `json:"delivery_id"`

	InvoiceDate     string          `json:"invoice_date"` // YYYY-MM-DD, the branch's local date
	DueDate         *string         `json:"due_date"`
	PaymentTermsID  uuid.UUID       `json:"payment_terms_id"`
	DiscountDueDate *string         `json:"discount_due_date"`
	DiscountPercent *httpx.Quantity `json:"discount_percent"`

	SubtotalCents  httpx.Cents        `json:"subtotal_cents"`
	TaxCents       httpx.Cents        `json:"tax_cents"`
	TaxRatePercent *string            `json:"tax_rate_percent"` // "8.875", null when the provider answered
	TaxExempt      bool               `json:"tax_exempt"`
	TaxSource      salesdoc.TaxSource `json:"tax_source"`
	TotalCents     httpx.Cents        `json:"total_cents"`
	// OpenCents is what is still owed: the total less the payments recorded
	// against the invoice while it is unpaid or partial, zero when it is
	// paid, written off or void. Until C2-4 stores amount_open this is read
	// from the documents, never a stale snapshot.
	OpenCents httpx.Cents `json:"open_cents"`
	IsOverdue bool        `json:"is_overdue"`

	PaidAt     *httpx.Timestamp `json:"paid_at"`
	GLEntryID  *uuid.UUID       `json:"gl_entry_id"`
	VoidedAt   *httpx.Timestamp `json:"voided_at"`
	VoidedBy   *string          `json:"voided_by"`
	VoidReason *string          `json:"void_reason"`
	CreatedAt  httpx.Timestamp  `json:"created_at"`
	UpdatedAt  httpx.Timestamp  `json:"updated_at"`
}

// Invoice is the full document: the summary, the ship-to captured at confirm
// and the lines (IN-1.8).
type Invoice struct {
	InvoiceSummary

	ShipTo *salesdoc.ShipToSnapshot `json:"ship_to"`
	Lines  []InvoiceLine            `json:"lines"`
}

// InvoiceLine is the shared sales line shape (salesdoc.Line, ADR 0005 2.2)
// with the invoice's own columns: the order line it bills and the cost that
// left (8.4), shown to the roles that see margin, which are the roles that
// reach these routes.
type InvoiceLine struct {
	salesdoc.Line

	OrderLineID *uuid.UUID   `json:"order_line_id"`
	UnitCost    *httpx.Price `json:"unit_cost_ten_thousandths"`
	CostCents   httpx.Cents  `json:"cost_cents"`
}

// CreditStatus is the credit memo lifecycle in its storage vocabulary (ADR
// 0005 section 6.3).
type CreditStatus string

const (
	CreditDraft   CreditStatus = "DRAFT"
	CreditOpen    CreditStatus = "OPEN"
	CreditPartial CreditStatus = "PARTIAL"
	CreditApplied CreditStatus = "APPLIED"
	CreditVoid    CreditStatus = "VOID"
)

var creditStatusNames = []string{"draft", "open", "partial", "applied", "void"}

// Status is the wire name.
func (s CreditStatus) Status() string { return strings.ToLower(string(s)) }

// MarshalText writes the lowercase wire name.
func (s CreditStatus) MarshalText() ([]byte, error) { return []byte(s.Status()), nil }

// ParseCreditStatus maps a lowercase wire name to its status.
func ParseCreditStatus(name string) (CreditStatus, bool) {
	for _, n := range creditStatusNames {
		if name == n {
			return CreditStatus(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// ReasonCode is why a credit memo exists (ADR 0005 section 6.3).
type ReasonCode string

const (
	ReasonReturn          ReasonCode = "RETURN"
	ReasonPriceAdjustment ReasonCode = "PRICE_ADJUSTMENT"
	ReasonDamage          ReasonCode = "DAMAGE"
	ReasonOther           ReasonCode = "OTHER"
)

var reasonCodeNames = []string{"return", "price_adjustment", "damage", "other"}

// MarshalText writes the lowercase wire name.
func (r ReasonCode) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(r))), nil }

// ParseReasonCode maps a lowercase wire name.
func ParseReasonCode(name string) (ReasonCode, bool) {
	for _, n := range reasonCodeNames {
		if name == n {
			return ReasonCode(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// CreditMemoSummary is the head of a credit memo: a list item, and the
// summary of the full document. Lines, totals and the open amount are
// negative (ADR 0001 section 7a: a plain sum is the AR effect). The number is
// null while the memo is a draft: it is minted at post.
type CreditMemoSummary struct {
	ID           uuid.UUID    `json:"id"`
	Number       *string      `json:"number"`
	BranchID     uuid.UUID    `json:"branch_id"`
	CustomerID   uuid.UUID    `json:"customer_id"`
	CustomerName string       `json:"customer_name"`
	InvoiceID    *uuid.UUID   `json:"invoice_id"`
	PosReturnID  *uuid.UUID   `json:"pos_return_id"`
	JobID        *uuid.UUID   `json:"job_id"`
	ShipToID     *uuid.UUID   `json:"ship_to_id"`
	Status       CreditStatus `json:"status"`
	Revision     int64        `json:"revision"`
	Currency     string       `json:"currency"`
	ReasonCode   ReasonCode   `json:"reason_code"`
	Reason       string       `json:"reason"`

	SubtotalCents  httpx.Cents `json:"subtotal_cents"`
	TaxCents       httpx.Cents `json:"tax_cents"`
	TaxRatePercent *string     `json:"tax_rate_percent"`
	TotalCents     httpx.Cents `json:"total_cents"`
	// OpenCents is the credit not yet used: the total while the memo is open
	// (or partial or applied, until C2-4 stores amount_open and its
	// applications), zero for a draft or a void memo.
	OpenCents httpx.Cents `json:"open_cents"`

	GLEntryID  *uuid.UUID       `json:"gl_entry_id"`
	MemoDate   string           `json:"memo_date"` // YYYY-MM-DD
	VoidedAt   *httpx.Timestamp `json:"voided_at"`
	VoidedBy   *string          `json:"voided_by"`
	VoidReason *string          `json:"void_reason"`
	CreatedAt  httpx.Timestamp  `json:"created_at"`
	UpdatedAt  httpx.Timestamp  `json:"updated_at"`
}

// CreditMemo is the full document: the summary and its lines.
type CreditMemo struct {
	CreditMemoSummary

	Lines []CreditLine `json:"lines"`
}

// CreditLine is a credit memo line: the shared line shape with negative
// quantity and extension, the invoice line it credits, whether it returns
// the goods to stock and the cost that comes back (negative: ADR 0005 8.4).
type CreditLine struct {
	salesdoc.Line

	InvoiceLineID *uuid.UUID   `json:"invoice_line_id"`
	Restock       bool         `json:"restock"`
	UnitCost      *httpx.Price `json:"unit_cost_ten_thousandths"`
	CostCents     httpx.Cents  `json:"cost_cents"`
}

// Event types the module writes to the outbox (ADR 0005 section 12).
const (
	EventInvoiceCreated = "invoice.created"
	EventInvoiceVoided  = "invoice.voided"

	EventCreditCreated = "credit_memo.created"
	EventCreditUpdated = "credit_memo.updated"
	EventCreditPosted  = "credit_memo.posted"
	EventCreditVoided  = "credit_memo.voided"
)

// ---------------------------------------------------------------------------
// The legacy writers: the counter's account charge (and tests) still hand the
// invoice module whole cents per sale unit and float quantities. C2-5 retires
// this path; it lands in the shared line shape in the repository.
// ---------------------------------------------------------------------------

// LegacyInvoice is the invoice the counter's account charge creates through
// CreateInvoice. Money is whole cents; the terms are the customer's, by id.
type LegacyInvoice struct {
	ID          uuid.UUID
	BranchID    uuid.UUID
	OrderID     uuid.UUID
	CustomerID  uuid.UUID
	Status      InvoiceStatus
	Subtotal    int64   // cents
	TaxRate     float64 // e.g. 0.0825 for 8.25%
	TaxAmount   int64   // cents
	TotalAmount int64   // cents (subtotal + tax)
	DueDate     *time.Time
	PaidAt      *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Lines       []LegacyLine
}

// LegacyLine is one line of a LegacyInvoice.
type LegacyLine struct {
	ID        uuid.UUID
	InvoiceID uuid.UUID
	ProductID uuid.UUID
	Quantity  float64
	PriceEach int64 // cents
	CreatedAt time.Time
}
