// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Status is the purchase order lifecycle. Storage keeps its CHECK vocabulary
// (uppercase); the wire is lowercase snake_case and input in any other case
// is a 400 (ADR 0001 section 6).
type Status string

const (
	StatusDraft          Status = "DRAFT"
	StatusSent           Status = "SENT"
	StatusPartialReceive Status = "PARTIAL"
	StatusReceived       Status = "RECEIVED"
	StatusCancelled      Status = "CANCELLED"
)

// ParseStatus accepts only the lowercase spelling of a status.
func ParseStatus(s string) (Status, bool) {
	switch s {
	case "draft":
		return StatusDraft, true
	case "sent":
		return StatusSent, true
	case "partial":
		return StatusPartialReceive, true
	case "received":
		return StatusReceived, true
	case "cancelled":
		return StatusCancelled, true
	}
	return "", false
}

// Wire is the lowercase status the contract serves.
func (s Status) Wire() string {
	switch s {
	case StatusDraft:
		return "draft"
	case StatusSent:
		return "sent"
	case StatusPartialReceive:
		return "partial"
	case StatusReceived:
		return "received"
	case StatusCancelled:
		return "cancelled"
	}
	return string(s)
}

// Source records how the purchase order got here. Storage keeps migration
// 055's vocabulary; the wire is lowercase snake_case.
type Source string

const (
	SourceManual       Source = "MANUAL"
	SourceReorder      Source = "REORDER"
	SourceSpecialOrder Source = "SPECIAL_ORDER"
	SourceA2A          Source = "A2A"
)

func (s Source) Wire() string {
	switch s {
	case SourceManual:
		return "manual"
	case SourceReorder:
		return "reorder"
	case SourceSpecialOrder:
		return "special_order"
	case SourceA2A:
		return "a2a"
	}
	return string(s)
}

// PurchaseOrder is the full document on the wire (ADR 0001, ADR 0008
// section 1): the PO- number, the lowercase status and source, the revision,
// money in `_cents`, quantities as decimal strings and unit costs in
// `_ten_thousandths`.
type PurchaseOrder struct {
	ID         uuid.UUID `json:"id"`
	Number     string    `json:"number"`
	BranchID   uuid.UUID `json:"branch_id"`
	VendorID   *uuid.UUID `json:"vendor_id"`
	Status     string    `json:"status"`
	Source     string    `json:"source"`
	Currency   string    `json:"currency"`
	Revision   int64     `json:"revision"`
	CreatedAt  httpx.Timestamp `json:"created_at"`
	UpdatedAt  httpx.Timestamp `json:"updated_at"`
	SentAt     *httpx.Timestamp `json:"sent_at"`
	Lines      []PurchaseOrderLine `json:"lines"`
	LineCount  int           `json:"line_count"`
	TotalCents httpx.Cents   `json:"total_cents"`
}

// PurchaseOrderSummary is the list item: the document without its lines,
// carrying their count and total instead, so the two cannot drift.
type PurchaseOrderSummary struct {
	ID         uuid.UUID  `json:"id"`
	Number     string     `json:"number"`
	BranchID   uuid.UUID  `json:"branch_id"`
	VendorID   *uuid.UUID `json:"vendor_id"`
	Status     string     `json:"status"`
	Source     string     `json:"source"`
	Currency   string     `json:"currency"`
	Revision   int64      `json:"revision"`
	CreatedAt  httpx.Timestamp `json:"created_at"`
	UpdatedAt  httpx.Timestamp `json:"updated_at"`
	SentAt     *httpx.Timestamp `json:"sent_at"`
	LineCount  int        `json:"line_count"`
	TotalCents httpx.Cents `json:"total_cents"`
}

// PurchaseOrderLine carries ADR 0008 section 1's line shape: the purchase
// unit beside the quantity, the price unit beside the unit cost, the
// conversion pair, and the stocking unit conversion the receipt draws on.
// While the unit catalogue has not landed every line is held to the
// product's stocking unit with the pair 1 and 1.
type PurchaseOrderLine struct {
	ID          uuid.UUID        `json:"id"`
	POID        uuid.UUID        `json:"-"`
	Position    int              `json:"position"`
	ProductID   *uuid.UUID       `json:"product_id"`
	Description string           `json:"description"`
	Quantity    httpx.Quantity   `json:"quantity"`
	QtyReceived httpx.Quantity   `json:"qty_received"`
	UOM         string           `json:"uom"`
	PriceUOM    string           `json:"price_uom"`
	UOMQty      httpx.Quantity   `json:"uom_qty"`
	PriceUOMQty httpx.Quantity   `json:"price_uom_qty"`
	StockUOM    *string          `json:"stock_uom"`
	StockQuantity *httpx.Quantity `json:"stock_quantity"`
	UnitCostTenThousandths httpx.Price `json:"unit_cost_ten_thousandths"`
	LineTotalCents         httpx.Cents `json:"line_total_cents"`
	LinkedSOLineID *uuid.UUID       `json:"linked_so_line_id"`
}

// FreightCharge keeps its own shape in freight.go until C4-2 F converts the
// freight routes.
