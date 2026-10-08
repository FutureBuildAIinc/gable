// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Every request here decodes loosely (strings, pointers, raw JSON) and is
// parsed by Parse into a draft, so a bad value is a field error with its path,
// collected with every other in one 400 (ADR 0001 section 4). Unknown fields
// are refused by httpx.DecodeJSON. A PUT replaces the fields the route
// lists: a field it leaves out takes its default (null for an optional one).

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	countryCode  = regexp.MustCompile(`^[A-Z]{2}$`)
	termsCode    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,39}$`)
)

func isAbsentRaw(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// optText trims an optional text field; an empty one is null.
func optText(v *httpx.Validator, field string, s *string, max int) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	v.Check(utf8.RuneCountInString(t) <= max, field, "must be at most "+itoa(max)+" characters")
	return &t
}

func reqText(v *httpx.Validator, field string, s *string, max int) string {
	if s == nil || strings.TrimSpace(*s) == "" {
		v.Check(false, field, "is required")
		return ""
	}
	t := strings.TrimSpace(*s)
	v.Check(utf8.RuneCountInString(t) <= max, field, "must be at most "+itoa(max)+" characters")
	return t
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func optEmail(v *httpx.Validator, field string, s *string) *string {
	e := optText(v, field, s, 254)
	if e != nil {
		at := strings.IndexByte(*e, '@')
		v.Check(at > 0 && at < len(*e)-1 && !strings.ContainsAny(*e, " \t\r\n"), field, "must be an email address")
	}
	return e
}

// optRevision reads the body revision; the header carries the other copy.
func optRevision(v *httpx.Validator, raw json.RawMessage) *int64 {
	if n, ok := v.Int("revision", raw, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		return &n
	}
	return nil
}

// ---- customers ----

// Request is the body of POST /customers and PUT /customers/{id}.
type Request struct {
	AccountNumber    *string         `json:"account_number"`
	Name             *string         `json:"name"`
	Email            *string         `json:"email"`
	Phone            *string         `json:"phone"`
	Address          *string         `json:"address"`
	Tier             *string         `json:"tier"`
	IsActive         *bool           `json:"is_active"`
	PrimaryBranchID  *string         `json:"primary_branch_id"`
	PriceLevelID     *string         `json:"price_level_id"`
	SalespersonID    *string         `json:"salesperson_id"`
	CreditLimitCents json.RawMessage `json:"credit_limit_cents"`
	Currency         *string         `json:"currency"`
	PaymentTermsID   *string         `json:"payment_terms_id"`
	POrequired       *bool           `json:"po_required"`
	Revision         json.RawMessage `json:"revision"`
}

// Draft is a validated customer header.
type Draft struct {
	AccountNumber    string
	Name             string
	Email            *string
	Phone            *string
	Address          *string
	Tier             CustomerTier
	IsActive         bool
	PrimaryBranchID  *uuid.UUID
	PriceLevelID     *uuid.UUID
	SalespersonID    *uuid.UUID
	CreditLimitCents *httpx.Cents
	Currency         *string
	PaymentTermsID   *uuid.UUID // nil: the default terms on create
	POrequired       bool
	Revision         *int64
}

func (req *Request) Parse() (*Draft, error)       { return req.parse(false) }
func (req *Request) ParseUpdate() (*Draft, error) { return req.parse(true) }

func (req *Request) parse(update bool) (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{Tier: TierRetail, IsActive: true}

	d.AccountNumber = reqText(v, "account_number", req.AccountNumber, 40)
	d.Name = reqText(v, "name", req.Name, 200)
	d.Email = optEmail(v, "email", req.Email)
	d.Phone = optText(v, "phone", req.Phone, 40)
	d.Address = optText(v, "address", req.Address, 500)

	if req.Tier != nil {
		if t, ok := ParseTier(*req.Tier); ok {
			d.Tier = t
		} else {
			v.Check(false, "tier", "must be one of: "+strings.Join(tierNames, ", "))
		}
	}
	if req.IsActive != nil {
		d.IsActive = *req.IsActive
	}
	if req.POrequired != nil {
		d.POrequired = *req.POrequired
	}

	if update {
		// A replace may not reset a control by leaving it out.
		v.Check(req.PaymentTermsID != nil, "payment_terms_id", "is required on a PUT: a left out field would reset the customer's terms")
		v.Check(req.POrequired != nil, "po_required", "is required on a PUT: a left out field would clear the purchase order requirement")
		v.Check(req.PrimaryBranchID == nil, "primary_branch_id", "cannot be changed by an edit: it is fixed when the customer is created")
		d.Revision = optRevision(v, req.Revision)
	} else {
		v.Check(isAbsentRaw(req.Revision), "revision", "is not accepted on create: a new customer has no revision to precondition on")
		if id, ok := v.UUID("primary_branch_id", req.PrimaryBranchID, false); ok {
			d.PrimaryBranchID = &id
		}
	}
	if id, ok := v.UUID("price_level_id", req.PriceLevelID, false); ok {
		d.PriceLevelID = &id
	}
	if id, ok := v.UUID("salesperson_id", req.SalespersonID, false); ok {
		d.SalespersonID = &id
	}
	if id, ok := v.UUID("payment_terms_id", req.PaymentTermsID, false); ok {
		d.PaymentTermsID = &id
	}
	if n, ok := v.Int("credit_limit_cents", req.CreditLimitCents, false); ok {
		v.Check(n >= 0, "credit_limit_cents", "must not be negative; send null for no limit")
		c := httpx.Cents(n)
		d.CreditLimitCents = &c
	}
	if req.Currency != nil {
		c := strings.TrimSpace(*req.Currency)
		if currencyCode.MatchString(c) {
			d.Currency = &c
		} else {
			v.Check(false, "currency", "must be an ISO 4217 code of three capital letters, or null for the dealer default")
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// SalespersonRequest is the body of PATCH /customers/{id}/salesperson. The
// key must be present; null unassigns.
type SalespersonRequest struct {
	SalespersonID json.RawMessage `json:"salesperson_id"`
	Revision      json.RawMessage `json:"revision"`
}

// SalespersonDraft is a validated salesperson change.
type SalespersonDraft struct {
	SalespersonID *uuid.UUID
	Revision      *int64
}

func (req *SalespersonRequest) Parse() (*SalespersonDraft, error) {
	v := &httpx.Validator{}
	d := &SalespersonDraft{}
	t := bytes.TrimSpace(req.SalespersonID)
	switch {
	case len(t) == 0:
		v.Check(false, "salesperson_id", "is required: send a salesperson id, or null to unassign")
	case bytes.Equal(t, []byte("null")):
	default:
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			v.Check(false, "salesperson_id", "must be a UUID")
		} else if id, ok := v.UUID("salesperson_id", &s, true); ok {
			d.SalespersonID = &id
		}
	}
	d.Revision = optRevision(v, req.Revision)
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// ---- escalation policy ----

// PolicyRequest is the body of PUT /customers/{id}/escalation-policy.
type PolicyRequest struct {
	Policy            *string         `json:"policy"`
	ThresholdPercent  json.RawMessage `json:"threshold_percent"`
	AgreementSignedAt json.RawMessage `json:"agreement_signed_at"`
	AgreementRef      *string         `json:"agreement_ref"`
	Revision          json.RawMessage `json:"revision"`
}

// PolicyDraft is a validated policy change.
type PolicyDraft struct {
	Policy            PolicyMode
	ThresholdPercent  httpx.Quantity
	AgreementSignedAt *httpx.Timestamp
	AgreementRef      *string
	Revision          *int64
}

// thresholdMax is the largest escalation threshold, 50 percent.
const thresholdMax httpx.Quantity = 50 * 10000

func (req *PolicyRequest) Parse() (*PolicyDraft, error) {
	v := &httpx.Validator{}
	d := &PolicyDraft{}
	switch {
	case req.Policy == nil:
		v.Check(false, "policy", "is required")
	default:
		ok := false
		for _, n := range policyNames {
			if *req.Policy == n {
				d.Policy = PolicyMode(strings.ToUpper(n))
				ok = true
			}
		}
		v.Check(ok, "policy", "must be one of: "+strings.Join(policyNames, ", "))
	}
	if q, ok := v.Quantity("threshold_percent", req.ThresholdPercent, true); ok {
		v.Check(q > 0 && q <= thresholdMax, "threshold_percent", "must be greater than 0 and at most 50")
		d.ThresholdPercent = q
	}
	if ts, ok := v.Timestamp("agreement_signed_at", req.AgreementSignedAt, false); ok {
		d.AgreementSignedAt = ts
	}
	d.AgreementRef = optText(v, "agreement_ref", req.AgreementRef, 200)
	d.Revision = optRevision(v, req.Revision)
	if d.Policy == PolicyMode(PolicyAutoEscalate) && d.AgreementSignedAt == nil {
		v.Check(false, "agreement_signed_at", "is required for auto_escalate: it needs a signed escalation agreement")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// ---- contacts ----

// ContactRequest is the body of POST /customers/{id}/contacts and PUT /contacts/{id}.
type ContactRequest struct {
	FirstName       *string         `json:"first_name"`
	LastName        *string         `json:"last_name"`
	Title           *string         `json:"title"`
	Email           *string         `json:"email"`
	Phone           *string         `json:"phone"`
	Role            *string         `json:"role"`
	IsPrimary       *bool           `json:"is_primary"`
	IsActive        *bool           `json:"is_active"`
	CanPlaceOrders  *bool           `json:"can_place_orders"`
	OrderLimitCents json.RawMessage `json:"order_limit_cents"`
	Revision        json.RawMessage `json:"revision"`
}

// ContactDraft is a validated contact.
type ContactDraft struct {
	FirstName       string
	LastName        string
	Title           *string
	Email           *string
	Phone           *string
	Role            *ContactRole
	IsPrimary       bool
	IsActive        bool
	CanPlaceOrders  bool
	OrderLimitCents *httpx.Cents
	Revision        *int64
}

func (req *ContactRequest) Parse(update bool) (*ContactDraft, error) {
	v := &httpx.Validator{}
	d := &ContactDraft{IsActive: true, CanPlaceOrders: true}
	d.FirstName = reqText(v, "first_name", req.FirstName, 100)
	d.LastName = reqText(v, "last_name", req.LastName, 100)
	d.Title = optText(v, "title", req.Title, 100)
	d.Email = optEmail(v, "email", req.Email)
	d.Phone = optText(v, "phone", req.Phone, 40)
	if r := optText(v, "role", req.Role, 40); r != nil {
		v.Enum("role", *r, roleNames...)
		role := ContactRole(*r)
		d.Role = &role
	}
	if req.IsPrimary != nil {
		d.IsPrimary = *req.IsPrimary
	}
	if req.IsActive != nil {
		d.IsActive = *req.IsActive
	}
	if req.CanPlaceOrders != nil {
		d.CanPlaceOrders = *req.CanPlaceOrders
	}
	if update {
		// A replace may not reset a control by leaving it out; an explicit
		// null order limit is still "no limit of the contact's own".
		v.Check(req.CanPlaceOrders != nil, "can_place_orders", "is required on a PUT: a left out field would re-grant order authority")
		v.Check(len(bytes.TrimSpace(req.OrderLimitCents)) > 0, "order_limit_cents", "is required on a PUT: send the limit, or null for no limit of the contact's own")
	}
	if n, ok := v.Int("order_limit_cents", req.OrderLimitCents, false); ok {
		v.Check(n >= 0, "order_limit_cents", "must not be negative; send null for no limit of the contact's own")
		c := httpx.Cents(n)
		d.OrderLimitCents = &c
	}
	if update {
		d.Revision = optRevision(v, req.Revision)
	} else {
		v.Check(isAbsentRaw(req.Revision), "revision", "is not accepted on create: a new contact has no revision to precondition on")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// ---- ship-tos ----

// ShipToRequest is the body of POST /customers/{id}/ship-tos and PUT /ship-tos/{id}.
type ShipToRequest struct {
	Code                 *string         `json:"code"`
	Name                 *string         `json:"name"`
	Line1                *string         `json:"line1"`
	Line2                *string         `json:"line2"`
	City                 *string         `json:"city"`
	Region               *string         `json:"region"`
	PostalCode           *string         `json:"postal_code"`
	Country              *string         `json:"country"`
	Phone                *string         `json:"phone"`
	DeliveryInstructions *string         `json:"delivery_instructions"`
	TaxRatePercent       json.RawMessage `json:"tax_rate_percent"`
	IsDefault            *bool           `json:"is_default"`
	IsActive             *bool           `json:"is_active"`
	Revision             json.RawMessage `json:"revision"`
}

// ShipToDraft is a validated ship-to.
type ShipToDraft struct {
	Code                 string
	Name                 string
	Line1                string
	Line2                *string
	City                 *string
	Region               *string
	PostalCode           *string
	Country              *string
	Phone                *string
	DeliveryInstructions *string
	TaxRatePercent       *httpx.Quantity
	IsDefault            bool
	IsActive             bool
	// DefaultGiven is true when the request named is_default, so a create
	// that leaves it out can make the customer's first ship-to the default.
	DefaultGiven bool
	Revision     *int64
}

// percentMax is 100 percent at the quantity scale.
const percentMax httpx.Quantity = 100 * 10000

func (req *ShipToRequest) Parse(update bool) (*ShipToDraft, error) {
	v := &httpx.Validator{}
	d := &ShipToDraft{IsActive: true}
	d.Code = reqText(v, "code", req.Code, 40)
	d.Name = reqText(v, "name", req.Name, 200)
	d.Line1 = reqText(v, "line1", req.Line1, 200)
	d.Line2 = optText(v, "line2", req.Line2, 200)
	d.City = optText(v, "city", req.City, 100)
	d.Region = optText(v, "region", req.Region, 100)
	d.PostalCode = optText(v, "postal_code", req.PostalCode, 20)
	if c := optText(v, "country", req.Country, 2); c != nil {
		v.Check(countryCode.MatchString(*c), "country", "must be an ISO 3166 alpha-2 code of two capital letters")
		d.Country = c
	}
	d.Phone = optText(v, "phone", req.Phone, 40)
	d.DeliveryInstructions = optText(v, "delivery_instructions", req.DeliveryInstructions, 1000)
	if q, ok := v.Quantity("tax_rate_percent", req.TaxRatePercent, false); ok {
		v.Check(q >= 0 && q <= percentMax, "tax_rate_percent", "must be between 0 and 100")
		d.TaxRatePercent = &q
	}
	if req.IsDefault != nil {
		d.IsDefault, d.DefaultGiven = *req.IsDefault, true
	}
	if req.IsActive != nil {
		d.IsActive = *req.IsActive
	}
	v.Check(!(d.IsDefault && !d.IsActive), "is_default", "an inactive ship-to cannot be the default")
	if update {
		d.Revision = optRevision(v, req.Revision)
	} else {
		v.Check(isAbsentRaw(req.Revision), "revision", "is not accepted on create: a new ship-to has no revision to precondition on")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

// ---- payment terms ----

// TermsRequest is the body of POST /payment-terms and PUT /payment-terms/{id}.
type TermsRequest struct {
	Code            *string         `json:"code"`
	Name            *string         `json:"name"`
	Kind            *string         `json:"kind"`
	NetDays         json.RawMessage `json:"net_days"`
	DayOfMonth      json.RawMessage `json:"day_of_month"`
	DiscountPercent json.RawMessage `json:"discount_percent"`
	DiscountDays    json.RawMessage `json:"discount_days"`
	IsActive        *bool           `json:"is_active"`
	Revision        json.RawMessage `json:"revision"`
}

// TermsDraft is a validated payment terms row.
type TermsDraft struct {
	Code            string
	Name            string
	Kind            TermsKind
	NetDays         *int
	DayOfMonth      *int
	DiscountPercent *httpx.Quantity
	DiscountDays    *int
	IsActive        bool
	Revision        *int64
}

func (req *TermsRequest) Parse(update bool) (*TermsDraft, error) {
	v := &httpx.Validator{}
	d := &TermsDraft{IsActive: true}
	d.Code = reqText(v, "code", req.Code, 40)
	if d.Code != "" {
		v.Check(termsCode.MatchString(d.Code), "code", "must be capital letters, digits, underscore or hyphen, starting with a letter or digit")
	}
	d.Name = reqText(v, "name", req.Name, 100)
	if req.Kind == nil {
		v.Check(false, "kind", "is required")
	} else if k, ok := ParseTermsKind(*req.Kind); ok {
		d.Kind = k
	} else {
		v.Check(false, "kind", "must be one of: "+strings.Join(termsKindNames, ", "))
	}
	netDays := optInt(v, "net_days", req.NetDays, 0, 3650)
	dom := optInt(v, "day_of_month", req.DayOfMonth, 1, 31)
	discDays := optInt(v, "discount_days", req.DiscountDays, 0, 3650)
	if q, ok := v.Quantity("discount_percent", req.DiscountPercent, false); ok {
		v.Check(q > 0 && q <= percentMax, "discount_percent", "must be greater than 0 and at most 100")
		d.DiscountPercent = &q
	}
	d.DiscountDays = discDays
	v.Check((d.DiscountPercent == nil) == (d.DiscountDays == nil), "discount_percent",
		"discount_percent and discount_days go together: send both or neither")

	switch d.Kind {
	case TermsNetDays:
		v.Check(netDays != nil, "net_days", "is required for net_days terms")
		v.Check(dom == nil, "day_of_month", "applies only to day_of_month terms")
	case TermsDayOfMonth:
		v.Check(dom != nil, "day_of_month", "is required for day_of_month terms")
		v.Check(netDays == nil, "net_days", "applies only to net_days terms")
	case TermsDueOnReceipt:
		v.Check(netDays == nil, "net_days", "does not apply to due_on_receipt terms")
		v.Check(dom == nil, "day_of_month", "does not apply to due_on_receipt terms")
	}
	d.NetDays, d.DayOfMonth = netDays, dom
	if req.IsActive != nil {
		d.IsActive = *req.IsActive
	}
	if update {
		d.Revision = optRevision(v, req.Revision)
	} else {
		v.Check(isAbsentRaw(req.Revision), "revision", "is not accepted on create: new terms have no revision to precondition on")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func optInt(v *httpx.Validator, field string, raw json.RawMessage, min, max int64) *int {
	n, ok := v.Int(field, raw, false)
	if !ok {
		return nil
	}
	v.Check(n >= min && n <= max, field, "must be between "+itoa(int(min))+" and "+itoa(int(max)))
	i := int(n)
	return &i
}
