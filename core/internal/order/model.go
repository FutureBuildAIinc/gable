// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// OrderStatus is the order lifecycle in its storage vocabulary (ADR 0005
// section 5.2): UPPERCASE in the database CHECK, lowercase on the wire.
type OrderStatus string

const (
	StatusDraft       OrderStatus = "DRAFT"
	StatusOnHold      OrderStatus = "ON_HOLD"
	StatusConfirmed   OrderStatus = "CONFIRMED"
	StatusBackordered OrderStatus = "BACKORDERED"
	StatusFulfilled   OrderStatus = "FULFILLED"
	StatusCancelled   OrderStatus = "CANCELLED"
)

var statusNames = []string{"draft", "on_hold", "confirmed", "backordered", "fulfilled", "cancelled"}

// Status is the wire name.
func (s OrderStatus) Status() string { return strings.ToLower(string(s)) }

// MarshalText writes the lowercase wire name.
func (s OrderStatus) MarshalText() ([]byte, error) { return []byte(s.Status()), nil }

// ParseStatus maps a lowercase wire name to its status. Any other spelling,
// the legacy uppercase included, is not a status.
func ParseStatus(name string) (OrderStatus, bool) {
	for _, n := range statusNames {
		if name == n {
			return OrderStatus(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// DeliveryType is the order's fulfilment: pickup is will-call (the customer
// collects at the branch, never routed), delivery goes on a truck
// (ADR 0005 section 5.5).
type DeliveryType string

const (
	DeliveryPickup   DeliveryType = "PICKUP"
	DeliveryDelivery DeliveryType = "DELIVERY"
)

// MarshalText writes the lowercase wire name.
func (d DeliveryType) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(d))), nil }

// ParseDeliveryType maps a lowercase wire name.
func ParseDeliveryType(name string) (DeliveryType, bool) {
	switch name {
	case "pickup":
		return DeliveryPickup, true
	case "delivery":
		return DeliveryDelivery, true
	}
	return "", false
}

// HoldReason is why an order sits on hold: the credit limit or a person
// (ADR 0005 section 5.1).
type HoldReason string

const (
	HoldCreditLimit HoldReason = "CREDIT_LIMIT"
	HoldManual      HoldReason = "MANUAL"
)

// MarshalText writes the lowercase wire name.
func (h HoldReason) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(h))), nil }

// ShipToSnapshot is the address captured at confirm (ADR 0005 section 5.1),
// stored as JSONB on the order and read back as the wire's ship_to object.
type ShipToSnapshot struct {
	ID                   uuid.UUID `json:"id"`
	Code                 string    `json:"code"`
	Name                 string    `json:"name"`
	Line1                string    `json:"line1"`
	Line2                *string   `json:"line2"`
	City                 string    `json:"city"`
	Region               string    `json:"region"`
	PostalCode           string    `json:"postal_code"`
	Country              *string   `json:"country"`
	Phone                *string   `json:"phone"`
	DeliveryInstructions *string   `json:"delivery_instructions"`
}

// OrderSummary is the head of an order: a list item, and the summary of the
// full document. Money is integer cents; optional fields are present as
// null (ADR 0001 sections 7 and 12).
type OrderSummary struct {
	ID           uuid.UUID   `json:"id"`
	Number       string      `json:"number"`
	BranchID     uuid.UUID   `json:"branch_id"`
	CustomerID   uuid.UUID   `json:"customer_id"`
	CustomerName string      `json:"customer_name"`
	QuoteID      *uuid.UUID  `json:"quote_id"`
	JobID        *uuid.UUID  `json:"job_id"`
	Status       OrderStatus `json:"status"`
	Revision     int64       `json:"revision"`
	Currency     string      `json:"currency"`

	DeliveryType          DeliveryType `json:"delivery_type"`
	ShipToID              *uuid.UUID   `json:"ship_to_id"`
	CustomerPO            *string      `json:"customer_po"`
	OrderedByContactID    *uuid.UUID   `json:"ordered_by_contact_id"`
	SalespersonID         *uuid.UUID   `json:"salesperson_id"`
	SalespersonName       *string      `json:"salesperson_name"`
	ScheduledDeliveryDate *string      `json:"scheduled_delivery_date"`

	SubtotalCents  httpx.Cents        `json:"subtotal_cents"`
	TaxCents       httpx.Cents        `json:"tax_cents"`
	TaxRatePercent *string            `json:"tax_rate_percent"` // "8.875", null when the provider answered
	TaxExempt      bool               `json:"tax_exempt"`
	TaxSource      salesdoc.TaxSource `json:"tax_source"`
	TotalCents     httpx.Cents        `json:"total_cents"`

	// The margin fields of today, kept: costs read from the products at
	// read time, all cents, the percentage a decimal string.
	TotalCostCents       httpx.Cents `json:"total_cost_cents"`
	TotalMarginCents     httpx.Cents `json:"total_margin_cents"`
	MarginPercent        *string     `json:"margin_percent"`
	TotalCommissionCents httpx.Cents `json:"total_commission_cents"`

	HoldReason  *HoldReason      `json:"hold_reason"`
	HoldNote    *string          `json:"hold_note"`
	ConfirmedAt *httpx.Timestamp `json:"confirmed_at"`
	CreatedAt   httpx.Timestamp  `json:"created_at"`
	UpdatedAt   httpx.Timestamp  `json:"updated_at"`

	InvoiceIDs []uuid.UUID `json:"invoice_ids"`
}

// Order is the full document: the summary, the ship-to captured at confirm,
// and the lines.
type Order struct {
	OrderSummary

	ShipTo *ShipToSnapshot `json:"ship_to"`
	Lines  []OrderLine     `json:"lines"`
}

// OrderLine is the shared sales line shape (salesdoc.Line, ADR 0005 2.2)
// with the order's own columns: the quote line it came from and the
// allocated, backordered and fulfilled quantities of section 5.4.
type OrderLine struct {
	salesdoc.Line

	QuoteLineID         *uuid.UUID     `json:"quote_line_id"`
	QuantityAllocated   httpx.Quantity `json:"quantity_allocated"`
	QuantityBackordered httpx.Quantity `json:"quantity_backordered"`
	QuantityFulfilled   httpx.Quantity `json:"quantity_fulfilled"`
}

// Billable reports whether the line carries money: every line a total sums
// except a text note.
func (l *OrderLine) Billable() bool { return l.LineType != salesdoc.LineText }
