// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// SaleStatus is a counter sale's lifecycle state. Storage keeps the
// uppercase vocabulary; the wire is lowercase (ADR 0001 section 6).
type SaleStatus string

const (
	StatusOpen      SaleStatus = "OPEN"
	StatusHeld      SaleStatus = "HELD"
	StatusCompleted SaleStatus = "COMPLETED"
	StatusVoided    SaleStatus = "VOIDED"
)

// Status returns the wire spelling.
func (s SaleStatus) Status() string { return strings.ToLower(string(s)) }

// MarshalText writes the lowercase wire name.
func (s SaleStatus) MarshalText() ([]byte, error) { return []byte(s.Status()), nil }

// TenderMethod is how a sale was paid. Storage keeps the uppercase
// vocabulary; the wire is lowercase.
type TenderMethod string

const (
	TenderCash    TenderMethod = "CASH"
	TenderCheck   TenderMethod = "CHECK"
	TenderCard    TenderMethod = "CARD"
	TenderAccount TenderMethod = "ACCOUNT"
)

// Status returns the wire spelling.
func (m TenderMethod) Status() string { return strings.ToLower(string(m)) }

// MarshalText writes the lowercase wire name.
func (m TenderMethod) MarshalText() ([]byte, error) { return []byte(m.Status()), nil }

// ParseTenderMethod maps a lowercase wire name to its method.
func ParseTenderMethod(name string) (TenderMethod, bool) {
	switch name {
	case "cash":
		return TenderCash, true
	case "check":
		return TenderCheck, true
	case "card":
		return TenderCard, true
	case "account":
		return TenderAccount, true
	}
	return "", false
}

// RefundMethod is how a return is refunded: out of the drawer, back on the
// card, or left as account credit.
type RefundMethod string

const (
	RefundCash    RefundMethod = "CASH"
	RefundCard    RefundMethod = "CARD"
	RefundAccount RefundMethod = "ACCOUNT"
)

// Status returns the wire spelling.
func (m RefundMethod) Status() string { return strings.ToLower(string(m)) }

// MarshalText writes the lowercase wire name.
func (m RefundMethod) MarshalText() ([]byte, error) { return []byte(m.Status()), nil }

// Sale is one counter sale on the wire (ADR 0005 section 14.2 C2-5): the
// cart the register builds, then the completed money moment, all money in
// cents and prices at scale 4, the lines in the shared shape of section 2.2.
type Sale struct {
	ID            uuid.UUID     `json:"id"`
	Number        string        `json:"number"`
	Revision      int64         `json:"revision"`
	BranchID      uuid.UUID     `json:"branch_id"`
	RegisterID    string        `json:"register_id"`
	CashierID     uuid.UUID     `json:"cashier_id"`
	CustomerID    *uuid.UUID    `json:"customer_id"`
	Currency      string        `json:"currency"`
	SubtotalCents httpx.Cents   `json:"subtotal_cents"`
	TaxCents      httpx.Cents   `json:"tax_cents"`
	TotalCents    httpx.Cents   `json:"total_cents"`
	ChangeCents   httpx.Cents   `json:"change_cents"`
	TillSessionID *uuid.UUID    `json:"till_session_id"`
	Status        SaleStatus    `json:"status"`
	InvoiceID     *uuid.UUID    `json:"invoice_id"`
	CompletedAt   *httpx.Timestamp `json:"completed_at"`
	CreatedAt     httpx.Timestamp  `json:"created_at"`

	Lines   []salesdoc.Line `json:"lines"`
	Tenders []Tender        `json:"tenders"`

	// WalkInID is the walk-in customer the invoice falls back to when the
	// sale names no customer; never on the wire.
	WalkInID uuid.UUID `json:"-"`
}

// SaleSummary is the list item; the full document embeds it, so the two
// cannot drift (the recipe's rule).
type SaleSummary struct {
	ID            uuid.UUID    `json:"id"`
	Number        string       `json:"number"`
	Revision      int64        `json:"revision"`
	BranchID      uuid.UUID    `json:"branch_id"`
	RegisterID    string       `json:"register_id"`
	CashierID     uuid.UUID    `json:"cashier_id"`
	CustomerID    *uuid.UUID   `json:"customer_id"`
	Currency      string       `json:"currency"`
	TotalCents    httpx.Cents  `json:"total_cents"`
	Status        SaleStatus   `json:"status"`
	InvoiceID     *uuid.UUID   `json:"invoice_id"`
	CompletedAt   *httpx.Timestamp `json:"completed_at"`
	CreatedAt     httpx.Timestamp  `json:"created_at"`
	ItemCount     int          `json:"item_count"`
}

// Tender is one payment taken at the counter, stored net: the tendered
// amount less any change given from it, so the payment it became is the
// money kept (ADR 0005 section 14.2 C2-5).
type Tender struct {
	ID            uuid.UUID     `json:"id"`
	SaleID        uuid.UUID     `json:"sale_id"`
	Method        TenderMethod  `json:"method"`
	AmountCents   httpx.Cents   `json:"amount_cents"`
	PaymentID     *uuid.UUID    `json:"payment_id"`
	Reference     *string       `json:"reference"`
	CardLast4     *string       `json:"card_last4"`
	CardBrand     *string       `json:"card_brand"`
	GatewayTxID   *string       `json:"gateway_tx_id"`
	AuthCode      *string       `json:"auth_code"`
	CreatedAt     httpx.Timestamp `json:"created_at"`
}

// Return is one counter return: a credit memo created and posted in one act
// (ADR 0005 section 14.2 C2-5), refunded in cash or card or left as account
// credit.
type Return struct {
	ID              uuid.UUID       `json:"id"`
	Number          string          `json:"number"`
	Revision        int64           `json:"revision"`
	BranchID        *uuid.UUID      `json:"branch_id"`
	RegisterID      string          `json:"register_id"`
	TillSessionID   *uuid.UUID      `json:"till_session_id"`
	OriginalSaleID  *uuid.UUID      `json:"original_sale_id"`
	CustomerID      *uuid.UUID      `json:"customer_id"`
	CashierID       uuid.UUID       `json:"cashier_id"`
	Currency        string          `json:"currency"`
	SubtotalCents   httpx.Cents     `json:"subtotal_cents"`
	TaxCents        httpx.Cents     `json:"tax_cents"`
	TotalCents      httpx.Cents     `json:"total_cents"`
	RefundMethod    RefundMethod    `json:"refund_method"`
	Reason          string          `json:"reason"`
	CreditMemoID    *uuid.UUID      `json:"credit_memo_id"`
	CreatedAt       httpx.Timestamp `json:"created_at"`

	Lines []ReturnLine `json:"lines"`
}

// ReturnLine is one returned line, negative quantities and extensions (the
// credit memo line shape).
type ReturnLine struct {
	ID          uuid.UUID     `json:"id"`
	Position    int           `json:"position"`
	ProductID   *uuid.UUID    `json:"product_id"`
	Description string        `json:"description"`
	Quantity    *httpx.Quantity `json:"quantity"`
	UOM         *string       `json:"uom"`
	UnitPrice   *httpx.Price  `json:"unit_price_ten_thousandths"`
	LineTotal   *httpx.Cents  `json:"line_total_cents"`
	Restock     bool          `json:"restock"`
}

// TillSessionStatus is the drawer lifecycle.
type TillSessionStatus string

const (
	TillOpen   TillSessionStatus = "OPEN"
	TillClosed TillSessionStatus = "CLOSED"
)

// Status returns the wire spelling.
func (s TillSessionStatus) Status() string { return strings.ToLower(string(s)) }

// MarshalText writes the lowercase wire name.
func (s TillSessionStatus) MarshalText() ([]byte, error) { return []byte(s.Status()), nil }

// TillSession is one drawer shift on a register. The expected cash is the
// sum of the session's cash payments (already net of change) plus the opening
// float less cash refunds (ADR 0005 section 14.2 C2-5).
type TillSession struct {
	ID           uuid.UUID         `json:"id"`
	RegisterID   string            `json:"register_id"`
	BranchID     *uuid.UUID        `json:"branch_id"`
	CashierID    uuid.UUID         `json:"cashier_id"`
	Status       TillSessionStatus `json:"status"`
	OpeningFloat httpx.Cents       `json:"opening_float_cents"`
	OpenedAt     httpx.Timestamp   `json:"opened_at"`
	ClosedAt     *httpx.Timestamp  `json:"closed_at"`

	ExpectedByMethod map[string]int64   `json:"expected_by_method"`
	CountedByMethod  map[string]int64   `json:"counted_by_method"`
	OverShort        *httpx.Cents       `json:"over_short_cents"`
	GLEntryID        *uuid.UUID         `json:"gl_entry_id"`
	Notes            string             `json:"notes"`
}

// TillReport is the live (X) or closing (Z) summary for a session.
type TillReport struct {
	Session          TillSession      `json:"session"`
	SaleCount        int              `json:"sale_count"`
	SalesTotalCents  httpx.Cents      `json:"sales_total_cents"`
	TaxTotalCents    httpx.Cents      `json:"tax_total_cents"`
	ChangeCents      httpx.Cents      `json:"change_cents"`
	TenderedByMethod map[string]int64 `json:"tendered_by_method"`
	ExpectedByMethod map[string]int64 `json:"expected_by_method"`
}

// ZReport is the immutable end-of-day snapshot generated once when a till
// session closes. Payload is the frozen closing TillReport.
type ZReport struct {
	ID            uuid.UUID       `json:"id"`
	TillSessionID uuid.UUID       `json:"till_session_id"`
	RegisterID    string          `json:"register_id"`
	BranchID      *uuid.UUID      `json:"branch_id"`
	OverShort     httpx.Cents     `json:"over_short_cents"`
	Payload       []byte          `json:"payload"`
	GeneratedAt   httpx.Timestamp `json:"generated_at"`
}

// QuickSearchResult is a lightweight product result for the counter's
// typeahead (the product module's data; a bare array, like the partner
// surface the recipe exempts).
type QuickSearchResult struct {
	ProductID       uuid.UUID     `json:"product_id"`
	SKU             string        `json:"sku"`
	Description     string        `json:"description"`
	UnitPriceCents  httpx.Cents   `json:"unit_price_cents"`
	UOM             string        `json:"uom"`
	InStock         httpx.Quantity `json:"in_stock"`
}

// CatalogProduct is a lightweight product for the offline catalog cache.
type CatalogProduct struct {
	ProductID      uuid.UUID      `json:"product_id"`
	SKU            string         `json:"sku"`
	Description    string         `json:"description"`
	UnitPriceCents httpx.Cents    `json:"unit_price_cents"`
	UOM            string         `json:"uom"`
	InStock        httpx.Quantity `json:"in_stock"`
}

// now is the service's clock, replaceable in tests.
var saleNow = time.Now
