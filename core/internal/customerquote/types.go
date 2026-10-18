// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"errors"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Status is where a customer quote stands. Expiry is not a stored status: a
// sent quote past its valid_until reads as expired (see Quote.Effective).
type Status string

const (
	StatusDraft     Status = "draft"
	StatusSent      Status = "sent"
	StatusApproved  Status = "approved"
	StatusDeclined  Status = "declined"
	StatusConfirmed Status = "confirmed"
	StatusWithdrawn Status = "withdrawn"
)

// StatusExpired is the effective status of a sent quote past valid_until.
const StatusExpired Status = "expired"

// The approval log's steps and actors.
const (
	StepCustomerApproved    = "customer_approved"
	StepCustomerDeclined    = "customer_declined"
	StepContractorConfirmed = "contractor_confirmed"
	StepDealerAccepted      = "dealer_accepted"

	ActorCustomer   = "customer"
	ActorContractor = "contractor"
	ActorDealer     = "dealer"
)

// The sentinel errors the service answers with; the handlers map them to the
// wire (see httpErr).
var (
	ErrNotFound        = errors.New("customer quote not found")
	ErrForbidden       = errors.New("the portal role may not do this")
	ErrInvalidState    = errors.New("the quote is not in a state that allows this")
	ErrStaleRevision   = errors.New("the quote changed after it was read")
	ErrLinkUnknown     = errors.New("unknown link")
	ErrLinkUnavailable = errors.New("the link is expired or revoked")
	ErrRateLimited     = errors.New("too many requests")
	ErrCodeRequired    = errors.New("a verification code is required")
	ErrDeliveryFailed  = errors.New("the code could not be delivered")
	ErrIncomplete      = errors.New("every selection must be decided")
	ErrNothingChosen   = errors.New("nothing is approved")
	// ErrCodesUnavailable refuses a verification code the server cannot send.
	ErrCodesUnavailable = errors.New(CodesUnavailableMessage)
)

// CodesUnavailableMessage is the one sentence every refusal about
// verification codes carries.
const CodesUnavailableMessage = "Verification codes need a real email or text sender on this server."

// Contact is a person the quote names.
type Contact struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// Tax is the contractor's tax treatment of the quote.
type Tax struct {
	Mode    string `json:"mode"`
	RateBPS int64  `json:"rate_bps"`
}

// OptionGroup is a choose one of several set of lines.
type OptionGroup struct {
	ID       uuid.UUID `json:"id"`
	Label    string    `json:"label"`
	Position int       `json:"position"`
}

// Line is a material line with its captured cost, the tier that applied and
// the customer's decision. Everything but the decision is frozen at send.
type Line struct {
	ID                     uuid.UUID      `json:"id"`
	Position               int            `json:"position"`
	ProductID              uuid.UUID      `json:"product_id"`
	CategoryID             *uuid.UUID     `json:"category_id"`
	SKU                    string         `json:"sku"`
	Description            string         `json:"description"`
	UOM                    string         `json:"uom"`
	Quantity               httpx.Quantity `json:"quantity"`
	UnitCostTenThousandths int64          `json:"unit_cost_ten_thousandths"`
	CostCents              int64          `json:"cost_cents"`
	PriceBasis             string         `json:"price_basis"`
	PricedAt               time.Time      `json:"-"`
	GroupID                *uuid.UUID     `json:"group_id"`
	Override               *Markup        `json:"markup"`
	Applied                AppliedMarkup  `json:"applied"`
	SellCents              int64          `json:"sell_cents"`
	Note                   string         `json:"note"`
	Selection              SelectionState `json:"selection"`
}

// AppliedMarkup is what a line was priced with.
type AppliedMarkup struct {
	Tier Tier       `json:"tier"`
	Kind MarkupKind `json:"kind"`
	BPS  int64      `json:"bps"`
}

// CategoryRule is tier two of the markup.
type CategoryRule struct {
	CategoryID uuid.UUID `json:"category_id"`
	Markup     Markup    `json:"markup"`
}

// Labour is a labour line as stored.
type Labour struct {
	ID             uuid.UUID      `json:"id"`
	Position       int            `json:"position"`
	Description    string         `json:"description"`
	RateType       string         `json:"rate_type"`
	RateCents      int64          `json:"rate_cents"`
	Hours          httpx.Quantity `json:"hours,omitempty"`
	ContractorOnly bool           `json:"contractor_only"`
}

// Overhead is an overhead line as stored.
type Overhead struct {
	ID             uuid.UUID `json:"id"`
	Position       int       `json:"position"`
	Label          string    `json:"label"`
	AmountType     string    `json:"amount_type"`
	Value          int64     `json:"value"`
	ContractorOnly bool      `json:"contractor_only"`
}

// Quote is the whole contractor's quote, internal and contractor facing.
type Quote struct {
	ID               uuid.UUID
	CustomerID       uuid.UUID
	Reference        string
	Title            string
	ProjectID        *uuid.UUID
	SourceQuoteID    *uuid.UUID
	JobSiteAddress   string
	EndCustomer      Contact
	SiteContact      Contact
	Status           Status
	Markup           Markup
	HideLinePrices   bool
	Tax              Tax
	PaymentTerms     string
	DepositBPS       int64
	CustomerNote     string
	RequireCode      bool
	ValidUntil       time.Time
	Revision         int64
	SentAt           *time.Time
	ApprovedAt       *time.Time
	DeclinedAt       *time.Time
	ConfirmedAt      *time.Time
	DealerAcceptedAt *time.Time
	OrderID          *uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time

	Groups        []OptionGroup
	Lines         []Line
	CategoryRules []CategoryRule
	Labour        []Labour
	Overhead      []Overhead
}

// Effective is the status the quote reads as at now: a sent quote past its
// valid_until is expired.
func (q *Quote) Effective(now time.Time) Status {
	if q.Status == StatusSent && !now.Before(q.ValidUntil) {
		return StatusExpired
	}
	return q.Status
}

// States returns the customer's decisions by line.
func (q *Quote) States() map[uuid.UUID]SelectionState {
	m := make(map[uuid.UUID]SelectionState, len(q.Lines))
	for _, l := range q.Lines {
		m[l.ID] = l.Selection
	}
	return m
}

// TotalsInput converts the quote to the money engine's input.
func (q *Quote) TotalsInput() TotalsInput {
	in := TotalsInput{
		States:     q.States(),
		TaxMode:    q.Tax.Mode,
		TaxRateBPS: q.Tax.RateBPS,
		DepositBPS: q.DepositBPS,
	}
	for _, l := range q.Lines {
		in.Lines = append(in.Lines, MaterialLine{
			ID: l.ID, GroupID: l.GroupID, CategoryID: l.CategoryID, CostCents: l.CostCents,
			Override: l.Override, Applied: Markup{Kind: l.Applied.Kind, BPS: l.Applied.BPS},
			Tier: l.Applied.Tier, SellCents: l.SellCents,
		})
	}
	for _, l := range q.Labour {
		in.Labour = append(in.Labour, LabourLine{ID: l.ID, Description: l.Description, RateType: l.RateType,
			RateCents: l.RateCents, Hours: l.Hours, ContractorOnly: l.ContractorOnly})
	}
	for _, o := range q.Overhead {
		in.Overhead = append(in.Overhead, OverheadLine{ID: o.ID, Label: o.Label, AmountType: o.AmountType,
			Value: o.Value, ContractorOnly: o.ContractorOnly})
	}
	return in
}

// Totals totals the quote under the customer's current decisions.
func (q *Quote) Totals() (Totals, error) { return Compute(q.TotalsInput()) }

// Settings are the contractor's own defaults and mark.
type Settings struct {
	CustomerID   uuid.UUID `json:"-"`
	DisplayName  string    `json:"display_name"`
	HasLogo      bool      `json:"has_logo"`
	PaymentTerms string    `json:"payment_terms"`
	DepositBPS   int64     `json:"deposit_bps"`
	Tax          Tax       `json:"tax"`
	Markup       Markup    `json:"markup"`
	RequireCode  bool      `json:"require_code"`
}

// Logo is the contractor's mark.
type Logo struct {
	Bytes       []byte
	ContentType string
}

// ApprovalEntry is one row of the approval log.
type ApprovalEntry struct {
	ID             uuid.UUID
	QuoteID        uuid.UUID
	QuoteRevision  int64
	Step           string
	ActorKind      string
	ActorName      string
	ActorContact   string
	ActorRef       string
	OccurredAt     time.Time
	TokenHash      string
	SignatureKind  string
	SignatureData  string
	SourceAddress  string
	UserAgent      string
	CodeVerified   bool
	Snapshot       string
	SnapshotSHA256 string
	TotalCents     *int64
	Note           string
}

// Actor is who acts on the contractor side.
type Actor struct {
	CustomerID uuid.UUID
	UserID     *uuid.UUID
	Name       string
	Email      string
	Role       string
}

// DealerActor is the desk user who acts on the dealer side.
//
// Ref is the desk user's id (the token's subject) and Name the display name
// the record keeps. Email is only used to find the display name on the staff
// roster; it is never stored and never shown.
type DealerActor struct {
	Ref   string
	Name  string
	Email string
}

// UnknownDeskUser is the display name of a desk user the staff roster does
// not name.
const UnknownDeskUser = "Desk user"

// Capability is something a portal role may do with customer quotes.
type Capability string

const (
	// CapQuoteCustomers lets a portal user build and send customer quotes.
	CapQuoteCustomers Capability = "quote_customers"
	// CapSeeMoney lets a portal user see costs, markups and margins.
	CapSeeMoney Capability = "see_money"
)

// Capabilities decides what a portal actor may do. One small seam: the
// contractor roles and capabilities of the portal replace RoleCapabilities
// here without touching the domain.
type Capabilities interface {
	Has(a Actor, c Capability) bool
}

// RoleCapabilities is the rule at this base: the Admin and Buyer roles hold
// both capabilities, View-Only holds neither.
type RoleCapabilities struct{}

// Has implements Capabilities.
func (RoleCapabilities) Has(a Actor, c Capability) bool {
	switch a.Role {
	case "Admin", "Buyer":
		return c == CapQuoteCustomers || c == CapSeeMoney
	}
	return false
}
