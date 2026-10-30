// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

// The one expiry rule (QE-01-a, the principal's strict ruling): a quote whose
// expires_at is at or before the service's clock cannot go to SENT or
// ACCEPTED and cannot convert, on any path, until it is re-dated. No owner or
// admin override. The refusal says what to do, with the date in the branch's
// own calendar, as the desk shows dates.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// CodeQuoteExpired is the blocker code of the expiry refusal.
const CodeQuoteExpired = "quote_expired"

// expiryWords is the ruling's refusal, the day as the desk shows dates.
func expiryWords(day string) string {
	return "This quote expired on " + day + ": revise it with a new valid until date to send or convert it"
}

// expiryDay renders the expiry instant's calendar day in the quote's branch's
// own zone, UTC when the zone cannot be read or loaded: the desk shows the
// Valid until as a day, and the refusal names that same day.
func (s *Service) expiryDay(ctx context.Context, at time.Time, branch uuid.UUID) string {
	zone, err := s.repo.BranchZone(ctx, branch)
	if err != nil {
		s.logger.Warn("the expiry day's branch zone read failed; the day is shown in UTC",
			"branch_id", branch, "error", err)
		zone = "UTC"
	}
	loc, lerr := time.LoadLocation(zone)
	if lerr != nil {
		loc = time.UTC
	}
	return at.In(loc).Format("Jan 2, 2006")
}

// expiryRefusal is the rule itself: nil when the quote has not expired, and
// the desk's 409 (ADR 0001's blocker shape) when it has. It runs inside the
// act's transaction, on the locked quote, so a refusal leaves the quote
// exactly as it was.
func (s *Service) expiryRefusal(ctx context.Context, cur *Quote) error {
	if cur.ExpiresAt == nil || s.now().Before(cur.ExpiresAt.Time) {
		return nil
	}
	msg := expiryWords(s.expiryDay(ctx, cur.ExpiresAt.Time, cur.BranchID))
	return httpx.InvalidStateTransition(msg, httpx.Blocker(CodeQuoteExpired, msg))
}

// ExpiredRefusal builds the one expiry refusal for the quote as it stands, or
// nil when it has not expired. The portal asks for it so its decide and
// convert paths answer the same words the desk does, in the portal's own
// error shape.
func (s *Service) ExpiredRefusal(ctx context.Context, id uuid.UUID) error {
	cur, err := s.repo.GetQuote(ctx, id)
	if err != nil {
		return notFound(err)
	}
	return s.expiryRefusal(ctx, cur)
}

// ExpiryRefusalMessage reports the message of the one expiry refusal when err
// carries it (the quote_expired blocker), so another module's path can say
// the same words without rebuilding them.
func ExpiryRefusalMessage(err error) (string, bool) {
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusConflict {
		return "", false
	}
	for _, d := range he.Details {
		if d.Code == CodeQuoteExpired && d.Message != "" {
			return d.Message, true
		}
	}
	return "", false
}

// sweepBatch is how many quotes one sweep pass moves at most; a larger
// backlog drains across passes.
const sweepBatch = 100

// SweepExpired moves SENT quotes whose expires_at is at or before now to
// EXPIRED through the service's own transition, so the event rides the act's
// transaction (ADR 0003) and the same lifecycle rules decide it. The pass is
// idempotent (a moved quote is no longer SENT) and moves each quote at most
// once: a concurrent act that accepts the quote first leaves the transition
// refused, which the pass counts as moved by someone else, not an error.
func (s *Service) SweepExpired(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = sweepBatch
	}
	ids, err := s.repo.SentQuotesPastExpiry(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, id := range ids {
		if _, err := s.transition(ctx, id, QuoteStateExpired, nil, nil); err != nil {
			// Another act moved the quote between the batch read and the row
			// lock (the desk accepted it, a first sweep expired it): the
			// sweep never fights the desk, and a pass only reports.
			s.logger.Info("the expiry sweep left a quote it read as sent",
				"quote_id", id, "error", err)
			continue
		}
		moved++
	}
	return moved, nil
}
