// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The payback of a declined void tender's credit (C2-5, FROM-PRINCIPAL
// 60b): a tender whose refund the terminal declined keeps its charge as
// unapplied credit on the customer once the void completes (held to its
// tender, round 9's apply refusal), and the one way back is that card,
// through the counter terminal rail: the same rail the charge came from,
// never the payment route's online gateway. The payback route is a small
// sibling of the resolve route, keyed on the void's own released claim,
// finance and owner only, and it runs its terminal call under the same
// claim discipline every terminal refund runs under: a committed payback
// claim per tender opened before the call (one live claim per tender, the
// in flight lease), the terminal's refund id written in its own committed
// write the moment it answers, and the money transaction booking the
// credit out only under the claim's row lock. A payback whose call answers
// no verdict stands PENDING like any act and settles through the resolve
// route.

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// PaybackIntentIn is a parsed payback request.
type PaybackIntentIn struct {
	Reason string
}

// RefundIntentPaidBack is the payback's answer: the credit it paid, the
// terminal's refund id, and the payback's own claim beside the void's.
type RefundIntentPaidBack struct {
	ID          uuid.UUID `json:"id"`
	PaybackID   uuid.UUID `json:"payback_id"`
	SaleID      uuid.UUID `json:"sale_id"`
	TenderID    uuid.UUID `json:"tender_id"`
	AmountCents int64     `json:"amount_cents"`
	RefundTxID  string    `json:"refund_tx_id"`
	State       string    `json:"state"`
}

// paybackAuditOf builds the audit row the payback writes, the resolve
// route's own shape (the person, the intent, the outcome and the reason):
// the intent named is the void's released claim the route keys on, and the
// payback's own claim rides beside it.
func paybackAuditOf(origin *RefundIntentRecord, claim RefundIntent, refundTxID, reason, actor string) audit.Entry {
	return audit.Entry{
		Action: "pos.refund_intent.paid_back", EntityType: "pos_transaction", EntityID: origin.SaleID, UserID: actor,
		Changes: map[string]any{
			"intent_id": origin.ID, "payback_intent_id": claim.ID, "act": claim.Act, "tender_id": claim.TenderID,
			"amount_cents": claim.AmountCents, "outcome": "refund", "reason": reason,
			"refund_tx_id": refundTxID,
		}}
}

// PaybackRefundIntent pays a declined void tender's credit back to its card
// through the counter terminal rail (FROM-PRINCIPAL 60b). The route keys on
// the void's own released claim: only a void claim the terminal (or a
// recorded no refund) released, of a sale whose void completed, holds a
// credit, and only while the tender's payment still carries it unapplied;
// a second payback answers 409 and never reaches the terminal.
func (s *Service) PaybackRefundIntent(ctx context.Context, intentID uuid.UUID, in PaybackIntentIn, actor string) (*RefundIntentPaidBack, error) {
	if s.ar == nil {
		return nil, errNotConfigured
	}
	if s.gateway == nil {
		return nil, unavailableTerminal()
	}
	rec, err := s.repo.GetRefundIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	// The branch wall, the resolve route's own (r12 P1-2): the sale read
	// carries the branch context the middleware settled, so a caller outside
	// the sale's branch finds the missing row before anything of the claim is
	// read; the handler holds the no context caller the same way.
	sale, err := s.repo.GetSale(ctx, rec.SaleID)
	if err != nil {
		return nil, err
	}
	// Every guard that needs no lock runs BEFORE the terminal is asked for
	// money: a refusal never moves money at the terminal.
	if rec.Act != "void" {
		return nil, conflict("no_held_credit",
			fmt.Sprintf("the claim's act is %s: only a void's declined tender leaves a credit on its payment", rec.Act))
	}
	if rec.State != "FAILED" {
		return nil, conflict("intent_not_failed",
			fmt.Sprintf("the refund claim is %s: only a void claim the terminal (or a recorded no refund) released holds a credit to pay back", rec.State))
	}
	if sale.Status != StatusVoided {
		return nil, conflict("no_held_credit",
			fmt.Sprintf("the sale is %s: the void has not completed, so the tender's charge is still applied to its invoice", sale.Status.Status()))
	}
	_, unapplied, err := s.repo.TenderPaymentFacts(ctx, rec.TenderID)
	if err != nil {
		return nil, err
	}
	if unapplied <= 0 {
		return nil, conflict("no_held_credit",
			"the tender's payment holds no unapplied credit: the credit was already paid back to its card")
	}
	// The payback's own claim, committed before the terminal is reached (the
	// same discipline every terminal refund runs under): its unique live
	// (sale, tender) index refuses a concurrent payback, and its lease holds
	// the resolve route and the drawer close while the call is on the wire.
	claim := RefundIntent{ID: uuid.New(), SaleID: rec.SaleID, TenderID: rec.TenderID, GatewayTxID: rec.GatewayTxID,
		AmountCents: unapplied, Act: "payback"}
	if err := s.inTx(ctx, func(ctx context.Context) error {
		return s.repo.InsertRefundIntent(ctx, claim)
	}); err != nil {
		return nil, err
	}
	// The terminal call, bounded: every path it can take ends inside the
	// bound, so the claim it holds never sits in flight past its lease.
	callCtx, cancel := context.WithTimeout(ctx, refundCallTimeout)
	res, err := s.gateway.Refund(callCtx, claim.GatewayTxID, claim.AmountCents)
	cancel()
	if err != nil {
		s.unknownRefundOutcome(ctx, claim, err)
		return nil, fmt.Errorf("card refund failed: %w", err)
	}
	verdict := refundVerdictOf(res)
	if verdict != refundAccepted {
		if verdict == refundDeclined {
			s.failRefundIntent(ctx, claim)
		} else {
			s.unknownRefundOutcome(ctx, claim, noVerdictOf(res))
		}
		return nil, conflict("card_refund", fmt.Sprintf("the card refund was not accepted (%s)", res.Status))
	}
	// The terminal's own refund id, on the claim the moment it answers, in
	// its own committed write whatever happens next.
	if err := s.repo.MarkRefundIntentRefunded(ctx, claim.ID, res.TransactionID); err != nil {
		s.logger.Error("CRITICAL: the payback's refund id was not written to the claim",
			"intent_id", claim.ID, "sale_id", claim.SaleID, "error", err)
	}
	entry := paybackAuditOf(rec, claim, res.TransactionID, in.Reason, actor)
	out, err := s.runPaybackTx(ctx, claim, res.TransactionID, in.Reason, actor, &entry)
	if err != nil {
		// the terminal moved the money and the books refused: one committed
		// orphan row carries the ids, and the claim it leaves standing keeps
		// the tender from being paid twice until it is reconciled.
		s.recordOrphanRefund(ctx, gatewayRefund{gatewayTxID: claim.GatewayTxID, refundTxID: res.TransactionID,
			amountCents: claim.AmountCents, tenderID: claim.TenderID, act: "payback", entity: claim.SaleID, actor: actor}, err)
		return nil, err
	}
	return out, nil
}

// runPaybackTx books one payback under its claim's row lock: the credit is
// paid out of the tender's payment's unapplied cash under the terminal's own
// refund id, and the claim completes DONE with it. The payback route runs it
// after its terminal call; the resolve route's refund leg runs the same
// booking on a payback claim whose call answered no verdict, on the word of
// the operator's recorded id.
func (s *Service) runPaybackTx(ctx context.Context, claim RefundIntent, refundTxID, reason, actor string, entry *audit.Entry) (*RefundIntentPaidBack, error) {
	var out *RefundIntentPaidBack
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, claim.SaleID); err != nil {
			return err
		}
		// The claim row, locked and read right after the sale lock (r12
		// P3-1): the loser of a race is told the race, never a cap the
		// winner's own booking answers; a claim still inside its in flight
		// lease with no answer settles nothing (the fourteenth review's P1-1),
		// and the refund booked must be the terminal's own id when the claim
		// carries one.
		held, err := s.repo.LockRefundIntent(ctx, claim.ID)
		if err != nil {
			return err
		}
		if held.State != "PENDING" {
			return conflict("intent_not_pending",
				fmt.Sprintf("the refund claim is %s: only a PENDING claim is resolved", held.State))
		}
		if err := s.settleUnansweredClaim(ctx, held); err != nil {
			return err
		}
		if held.RefundTxID != "" && held.RefundTxID != refundTxID {
			return conflict("refund_tx_id_conflict",
				fmt.Sprintf("the claim carries the terminal's refund id %s, not %s: record the terminal's own id",
					held.RefundTxID, refundTxID))
		}
		tenders, err := s.repo.GetTenders(ctx, claim.SaleID)
		if err != nil {
			return err
		}
		var tender *Tender
		for i := range tenders {
			if tenders[i].ID == claim.TenderID {
				tender = &tenders[i]
			}
		}
		if tender == nil || tender.PaymentID == nil {
			return conflict("no_held_credit", "the claim names no card tender the sale still holds")
		}
		sale, err := s.repo.GetSale(ctx, claim.SaleID)
		if err != nil {
			return err
		}
		date, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
		if err != nil {
			return err
		}
		originID := originOf(ctx, s, claim)
		// the claim completes with the posting; the credit leaves the
		// payment's unapplied cash under the terminal's own refund id (the
		// cash account moves by the refund), exactly the way the resolve
		// route's refund leg books a recorded refund
		if err := s.repo.CompleteRefundIntent(ctx, claim.SaleID, claim.TenderID, refundTxID); err != nil {
			return err
		}
		_, rfx, err := s.ar.RefundPayment(ctx, account.RefundPaymentIn{
			PaymentID: *tender.PaymentID, AmountCents: held.AmountCents, Reason: reason,
			GatewayRefundID: refundTxID, Actor: actor, On: date})
		if err != nil {
			return err
		}
		if entry != nil && s.auditLog != nil {
			if err := s.auditLog.Log(ctx, *entry); err != nil {
				return fmt.Errorf("failed to write the payback audit row: %w", err)
			}
		}
		if rfx != nil {
			for _, ev := range rfx.Events() {
				if err := s.recordEvent(ctx, ev); err != nil {
					return err
				}
			}
		}
		out = &RefundIntentPaidBack{ID: originID, PaybackID: claim.ID, SaleID: claim.SaleID,
			TenderID: claim.TenderID, AmountCents: held.AmountCents, RefundTxID: refundTxID, State: "DONE"}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// originOf reads the void claim the payback route keyed on (the tender's
// released void claim), for the answer's own id.
func originOf(ctx context.Context, s *Service, claim RefundIntent) uuid.UUID {
	var id uuid.UUID
	if err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM pos_refund_intents WHERE sale_id = $1 AND tender_id = $2 AND act = 'void' AND state = 'FAILED'
		ORDER BY created_at, id LIMIT 1`, claim.SaleID, claim.TenderID).Scan(&id); err != nil {
		return claim.ID
	}
	return id
}

// unavailableTerminal answers the payback's own unconfigured terminal
// refusal: the held credit's one rail is the counter terminal.
func unavailableTerminal() error {
	return httpx.Unavailable("the card terminal is not configured: a held credit is paid back through it alone")
}
