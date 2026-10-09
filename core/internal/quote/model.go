// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"encoding/json"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/google/uuid"
)

// QuoteState is the lifecycle in its storage vocabulary: the UPPERCASE values
// of the quote_state enum, which the database CHECK vocabulary keeps and the
// portal, pricing and integration seams read. On the wire it is `status` in
// lowercase (ADR 0001 section 6): MarshalText does the mapping at the module
// boundary, ParseStatus the reverse.
type QuoteState string

const (
	QuoteStateDraft    QuoteState = "DRAFT"
	QuoteStateSent     QuoteState = "SENT"
	QuoteStateAccepted QuoteState = "ACCEPTED"
	QuoteStateRejected QuoteState = "REJECTED"
	QuoteStateExpired  QuoteState = "EXPIRED"
)

// statusNames is the wire vocabulary in lifecycle order.
var statusNames = []string{"draft", "sent", "accepted", "rejected", "expired"}

// Status is the state's wire name.
func (s QuoteState) Status() string { return strings.ToLower(string(s)) }

// MarshalText writes the lowercase wire name, so `status` is lowercase
// wherever a QuoteState is encoded.
func (s QuoteState) MarshalText() ([]byte, error) { return []byte(s.Status()), nil }

// ParseStatus maps a lowercase wire name to its state. Any other spelling,
// the legacy uppercase included, is not a status.
func ParseStatus(name string) (QuoteState, bool) {
	for _, n := range statusNames {
		if name == n {
			return QuoteState(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// lowerEnum is the wire spelling of a storage vocabulary value.
type lowerEnum string

func (e lowerEnum) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(e))), nil }

// DeliveryType is a quote's fulfilment: stored UPPERCASE, `pickup` or
// `delivery` on the wire.
type DeliveryType string

const (
	DeliveryPickup   DeliveryType = "PICKUP"
	DeliveryDelivery DeliveryType = "DELIVERY"
)

func (d DeliveryType) MarshalText() ([]byte, error) { return lowerEnum(d).MarshalText() }

// ExposureState is the price protection rollup stored on the quote header. The
// vocabulary is owned by the pricing module, whose own routes still speak
// UPPERCASE until they convert; the quote's wire spells it lowercase.
type ExposureState string

func (e ExposureState) MarshalText() ([]byte, error) { return lowerEnum(e).MarshalText() }

// QuoteSummary is the header of a quote: a list item, and the head of the
// full document. Money is integer cents; optional fields are present as null.
type QuoteSummary struct {
	ID           uuid.UUID  `json:"id"`
	Number       string     `json:"number"`
	BranchID     uuid.UUID  `json:"branch_id"`
	CustomerID   uuid.UUID  `json:"customer_id"`
	CustomerName string     `json:"customer_name"`
	JobID        *uuid.UUID `json:"job_id"`
	Status       QuoteState `json:"status"`
	Revision     int64      `json:"revision"`

	TotalCents       httpx.Cents `json:"total_cents"`
	FreightCents     httpx.Cents `json:"freight_cents"`
	MarginTotalCents httpx.Cents `json:"margin_total_cents"`

	DeliveryType DeliveryType `json:"delivery_type"`
	VehicleID    *uuid.UUID   `json:"vehicle_id"`
	VehicleName  *string      `json:"vehicle_name"`
	Source       string       `json:"source"` // "manual" or "ai" (the portal writes "portal")

	ExpiresAt  *httpx.Timestamp `json:"expires_at"`
	SentAt     *httpx.Timestamp `json:"sent_at"`
	AcceptedAt *httpx.Timestamp `json:"accepted_at"`
	RejectedAt *httpx.Timestamp `json:"rejected_at"`
	CreatedAt  httpx.Timestamp  `json:"created_at"`
	UpdatedAt  httpx.Timestamp  `json:"updated_at"`
}

// Quote is the full document: the header, the original upload's metadata, the
// price protection rollup, and the lines.
type Quote struct {
	QuoteSummary

	// Original upload (only for AI sourced quotes). The bytes are served by
	// GET /quotes/{id}/file, never inline.
	OriginalFile        []byte  `json:"-"`
	OriginalFilename    *string `json:"original_filename"`
	OriginalContentType *string `json:"original_content_type"`

	// AI parse mapping data (stored as JSONB), null when absent.
	ParseMap json.RawMessage `json:"parse_map"`

	// Index aware price protection rollup (migration 081): the worst state
	// across the quote's active escalators, maintained by the pricing
	// exposure scanner.
	ExposureState         ExposureState    `json:"exposure_state"`
	ExposureCents         httpx.Cents      `json:"exposure_cents"`
	ExposureLastCheckedAt *httpx.Timestamp `json:"exposure_last_checked_at"`

	Lines []QuoteLine `json:"lines"`
}

// QuoteLine is one priced line. Every priced line carries the same fields
// (ADR 0001 section 7a): the quantity as a decimal string with its unit, the
// scaled unit price with its price unit, the conversion pair, and the
// extension in cents rounded once. From C3-2A-units a line that names a
// product also carries its stocking unit and quantity (ADR 0006 sections 3.4
// and 6) and, on a random length product, its tally (section 4): both null
// on a line without a product, so a client reads one line shape.
type QuoteLine struct {
	ID          uuid.UUID  `json:"id"`
	QuoteID     uuid.UUID  `json:"quote_id"`
	ProductID   *uuid.UUID `json:"product_id"` // null for a special order line the dealer does not stock
	SKU         string     `json:"sku"`
	Description string     `json:"description"`

	// CustomerNote is what a portal user wrote when they asked for this line
	// to be priced (migration 084). Read mostly on the ERP side: an edit that
	// keeps the line's id keeps its note.
	CustomerNote *string `json:"customer_note"`

	Quantity    httpx.Quantity `json:"quantity"`
	UOM         product.UOM    `json:"uom"`
	PriceUOM    string         `json:"price_uom"`
	UOMQty      httpx.Quantity `json:"uom_qty"`
	PriceUOMQty httpx.Quantity `json:"price_uom_qty"`
	UnitPrice   httpx.Price    `json:"unit_price_ten_thousandths"`
	LineTotal   httpx.Cents    `json:"line_total_cents"`

	// The stocking unit and quantity of ADR 0006 section 3.4: the product's
	// stocking unit at the line's create, and the quantity converted into it,
	// exact (R5). Null on a line without a product.
	StockUOM      *string         `json:"stock_uom"`
	StockQuantity *httpx.Quantity `json:"stock_quantity"`

	// The tally of ADR 0006 section 4: the pieces by length a random length
	// line carries, its cross section snapshotted from the product. Null on
	// every other line.
	Tally *Tally `json:"tally"`

	// UnitCost is the product's average cost, read only: the margin basis and
	// the special order indicator for the auto purchase order.
	UnitCost httpx.Price `json:"unit_cost_ten_thousandths"`

	CreatedAt httpx.Timestamp `json:"created_at"`
}

// Tally is a random length line's pieces by length (ADR 0006 section 4.3).
// Pieces are JSON integers (a count); the lengths and the derived fields are
// decimal strings. LinearFeet is the exact sum the line's quantity carries;
// BoardFeet is the display rounding of R4.3 (linear feet x thickness x width
// / 12), read by nothing; ThicknessIn and WidthIn are null on a tally without
// a cross section.
type Tally struct {
	ThicknessIn *httpx.Quantity `json:"thickness_in"`
	WidthIn     *httpx.Quantity `json:"width_in"`
	Rows        []TallyRow      `json:"rows"`
	LinearFeet  httpx.Quantity  `json:"linear_feet"`
	BoardFeet   *httpx.Quantity `json:"board_feet"`
}

// TallyRow is one length of a tally: pieces at that length.
type TallyRow struct {
	Pieces   int            `json:"pieces"`
	LengthFT httpx.Quantity `json:"length_ft"`
}

// TallyThickness is the cross section the tally snapshotted from the
// product, for the line's write.
func (l *QuoteLine) TallyThickness() *httpx.Quantity {
	if l.Tally == nil {
		return nil
	}
	return l.Tally.ThicknessIn
}

// TallyWidth is the cross section's other half.
func (l *QuoteLine) TallyWidth() *httpx.Quantity {
	if l.Tally == nil {
		return nil
	}
	return l.Tally.WidthIn
}

// QuoteAnalytics holds aggregated quote analytics. Money is integer cents;
// the rates are percentages and stay floats.
type QuoteAnalytics struct {
	TotalQuotes            int                   `json:"total_quotes"`
	DraftCount             int                   `json:"draft_count"`
	SentCount              int                   `json:"sent_count"`
	AcceptedCount          int                   `json:"accepted_count"`
	RejectedCount          int                   `json:"rejected_count"`
	ExpiredCount           int                   `json:"expired_count"`
	ConversionRate         float64               `json:"conversion_rate"`
	AvgMarginAcceptedCents httpx.Cents           `json:"avg_margin_accepted_cents"`
	AvgMarginRejectedCents httpx.Cents           `json:"avg_margin_rejected_cents"`
	AvgDaysToClose         float64               `json:"avg_days_to_close"`
	TotalQuoteValueCents   httpx.Cents           `json:"total_quote_value_cents"`
	TotalAcceptedCents     httpx.Cents           `json:"total_accepted_value_cents"`
	AISourcedCount         int                   `json:"ai_sourced_count"`
	AIConversionRate       float64               `json:"ai_conversion_rate"`
	ManualConversionRate   float64               `json:"manual_conversion_rate"`
	TrendData              []QuoteAnalyticsTrend `json:"trend_data"`
}

// QuoteAnalyticsTrend holds daily quote counts for trend charts.
type QuoteAnalyticsTrend struct {
	Date               string      `json:"date"`
	Created            int         `json:"created"`
	Accepted           int         `json:"accepted"`
	Rejected           int         `json:"rejected"`
	TotalValueCents    httpx.Cents `json:"total_value_cents"`
	AcceptedValueCents httpx.Cents `json:"accepted_value_cents"`
}

// productUOM converts the stored unit code to the typed unit.
func productUOM(code string) product.UOM { return product.UOM(code) }
