// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

// The dealer's revise (QE-01-b, Colton's ruling): a sent quote revised stays
// Q-NNNNNN and becomes the next version, with an immutable snapshot of each
// sent version and a history the dealer can read on the quote page. The
// ruling's model is the portal's own Revise (customerquote.Service.Revise):
// the old link dies, the quote returns to draft for the builder, and the
// order keeps converting from the one quote id.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// EventRevised is the revise's own event, written as the revise's last
// statement (ADR 0003 section 2).
const EventRevised = "quote.revised"

// QuoteRevision is one sent version of a quote: the immutable snapshot row
// the send wrote. Snapshot is the quote's own wire body exactly as sent, so
// the history the dealer reads is the document the customer saw, money in
// the integer units of the wire.
type QuoteRevision struct {
	ID            uuid.UUID       `json:"id"`
	QuoteID       uuid.UUID       `json:"quote_id"`
	VersionNumber int             `json:"version_number"`
	Snapshot      json.RawMessage `json:"snapshot"`
	CreatedBy     string          `json:"created_by"`
	CreatedAt     httpx.Timestamp `json:"created_at"`
	Reason        string          `json:"reason"`
}

// actorOf reads the caller the request's claims name, when the context
// carries them; the empty string otherwise.
func actorOf(ctx context.Context) string {
	if c := middleware.ClaimsFromContext(ctx); c != nil {
		return c.Subject
	}
	return ""
}

// Revise takes a sent, rejected or expired quote back to draft as its next
// version, on the client's revision. The quote's id and number never move;
// version_number goes up by one; any live share link dies, so the contractor
// cannot accept a version the dealer has taken back; the lifecycle stamps of
// the old version clear, so the next send stamps its own. An accepted or
// converted quote cannot be revised: its acceptance already happened. An
// expired quote revises, and still needs a new Valid until before it sends
// (QE-01-a's rule, which the send enforces). The act, the link revocation,
// the audit row and the quote.revised event are one transactional fact.
func (s *Service) Revise(ctx context.Context, id uuid.UUID, reason string, pre Precondition) (*Quote, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	reason = strings.TrimSpace(reason)
	var out *Quote
	var links int64
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockQuote(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetQuote(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkQuoteBranch(ctx, cur); err != nil {
			return err
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		switch cur.Status {
		case QuoteStateSent, QuoteStateRejected, QuoteStateExpired:
		case QuoteStateAccepted:
			// Terminal, and a converted quote is an accepted quote: the one
			// order per quote rule would have nothing to convert from.
			return httpx.InvalidStateTransition("an accepted quote cannot be revised",
				httpx.Blocker("quote_accepted", "an accepted quote cannot be revised"))
		case QuoteStateDraft:
			return httpx.InvalidStateTransition("only sent, rejected and expired quotes can be revised; a draft is already editable",
				httpx.Blocker("quote_not_revisionable", "the quote is a draft: edit it"))
		default:
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot revise a quote that is %s", cur.Status.Status()))
		}
		if links, err = s.repo.RevokeDealerShareLinks(ctx, id, s.now().UTC()); err != nil {
			return err
		}
		if err := s.repo.ReviseToDraft(ctx, id); err != nil {
			return err
		}
		if out, err = s.repo.GetQuote(ctx, id); err != nil {
			return err
		}
		if s.auditor != nil {
			if err := s.auditor.Log(ctx, audit.Entry{
				Action:     "quote.revised",
				EntityType: "quote",
				EntityID:   id,
				UserID:     actorOf(ctx),
				Changes: map[string]any{
					"from_status": cur.Status.Status(), "version_number": out.VersionNumber,
					"reason": reason, "share_links_revoked": links,
				},
			}); err != nil {
				return err
			}
		}
		return s.writeEvent(ctx, s.buildEvent(out, EventRevised, cur.Status.Status()))
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListRevisions lists the quote's sent versions, newest first, behind the
// quote's own branch wall: one row past the limit tells the caller a next
// page exists.
func (s *Service) ListRevisions(ctx context.Context, id uuid.UUID, limit int, after *int) (items []QuoteRevision, hasMore bool, err error) {
	cur, err := s.repo.GetQuote(ctx, id)
	if err != nil {
		return nil, false, notFound(err)
	}
	if err := s.checkQuoteBranch(ctx, cur); err != nil {
		return nil, false, err
	}
	rows, err := s.repo.ListRevisions(ctx, id, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	return rows, hasMore, nil
}
