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

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
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
	// The branch wall (ninth review P1-3, tenth review P2-2): the claim is
	// settled only inside the caller's branch. The sale read carries the
	// branch context the middleware settled, so a caller held to another
	// branch finds the missing row (404) before anything of the claim is
	// touched, on either leg; the legs' transactions take the same scoped
	// sale row lock.
	if _, err := s.repo.GetSale(ctx, rec.SaleID); err != nil {
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
		// is reconciled by the reference the operator recorded. The sale row
		// lock (scoped to the caller's branch) comes first, so the wall holds
		// under the release too.
		out.State = "FAILED"
		if err := s.inTx(ctx, func(ctx context.Context) error {
			if err := s.repo.LockSale(ctx, rec.SaleID); err != nil {
				return err
			}
			// the claim row, locked under the sale: a contender resolution
			// of the same claim loses here with the race named, never beside
			// this release (ninth review P2-2)
			held, err := s.repo.LockRefundIntent(ctx, intentID)
			if err != nil {
				return err
			}
			if held.State != "PENDING" {
				return conflict("intent_not_pending",
					fmt.Sprintf("the refund claim is %s: only a PENDING claim is resolved", held.State))
			}
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
			refund := gatewayRefund{gatewayTxID: rec.GatewayTxID, refundTxID: in.RefundTxID,
				amountCents: rec.AmountCents, tenderID: rec.TenderID, act: "void", entity: rec.SaleID, actor: actor}
			if _, err := s.runVoidResolutionTx(ctx, rec, in, entry, actor); err != nil {
				// the replay refused after the terminal's money moved: the
				// claim stands and the orphan row records the refund
				s.recordOrphanRefund(ctx, refund, err)
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

// runVoidResolutionTx books one tender's leg of a stranded void (ninth review
// P1-1, tenth review P1-2): the void's own money transaction reverses and
// refunds every card tender, so replaying it on the word of one claim booked a
// refund of every card of the sale, including the cards the terminal declined.
// The resolution accounts for each tender separately. It books only the
// resolved tender: the reversal of its application and the payout of the money
// the terminal moved, the claim completing DONE with the recorded refund id.
// The sale's void (the cash legs, the declined tenders' reversals, the invoice
// void, the stock, the sale's own VOIDED) completes in the same transaction
// only when no claim of the sale is PENDING anymore; until then the sale
// stands completed between resolutions. A tender whose refund was declined,
// or recorded as a no refund, is never paid out: its application is reversed
// and the money stays unapplied on the customer's account, the credit the
// dealer owes, and no method may return the sale's goods again once a tender's
// refund is booked (the return routes read the same table).
func (s *Service) runVoidResolutionTx(ctx context.Context, rec *RefundIntentRecord, in ResolveIntentIn, entry audit.Entry, actor string) (*Sale, error) {
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, rec.SaleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, rec.SaleID)
		if err != nil {
			return err
		}
		if sale.Status != StatusCompleted {
			return httpx.InvalidStateTransition("only a completed sale is voided")
		}
		if has, err := s.repo.SaleHasReturns(ctx, rec.SaleID); err != nil {
			return err
		} else if has {
			return conflict("has_returns",
				"this sale has been returned against: return the rest through POST /pos/returns instead of voiding it")
		}
		if sale.TillSessionID != nil {
			if err := s.repo.LockTillSession(ctx, *sale.TillSessionID, false); err != nil {
				return err
			}
			session, err := s.repo.GetTillSession(ctx, *sale.TillSessionID)
			if err != nil {
				return err
			}
			if session.Status != TillOpen {
				return conflict("session_closed",
					"the till session this sale was made in is closed: return the goods through POST /pos/returns instead")
			}
		}
		if sale.InvoiceID == nil {
			return conflict("no_invoice", "the sale carries no invoice to void")
		}
		// The claim row, locked under the sale: a concurrent resolution of the
		// same claim settles here or waits on the sale lock, never beside this
		// booking.
		held, err := s.repo.LockRefundIntent(ctx, rec.ID)
		if err != nil {
			return err
		}
		if held.State != "PENDING" {
			return conflict("intent_not_pending",
				fmt.Sprintf("the refund claim is %s: only a PENDING claim is resolved", held.State))
		}
		tenders, err := s.repo.GetTenders(ctx, rec.SaleID)
		if err != nil {
			return err
		}
		var tender *Tender
		for i := range tenders {
			if tenders[i].ID == rec.TenderID {
				tender = &tenders[i]
			}
		}
		if tender == nil || tender.PaymentID == nil {
			return conflict("tender_refunded", "the claim names no card tender the sale still holds")
		}
		date, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
		if err != nil {
			return err
		}
		// The resolved tender's leg: the claim completes with the recorded
		// refund, its application reverses, and the money the terminal moved
		// is paid out under the refund id the operator recorded.
		if err := s.repo.CompleteRefundIntent(ctx, rec.SaleID, rec.TenderID, in.RefundTxID); err != nil {
			return err
		}
		applicationID, err := cardApplicationOf(ctx, s, *tender.PaymentID)
		if err != nil {
			return err
		}
		var fxAll *account.Effects
		fx, err := s.ar.Reverse(ctx, account.ReverseIn{
			ApplicationID: applicationID, Reason: in.Reason, Actor: actor, On: date})
		if err != nil {
			return err
		}
		mergeEffects(&fxAll, fx)
		_, rfx, err := s.ar.RefundPayment(ctx, account.RefundPaymentIn{
			PaymentID: *tender.PaymentID, AmountCents: rec.AmountCents, Reason: in.Reason,
			GatewayRefundID: in.RefundTxID, Actor: actor, On: date})
		if err != nil {
			return err
		}
		mergeEffects(&fxAll, rfx)
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, entry); err != nil {
				return fmt.Errorf("failed to write the resolution audit row: %w", err)
			}
		}
		// The completion: only when no claim of the sale still stands, so the
		// void never completes on the word of one tender while another's
		// outcome is unknown. The tenders whose refunds were never made keep
		// their charge, held unapplied on the account as the credit owed.
		pending, err := s.repo.PendingRefundIntentIDs(ctx, rec.SaleID)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			lines, err := s.repo.GetLines(ctx, rec.SaleID)
			if err != nil {
				return err
			}
			for _, t := range tendersInIDOrder(tenders) {
				if t.PaymentID == nil {
					continue
				}
				switch t.Method {
				case TenderCash, TenderCheck:
					fx, err := s.ar.VoidPayment(ctx, account.VoidPaymentIn{
						PaymentID: *t.PaymentID, Reason: in.Reason, Actor: actor, On: date})
					if err != nil {
						return err
					}
					mergeEffects(&fxAll, fx)
				case TenderCard:
					applicationID, live, err := cardApplicationLive(ctx, s, *t.PaymentID)
					if err != nil {
						return err
					}
					if !live {
						continue
					}
					fx, err := s.ar.Reverse(ctx, account.ReverseIn{
						ApplicationID: applicationID, Reason: in.Reason, Actor: actor, On: date})
					if err != nil {
						return err
					}
					mergeEffects(&fxAll, fx)
				}
			}
			var entryID *uuid.UUID
			if err := s.db.GetExecutor(ctx).QueryRow(ctx,
				`SELECT gl_entry_id FROM invoices WHERE id = $1`, sale.InvoiceID).Scan(&entryID); err != nil {
				return err
			}
			fx, err := s.ar.VoidInvoice(ctx, account.VoidInvoiceIn{
				InvoiceID: *sale.InvoiceID, EntryID: entryID, Reason: in.Reason, Actor: actor, On: date})
			if err != nil {
				return err
			}
			mergeEffects(&fxAll, fx)
			for i := range lines {
				l := &lines[i]
				if !l.IsStocked() || l.Quantity == nil {
					continue
				}
				if err := s.inventory.RestockQty(ctx, *l.ProductID, sale.BranchID, *l.Quantity); err != nil {
					return err
				}
			}
			if err := s.repo.VoidSale(ctx, rec.SaleID); err != nil {
				return err
			}
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, audit.Entry{
					Action: "pos.transaction.voided", EntityType: "pos_transaction", EntityID: rec.SaleID, UserID: actor,
					Changes: map[string]any{
						"number": sale.Number, "total_cents": int64(sale.TotalCents), "reason": in.Reason,
						"register_id": sale.RegisterID, "customer_id": sale.CustomerID,
						"resolved_by_claim": rec.ID,
					}}); err != nil {
					return fmt.Errorf("failed to write the audit row: %w", err)
				}
			}
			branch := sale.BranchID
			if err := s.recordEvent(ctx, outbox.Event{Type: EventSaleVoided, EntityType: "pos_transaction", EntityID: rec.SaleID,
				BranchID: &branch, Data: eventJSON(map[string]any{
					"number": sale.Number, "customer_id": sale.CustomerID, "status": StatusVoided.Status(),
					"from_status": StatusCompleted.Status(), "revision": sale.Revision + 1, "currency": sale.Currency,
					"total_cents": int64(sale.TotalCents), "reason": in.Reason,
				})}); err != nil {
				return err
			}
		}
		if fxAll != nil {
			for _, ev := range fxAll.Events() {
				if err := s.recordEvent(ctx, ev); err != nil {
					return err
				}
			}
		}
		out, err = s.GetSale(ctx, rec.SaleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
