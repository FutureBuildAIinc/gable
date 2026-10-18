// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// QuoteView is the contractor's own view of a quote: costs, markups, labour,
// overhead, margin and the customer's prices. It is for the contractor's
// portal session only; it never reaches the customer or the dealer.
type QuoteView struct {
	ID               uuid.UUID        `json:"id"`
	Reference        string           `json:"reference"`
	Title            string           `json:"title"`
	Status           string           `json:"status"`
	EffectiveStatus  string           `json:"effective_status"`
	ProjectID        *uuid.UUID       `json:"project_id"`
	SourceQuoteID    *uuid.UUID       `json:"source_quote_id"`
	JobSiteAddress   string           `json:"job_site_address"`
	EndCustomer      Contact          `json:"end_customer"`
	SiteContact      Contact          `json:"site_contact"`
	Markup           Markup           `json:"markup"`
	HideLinePrices   bool             `json:"hide_line_prices"`
	Tax              Tax              `json:"tax"`
	PaymentTerms     string           `json:"payment_terms"`
	DepositBPS       int64            `json:"deposit_bps"`
	CustomerNote     string           `json:"customer_note"`
	RequireCode      bool             `json:"require_code"`
	ValidUntil       httpx.Timestamp  `json:"valid_until"`
	Revision         int64            `json:"revision"`
	SentAt           *httpx.Timestamp `json:"sent_at"`
	ApprovedAt       *httpx.Timestamp `json:"approved_at"`
	DeclinedAt       *httpx.Timestamp `json:"declined_at"`
	ConfirmedAt      *httpx.Timestamp `json:"confirmed_at"`
	DealerAcceptedAt *httpx.Timestamp `json:"dealer_accepted_at"`
	Groups           []OptionGroup    `json:"groups"`
	Lines            []Line           `json:"lines"`
	CategoryRules    []CategoryRule   `json:"category_rules"`
	Labour           []LabourView     `json:"labour"`
	Overhead         []OverheadView   `json:"overhead"`
	Totals           Totals           `json:"totals"`
	Link             *LinkView        `json:"link"`
	Order            *OrderView       `json:"order"`
	CreatedAt        httpx.Timestamp  `json:"created_at"`
	UpdatedAt        httpx.Timestamp  `json:"updated_at"`
}

// LabourView is a labour line with its amount.
type LabourView struct {
	Labour
	TotalCents int64 `json:"total_cents"`
}

// OverheadView is an overhead line with its amount.
type OverheadView struct {
	Overhead
	TotalCents int64 `json:"total_cents"`
}

// LinkView says whether the quote has a live link. The token itself is never
// readable after it is issued.
type LinkView struct {
	ExpiresAt httpx.Timestamp `json:"expires_at"`
}

// OrderView is the converted order, as the contractor sees its progress.
type OrderView struct {
	ID         uuid.UUID `json:"id"`
	Number     string    `json:"number"`
	Status     string    `json:"status"`
	Fulfilment string    `json:"fulfilment"`
	TotalCents int64     `json:"total_cents"`
}

func tsPtr(t *time.Time) *httpx.Timestamp { return httpx.PtrTimestamp(t) }

// BuildQuoteView builds the contractor's view.
func BuildQuoteView(q *Quote, now time.Time, link *Link, order *OrderBrief) (*QuoteView, error) {
	t, err := q.Totals()
	if err != nil {
		return nil, err
	}
	v := &QuoteView{
		ID: q.ID, Reference: q.Reference, Title: q.Title, Status: string(q.Status), EffectiveStatus: string(q.Effective(now)),
		ProjectID: q.ProjectID, SourceQuoteID: q.SourceQuoteID, JobSiteAddress: q.JobSiteAddress,
		EndCustomer: q.EndCustomer, SiteContact: q.SiteContact, Markup: q.Markup, HideLinePrices: q.HideLinePrices,
		Tax: q.Tax, PaymentTerms: q.PaymentTerms, DepositBPS: q.DepositBPS, CustomerNote: q.CustomerNote, RequireCode: q.RequireCode,
		ValidUntil: httpx.TimestampOf(q.ValidUntil), Revision: q.Revision,
		SentAt: tsPtr(q.SentAt), ApprovedAt: tsPtr(q.ApprovedAt), DeclinedAt: tsPtr(q.DeclinedAt), ConfirmedAt: tsPtr(q.ConfirmedAt),
		DealerAcceptedAt: tsPtr(q.DealerAcceptedAt),
		Groups:           nonNil(q.Groups), Lines: nonNil(q.Lines), CategoryRules: nonNil(q.CategoryRules),
		Labour: []LabourView{}, Overhead: []OverheadView{}, Totals: t,
		CreatedAt: httpx.TimestampOf(q.CreatedAt), UpdatedAt: httpx.TimestampOf(q.UpdatedAt),
	}
	for _, l := range q.Labour {
		amt, err := LabourTotalCents(LabourLine{RateType: l.RateType, RateCents: l.RateCents, Hours: l.Hours})
		if err != nil {
			return nil, err
		}
		v.Labour = append(v.Labour, LabourView{Labour: l, TotalCents: amt})
	}
	base := t.MaterialSellCents + t.LabourCents
	for _, o := range q.Overhead {
		amt, err := OverheadTotalCents(OverheadLine{AmountType: o.AmountType, Value: o.Value}, base)
		if err != nil {
			return nil, err
		}
		v.Overhead = append(v.Overhead, OverheadView{Overhead: o, TotalCents: amt})
	}
	if link != nil && link.Live(now) {
		v.Link = &LinkView{ExpiresAt: httpx.TimestampOf(link.ExpiresAt)}
	}
	if order != nil {
		v.Order = &OrderView{ID: order.ID, Number: order.Number, Status: order.Status, Fulfilment: FulfilmentOf(order.Status), TotalCents: order.TotalCents}
	}
	return v, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// QuoteSummary is one row of the contractor's list.
type QuoteSummary struct {
	ID              uuid.UUID       `json:"id"`
	Reference       string          `json:"reference"`
	Title           string          `json:"title"`
	Status          string          `json:"status"`
	EffectiveStatus string          `json:"effective_status"`
	EndCustomerName string          `json:"end_customer_name"`
	ProjectID       *uuid.UUID      `json:"project_id"`
	ValidUntil      httpx.Timestamp `json:"valid_until"`
	TotalCents      int64           `json:"total_cents"`
	MarginCents     int64           `json:"margin_cents"`
	Revision        int64           `json:"revision"`
	CreatedAt       httpx.Timestamp `json:"created_at"`
	UpdatedAt       httpx.Timestamp `json:"updated_at"`
}

// SummaryOf builds a list row.
func SummaryOf(q *Quote, now time.Time) (QuoteSummary, error) {
	t, err := q.Totals()
	if err != nil {
		return QuoteSummary{}, err
	}
	return QuoteSummary{ID: q.ID, Reference: q.Reference, Title: q.Title, Status: string(q.Status), EffectiveStatus: string(q.Effective(now)),
		EndCustomerName: q.EndCustomer.Name, ProjectID: q.ProjectID, ValidUntil: httpx.TimestampOf(q.ValidUntil),
		TotalCents: t.TotalCents, MarginCents: t.MarginCents, Revision: q.Revision,
		CreatedAt: httpx.TimestampOf(q.CreatedAt), UpdatedAt: httpx.TimestampOf(q.UpdatedAt)}, nil
}

// ApprovalView is one row of the approval record as the contractor reads it,
// everything the record holds but the signature image bytes and the snapshot
// body (the snapshot is read by its hash and by the export).
type ApprovalView struct {
	Step           string          `json:"step"`
	QuoteRevision  int64           `json:"quote_revision"`
	ActorKind      string          `json:"actor_kind"`
	ActorName      string          `json:"actor_name"`
	ActorContact   string          `json:"actor_contact"`
	OccurredAt     httpx.Timestamp `json:"occurred_at"`
	TokenHash      string          `json:"token_hash,omitempty"`
	SignatureKind  string          `json:"signature_kind,omitempty"`
	SourceAddress  string          `json:"source_address,omitempty"`
	UserAgent      string          `json:"user_agent,omitempty"`
	CodeVerified   bool            `json:"code_verified"`
	SnapshotSHA256 string          `json:"snapshot_sha256,omitempty"`
	TotalCents     *int64          `json:"total_cents,omitempty"`
	Note           string          `json:"note,omitempty"`
}

// ApprovalViewOf builds the contractor's view of a log row.
func ApprovalViewOf(e ApprovalEntry) ApprovalView {
	return ApprovalView{Step: e.Step, QuoteRevision: e.QuoteRevision, ActorKind: e.ActorKind, ActorName: e.ActorName,
		ActorContact: e.ActorContact, OccurredAt: httpx.TimestampOf(e.OccurredAt), TokenHash: e.TokenHash,
		SignatureKind: e.SignatureKind, SourceAddress: e.SourceAddress, UserAgent: e.UserAgent, CodeVerified: e.CodeVerified,
		SnapshotSHA256: e.SnapshotSHA256, TotalCents: e.TotalCents, Note: e.Note}
}
