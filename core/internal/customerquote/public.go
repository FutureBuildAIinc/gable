// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Price wall 2 (contractor to customer). Everything the capability link and
// the customer PDF show is built from the types in this file and from no
// other. They are an allowlist: there is no field for the dealer's cost, a
// markup, basis points, a margin or a contractor only line, so no code path
// can put one on the wire. Adding a field here is a deliberate act, and the
// wire test (wall2_wire_test.go) fails until its allowlist knows it too.

// PublicView is what the customer sees at their link.
type PublicView struct {
	Reference         string           `json:"reference"`
	Title             string           `json:"title"`
	Status            string           `json:"status"`
	SelectionsVersion int64            `json:"selections_version"`
	Contractor        PublicContractor `json:"contractor"`
	CustomerName      string           `json:"customer_name"`
	JobSiteAddress    string           `json:"job_site_address"`
	ValidUntil        httpx.Timestamp  `json:"valid_until"`
	Note              string           `json:"note"`
	PaymentTerms      string           `json:"payment_terms"`
	HidePrices        bool             `json:"hide_line_prices"`
	Groups            []PublicGroup    `json:"groups"`
	Lines             []PublicLine     `json:"lines"`
	Extras            []PublicExtra    `json:"extras"`
	Totals            PublicTotals     `json:"totals"`
	RequiresCode      bool             `json:"requires_code"`
	CanDecide         bool             `json:"can_decide"`
	Notice            string           `json:"notice,omitempty"`
	Approval          *PublicApproval  `json:"approval,omitempty"`
	Fulfilment        *string          `json:"fulfilment,omitempty"`
}

// PublicContractor is the contractor's mark.
type PublicContractor struct {
	Name    string `json:"name"`
	HasLogo bool   `json:"has_logo"`
}

// PublicGroup is a choose one of several set.
type PublicGroup struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
}

// PublicLine is one material selection. PriceCents is absent when the
// contractor shows a lump sum.
type PublicLine struct {
	ID          uuid.UUID      `json:"id"`
	GroupID     *uuid.UUID     `json:"group_id,omitempty"`
	Description string         `json:"description"`
	Quantity    httpx.Quantity `json:"quantity"`
	UOM         string         `json:"uom"`
	Note        string         `json:"note,omitempty"`
	PriceCents  *int64         `json:"price_cents,omitempty"`
	Selection   string         `json:"selection"`
}

// PublicExtra is a labour or overhead line the customer pays. A contractor
// only line is not here at all.
type PublicExtra struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	PriceCents  *int64    `json:"price_cents,omitempty"`
}

// PublicTotals are the customer's figures.
type PublicTotals struct {
	SubtotalCents int64  `json:"subtotal_cents"`
	TaxCents      int64  `json:"tax_cents"`
	TotalCents    int64  `json:"total_cents"`
	DepositCents  int64  `json:"deposit_cents"`
	TaxMode       string `json:"tax_mode"`
	TaxRateBPS    int64  `json:"tax_rate_bps,omitempty"`
	TaxNote       string `json:"tax_note"`
}

// PublicApproval is the customer's own recorded decision.
type PublicApproval struct {
	Decision string          `json:"decision"`
	SignedBy string          `json:"signed_by"`
	At       httpx.Timestamp `json:"at"`
}

// Fulfilment states a customer sees for the dealer's order. The order's own
// status vocabulary, its numbers and its prices stay with the contractor and
// the dealer.
const (
	FulfilmentOrdered   = "ordered"
	FulfilmentPreparing = "being_prepared"
	FulfilmentFulfilled = "fulfilled"
	FulfilmentCancelled = "cancelled"
)

// FulfilmentOf maps an order status onto the customer's coarse states.
func FulfilmentOf(orderStatus string) string {
	switch orderStatus {
	case "FULFILLED":
		return FulfilmentFulfilled
	case "CANCELLED":
		return FulfilmentCancelled
	case "BACKORDERED", "ON_HOLD":
		return FulfilmentPreparing
	default:
		return FulfilmentOrdered
	}
}

// CodesBlockedNotice is what the customer's page says when the contractor asks
// for a verification code the server cannot send.
const CodesBlockedNotice = "This quote cannot be accepted yet. The contractor asked for a verification code, and this server cannot send one. Ask the contractor to turn off the code requirement and send the quote again."

// taxNote words the tax treatment for the customer.
func taxNote(t Tax) string {
	switch t.Mode {
	case TaxIncluded:
		return "Tax is included in the prices shown."
	case TaxAtCheckout:
		return "Tax on the materials is added when you pay."
	default:
		return "Tax is calculated on the materials at the site rate."
	}
}

// PublicInputs are what the view needs besides the quote.
type PublicInputs struct {
	ContractorName string
	HasLogo        bool
	OrderStatus    string // the linked order's status, "" when none
	Approval       *ApprovalEntry
	Now            time.Time
	// CodesBlocked: the quote asks for a verification code and the server
	// cannot send one, so it cannot be accepted until the contractor turns the
	// requirement off.
	CodesBlocked bool
}

func publicStatus(q *Quote, now time.Time) string {
	switch q.Effective(now) {
	case StatusSent:
		return "open"
	case StatusExpired:
		return "expired"
	case StatusApproved, StatusConfirmed:
		return string(q.Effective(now))
	case StatusDeclined:
		return "declined"
	default:
		return "withdrawn"
	}
}

// BuildPublicView builds the customer's view of a quote under the
// customer's current decisions. It is the only constructor of PublicView.
func BuildPublicView(q *Quote, in PublicInputs) (*PublicView, error) {
	t, err := q.Totals()
	if err != nil {
		return nil, err
	}
	ver := q.SelectionsVersion
	if ver < 1 {
		ver = 1
	}
	v := &PublicView{
		Reference:         q.Reference,
		Title:             q.Title,
		Status:            publicStatus(q, in.Now),
		SelectionsVersion: ver,
		Contractor:        PublicContractor{Name: in.ContractorName, HasLogo: in.HasLogo},
		CustomerName:      q.EndCustomer.Name,
		JobSiteAddress:    q.JobSiteAddress,
		ValidUntil:        httpx.TimestampOf(q.ValidUntil),
		Note:              q.CustomerNote,
		PaymentTerms:      q.PaymentTerms,
		HidePrices:        q.HideLinePrices,
		Groups:            []PublicGroup{},
		Lines:             []PublicLine{},
		Extras:            []PublicExtra{},
		RequiresCode:      q.RequireCode,
		CanDecide:         q.Effective(in.Now) == StatusSent,
		Totals: PublicTotals{
			SubtotalCents: t.SubtotalCents, TaxCents: t.TaxCents, TotalCents: t.TotalCents,
			DepositCents: t.DepositCents, TaxMode: q.Tax.Mode, TaxNote: taxNote(q.Tax),
		},
	}
	if q.Tax.Mode == TaxSiteRate {
		v.Totals.TaxRateBPS = q.Tax.RateBPS
	}
	for _, g := range q.Groups {
		v.Groups = append(v.Groups, PublicGroup{ID: g.ID, Label: g.Label})
	}
	for _, l := range q.Lines {
		pl := PublicLine{ID: l.ID, GroupID: l.GroupID, Description: l.Description, Quantity: l.Quantity,
			UOM: l.UOM, Note: l.Note, Selection: string(l.Selection)}
		if !q.HideLinePrices {
			p := l.SellCents
			pl.PriceCents = &p
		}
		v.Lines = append(v.Lines, pl)
	}
	for _, l := range q.Labour {
		if l.ContractorOnly {
			continue
		}
		amt, err := LabourTotalCents(LabourLine{RateType: l.RateType, RateCents: l.RateCents, Hours: l.Hours})
		if err != nil {
			return nil, err
		}
		e := PublicExtra{ID: l.ID, Kind: "labour", Description: l.Description}
		if !q.HideLinePrices {
			e.PriceCents = &amt
		}
		v.Extras = append(v.Extras, e)
	}
	base := t.MaterialSellCents + t.LabourCents
	for _, o := range q.Overhead {
		if o.ContractorOnly {
			continue
		}
		amt, err := OverheadTotalCents(OverheadLine{AmountType: o.AmountType, Value: o.Value}, base)
		if err != nil {
			return nil, err
		}
		e := PublicExtra{ID: o.ID, Kind: "overhead", Description: o.Label}
		if !q.HideLinePrices {
			e.PriceCents = &amt
		}
		v.Extras = append(v.Extras, e)
	}
	if in.CodesBlocked {
		v.Notice = CodesBlockedNotice
	}
	if in.Approval != nil {
		decision := "approved"
		if in.Approval.Step == StepCustomerDeclined {
			decision = "declined"
		}
		v.Approval = &PublicApproval{Decision: decision, SignedBy: in.Approval.ActorName, At: httpx.TimestampOf(in.Approval.OccurredAt)}
	}
	if in.OrderStatus != "" {
		f := FulfilmentOf(in.OrderStatus)
		v.Fulfilment = &f
	}
	return v, nil
}
