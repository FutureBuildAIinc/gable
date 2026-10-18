// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// The dealer's own quote link (PE-05). The dealer sends its quote to a
// contractor, who accepts or declines it from a link with no login. It is the
// same token module as the customer's link: the same 256 bit token, hash only
// at rest, the same lifetime bound, rate limits and log redaction. A link
// names one target and opens nothing else: a dealer quote link never opens a
// customer quote, and the converse.

// DealerQuoteLine is a line of a dealer quote as the contractor sees it. The
// prices are the contractor's own account prices.
type DealerQuoteLine struct {
	Description string
	Quantity    httpx.Quantity
	UOM         string
	PriceUOM    string
	UnitPrice   httpx.Price
	LineTotal   int64
}

// DealerQuoteDoc is the dealer quote the link opens.
type DealerQuoteDoc struct {
	ID           uuid.UUID
	Number       string
	State        string // the quote lifecycle's uppercase state
	CustomerName string
	ExpiresAt    *time.Time
	FreightCents int64
	TotalCents   int64
	Lines        []DealerQuoteLine
}

// DealerQuotes reads and decides the dealer's quotes through the quote
// module's own lifecycle.
type DealerQuotes interface {
	// Read reads a quote under the caller's wall (the dealer's desk).
	Read(ctx context.Context, id uuid.UUID) (*DealerQuoteDoc, error)
	// ReadByLink reads the quote a link names: the link is the authority.
	ReadByLink(ctx context.Context, id uuid.UUID) (*DealerQuoteDoc, error)
	// Decide moves a sent quote to accepted or rejected, as the lifecycle
	// allows and no further.
	Decide(ctx context.Context, id uuid.UUID, accept bool) error
}

// AuditSink records the decision (pkg/audit.Logger).
type AuditSink interface {
	Log(ctx context.Context, e audit.Entry) error
}

// WithDealerQuotes wires the dealer quote link: the quote module's lifecycle
// and the audit log the decision is recorded in.
func (s *Service) WithDealerQuotes(dq DealerQuotes, a AuditSink) *Service {
	s.dealerQuotes, s.audit = dq, a
	return s
}

// PublicDealerQuote is the contractor's page for the dealer's quote: the
// strict allowlist. There is no field for the dealer's cost or margin.
type PublicDealerQuote struct {
	Number       string                  `json:"number"`
	Status       string                  `json:"status"`
	DealerName   string                  `json:"dealer_name"`
	CustomerName string                  `json:"customer_name"`
	ExpiresAt    *httpx.Timestamp        `json:"expires_at"`
	CanDecide    bool                    `json:"can_decide"`
	Lines        []PublicDealerQuoteLine `json:"lines"`
	FreightCents int64                   `json:"freight_cents"`
	TotalCents   int64                   `json:"total_cents"`
}

// PublicDealerQuoteLine is one priced line.
type PublicDealerQuoteLine struct {
	Description string         `json:"description"`
	Quantity    httpx.Quantity `json:"quantity"`
	UOM         string         `json:"uom"`
	PriceUOM    string         `json:"price_uom"`
	UnitPrice   int64          `json:"unit_price_ten_thousandths"`
	LineTotal   int64          `json:"line_total_cents"`
}

func dealerQuoteStatus(d *DealerQuoteDoc, now time.Time) string {
	switch d.State {
	case "SENT":
		if d.ExpiresAt != nil && !now.Before(*d.ExpiresAt) {
			return "expired"
		}
		return "open"
	case "ACCEPTED":
		return "accepted"
	case "REJECTED":
		return "declined"
	case "EXPIRED":
		return "expired"
	}
	return "withdrawn"
}

func buildPublicDealerQuote(d *DealerQuoteDoc, dealerName string, now time.Time) *PublicDealerQuote {
	v := &PublicDealerQuote{Number: d.Number, Status: dealerQuoteStatus(d, now), DealerName: dealerName,
		CustomerName: d.CustomerName, ExpiresAt: httpx.PtrTimestamp(d.ExpiresAt), Lines: []PublicDealerQuoteLine{},
		FreightCents: d.FreightCents, TotalCents: d.TotalCents}
	v.CanDecide = v.Status == "open"
	for _, l := range d.Lines {
		v.Lines = append(v.Lines, PublicDealerQuoteLine{Description: l.Description, Quantity: l.Quantity, UOM: l.UOM,
			PriceUOM: l.PriceUOM, UnitPrice: int64(l.UnitPrice), LineTotal: l.LineTotal})
	}
	return v
}

// DealerLinkResult is what issuing a dealer quote link answers.
type DealerLinkResult struct {
	Path      string
	URL       string
	ExpiresAt time.Time
	Delivered bool
}

// ShareDealerQuote issues a link for a sent dealer quote, killing any earlier
// link of the same quote. The token is returned here once. sendTo, when it is
// an email address, gets the link through the email path.
func (s *Service) ShareDealerQuote(ctx context.Context, who DealerActor, id uuid.UUID, sendTo string) (*DealerLinkResult, error) {
	if s.dealerQuotes == nil {
		return nil, httpx.Unavailable("the dealer quote link is not configured")
	}
	sendTo = strings.TrimSpace(sendTo)
	if sendTo != "" && !validEmail(sendTo) {
		return nil, httpx.BadRequest("send_to is not an email address", httpx.FieldError{Field: "send_to", Message: "is not an email address"})
	}
	doc, err := s.dealerQuotes.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, ErrNotFound
	}
	now := s.now()
	if doc.State != "SENT" || (doc.ExpiresAt != nil && !now.Before(*doc.ExpiresAt)) {
		return nil, ErrInvalidState
	}
	cfg := s.config(ctx)
	validUntil := now.Add(cfg.linkMaxTTL)
	if doc.ExpiresAt != nil {
		validUntil = *doc.ExpiresAt
	}
	expires, err := LinkExpiry(now, validUntil, cfg.linkMaxTTL)
	if err != nil {
		return nil, ErrInvalidState
	}
	token, hash, err := MintToken()
	if err != nil {
		return nil, err
	}
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		if _, err := s.store.RevokeLiveDealer(ctx, id, "rotated", now); err != nil {
			return err
		}
		if _, err := s.store.InsertDealerLink(ctx, id, hash, expires); err != nil {
			return err
		}
		if s.audit != nil {
			return s.audit.Log(ctx, audit.Entry{Action: "quote.share_link_issued", EntityType: "quote", EntityID: id, UserID: who.Ref,
				Changes: map[string]interface{}{"expires_at": expires.UTC().Format(time.RFC3339), "emailed": sendTo != ""}})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	path := "/dq/" + token
	res := &DealerLinkResult{Path: path, ExpiresAt: expires}
	if s.publicURL != "" {
		res.URL = s.publicURL + path
	}
	if sendTo != "" && res.URL != "" && s.mail != nil {
		name, _ := s.store.DealerName(ctx)
		if ok, err := s.mail.SendLink(ctx, Contact{Email: sendTo}, name, "Quote "+doc.Number, res.URL); err == nil {
			res.Delivered = ok
		} else {
			s.log.Warn("dealer quote link was not delivered", "quote_id", id, "error", err)
		}
	}
	return res, nil
}

// resolveDealer is resolve for a dealer quote link.
func (s *Service) resolveDealer(ctx context.Context, token string, c Caller) (*Link, *DealerQuoteDoc, error) {
	if s.dealerQuotes == nil {
		return nil, nil, ErrLinkUnknown
	}
	if !s.perAddr.Allow(clientKey(c.Address)) {
		return nil, nil, ErrRateLimited
	}
	if !TokenWellFormed(token) {
		if !s.misses.Allow(clientKey(c.Address)) {
			return nil, nil, ErrRateLimited
		}
		return nil, nil, ErrLinkUnknown
	}
	hash := HashToken(token)
	if !s.perLink.Allow("link:" + hash) {
		return nil, nil, ErrRateLimited
	}
	link, err := s.store.LinkByHash(ctx, hash)
	if err != nil {
		return nil, nil, err
	}
	if link == nil || !HashesEqual(link.Hash, hash) || link.DealerQuoteID == "" {
		if !s.misses.Allow(clientKey(c.Address)) {
			return nil, nil, ErrRateLimited
		}
		return nil, nil, ErrLinkUnknown
	}
	if !link.Live(s.now()) {
		return nil, nil, ErrLinkUnavailable
	}
	id, err := uuid.Parse(link.DealerQuoteID)
	if err != nil {
		return nil, nil, ErrLinkUnknown
	}
	doc, err := s.dealerQuotes.ReadByLink(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if doc == nil || doc.State == "DRAFT" {
		return nil, nil, ErrLinkUnavailable
	}
	return link, doc, nil
}

// DealerQuoteView is the contractor's page for the dealer's quote.
func (s *Service) DealerQuoteView(ctx context.Context, token string, c Caller) (*PublicDealerQuote, error) {
	link, doc, err := s.resolveDealer(ctx, token, c)
	if err != nil {
		return nil, err
	}
	_ = s.store.TouchLink(ctx, link.ID, s.now())
	name, err := s.store.DealerName(ctx)
	if err != nil {
		return nil, err
	}
	return buildPublicDealerQuote(doc, name, s.now()), nil
}

// DealerQuoteAcceptInput is the body of an acceptance.
type DealerQuoteAcceptInput struct {
	FullName string `json:"full_name"`
}

// DealerQuoteDeclineInput is the body of a decline.
type DealerQuoteDeclineInput struct {
	FullName string `json:"full_name"`
	Reason   string `json:"reason"`
}

// DealerQuoteDecide accepts or declines the dealer's quote from its link,
// through the quote module's own lifecycle (a quote that is not open, or
// already decided, is refused there), and records who, from where and when in
// the audit log in the same transaction.
func (s *Service) DealerQuoteDecide(ctx context.Context, token string, c Caller, accept bool, fullName, reason string) (*PublicDealerQuote, error) {
	link, doc, err := s.resolveDealer(ctx, token, c)
	if err != nil {
		return nil, err
	}
	fullName, reason = strings.TrimSpace(fullName), strings.TrimSpace(reason)
	v := &httpx.Validator{}
	v.Check(runes(fullName) >= 2 && runes(fullName) <= 200, "full_name", "is required: type your full name")
	v.Check(runes(reason) <= 1000, "reason", "is at most 1000 characters")
	if err := v.Err(); err != nil {
		return nil, err
	}
	if dealerQuoteStatus(doc, s.now()) != "open" {
		return nil, ErrInvalidState
	}
	action := "quote.accepted_from_link"
	if !accept {
		action = "quote.declined_from_link"
	}
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		if err := s.dealerQuotes.Decide(ctx, doc.ID, accept); err != nil {
			return err
		}
		if s.audit == nil {
			return nil
		}
		ch := map[string]interface{}{"decided_by": fullName, "source_address": clip(c.Address, 64),
			"user_agent": clip(c.UserAgent, maxUserAgent), "token_hash": link.Hash}
		if reason != "" {
			ch["reason"] = reason
		}
		return s.audit.Log(ctx, audit.Entry{Action: action, EntityType: "quote", EntityID: doc.ID, UserID: "quote-link", Changes: ch})
	})
	if err != nil {
		return nil, err
	}
	fresh, err := s.dealerQuotes.ReadByLink(ctx, doc.ID)
	if err != nil || fresh == nil {
		return nil, err
	}
	name, _ := s.store.DealerName(ctx)
	return buildPublicDealerQuote(fresh, name, s.now()), nil
}
