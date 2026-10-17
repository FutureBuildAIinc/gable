// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// QuoteInput is the body of a create and of an update (an update replaces the
// whole draft). Money is cents, quantities are decimal strings, markups are
// basis points. Cost and sell prices are never an input: the server prices
// every line from the dealer.
type QuoteInput struct {
	Title          string           `json:"title"`
	ProjectID      *string          `json:"project_id"`
	SourceQuoteID  *string          `json:"source_quote_id"`
	JobSiteAddress string           `json:"job_site_address"`
	EndCustomer    Contact          `json:"end_customer"`
	SiteContact    Contact          `json:"site_contact"`
	Markup         *Markup          `json:"markup"`
	HideLinePrices bool             `json:"hide_line_prices"`
	Tax            *Tax             `json:"tax"`
	PaymentTerms   *string          `json:"payment_terms"`
	DepositBPS     *int64           `json:"deposit_bps"`
	CustomerNote   string           `json:"customer_note"`
	RequireCode    *bool            `json:"require_code"`
	ValidUntil     *httpx.Timestamp `json:"valid_until"`
	Groups         []GroupInput     `json:"groups"`
	Lines          []LineInput      `json:"lines"`
	CategoryRules  []RuleInput      `json:"category_rules"`
	Labour         []LabourInput    `json:"labour"`
	Overhead       []OverheadInput  `json:"overhead"`
}

// GroupInput is an option group; Key is the caller's own handle that lines
// name in group_key.
type GroupInput struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// LineInput is a material line. ID names an existing line of the quote whose
// captured cost is kept while its product and quantity are unchanged.
// SourceLineID takes the cost from a line of the source dealer quote.
type LineInput struct {
	ID           *string        `json:"id"`
	ProductID    string         `json:"product_id"`
	SourceLineID *string        `json:"source_line_id"`
	Quantity     httpx.Quantity `json:"quantity"`
	GroupKey     *string        `json:"group_key"`
	Markup       *Markup        `json:"markup"`
	Note         string         `json:"note"`
}

// RuleInput is a category markup rule.
type RuleInput struct {
	CategoryID string `json:"category_id"`
	Markup     Markup `json:"markup"`
}

// LabourInput is a labour line.
type LabourInput struct {
	Description    string         `json:"description"`
	RateType       string         `json:"rate_type"`
	RateCents      int64          `json:"rate_cents"`
	Hours          httpx.Quantity `json:"hours"`
	ContractorOnly bool           `json:"contractor_only"`
}

// OverheadInput is an overhead line.
type OverheadInput struct {
	Label          string `json:"label"`
	AmountType     string `json:"amount_type"`
	Value          int64  `json:"value"`
	ContractorOnly bool   `json:"contractor_only"`
}

// Limits on one quote.
const (
	maxLines    = 200
	maxLabour   = 50
	maxOverhead = 50
	maxGroups   = 50
	maxRules    = 100
)

var phoneShape = regexp.MustCompile(`^[0-9+()\-.\s]{7,40}$`)

func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s, "@")
}

func validPhone(s string) bool {
	if !phoneShape.MatchString(s) {
		return false
	}
	digits := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits >= 7 && digits <= 15
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// Draft is a validated input with its identifiers parsed. Defaults the input
// left out are filled from the contractor's settings by the service.
type Draft struct {
	Title          string
	ProjectID      *uuid.UUID
	SourceQuoteID  *uuid.UUID
	JobSiteAddress string
	EndCustomer    Contact
	SiteContact    Contact
	Markup         *Markup
	HideLinePrices bool
	Tax            *Tax
	PaymentTerms   *string
	DepositBPS     *int64
	CustomerNote   string
	RequireCode    *bool
	ValidUntil     *time.Time
	Groups         []GroupInput
	Lines          []DraftLine
	CategoryRules  []CategoryRule
	Labour         []LabourInput
	Overhead       []OverheadInput
}

// DraftLine is a validated material line.
type DraftLine struct {
	ExistingID   *uuid.UUID
	ProductID    uuid.UUID
	SourceLineID *uuid.UUID
	Quantity     httpx.Quantity
	GroupKey     string
	Override     *Markup
	Note         string
}

// Parse validates the input and returns the draft, or a 400 naming every
// field at fault.
func (in *QuoteInput) Parse() (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{
		Title: strings.TrimSpace(in.Title), JobSiteAddress: strings.TrimSpace(in.JobSiteAddress),
		EndCustomer: trimContact(in.EndCustomer), SiteContact: trimContact(in.SiteContact),
		Markup: in.Markup, HideLinePrices: in.HideLinePrices, Tax: in.Tax, PaymentTerms: in.PaymentTerms,
		DepositBPS: in.DepositBPS, CustomerNote: strings.TrimSpace(in.CustomerNote), RequireCode: in.RequireCode,
		Groups: in.Groups, Labour: in.Labour, Overhead: in.Overhead,
	}
	v.Check(runes(d.Title) >= 1 && runes(d.Title) <= 200, "title", "is required and at most 200 characters")
	v.Check(runes(d.JobSiteAddress) <= 500, "job_site_address", "is at most 500 characters")
	v.Check(runes(d.CustomerNote) <= 4000, "customer_note", "is at most 4000 characters")
	v.Check(runes(d.EndCustomer.Name) >= 1 && runes(d.EndCustomer.Name) <= 200, "end_customer.name", "is required and at most 200 characters")
	v.Check(d.EndCustomer.Email == "" || validEmail(d.EndCustomer.Email), "end_customer.email", "is not an email address")
	v.Check(d.EndCustomer.Phone == "" || validPhone(d.EndCustomer.Phone), "end_customer.phone", "is not a phone number")
	v.Check(runes(d.SiteContact.Name) <= 200, "site_contact.name", "is at most 200 characters")
	v.Check(d.SiteContact.Phone == "" || validPhone(d.SiteContact.Phone), "site_contact.phone", "is not a phone number")
	v.Check(d.SiteContact.Email == "", "site_contact.email", "is not accepted: a site contact has a name and a phone")

	if in.ProjectID != nil {
		if id, ok := v.UUID("project_id", in.ProjectID, false); ok {
			d.ProjectID = &id
		}
	}
	if in.SourceQuoteID != nil {
		if id, ok := v.UUID("source_quote_id", in.SourceQuoteID, false); ok {
			d.SourceQuoteID = &id
		}
	}
	if in.Markup != nil {
		if err := in.Markup.Validate(); err != nil {
			v.Check(false, "markup", err.Error())
		}
	}
	if in.Tax != nil {
		switch in.Tax.Mode {
		case TaxSiteRate:
			v.Check(in.Tax.RateBPS >= 0 && in.Tax.RateBPS <= MaxTaxRateBPS, "tax.rate_bps", fmt.Sprintf("must be between 0 and %d", MaxTaxRateBPS))
		case TaxIncluded, TaxAtCheckout:
			v.Check(in.Tax.RateBPS == 0, "tax.rate_bps", "must be 0 unless the mode is site_rate")
		default:
			v.Check(false, "tax.mode", "must be one of: site_rate, included, at_checkout")
		}
	}
	if in.PaymentTerms != nil {
		v.Check(runes(*in.PaymentTerms) <= 1000, "payment_terms", "is at most 1000 characters")
	}
	if in.DepositBPS != nil {
		v.Check(*in.DepositBPS >= 0 && *in.DepositBPS <= BPSWhole, "deposit_bps", "must be between 0 and 10000")
	}
	if in.ValidUntil != nil {
		t := in.ValidUntil.Time
		d.ValidUntil = &t
	}

	v.Check(len(in.Groups) <= maxGroups, "groups", fmt.Sprintf("at most %d", maxGroups))
	keys := map[string]bool{}
	for i, g := range in.Groups {
		f := fmt.Sprintf("groups[%d]", i)
		v.Check(g.Key != "" && len(g.Key) <= 64, f+".key", "is required and at most 64 characters")
		v.Check(!keys[g.Key], f+".key", "is repeated")
		keys[g.Key] = true
		v.Check(runes(strings.TrimSpace(g.Label)) >= 1 && runes(g.Label) <= 200, f+".label", "is required and at most 200 characters")
	}
	v.Check(len(in.Lines) <= maxLines, "lines", fmt.Sprintf("at most %d", maxLines))
	seen := map[uuid.UUID]bool{}
	for i, l := range in.Lines {
		f := fmt.Sprintf("lines[%d]", i)
		dl := DraftLine{Quantity: l.Quantity, Override: l.Markup, Note: strings.TrimSpace(l.Note)}
		pid := l.ProductID
		if id, ok := v.UUID(f+".product_id", &pid, true); ok {
			dl.ProductID = id
		}
		if l.ID != nil {
			if id, ok := v.UUID(f+".id", l.ID, false); ok {
				dl.ExistingID = &id
				v.Check(!seen[id], f+".id", "is repeated")
				seen[id] = true
			}
		}
		if l.SourceLineID != nil {
			if id, ok := v.UUID(f+".source_line_id", l.SourceLineID, false); ok {
				dl.SourceLineID = &id
				v.Check(d.SourceQuoteID != nil, f+".source_line_id", "needs a source_quote_id")
			}
		}
		v.Check(l.Quantity > 0, f+".quantity", "must be positive")
		v.Check(runes(dl.Note) <= 1000, f+".note", "is at most 1000 characters")
		if l.Markup != nil {
			if err := l.Markup.Validate(); err != nil {
				v.Check(false, f+".markup", err.Error())
			}
		}
		if l.GroupKey != nil {
			dl.GroupKey = *l.GroupKey
			v.Check(keys[dl.GroupKey], f+".group_key", "names no group")
		}
		d.Lines = append(d.Lines, dl)
	}
	v.Check(len(in.CategoryRules) <= maxRules, "category_rules", fmt.Sprintf("at most %d", maxRules))
	ruleSeen := map[uuid.UUID]bool{}
	for i, r := range in.CategoryRules {
		f := fmt.Sprintf("category_rules[%d]", i)
		cid := r.CategoryID
		if id, ok := v.UUID(f+".category_id", &cid, true); ok {
			v.Check(!ruleSeen[id], f+".category_id", "is repeated")
			ruleSeen[id] = true
			d.CategoryRules = append(d.CategoryRules, CategoryRule{CategoryID: id, Markup: r.Markup})
		}
		if err := r.Markup.Validate(); err != nil {
			v.Check(false, f+".markup", err.Error())
		}
	}
	v.Check(len(in.Labour) <= maxLabour, "labour", fmt.Sprintf("at most %d", maxLabour))
	for i, l := range in.Labour {
		f := fmt.Sprintf("labour[%d]", i)
		v.Check(runes(strings.TrimSpace(l.Description)) >= 1 && runes(l.Description) <= 500, f+".description", "is required and at most 500 characters")
		if _, err := LabourTotalCents(LabourLine{RateType: l.RateType, RateCents: l.RateCents, Hours: l.Hours}); err != nil {
			v.Check(false, f, err.Error())
		}
		v.Check(l.RateType == RateHourly || l.Hours == 0, f+".hours", "is only for an hourly line")
	}
	v.Check(len(in.Overhead) <= maxOverhead, "overhead", fmt.Sprintf("at most %d", maxOverhead))
	for i, o := range in.Overhead {
		f := fmt.Sprintf("overhead[%d]", i)
		v.Check(runes(strings.TrimSpace(o.Label)) >= 1 && runes(o.Label) <= 200, f+".label", "is required and at most 200 characters")
		if _, err := OverheadTotalCents(OverheadLine{AmountType: o.AmountType, Value: o.Value}, 0); err != nil {
			v.Check(false, f, err.Error())
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func trimContact(c Contact) Contact {
	return Contact{Name: strings.TrimSpace(c.Name), Email: strings.TrimSpace(c.Email), Phone: strings.TrimSpace(c.Phone)}
}
