// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The gateway money of a refused transaction (ADR 0005 section 14.2 C2-5,
// first review P1-2, second review P1-5). The guards that need no lock run
// before any gateway call, so a refusal never moves money at the terminal;
// the residual race (the state moves between the pre-read and the lock) is
// closed here: a charge the transaction refuses is put back (the same day
// void first, the refund when the void is refused, as the C2-4 card route
// does, answering 502 charge_not_reversed when neither works), and a refund
// the transaction refuses is recorded in a committed audit row of its own
// carrying the gateway id, for finance to reconcile.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// reversalTimeout bounds the gateway calls and the audit write of a
// compensation, which run detached from the refused request.
const reversalTimeout = 30 * time.Second

// refundCallTimeout bounds one terminal refund call, so every path a claim's
// call can take ends inside it: an answer, the terminal's own no, or an
// error that stamps the claim answered with no verdict. Without a bound a
// hung connection would hold the tender's claim in flight forever.
const refundCallTimeout = 30 * time.Second

// refundAnswerLease is the stated in flight timeout a claim's terminal
// call carries (fourteenth review P1-1): while it runs and the terminal has not
// answered, the resolve route and the drawer close refuse the claim; once it
// expires with no answer at all (a crashed or hung act) the claim becomes
// resolvable and records that the terminal never spoke. It exceeds the call
// bound so a bounded call's error always lands inside the lease; that holds
// for one call, so a void's sequential calls restamp each claim's lease in
// a committed write immediately before its own call, and a later tender's
// call never runs under a lease the earlier calls already spent (the
// sixteenth review's P1-2).
const refundAnswerLease = 90 * time.Second

// gatewayRefund is one refund the counter made at the gateway before its
// transaction opened.
type gatewayRefund struct {
	// gatewayTxID is the charge the refund was taken against; refundTxID the
	// refund's own id at the gateway.
	gatewayTxID, refundTxID string
	amountCents             int64
	// tenderID is the tender the intent claimed (the money transaction marks
	// that intent done as it posts).
	tenderID uuid.UUID
	// act names the refused act ("void" or "return") for the audit row.
	act    string
	entity uuid.UUID
	actor  string
}

// abandonRefundIntent strands a claim the act ended before the terminal was
// reached, in its own committed write: the terminal will never answer for it,
// so the claim records that (UNKNOWN) and stays resolvable as any stranded
// claim is. A failing write is logged and left alone: the claim stays on its
// in flight lease, which expires and reaches the same state (the safe side).
func (s *Service) abandonRefundIntent(ctx context.Context, intent RefundIntent) {
	if err := s.repo.AbandonRefundIntent(context.WithoutCancel(ctx), intent.ID); err != nil {
		s.logger.Error("CRITICAL: the never called claim was not abandoned",
			"act", intent.Act, "sale_id", intent.SaleID, "tender_id", intent.TenderID, "error", err)
	}
}

// failRefundIntent releases a refund intent whose gateway call failed, in
// its own committed write: no money moved for it, so the tender may be
// refunded again. A failing write is logged and left alone: the intent
// stays PENDING, which refuses future refunds of the tender until it is
// reconciled (the safe side). A release that settles no row means another
// hand (a concurrent resolution) settled the claim first: nothing of this
// call's own refusal is lost by that, so it is noted and not alarmed.
func (s *Service) failRefundIntent(ctx context.Context, intent RefundIntent) {
	if err := s.repo.FailRefundIntent(ctx, intent.SaleID, intent.TenderID); err != nil {
		var hx *httpx.Error
		if errors.As(err, &hx) && hx.Code == httpx.CodeConflict {
			s.logger.Info("the declined refund claim was settled by another hand",
				"act", intent.Act, "sale_id", intent.SaleID, "tender_id", intent.TenderID)
			return
		}
		s.logger.Error("CRITICAL: the failed refund intent was not released",
			"act", intent.Act, "sale_id", intent.SaleID, "tender_id", intent.TenderID, "error", err)
	}
}

// refundVerdict is how a refund call's answer reads: accepted (the money
// moved), declined (the terminal's own no: nothing moved), or unknown (every
// other answer proves nothing either way; tenth review P2-3).
type refundVerdict int

const (
	refundAccepted refundVerdict = iota
	refundDeclined
	refundUnknown
)

// refundVerdictOf reads the terminal's answer to a refund or void call. Only
// the explicit decline releases a claim: a PENDING answer means the refund is
// under way and an ERROR is the terminal's own retry mapping, and neither
// proves the money did not move.
func refundVerdictOf(res *payment.GatewayResult) refundVerdict {
	switch res.Status {
	case payment.GatewayStatusRefunded, payment.GatewayStatusVoided, payment.GatewayStatusApproved:
		return refundAccepted
	case payment.GatewayStatusDeclined:
		return refundDeclined
	default:
		return refundUnknown
	}
}

// noVerdictOf builds the record for an answer that carries no verdict, so
// the unknown outcome audit row names what the terminal said.
func noVerdictOf(res *payment.GatewayResult) error {
	return fmt.Errorf("the terminal answered %s", res.Status)
}

// checkCardClaim refuses a claim the intent table already contradicts
// (seventh review P1-1, made structural): a tender whose void intent is DONE
// was refunded whole; a tender another standing claim already holds is
// refused as in flight (the same refusal its unique index makes at the
// insert, answered before the insert); and no claim may take the tender past
// what it kept less every refund already booked (the card returns, whose
// DONE intents commit beside their rows). Runs under the sale row lock
// inside the claim's own transaction, before the terminal is reached.
func (s *Service) checkCardClaim(ctx context.Context, saleID, tenderID uuid.UUID, amountCents, tenderCap int64) error {
	facts, err := s.repo.RefundIntentFacts(ctx, saleID, tenderID)
	if err != nil {
		return err
	}
	if facts.DoneVoid {
		return conflict("tender_refunded",
			"the card tender was already refunded whole by this sale's void: no second refund of it can be made")
	}
	if facts.PendingCents > 0 {
		return conflict("refund_in_flight",
			"a card refund of this sale's tender is already in flight: only one refund of a tender runs at a time")
	}
	// Defence in depth (fourteenth review P1-1): a recorded refund the terminal moved but
	// the books never took holds the tender even on a settled claim, so a
	// late answer behind an early release can never be paid twice.
	if facts.UnbookedRefundedCents > 0 {
		return conflict("tender_refunded",
			"the terminal already refunded this tender under a recorded refund id that never booked: reconcile that claim before refunding the tender again")
	}
	booked, err := s.repo.CardRefundedCents(ctx, saleID)
	if err != nil {
		return err
	}
	if amountCents > tenderCap-booked {
		return conflict("exceeds_card_tender",
			fmt.Sprintf("the card tender kept %d cents less the %d booked card refunds: refund the rest from the drawer",
				tenderCap, booked))
	}
	return nil
}

// unknownRefundOutcome records a refund whose terminal call answered no
// verdict (a transport or context error: a timeout, a dropped connection, a
// cancel after the terminal acted): the intent stays PENDING, because the
// terminal may have moved the money and releasing the claim would let the
// next refund pay the tender twice, and one committed audit row carries the
// intent's own id and the unknown outcome for reconciliation.
func (s *Service) unknownRefundOutcome(ctx context.Context, intent RefundIntent, err error) {
	s.logger.Error("CRITICAL: the card refund answered no verdict; the claim stands until it is reconciled",
		"intent_id", intent.ID, "act", intent.Act, "sale_id", intent.SaleID, "tender_id", intent.TenderID,
		"gateway_tx_id", intent.GatewayTxID, "amount_cents", intent.AmountCents, "error", err)
	// The call is off the wire (fourteenth review P1-1): stamp the answer so the claim is
	// stranded and resolvable, exactly as the unknown outcome row records it,
	// never held as though its terminal call were still live. A failing stamp
	// is logged beside the row: the claim stays IN_FLIGHT until its lease
	// expires, the safe side.
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
	defer scancel()
	if serr := s.repo.StampRefundIntentAnswered(sctx, intent.ID); serr != nil {
		s.logger.Error("CRITICAL: the no verdict answer was not stamped on the claim",
			"intent_id", intent.ID, "sale_id", intent.SaleID, "error", serr)
	}
	if s.auditLog == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
	defer cancel()
	if aerr := s.auditLog.Log(rctx, audit.Entry{
		Action: "pos.refund_outcome_unknown", EntityType: "pos_transaction", EntityID: intent.SaleID,
		Changes: map[string]any{
			"intent_id": intent.ID, "act": intent.Act, "tender_id": intent.TenderID,
			"gateway_tx_id": intent.GatewayTxID, "amount_cents": intent.AmountCents,
			"outcome": "unknown", "error": fmt.Sprintf("%v", err),
		}}); aerr != nil {
		s.logger.Error("CRITICAL: the unknown refund outcome audit row was not written",
			"intent_id", intent.ID, "sale_id", intent.SaleID, "error", aerr)
	}
}

// recordOrphanRefund writes, in its own committed write after the refused
// transaction rolled back, one audit row per refund the transaction left
// standing, carrying the gateway ids; the CRITICAL log line carries the same
// facts. A failing audit write is logged and never hides the refusal.
func (s *Service) recordOrphanRefund(ctx context.Context, r gatewayRefund, cause error) {
	s.logger.Error("CRITICAL: the gateway refunded and the transaction refused",
		"act", r.act, "gateway_tx_id", r.gatewayTxID, "gateway_refund_id", r.refundTxID,
		"entity_id", r.entity, "amount_cents", r.amountCents, "error", cause)
	if s.auditLog == nil {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
	defer cancel()
	if err := s.auditLog.Log(rctx, audit.Entry{
		Action: "pos.gateway_refund_orphaned", EntityType: "pos_transaction", EntityID: r.entity, UserID: r.actor,
		Changes: map[string]any{
			"act": r.act, "gateway_tx_id": r.gatewayTxID, "gateway_refund_id": r.refundTxID,
			"amount_cents": r.amountCents, "cause": fmt.Sprintf("%v", cause),
		}}); err != nil {
		s.logger.Error("CRITICAL: the orphaned refund audit row was not written",
			"act", r.act, "gateway_tx_id", r.gatewayTxID, "gateway_refund_id", r.refundTxID, "error", err)
	}
}

// chargeOutcome is how a reversal of an approved charge ended.
type chargeOutcome string

const (
	chargeVoided   chargeOutcome = "voided"
	chargeRefunded chargeOutcome = "refunded"
	chargeFailed   chargeOutcome = "failed"
	chargeUnknown  chargeOutcome = "unknown"
)

// fallbackRefundKey is the key of the refund sent when the void of the same
// reversal was refused: a key of its own, so a gateway that answers a
// retried key from its record (or a driver that sends a key once) never
// answers the refund with the void's no (CP-05 round 3 P2-B).
func fallbackRefundKey(key string) string {
	if key == "" {
		return ""
	}
	return key + "-refund"
}

// refundUnder sends one refund and reads its verdict, not only its error
// (CP-05 round 3 P1-A): only an accepted answer is the money going back.
func refundUnder(ctx context.Context, gw payment.PaymentGateway, gatewayTxID string, amountCents int64, key string) (chargeOutcome, error) {
	res, err := payment.RefundUnderKey(ctx, gw, gatewayTxID, amountCents, key)
	if err != nil {
		return chargeFailed, err
	}
	switch refundVerdictOf(res) {
	case refundAccepted:
		return chargeRefunded, nil
	case refundDeclined:
		return chargeFailed, fmt.Errorf("the gateway declined the refund")
	default:
		return chargeUnknown, noVerdictOf(res)
	}
}

// voidThenRefund puts a whole charge back: the void first, then the refund
// under its own key when the void is refused (an error, as a settled capture
// answers) or definitely declined. The void's verdict is read, not only its
// error (CP-05 round 3 P1-A): a gateway may answer a decline with no error,
// as the Run terminal answers a C. An answer that is neither the reversal
// nor the terminal's own no proves nothing either way, so no refund is sent
// on top of a void that may have gone through, and the outcome is unknown.
func voidThenRefund(ctx context.Context, gw payment.PaymentGateway, gatewayTxID string, amountCents int64, key string) (chargeOutcome, error) {
	res, voidErr := payment.VoidUnderKey(ctx, gw, gatewayTxID, key)
	if voidErr == nil {
		switch refundVerdictOf(res) {
		case refundAccepted:
			return chargeVoided, nil
		case refundDeclined:
			voidErr = fmt.Errorf("the gateway declined the void")
		default:
			return chargeUnknown, fmt.Errorf("void: %v", noVerdictOf(res))
		}
	}
	outcome, refundErr := refundUnder(ctx, gw, gatewayTxID, amountCents, fallbackRefundKey(key))
	if refundErr != nil {
		return outcome, fmt.Errorf("void: %v; refund: %v", voidErr, refundErr)
	}
	return outcome, nil
}

// reverseCharge undoes an approved charge no document backs, the C2-4 card
// route's way: the same day void first, the refund when the void is refused
// (a settled capture). The outcome is logged and written to the audit log in
// its own committed write; a failing audit write is logged and never hides
// the outcome. On failure, or an outcome the gateway left unknown, it
// answers the 502 the route returns instead of the refusal that caused it:
// the un-returned money outranks the reason.
func (s *Service) reverseCharge(ctx context.Context, gatewayTxID string, amountCents int64, entity uuid.UUID, actor string, cause error, key string) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
	defer cancel()
	outcome, reverseCause := voidThenRefund(rctx, s.gateway, gatewayTxID, amountCents, key)
	switch outcome {
	case chargeFailed, chargeUnknown:
		s.logger.Error("CRITICAL: the gateway charged, the transaction refused, and the reversal failed or is unknown",
			"outcome", string(outcome),
			"gateway_tx_id", gatewayTxID, "entity_id", entity, "amount_cents", amountCents, "error", reverseCause)
	default:
		s.logger.Warn("the gateway charge was reversed: the transaction refused the act",
			"outcome", string(outcome), "gateway_tx_id", gatewayTxID, "entity_id", entity,
			"amount_cents", amountCents, "error", cause)
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(rctx, audit.Entry{
			Action: "pos.card_charge_reversal", EntityType: "pos_transaction", EntityID: entity, UserID: actor,
			Changes: map[string]any{
				"gateway_tx_id": gatewayTxID, "amount_cents": amountCents, "outcome": string(outcome),
				"cause": fmt.Sprintf("%v", cause),
			}}); err != nil {
			s.logger.Error("CRITICAL: the charge reversal audit row was not written",
				"gateway_tx_id", gatewayTxID, "entity_id", entity, "outcome", string(outcome), "error", err)
		}
	}
	if outcome == chargeFailed || outcome == chargeUnknown {
		return &httpx.Error{Status: http.StatusBadGateway, Code: httpx.CodeChargeNotReversed, Operator: true,
			Message: fmt.Sprintf("the card was charged and the charge could not be reversed; finance must reconcile gateway transaction %s", gatewayTxID)}
	}
	return nil
}
