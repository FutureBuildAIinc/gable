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
	"fmt"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// reversalTimeout bounds the gateway calls and the audit write of a
// compensation, which run detached from the refused request.
const reversalTimeout = 30 * time.Second

// gatewayRefund is one refund the counter made at the gateway before its
// transaction opened.
type gatewayRefund struct {
	// gatewayTxID is the charge the refund was taken against; refundTxID the
	// refund's own id at the gateway.
	gatewayTxID, refundTxID string
	amountCents             int64
	// act names the refused act ("void" or "return") for the audit row.
	act    string
	entity uuid.UUID
	actor  string
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
)

// reverseCharge undoes an approved charge no document backs, the C2-4 card
// route's way: the same day void first, the refund when the void is refused
// (a settled capture). The outcome is logged and written to the audit log in
// its own committed write; a failing audit write is logged and never hides
// the outcome. On failure it answers the 502 the route returns instead of
// the refusal that caused it: the un-returned money outranks the reason.
func (s *Service) reverseCharge(ctx context.Context, gatewayTxID string, amountCents int64, entity uuid.UUID, actor string, cause error) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
	defer cancel()
	outcome, reverseCause := chargeVoided, error(nil)
	if _, voidErr := s.gateway.Void(rctx, gatewayTxID); voidErr != nil {
		s.logger.Info("gateway void refused, refunding instead",
			"gateway_tx_id", gatewayTxID, "entity_id", entity, "error", voidErr)
		if _, refundErr := s.gateway.Refund(rctx, gatewayTxID, amountCents); refundErr != nil {
			outcome, reverseCause = chargeFailed, fmt.Errorf("void: %v; refund: %v", voidErr, refundErr)
		} else {
			outcome = chargeRefunded
		}
	}
	switch outcome {
	case chargeFailed:
		s.logger.Error("CRITICAL: the gateway charged, the transaction refused, and the reversal failed",
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
	if outcome == chargeFailed {
		return &httpx.Error{Status: http.StatusBadGateway, Code: httpx.CodeChargeNotReversed, Operator: true,
			Message: fmt.Sprintf("the card was charged and the charge could not be reversed; finance must reconcile gateway transaction %s", gatewayTxID)}
	}
	return nil
}
