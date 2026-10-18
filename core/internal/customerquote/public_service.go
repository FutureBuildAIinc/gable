// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The public leg. The capability link is the whole authority: whoever holds
// the token is the customer of one quote. Nothing here reads a session, and
// nothing a link grants reaches another quote.

// Caller is who is at the public route: the source address and user agent the
// approval record keeps.
type Caller struct {
	Address   string
	UserAgent string
}

const (
	maxUserAgent          = 300
	maxSignaturePNG       = 128 * 1024
	codeRequestsPerWindow = 3
	codeRequestWindow     = 10 * time.Minute
)

// resolve turns a presented token into its live link and quote, after the
// rate limits. A token that never existed, or was revoked or rotated, answers
// ErrLinkUnknown; one that expired answers ErrLinkUnavailable. Compares are on
// hashes, in constant time.
func (s *Service) resolve(ctx context.Context, token string, c Caller) (*Link, *Quote, error) {
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
	link, err := s.store.LinkByHash(ctx, hash)
	if err != nil {
		return nil, nil, err
	}
	// A link to a dealer quote opens nothing here: to this route it never existed.
	// An unknown, revoked or rotated token is charged to the address's miss budget
	// alone; it creates no per link key and answers as if it never existed.
	if link == nil || !HashesEqual(link.Hash, hash) || link.QuoteID == "" || link.RevokedAt != nil {
		if !s.misses.Allow(clientKey(c.Address)) {
			return nil, nil, ErrRateLimited
		}
		return nil, nil, ErrLinkUnknown
	}
	if !s.perLink.Allow("link:" + hash) {
		return nil, nil, ErrRateLimited
	}
	if !link.Live(s.now()) {
		return nil, nil, ErrLinkUnavailable
	}
	qid, err := uuid.Parse(link.QuoteID)
	if err != nil {
		return nil, nil, ErrLinkUnknown
	}
	q, err := s.store.GetQuote(ctx, qid, nil, false)
	if err != nil {
		return nil, nil, err
	}
	if q.Status == StatusDraft || q.Status == StatusWithdrawn {
		return nil, nil, ErrLinkUnavailable
	}
	return link, q, nil
}

// View is the customer's page.
func (s *Service) View(ctx context.Context, token string, c Caller) (*PublicView, error) {
	link, q, err := s.resolve(ctx, token, c)
	if err != nil {
		return nil, err
	}
	_ = s.store.TouchLink(ctx, link.ID, s.now())
	return s.publicViewOf(ctx, q)
}

// PublicPDF is the customer's PDF.
func (s *Service) PublicPDF(ctx context.Context, token string, c Caller) ([]byte, error) {
	_, q, err := s.resolve(ctx, token, c)
	if err != nil {
		return nil, err
	}
	return s.renderPDF(ctx, q)
}

// PublicLogo is the contractor's mark for the customer's page.
func (s *Service) PublicLogo(ctx context.Context, token string, c Caller) (*Logo, error) {
	_, q, err := s.resolve(ctx, token, c)
	if err != nil {
		return nil, err
	}
	return s.store.GetLogo(ctx, q.CustomerID)
}

// Choice is one decision on a material line.
type Choice struct {
	LineID uuid.UUID      `json:"line_id"`
	State  SelectionState `json:"state"`
}

// Choose records the customer's decisions on selections. Approving a line in
// an option group declines the group's other lines: the customer chooses one.
func (s *Service) Choose(ctx context.Context, token string, c Caller, choices []Choice) (*PublicView, error) {
	link, _, err := s.resolve(ctx, token, c)
	if err != nil {
		return nil, err
	}
	v := &httpx.Validator{}
	v.Check(len(choices) > 0 && len(choices) <= maxLines, "choices", "needs between 1 and 200 entries")
	for i, ch := range choices {
		v.Check(ch.State == SelectionApproved || ch.State == SelectionDeclined || ch.State == SelectionPending,
			fmt.Sprintf("choices[%d].state", i), "must be one of: approved, declined, pending")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	var q *Quote
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		qid, _ := uuid.Parse(link.QuoteID)
		q, err = s.store.GetQuote(ctx, qid, nil, true)
		if err != nil {
			return err
		}
		if !link.Live(s.now()) || q.Effective(s.now()) != StatusSent {
			return ErrInvalidState
		}
		states := q.States()
		byID := map[uuid.UUID]Line{}
		for _, l := range q.Lines {
			byID[l.ID] = l
		}
		for i, ch := range choices {
			l, ok := byID[ch.LineID]
			if !ok {
				return httpx.BadRequest("a selection is not on this quote", httpx.FieldError{Field: fmt.Sprintf("choices[%d].line_id", i), Message: "is not a line of this quote"})
			}
			states[l.ID] = ch.State
			if ch.State == SelectionApproved && l.GroupID != nil {
				for _, sib := range q.Lines {
					if sib.GroupID != nil && *sib.GroupID == *l.GroupID && sib.ID != l.ID {
						states[sib.ID] = SelectionDeclined
					}
				}
			}
		}
		changed := map[uuid.UUID]SelectionState{}
		for id, st := range states {
			if byID[id].Selection != st {
				changed[id] = st
			}
		}
		if err := s.store.SetSelections(ctx, q.ID, changed, s.now()); err != nil {
			return err
		}
		q, err = s.store.GetQuote(ctx, q.ID, nil, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.publicViewOf(ctx, q)
}

// RequestCode sends a fresh six digit code to the contact the contractor put
// on the quote, by the channel the customer picks.
func (s *Service) RequestCode(ctx context.Context, token string, c Caller, channel string) error {
	link, _, err := s.resolve(ctx, token, c)
	if err != nil {
		return err
	}
	var q *Quote
	var code string
	var dest string
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		qid, _ := uuid.Parse(link.QuoteID)
		q, err = s.store.GetQuote(ctx, qid, nil, true)
		if err != nil {
			return err
		}
		now := s.now()
		if !link.Live(now) || q.Effective(now) != StatusSent {
			return ErrInvalidState
		}
		if !q.RequireCode {
			return httpx.BadRequest("this quote does not use a verification code")
		}
		if s.mail == nil {
			return ErrCodesUnavailable
		}
		ch := s.mail.Channels()
		if !ch.Any() {
			return ErrCodesUnavailable
		}
		if !ch.Has(channel) && (channel == "email" || channel == "sms") {
			return httpx.BadRequest("that channel is not available", httpx.FieldError{Field: "channel", Message: "this server cannot send a code that way"})
		}
		switch channel {
		case "email":
			dest = q.EndCustomer.Email
		case "sms":
			dest = q.EndCustomer.Phone
		default:
			return httpx.BadRequest("the channel is not known", httpx.FieldError{Field: "channel", Message: "must be one of: email, sms"})
		}
		if dest == "" {
			return httpx.BadRequest("the contractor has no address for that channel", httpx.FieldError{Field: "channel", Message: "has no destination on this quote"})
		}
		totalCodes, totalAttempts, err := s.store.LinkCodeUsage(ctx, link.ID)
		if err != nil {
			return err
		}
		if totalCodes >= CodeMaxLifetimeCodes || totalAttempts >= CodeMaxLifetimeAttempts {
			return ErrCodeBudgetExhausted
		}
		n, err := s.store.CountCodesSince(ctx, link.ID, now.Add(-codeRequestWindow))
		if err != nil {
			return err
		}
		if n >= codeRequestsPerWindow {
			return ErrRateLimited
		}
		code, err = MintCode()
		if err != nil {
			return err
		}
		salt, err := NewSalt()
		if err != nil {
			return err
		}
		return s.store.InsertCode(ctx, q.ID, link.ID, channel, dest, salt, HashCode(salt, q.ID.String(), code), now, now.Add(CodeTTL))
	})
	if err != nil {
		return err
	}
	name, _, err := s.store.ContractorName(ctx, q.CustomerID)
	if err != nil {
		return err
	}
	if s.mail == nil {
		return ErrDeliveryFailed
	}
	if err := s.mail.SendCode(ctx, channel, dest, name, code); err != nil {
		s.log.Warn("verification code was not delivered", "quote_id", q.ID, "channel", channel, "error", err)
		return ErrDeliveryFailed
	}
	return nil
}

// Signature is the customer's mark on a sign off: typed, or drawn (a PNG as
// base64).
type Signature struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	PNG  string `json:"png_base64,omitempty"`
}

// AcceptInput is the body of a sign off.
type AcceptInput struct {
	FullName          string    `json:"full_name"`
	Email             string    `json:"email"`
	Phone             string    `json:"phone"`
	Signature         Signature `json:"signature"`
	Code              string    `json:"code"`
	SelectionsVersion int64     `json:"selections_version"`
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func (in *AcceptInput) validate() (sigData string, err error) {
	v := &httpx.Validator{}
	in.FullName, in.Email, in.Phone = strings.TrimSpace(in.FullName), strings.TrimSpace(in.Email), strings.TrimSpace(in.Phone)
	v.Check(runes(in.FullName) >= 2 && runes(in.FullName) <= 200, "full_name", "is required: type your full name")
	v.Check(in.Email != "" || in.Phone != "", "email", "needs an email or a phone")
	v.Check(in.Email == "" || validEmail(in.Email), "email", "is not an email address")
	v.Check(in.Phone == "" || validPhone(in.Phone), "phone", "is not a phone number")
	switch in.Signature.Kind {
	case "typed":
		t := strings.TrimSpace(in.Signature.Text)
		v.Check(runes(t) >= 2 && runes(t) <= 200, "signature.text", "is required for a typed signature")
		sigData = t
	case "drawn":
		b, derr := base64.StdEncoding.DecodeString(in.Signature.PNG)
		v.Check(derr == nil && len(b) > len(pngMagic) && len(b) <= maxSignaturePNG && strings.HasPrefix(string(b), string(pngMagic)),
			"signature.png_base64", "must be a PNG of at most 128 KB, base64 encoded")
		sigData = in.Signature.PNG
	default:
		v.Check(false, "signature.kind", "must be one of: typed, drawn")
	}
	in.Code = strings.TrimSpace(in.Code)
	return sigData, v.Err()
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func contactOf(email, phone string) string {
	if email != "" && phone != "" {
		return email + ", " + phone
	}
	return email + phone
}

// decided checks that every selection is decided and that something is
// approved; the customer signs off on a complete set of choices.
func decided(q *Quote) error {
	byGroup := map[uuid.UUID][]Line{}
	approved := 0
	for _, l := range q.Lines {
		if l.Selection == SelectionApproved {
			approved++
		}
		if l.GroupID == nil {
			if l.Selection == SelectionPending {
				return ErrIncomplete
			}
			continue
		}
		byGroup[*l.GroupID] = append(byGroup[*l.GroupID], l)
	}
	for _, ls := range byGroup {
		any, pending := false, false
		for _, l := range ls {
			if l.Selection == SelectionApproved {
				any = true
			}
			if l.Selection == SelectionPending {
				pending = true
			}
		}
		if pending && !any {
			return ErrIncomplete
		}
	}
	customerLabour := 0
	for _, l := range q.Labour {
		if !l.ContractorOnly {
			customerLabour++
		}
	}
	if approved == 0 && customerLabour == 0 {
		return ErrNothingChosen
	}
	return nil
}

// Accept commits the customer's approval: the code (when the contractor
// requires one), then the snapshot, the record and the status, in one
// transaction. Two acceptances of one link at once give exactly one approval:
// the quote row is locked and only a sent quote moves.
func (s *Service) Accept(ctx context.Context, token string, c Caller, in AcceptInput) (*PublicView, error) {
	link, _, err := s.resolve(ctx, token, c)
	if err != nil {
		return nil, err
	}
	sigData, err := in.validate()
	if err != nil {
		return nil, err
	}
	var wrong error
	var q *Quote
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		qid, _ := uuid.Parse(link.QuoteID)
		cur, err := s.store.GetQuote(ctx, qid, nil, true)
		if err != nil {
			return err
		}
		now := s.now()
		if !link.Live(now) || cur.Effective(now) != StatusSent {
			return ErrInvalidState
		}
		if cur.RequireCode && !s.codesAvailable() {
			return ErrCodesUnavailable
		}
		if err := decided(cur); err != nil {
			return err
		}
		if in.SelectionsVersion != cur.SelectionsVersion {
			return ErrSelectionsChanged
		}
		verified := false
		if cur.RequireCode {
			if in.Code == "" {
				return ErrCodeRequired
			}
			totalCodes, totalAttempts, err := s.store.LinkCodeUsage(ctx, link.ID)
			if err != nil {
				return err
			}
			if totalAttempts >= CodeMaxLifetimeAttempts {
				return ErrCodeBudgetExhausted
			}
			row, err := s.store.LatestCodeForUpdate(ctx, link.ID)
			if err != nil {
				return err
			}
			if row == nil {
				return ErrCodeRequired
			}
			if cerr := row.Check(now, in.Code); cerr != nil {
				if cerr == ErrCodeWrong {
					// The wrong guess is counted and kept: commit the count,
					// then report the miss.
					if err := s.store.BumpCodeAttempts(ctx, row.ID); err != nil {
						return err
					}
				}
				if cerr == ErrCodeLocked && totalCodes >= CodeMaxLifetimeCodes {
					wrong = ErrCodeBudgetExhausted
					return nil
				}
				wrong = cerr
				return nil
			}
			if ok, err := s.store.ConsumeCode(ctx, row.ID, now); err != nil {
				return err
			} else if !ok {
				wrong = ErrCodeConsumed
				return nil
			}
			verified = true
		}
		cname, _, err := s.store.ContractorName(ctx, cur.CustomerID)
		if err != nil {
			return err
		}
		body, sum, total, err := BuildSnapshot(cur, "approved", SnapshotMeta{ContractorName: cname})
		if err != nil {
			return err
		}
		if err := s.store.AppendLog(ctx, &ApprovalEntry{QuoteID: cur.ID, QuoteRevision: cur.Revision, Step: StepCustomerApproved,
			ActorKind: ActorCustomer, ActorName: in.FullName, ActorContact: contactOf(in.Email, in.Phone), OccurredAt: now,
			TokenHash: link.Hash, SignatureKind: in.Signature.Kind, SignatureData: sigData,
			SourceAddress: clip(c.Address, 64), UserAgent: clip(c.UserAgent, maxUserAgent), CodeVerified: verified,
			Snapshot: body, SnapshotSHA256: sum, TotalCents: &total}); err != nil {
			return err
		}
		moved, err := s.store.Transition(ctx, cur.ID, StatusSent, StatusApproved, "approved_at = $4", now)
		if err != nil {
			return err
		}
		if !moved {
			return ErrInvalidState
		}
		q, err = s.store.GetQuote(ctx, cur.ID, nil, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	if wrong != nil {
		return nil, wrong
	}
	return s.publicViewOf(ctx, q)
}

// DeclineInput is the body of a decline.
type DeclineInput struct {
	FullName          string `json:"full_name"`
	Reason            string `json:"reason"`
	SelectionsVersion int64  `json:"selections_version"`
}

// Decline records that the customer declines the quote.
func (s *Service) Decline(ctx context.Context, token string, c Caller, in DeclineInput) (*PublicView, error) {
	link, _, err := s.resolve(ctx, token, c)
	if err != nil {
		return nil, err
	}
	in.FullName, in.Reason = strings.TrimSpace(in.FullName), strings.TrimSpace(in.Reason)
	v := &httpx.Validator{}
	v.Check(runes(in.FullName) >= 2 && runes(in.FullName) <= 200, "full_name", "is required")
	v.Check(runes(in.Reason) <= 1000, "reason", "is at most 1000 characters")
	if err := v.Err(); err != nil {
		return nil, err
	}
	var q *Quote
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		qid, _ := uuid.Parse(link.QuoteID)
		cur, err := s.store.GetQuote(ctx, qid, nil, true)
		if err != nil {
			return err
		}
		now := s.now()
		if !link.Live(now) || cur.Effective(now) != StatusSent {
			return ErrInvalidState
		}
		if in.SelectionsVersion > 0 && in.SelectionsVersion != cur.SelectionsVersion {
			return ErrSelectionsChanged
		}
		cname, _, err := s.store.ContractorName(ctx, cur.CustomerID)
		if err != nil {
			return err
		}
		body, sum, total, err := BuildSnapshot(cur, "declined", SnapshotMeta{ContractorName: cname})
		if err != nil {
			return err
		}
		if err := s.store.AppendLog(ctx, &ApprovalEntry{QuoteID: cur.ID, QuoteRevision: cur.Revision, Step: StepCustomerDeclined,
			ActorKind: ActorCustomer, ActorName: in.FullName, OccurredAt: now, TokenHash: link.Hash,
			SourceAddress: clip(c.Address, 64), UserAgent: clip(c.UserAgent, maxUserAgent),
			Snapshot: body, SnapshotSHA256: sum, TotalCents: &total, Note: in.Reason}); err != nil {
			return err
		}
		moved, err := s.store.Transition(ctx, cur.ID, StatusSent, StatusDeclined, "declined_at = $4", now)
		if err != nil {
			return err
		}
		if !moved {
			return ErrInvalidState
		}
		q, err = s.store.GetQuote(ctx, cur.ID, nil, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.publicViewOf(ctx, q)
}

// clientKey is the limiter key of a source address: an IPv4 address itself, an
// IPv6 address by its /64 (one subscriber commonly holds a whole /64 and can
// rotate addresses inside it at will).
func clientKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "ip:" + ip
	}
	a = a.Unmap()
	if a.Is4() {
		return "ip:" + a.String()
	}
	return "ip:" + netip.PrefixFrom(a.WithZone(""), 64).Masked().String()
}
