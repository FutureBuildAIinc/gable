// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// PaymentTermsRef is the slice of a payment terms row a customer carries.
type PaymentTermsRef struct {
	ID   uuid.UUID `json:"id"`
	Code string    `json:"code"`
	Name string    `json:"name"`
}

// Customer is the account: the wire type and the domain type. Money is
// integer cents; optional fields are present as null. The balance is read
// only on the wire: the AR core is its single writer (ADR 0005 section 9.3).
type Customer struct {
	ID              uuid.UUID `json:"id"`
	AccountNumber   string    `json:"account_number"`
	Name            string    `json:"name"`
	Email           *string   `json:"email"`
	Phone           *string   `json:"phone"`
	Address         *string   `json:"address"`
	IsActive        bool      `json:"is_active"`
	PrimaryBranchID uuid.UUID `json:"primary_branch_id"`
	// BranchIDs are the yards the customer trades at (ruling A): the home
	// yard is always among them, and the first document at another yard
	// adds it.
	BranchIDs []uuid.UUID `json:"branch_ids"`
	// PriceProfileID is the customer's pricing program (ADR 0006 section
	// 5.2): the profile names its price level and carries its category
	// rules. Null on the wire only in the migration's back pocket; the
	// column is NOT NULL and the create takes the default profile.
	PriceProfileID  *uuid.UUID `json:"price_profile_id"`
	SalespersonID   *uuid.UUID `json:"salesperson_id"`
	SalespersonName *string    `json:"salesperson_name"`

	// CreditLimitCents is null for no limit; zero is a limit of nothing.
	CreditLimitCents *httpx.Cents `json:"credit_limit_cents"`
	BalanceCents     httpx.Cents  `json:"balance_cents"`

	// Currency is the customer's override; null means the dealer default.
	// EffectiveCurrency is what a new document of this customer takes.
	Currency          *string `json:"currency"`
	EffectiveCurrency string  `json:"effective_currency"`

	PaymentTermsID uuid.UUID       `json:"payment_terms_id"`
	PaymentTerms   PaymentTermsRef `json:"payment_terms"`
	POrequired     bool            `json:"po_required"`

	// NamedBuyerRequired asks every sale on account at the counter to name a
	// buyer from the contacts flagged as authorized buyers. CheckLimitCents
	// is the largest check the counter takes from this customer; null defers
	// to the database wide default.
	NamedBuyerRequired bool         `json:"named_buyer_required"`
	CheckLimitCents    *httpx.Cents `json:"check_limit_cents"`

	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`
}

// EmailOrEmpty is the customer's email, or "" when there is none.
func (c *Customer) EmailOrEmpty() string {
	if c == nil || c.Email == nil {
		return ""
	}
	return *c.Email
}

// CreditLimitDollars is the credit limit in dollars for the callers that
// still compute in float dollars, and whether the customer has a limit at
// all (a nil limit is no limit).
func (c *Customer) CreditLimitDollars() (float64, bool) {
	if c == nil || c.CreditLimitCents == nil {
		return 0, false
	}
	return float64(*c.CreditLimitCents) / 100, true
}

// Escalation policy modes (migration 081), in the storage vocabulary the
// pricing exposure scanner reads. AUTO_ESCALATE requires a signed agreement;
// FLAG_FOR_REQUOTE is the safe default. On the wire the policy is lowercase.
const (
	PolicyAutoEscalate   = "AUTO_ESCALATE"
	PolicyFlagForRequote = "FLAG_FOR_REQUOTE"
	PolicyRequireAck     = "REQUIRE_ACK"
)

// policyNames is the wire vocabulary.
var policyNames = []string{"auto_escalate", "flag_for_requote", "require_ack"}

// PolicyMode is an escalation policy in its storage vocabulary, lowercase on
// the wire.
type PolicyMode string

func (p PolicyMode) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(p))), nil
}

// EscalationPolicy is the per-customer lumber-index price-protection policy
// surfaced at GET/PUT /api/v1/customers/{id}/escalation-policy. It lives on
// the customer row, so its revision is the customer's.
type EscalationPolicy struct {
	CustomerID        uuid.UUID        `json:"customer_id"`
	Policy            PolicyMode       `json:"policy"`
	ThresholdPercent  httpx.Quantity   `json:"threshold_percent"`
	AgreementSignedAt *httpx.Timestamp `json:"agreement_signed_at"`
	AgreementRef      *string          `json:"agreement_ref"`
	Revision          int64            `json:"revision"`
}

// ContactRole is a contact's role.
type ContactRole string

const (
	RoleBuyer     ContactRole = "Buyer"
	RoleAP        ContactRole = "AP"
	RoleOwner     ContactRole = "Owner"
	RoleSiteSuper ContactRole = "Site Super"
)

var roleNames = []string{string(RoleBuyer), string(RoleAP), string(RoleOwner), string(RoleSiteSuper)}

// Contact is a person at a customer, with the authority to place orders.
// OrderLimitCents is the largest order the contact may place (null: no
// limit of their own); CanPlaceOrders false means the contact may place none.
type Contact struct {
	ID              uuid.UUID    `json:"id"`
	CustomerID      uuid.UUID    `json:"customer_id"`
	FirstName       string       `json:"first_name"`
	LastName        string       `json:"last_name"`
	Title           *string      `json:"title"`
	Email           *string      `json:"email"`
	Phone           *string      `json:"phone"`
	Role            *ContactRole `json:"role"`
	IsPrimary       bool         `json:"is_primary"`
	IsActive        bool         `json:"is_active"`
	CanPlaceOrders  bool         `json:"can_place_orders"`
	OrderLimitCents *httpx.Cents `json:"order_limit_cents"`

	// AuthorizedBuyer puts the contact on the account's buyer roster at the
	// counter; BuyerSaleCapCents is the most this buyer may put on the
	// account in one sale (null: no cap of their own).
	AuthorizedBuyer   bool         `json:"authorized_buyer"`
	BuyerSaleCapCents *httpx.Cents `json:"buyer_sale_cap_cents"`

	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`
}

// ShipTo is a delivery address of a customer: many per customer, used by
// orders, invoices, delivery and tax (ADR 0005 section 7.3). TaxRatePercent,
// when set, is the rate a delivery to this address is taxed at.
type ShipTo struct {
	ID                   uuid.UUID       `json:"id"`
	CustomerID           uuid.UUID       `json:"customer_id"`
	Code                 string          `json:"code"`
	Name                 string          `json:"name"`
	Line1                string          `json:"line1"`
	Line2                *string         `json:"line2"`
	City                 *string         `json:"city"`
	Region               *string         `json:"region"`
	PostalCode           *string         `json:"postal_code"`
	Country              *string         `json:"country"`
	Phone                *string         `json:"phone"`
	DeliveryInstructions *string         `json:"delivery_instructions"`
	TaxRatePercent       *httpx.Quantity `json:"tax_rate_percent"`
	IsDefault            bool            `json:"is_default"`
	IsActive             bool            `json:"is_active"`

	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`
}

// TermsKind is how a payment terms row computes its due date.
type TermsKind string

const (
	TermsNetDays      TermsKind = "NET_DAYS"
	TermsDayOfMonth   TermsKind = "DAY_OF_MONTH"
	TermsDueOnReceipt TermsKind = "DUE_ON_RECEIPT"
)

var termsKindNames = []string{"net_days", "day_of_month", "due_on_receipt"}

func (k TermsKind) MarshalText() ([]byte, error) {
	return []byte(strings.ToLower(string(k))), nil
}

// ParseTermsKind maps a lowercase wire name to its kind.
func ParseTermsKind(name string) (TermsKind, bool) {
	for _, n := range termsKindNames {
		if name == n {
			return TermsKind(strings.ToUpper(name)), true
		}
	}
	return "", false
}

// PaymentTerms is a row of the payment terms master (ADR 0005 section 7.2).
type PaymentTerms struct {
	ID              uuid.UUID       `json:"id"`
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	Kind            TermsKind       `json:"kind"`
	NetDays         *int            `json:"net_days"`
	DayOfMonth      *int            `json:"day_of_month"`
	DiscountPercent *httpx.Quantity `json:"discount_percent"`
	DiscountDays    *int            `json:"discount_days"`
	IsActive        bool            `json:"is_active"`

	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`
}
