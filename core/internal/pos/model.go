// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/tax"
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
	// TenderStoreCredit pays with the customer's open credit memos, applied to
	// the sale's invoice through the AR core. It is no drawer money.
	TenderStoreCredit TenderMethod = "STORE_CREDIT"
)

// The closed list of ID types a counter may record beside "ID checked": the
// kind of ID looked at, never its number.
var idTypes = []string{"drivers_license", "state_id", "passport", "military_id", "other"}

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
	case "store_credit":
		return TenderStoreCredit, true
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
	ID            uuid.UUID        `json:"id"`
	Number        string           `json:"number"`
	Revision      int64            `json:"revision"`
	BranchID      uuid.UUID        `json:"branch_id"`
	RegisterID    string           `json:"register_id"`
	CashierID     uuid.UUID        `json:"cashier_id"`
	CustomerID    *uuid.UUID       `json:"customer_id"`
	Currency      string           `json:"currency"`
	SubtotalCents httpx.Cents      `json:"subtotal_cents"`
	TaxCents      httpx.Cents      `json:"tax_cents"`
	TotalCents    httpx.Cents      `json:"total_cents"`
	ChangeCents   httpx.Cents      `json:"change_cents"`
	TillSessionID *uuid.UUID       `json:"till_session_id"`
	Status        SaleStatus       `json:"status"`
	InvoiceID     *uuid.UUID       `json:"invoice_id"`
	CompletedAt   *httpx.Timestamp `json:"completed_at"`
	CreatedAt     httpx.Timestamp  `json:"created_at"`

	// CustomerPO and JobID are the purchase order and job a charge sale
	// names when the account asks for them.
	CustomerPO *string    `json:"customer_po"`
	JobID      *uuid.UUID `json:"job_id"`

	// BuyerContactID is the roster buyer an on account sale names when the
	// account asks for one; PrintCount is how many times the receipt has been
	// printed; ExchangeReturnID is the return an exchange's replacement sale
	// stands against.
	BuyerContactID   *uuid.UUID `json:"buyer_contact_id"`
	PrintCount       int        `json:"print_count"`
	ExchangeReturnID *uuid.UUID `json:"exchange_return_id"`

	// ReturnedQuantities sums, per sale line id, the quantity every earlier
	// return brought back, so the counter's return dialog can show a line
	// already fully returned as not returnable. Set on a completed sale's
	// read; omitted when nothing of the sale was ever returned.
	ReturnedQuantities map[string]httpx.Quantity `json:"returned_quantities,omitempty"`

	// TaxExemption is the certificate that exempts this sale on the branch's
	// business day (CP-10): why the tax is zero, so the counter can say it.
	// Nil when no certificate covers the day.
	TaxExemption *tax.ExemptionStanding `json:"tax_exemption,omitempty"`

	Lines   []salesdoc.Line `json:"lines"`
	Tenders []Tender        `json:"tenders"`

	// WalkInID is the walk-in customer the invoice falls back to when the
	// sale names no customer; never on the wire.
	WalkInID uuid.UUID `json:"-"`
}

// SaleSummary is the list item; the full document embeds it, so the two
// cannot drift (the recipe's rule).
type SaleSummary struct {
	ID          uuid.UUID        `json:"id"`
	Number      string           `json:"number"`
	Revision    int64            `json:"revision"`
	BranchID    uuid.UUID        `json:"branch_id"`
	RegisterID  string           `json:"register_id"`
	CashierID   uuid.UUID        `json:"cashier_id"`
	CustomerID  *uuid.UUID       `json:"customer_id"`
	Currency    string           `json:"currency"`
	TotalCents  httpx.Cents      `json:"total_cents"`
	Status      SaleStatus       `json:"status"`
	InvoiceID   *uuid.UUID       `json:"invoice_id"`
	CompletedAt *httpx.Timestamp `json:"completed_at"`
	CreatedAt   httpx.Timestamp  `json:"created_at"`
	ItemCount   int              `json:"item_count"`

	// CustomerName and InvoiceNumber are the list's search and display
	// text: the account the sale names (null for a walk-in) and its
	// invoice's number (null until the sale completes).
	CustomerName  *string `json:"customer_name"`
	InvoiceNumber *string `json:"invoice_number"`

	// BusinessDay is the day the sale was made on in its branch's own
	// zone, YYYY-MM-DD (the search's date bound reads the same day); never
	// the UTC slice of the timestamp.
	BusinessDay string `json:"business_day"`
}

// Tender is one payment taken at the counter, stored net: the tendered
// amount less any change given from it, so the payment it became is the
// money kept (ADR 0005 section 14.2 C2-5).
type Tender struct {
	ID          uuid.UUID    `json:"id"`
	SaleID      uuid.UUID    `json:"sale_id"`
	Method      TenderMethod `json:"method"`
	AmountCents httpx.Cents  `json:"amount_cents"`
	PaymentID   *uuid.UUID   `json:"payment_id"`
	Reference   *string      `json:"reference"`
	CardLast4   *string      `json:"card_last4"`
	CardBrand   *string      `json:"card_brand"`
	GatewayTxID *string      `json:"gateway_tx_id"`
	AuthCode    *string      `json:"auth_code"`
	// IDChecked and IDType record that the cashier looked at the payer's ID
	// (a check or an account charge) and what kind it was; the number is
	// never taken.
	IDChecked bool            `json:"id_checked"`
	IDType    *string         `json:"id_type"`
	CreatedAt httpx.Timestamp `json:"created_at"`
}

// Return is one counter return: a credit memo created and posted in one act
// (ADR 0005 section 14.2 C2-5), refunded in cash or card or left as account
// credit.
type Return struct {
	ID             uuid.UUID       `json:"id"`
	Number         string          `json:"number"`
	Revision       int64           `json:"revision"`
	BranchID       *uuid.UUID      `json:"branch_id"`
	RegisterID     string          `json:"register_id"`
	TillSessionID  *uuid.UUID      `json:"till_session_id"`
	OriginalSaleID *uuid.UUID      `json:"original_sale_id"`
	CustomerID     *uuid.UUID      `json:"customer_id"`
	CashierID      uuid.UUID       `json:"cashier_id"`
	Currency       string          `json:"currency"`
	SubtotalCents  httpx.Cents     `json:"subtotal_cents"`
	TaxCents       httpx.Cents     `json:"tax_cents"`
	TotalCents     httpx.Cents     `json:"total_cents"`
	RefundMethod   RefundMethod    `json:"refund_method"`
	Reason         string          `json:"reason"`
	CreditMemoID   *uuid.UUID      `json:"credit_memo_id"`
	CreatedAt      httpx.Timestamp `json:"created_at"`
	// RestockingFeeCents is the fee withheld from the refund, shown on the
	// credit memo as its own line; the subtotal and total already carry it.
	RestockingFeeCents httpx.Cents `json:"restocking_fee_cents"`
	// ExchangeSaleID is the sale that replaced the goods when the return was
	// one half of an exchange.
	ExchangeSaleID *uuid.UUID `json:"exchange_sale_id"`

	Lines []ReturnLine `json:"lines"`
}

// ReturnLine is one returned line, negative quantities and extensions (the
// credit memo line shape).
type ReturnLine struct {
	ID          uuid.UUID       `json:"id"`
	Position    int             `json:"position"`
	ProductID   *uuid.UUID      `json:"product_id"`
	Description string          `json:"description"`
	Quantity    *httpx.Quantity `json:"quantity"`
	UOM         *string         `json:"uom"`
	UnitPrice   *httpx.Price    `json:"unit_price_ten_thousandths"`
	LineTotal   *httpx.Cents    `json:"line_total_cents"`
	Restock     bool            `json:"restock"`
	// ReasonCode is the line's reason from the closed list; the fee is what
	// its reason withheld from the line's refund.
	ReasonCode         string      `json:"reason_code"`
	RestockingFeeCents httpx.Cents `json:"restocking_fee_cents"`

	// The pair, price unit and stocking unit the line carries, read by the
	// memo line's and return line's inserts; internal, never on the wire.
	PriceUOM      *string         `json:"-"`
	UOMQty        *httpx.Quantity `json:"-"`
	PriceUOMQty   *httpx.Quantity `json:"-"`
	StockUOM      *string         `json:"-"`
	StockQuantity *httpx.Quantity `json:"-"`
	// LineType and ChargeCodeID carry a returned charge line's own shape.
	LineType     string     `json:"-"`
	ChargeCodeID *uuid.UUID `json:"-"`

	// SaleLineID is the sale line a linked return names; InvoiceLineID the
	// invoice line its restock cost reads. Both internal, never on the wire.
	SaleLineID    *uuid.UUID `json:"-"`
	InvoiceLineID *uuid.UUID `json:"-"`
}

// RefundIntentSummary is the manager list's item (CP-06): one open refund
// claim with its sale, amount, tender and state. Open means PENDING (a
// terminal answer still owed or a claim waiting on the resolve route) or a
// void's FAILED claim whose tender still holds the credit the pay back
// route returns to the card; HeldCreditCents carries that credit.
type RefundIntentSummary struct {
	ID              uuid.UUID       `json:"id"`
	SaleID          uuid.UUID       `json:"sale_id"`
	SaleNumber      string          `json:"sale_number"`
	TenderID        uuid.UUID       `json:"tender_id"`
	TenderMethod    TenderMethod    `json:"tender_method"`
	AmountCents     httpx.Cents     `json:"amount_cents"`
	Act             string          `json:"act"`
	State           string          `json:"state"`
	HeldCreditCents httpx.Cents     `json:"held_credit_cents"`
	CreatedAt       httpx.Timestamp `json:"created_at"`
}

// TillSessionStatus is the drawer lifecycle.
type TillSessionStatus string

const (
	TillOpen TillSessionStatus = "OPEN"
	// TillPendingSignoff is a counted drawer whose over or short passed the
	// branch's variance threshold: it waits for a manager, and its variance is
	// not on the ledger until the sign off posts it.
	TillPendingSignoff TillSessionStatus = "PENDING_SIGNOFF"
	TillClosed         TillSessionStatus = "CLOSED"
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

	ExpectedByMethod map[string]int64 `json:"expected_by_method"`
	CountedByMethod  map[string]int64 `json:"counted_by_method"`
	OverShort        *httpx.Cents     `json:"over_short_cents"`
	// OverShortByMethod is the variance per counted method once the drawer
	// is counted (CP-08): the methods never net into one another, so a check
	// overage cannot hide a cash shortage.
	OverShortByMethod map[string]int64 `json:"over_short_by_method,omitempty"`
	GLEntryID         *uuid.UUID       `json:"gl_entry_id"`
	Notes             string           `json:"notes"`

	// ClosedBy is who counted the drawer; VarianceThreshold the branch's
	// threshold in force at that count; SignOff the manager's release of a
	// close that passed it (CP-08).
	ClosedBy          *string      `json:"closed_by"`
	VarianceThreshold *httpx.Cents `json:"variance_threshold_cents"`
	SignOff           *TillSignOff `json:"sign_off"`
}

// TillSignOff is the manager's release of a counted drawer whose over or
// short passed the variance threshold: who, why and when.
type TillSignOff struct {
	SignedOffBy string          `json:"signed_off_by"`
	Reason      string          `json:"reason"`
	SignedOffAt httpx.Timestamp `json:"signed_off_at"`
}

// TillReport is the live (X) or closing (Z) summary for a session.
type TillReport struct {
	Session           TillSession      `json:"session"`
	SaleCount         int              `json:"sale_count"`
	SalesTotalCents   httpx.Cents      `json:"sales_total_cents"`
	TaxTotalCents     httpx.Cents      `json:"tax_total_cents"`
	ChangeCents       httpx.Cents      `json:"change_cents"`
	TenderedByMethod  map[string]int64 `json:"tendered_by_method"`
	ExpectedByMethod  map[string]int64 `json:"expected_by_method"`
	OverShortByMethod map[string]int64 `json:"over_short_by_method,omitempty"`

	// RefundsByMethod is what the session refunded, per method (CASH out of
	// the drawer, CARD back to the card), so the X report answers "refunds"
	// beside the taken and expected figures. A method appears only where the
	// session refunded on it.
	RefundsByMethod map[string]int64 `json:"refunds_by_method"`

	// Movements are the drops, pickups, paid in and paid out of the session,
	// oldest first, with their totals by kind; the expected cash carries them.
	Movements            []TillMovement   `json:"movements"`
	MovementTotalsByKind map[string]int64 `json:"movement_totals_by_kind"`
}

// ZReport is the immutable end-of-day snapshot generated once when a till
// session closes. Payload is the frozen closing TillReport, an object on the
// wire (raw JSON, never a base64 encoded string a reader must decode before
// it can parse).
type ZReport struct {
	ID            uuid.UUID       `json:"id"`
	TillSessionID uuid.UUID       `json:"till_session_id"`
	RegisterID    string          `json:"register_id"`
	BranchID      *uuid.UUID      `json:"branch_id"`
	OverShort     httpx.Cents     `json:"over_short_cents"`
	Payload       json.RawMessage `json:"payload"`
	GeneratedAt   httpx.Timestamp `json:"generated_at"`
}

// QuickSearchResult is a lightweight product result for the counter's
// typeahead (the product module's data; a bare array, like the partner
// surface the recipe exempts).
type QuickSearchResult struct {
	ProductID      uuid.UUID      `json:"product_id"`
	SKU            string         `json:"sku"`
	Description    string         `json:"description"`
	UnitPriceCents httpx.Cents    `json:"unit_price_cents"`
	UOM            string         `json:"uom"`
	InStock        httpx.Quantity `json:"in_stock"`
}

// CounterTile is one product tile of the counter's catalogue (GET
// /pos/tiles): the search's price and stock figures plus the category and the
// picture path, null unless the picture setting is on and the product has one.
type CounterTile struct {
	ProductID      uuid.UUID      `json:"product_id"`
	SKU            string         `json:"sku"`
	Description    string         `json:"description"`
	UOM            string         `json:"uom"`
	UnitPriceCents httpx.Cents    `json:"unit_price_cents"`
	InStock        httpx.Quantity `json:"in_stock"`
	Category       string         `json:"category"`
	ImageURL       *string        `json:"image_url"`
}

// TileCategory is one category chip: its name and its product count.
type TileCategory struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// TileSet is the tiles answer: the chips, the tiles and whether pictures are
// on (the catalog.product_images setting). Neither array is ever null.
type TileSet struct {
	Categories []TileCategory `json:"categories"`
	Items      []CounterTile  `json:"items"`
	Images     bool           `json:"images"`
}

// TileQuery is the tiles read's parameters, already validated.
type TileQuery struct {
	Category string
	Q        string
	Limit    int
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

// CounterRegister is a register the caller may use, with its branch.
type CounterRegister struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	BranchID   uuid.UUID `json:"branch_id"`
	BranchCode string    `json:"branch_code"`
	BranchName string    `json:"branch_name"`
}
