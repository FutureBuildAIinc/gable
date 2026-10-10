// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The operator resolution of a refund claim that outlived its act (seventh
// review P2-3, eighth review P3-2, FROM-PRINCIPAL 41): a PENDING intent left
// by a lost verdict, a crashed process or a refused transaction names money
// that may have left at the terminal, and nothing else in the counter can
// tell the two apart or move it. Finance reads the terminal, records what it
// answers, and the resolution books through the very rules any refund books
// through: a recorded refund replays the act's own money transaction with
// every cap and lock, an explicit no refund marks the claim FAILED and moves
// nothing. Both legs write one audit row naming the person, the intent, the
// outcome and the reason.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// ResolveIntentIn is a parsed resolution request.
type ResolveIntentIn struct {
	Outcome                 string
	RefundTxID              string
	ReconciliationReference string
	Reason                  string
}

// RefundIntentResolved is the resolution's answer: where the claim stands
// now and what the operator recorded.
type RefundIntentResolved struct {
	ID                uuid.UUID `json:"id"`
	SaleID            uuid.UUID `json:"sale_id"`
	TenderID          uuid.UUID `json:"tender_id"`
	Act               string    `json:"act"`
	State             string    `json:"state"`
	Outcome           string    `json:"outcome"`
	RefundTxID        string    `json:"refund_tx_id"`
	ReconciliationRef string    `json:"reconciliation_reference"`
}

// resolveAuditOf builds the audit row both legs write: the person, the
// intent, the outcome and the reason, one row, whatever moved or not.
func resolveAuditOf(rec *RefundIntentRecord, in ResolveIntentIn, actor string) audit.Entry {
	return audit.Entry{
		Action: "pos.refund_intent.resolved", EntityType: "pos_transaction", EntityID: rec.SaleID, UserID: actor,
		Changes: map[string]any{
			"intent_id": rec.ID, "act": rec.Act, "tender_id": rec.TenderID,
			"amount_cents": rec.AmountCents, "outcome": in.Outcome, "reason": in.Reason,
			"refund_tx_id": in.RefundTxID, "reconciliation_reference": in.ReconciliationReference,
		}}
}

// ResolveRefundIntent resolves one PENDING claim (FROM-PRINCIPAL 41): the
// operator records the terminal's actual outcome. A recorded refund (the
// gateway's own refund id) books through the refund's money transaction with
// every cap and lock; an explicit no refund with a reconciliation reference
// marks the claim FAILED and moves nothing. A claim that is not PENDING is
// refused: it has already been settled, one way or the other.
func (s *Service) ResolveRefundIntent(ctx context.Context, intentID uuid.UUID, in ResolveIntentIn, actor string) (*RefundIntentResolved, error) {
	if s.ar == nil || s.inventory == nil {
		return nil, errNotConfigured
	}
	rec, err := s.repo.GetRefundIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if rec.State != "PENDING" {
		return nil, conflict("intent_not_pending",
			fmt.Sprintf("the refund claim is %s: only a PENDING claim is resolved", rec.State))
	}
	out := &RefundIntentResolved{ID: rec.ID, SaleID: rec.SaleID, TenderID: rec.TenderID, Act: rec.Act,
		Outcome: in.Outcome, RefundTxID: in.RefundTxID, ReconciliationRef: in.ReconciliationReference}
	switch in.Outcome {
	case "no_refund":
		// nothing moved at the terminal: the claim is released and the money
		// is reconciled by the reference the operator recorded
		out.State = "FAILED"
		if err := s.inTx(ctx, func(ctx context.Context) error {
			if err := s.repo.FailRefundIntentByID(ctx, intentID); err != nil {
				return err
			}
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, resolveAuditOf(rec, in, actor)); err != nil {
					return fmt.Errorf("failed to write the resolution audit row: %w", err)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return out, nil
	case "refund":
		// the terminal moved the claim's money: the books take it through the
		// act's own money transaction, with every cap and lock, and the
		// recorded refund id on the claim
		out.State = "DONE"
		entry := resolveAuditOf(rec, in, actor)
		switch rec.Act {
		case "void":
			refunds := []gatewayRefund{{gatewayTxID: rec.GatewayTxID, refundTxID: in.RefundTxID,
				amountCents: rec.AmountCents, tenderID: rec.TenderID, act: "void", entity: rec.SaleID, actor: actor}}
			if _, err := s.runVoidTx(ctx, rec.SaleID, "", nil, in.Reason, actor, refunds, &entry); err != nil {
				// the replay refused after the terminal's money moved: the
				// claim stands and the orphan row records the refund
				s.recordOrphanRefund(ctx, refunds[0], err)
				return nil, err
			}
			return out, nil
		case "return":
			var stored storedReturnRequest
			if err := json.Unmarshal(rec.Request, &stored); err != nil {
				return nil, fmt.Errorf("the claim's stored request does not read back: %w", err)
			}
			st, err := s.priceReturn(ctx, stored.toReturnIn())
			if err != nil {
				return nil, err
			}
			st.actor = actor
			if id, err := uuid.Parse(actor); err == nil {
				st.cashierID = id
			}
			tenders, err := s.repo.GetTenders(ctx, rec.SaleID)
			if err != nil {
				return nil, err
			}
			var tenderCap int64
			for i := range tenders {
				if tenders[i].ID == rec.TenderID {
					tenderCap = int64(tenders[i].AmountCents)
				}
			}
			// the refund row names the recorded refund, and the claim the
			// replay completes with is the operator's own record
			st.in.GatewayTxID = in.RefundTxID
			claim := &refundClaim{intent: rec.RefundIntent, tenderCap: tenderCap,
				refundTxID: in.RefundTxID, resolveAudit: &entry}
			if _, err := s.postReturn(ctx, st, claim); err != nil {
				s.recordOrphanRefund(ctx, gatewayRefund{gatewayTxID: rec.GatewayTxID, refundTxID: in.RefundTxID,
					amountCents: rec.AmountCents, tenderID: rec.TenderID, act: "return", entity: rec.SaleID, actor: actor}, err)
				return nil, err
			}
			return out, nil
		default:
			return nil, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
				Message: "the claim names no act the counter can replay"}
		}
	default:
		return nil, invalid("outcome", "must be refund or no_refund")
	}
}
