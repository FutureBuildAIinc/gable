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

// IntentSaleBranch answers the branch of the claim's sale, for the resolution
// route's branch hold (r12 P1-2): a caller whose context carries no branch
// (multi_branch_enabled on, default_branch_required off, no X-Branch-Id
// header) reads the sale through a repository filter that never fires, so
// the route holds the sale's own branch to the caller's grants before either
// leg is entered. A caller whose context names a branch the sale is outside
// still finds the missing row here, as the sale read already answered.
func (s *Service) IntentSaleBranch(ctx context.Context, intentID uuid.UUID) (uuid.UUID, error) {
	rec, err := s.repo.GetRefundIntent(ctx, intentID)
	if err != nil {
		return uuid.Nil, err
	}
	sale, err := s.repo.GetSale(ctx, rec.SaleID)
	if err != nil {
		return uuid.Nil, err
	}
	return sale.BranchID, nil
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
	// The claim's own terminal call (fourteenth review P1-1): a claim with no answer inside
	// its in flight lease is live, not stranded. Releasing it (a no refund)
	// would free a tender whose money may still move, and the terminal's late
	// answer would then book beside a settled claim. The legs recheck the
	// same lease under the claim's row lock.
	if err := s.refuseUnansweredClaim(rec); err != nil {
		return nil, err
	}
	// The outcome the operator records cannot contradict the claim's own
	// record (ninth review P2-1, tenth review P3-1): the claim's refund id
	// was written in its own committed write the moment the terminal
	// answered, so a PENDING claim carrying one is a refund that moved. A no
	// refund over it would release a tender whose money is gone, and a
	// refund under a different id would book money the terminal never moved.
	if in.Outcome == "no_refund" && rec.RefundTxID != "" {
		return nil, conflict("refund_recorded",
			fmt.Sprintf("the claim carries the terminal's refund id %s: the money moved, record the refund, not a no refund", rec.RefundTxID))
	}
	if in.Outcome == "refund" && rec.RefundTxID != "" && in.RefundTxID != rec.RefundTxID {
		return nil, conflict("refund_tx_id_conflict",
			fmt.Sprintf("the claim carries the terminal's refund id %s, not %s: record the terminal's own id", rec.RefundTxID, in.RefundTxID))
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
			sale, err := s.repo.GetSale(ctx, rec.SaleID)
			if err != nil {
				return err
			}
			// A void's release can complete the sale's void (r12 P1-1), so
			// the drawer's session is held here too: this leg takes the sale,
			// then the session, then the claim, while the refund leg
			// (runVoidResolutionTx) takes the sale, the claim, then the
			// session. Both hold the sale row first, so the two legs
			// serialize against each other and against every other act on the
			// sale before they ever reach the session or the claim, and the
			// close's FOR UPDATE waits for this leg's FOR SHARE: the
			// completion and the close serialize whichever leg finishes the
			// void.
			if rec.Act == "void" && sale.TillSessionID != nil {
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
			// the claim row, locked under the sale: a contender resolution
			// of the same claim loses here with the race named, never beside
			// this release (ninth review P2-2), and a live claim (its own
			// terminal call unanswered inside its lease) is refused, never
			// released (fourteenth review P1-1)
			held, err := s.repo.LockRefundIntent(ctx, intentID)
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
			if err := s.repo.FailRefundIntentByID(ctx, intentID); err != nil {
				return err
			}
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, resolveAuditOf(rec, in, actor)); err != nil {
					return fmt.Errorf("failed to write the resolution audit row: %w", err)
				}
			}
			// The completion runs from this leg too (r12 P1-1): when the
			// release emptied the sale's standing claims and some void claim
			// already booked a tender's refund, the void completes here the
			// same way the refund leg completes it, under the same sale and
			// claim locks, so the order finance resolves the claims in never
			// decides the end state. A void whose every claim settled without
			// a booked refund keeps the sale completable by a fresh void, as
			// a terminal that declined every card does.
			if rec.Act == "void" && sale.Status == StatusCompleted {
				pending, err := s.repo.PendingRefundIntentIDs(ctx, rec.SaleID)
				if err != nil {
					return err
				}
				if len(pending) == 0 {
					done, err := s.repo.SaleHasVoidIntentDone(ctx, rec.SaleID)
					if err != nil {
						return err
					}
					if done {
						tenders, err := s.repo.GetTenders(ctx, rec.SaleID)
						if err != nil {
							return err
						}
						fxAll, err := s.completeVoidFromResolution(ctx, sale, tenders, in.Reason, actor, rec.ID)
						if err != nil {
							return err
						}
						if fxAll != nil {
							for _, ev := range fxAll.Events() {
								if err := s.recordEvent(ctx, ev); err != nil {
									return err
								}
							}
						}
					}
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
			// a refused replay writes no orphan row (tenth review P3-2): the
			// claim and the stranded act's own rows already record the money
			// that moved, and finance reconciling orphans must not see the
			// same refund once per attempt
			if _, err := s.runVoidResolutionTx(ctx, rec, in, entry, actor); err != nil {
				return nil, err
			}
			return out, nil
		case "payback":
			// a stranded payback claim (its terminal call answered no
			// verdict) books through the payback's own booking: the credit
			// leaves the tender's payment under the terminal's own id
			if _, err := s.runPaybackTx(ctx, rec.RefundIntent, in.RefundTxID, in.Reason, actor, &entry); err != nil {
				return nil, err
			}
			return out, nil
		case "return":
			var stored storedReturnRequest
			if err := json.Unmarshal(rec.Request, &stored); err != nil {
				return nil, fmt.Errorf("the claim's stored request does not read back: %w", err)
			}
			st, err := s.priceReturn(ctx, stored.toReturnIn(), false)
			if err != nil {
				// the replay prices outside the transaction, and a contender
				// may settle the claim and book while it does: the race is
				// the answer then, never the caps the winner's own booking
				// trips (the money transaction rechecks under the sale lock)
				if fresh, rerr := s.repo.GetRefundIntent(ctx, intentID); rerr == nil && fresh.State != "PENDING" {
					return nil, conflict("intent_not_pending",
						"the refund claim was settled by another resolution first")
				}
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
			// replay completes with is the operator's own record; the replay
			// writes no orphan row on refusal (tenth review P3-2)
			st.in.GatewayTxID = in.RefundTxID
			claim := &refundClaim{intent: rec.RefundIntent, tenderCap: tenderCap,
				refundTxID: in.RefundTxID, resolveAudit: &entry, replay: true}
			if _, err := s.postReturn(ctx, st, claim); err != nil {
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

// refuseUnansweredClaim is the in flight lease's read side (fourteenth review P1-1): a
// claim whose own terminal call has not answered and whose lease has not run
// out is live, and nothing may settle it.
func (s *Service) refuseUnansweredClaim(rec *RefundIntentRecord) error {
	if rec.AnsweredAt != nil || !s.now().Before(rec.AnswerDueAt) {
		return nil
	}
	return conflict("refund_in_flight",
		"the claim's own terminal call has not answered: resolve it once the terminal answers or its in flight lease expires")
}

// settleUnansweredClaim is the lease's write side, run under the claim's row
// lock inside a settling transaction (fourteenth review P1-1): an answered claim settles
// as it is; a claim whose lease expired with no answer at all is marked
// UNKNOWN first, so its own record says the terminal never spoke; a claim
// still inside its lease settles nothing.
func (s *Service) settleUnansweredClaim(ctx context.Context, held *RefundIntentRecord) error {
	if held.AnsweredAt != nil {
		return nil
	}
	if err := s.refuseUnansweredClaim(held); err != nil {
		return err
	}
	return s.repo.MarkRefundIntentUnansweredUnknown(ctx, held.ID)
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
// stands completed between resolutions, and the no refund leg completes it
// the same way when it empties the claims (r12 P1-1), so the order the claims
// are resolved in never decides the end state. A tender whose refund was
// declined, or recorded as a no refund, is never paid out: its application is
// reversed and the money stays unapplied on the customer's account, the
// credit the dealer owes, and no method may return the sale's goods again
// once a tender's refund is booked (the return routes read the same table).
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
		// The claim row, locked and read right after the sale lock (r12
		// P3-1): the loser of a same outcome race is told the race here (the
		// claim the winner settled), never a state refusal the winner's own
		// completion of the void would answer below. A claim still inside its
		// in flight lease with no answer settles nothing (fourteenth review P1-1), and the
		// refund booked must be the terminal's own id when the claim carries
		// one.
		held, err := s.repo.LockRefundIntent(ctx, rec.ID)
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
		if held.RefundTxID != "" && held.RefundTxID != in.RefundTxID {
			return conflict("refund_tx_id_conflict",
				fmt.Sprintf("the claim carries the terminal's refund id %s, not %s: record the terminal's own id",
					held.RefundTxID, in.RefundTxID))
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
			cfx, err := s.completeVoidFromResolution(ctx, sale, tenders, in.Reason, actor, rec.ID)
			if err != nil {
				return err
			}
			mergeEffects(&fxAll, cfx)
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

// completeVoidFromResolution finishes a stranded void's sale once every claim
// of the sale is settled (r12 P1-1): the cash and check payments voided,
// every live card application reversed to unapplied credit, the invoice
// voided, the stock back, the sale VOIDED, its audit row and its event. Both
// legs of the resolution call it under the same sale and claim locks (the
// refund leg after its own tender's booking, the no refund leg after its
// release emptied the claims beside a booked refund), so whatever order the
// claims are resolved in, the end state is this one completion.
func (s *Service) completeVoidFromResolution(ctx context.Context, sale *Sale, tenders []Tender, reason, actor string, claimID uuid.UUID) (*account.Effects, error) {
	var fxAll *account.Effects
	date, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
	if err != nil {
		return nil, err
	}
	for _, t := range tendersInIDOrder(tenders) {
		if t.PaymentID == nil {
			continue
		}
		switch t.Method {
		case TenderCash, TenderCheck:
			fx, err := s.ar.VoidPayment(ctx, account.VoidPaymentIn{
				PaymentID: *t.PaymentID, Reason: reason, Actor: actor, On: date})
			if err != nil {
				return nil, err
			}
			mergeEffects(&fxAll, fx)
		case TenderCard:
			applicationID, live, err := cardApplicationLive(ctx, s, *t.PaymentID)
			if err != nil {
				return nil, err
			}
			if !live {
				continue
			}
			fx, err := s.ar.Reverse(ctx, account.ReverseIn{
				ApplicationID: applicationID, Reason: reason, Actor: actor, On: date})
			if err != nil {
				return nil, err
			}
			mergeEffects(&fxAll, fx)
		}
	}
	var entryID *uuid.UUID
	if err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT gl_entry_id FROM invoices WHERE id = $1`, sale.InvoiceID).Scan(&entryID); err != nil {
		return nil, err
	}
	fx, err := s.ar.VoidInvoice(ctx, account.VoidInvoiceIn{
		InvoiceID: *sale.InvoiceID, EntryID: entryID, Reason: reason, Actor: actor, On: date})
	if err != nil {
		return nil, err
	}
	mergeEffects(&fxAll, fx)
	lines, err := s.repo.GetLines(ctx, sale.ID)
	if err != nil {
		return nil, err
	}
	for i := range lines {
		l := &lines[i]
		if !l.IsStocked() || l.Quantity == nil {
			continue
		}
		if err := s.inventory.RestockQty(ctx, *l.ProductID, sale.BranchID, *l.Quantity); err != nil {
			return nil, err
		}
	}
	if err := s.repo.VoidSale(ctx, sale.ID); err != nil {
		return nil, err
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "pos.transaction.voided", EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
			Changes: map[string]any{
				"number": sale.Number, "total_cents": int64(sale.TotalCents), "reason": reason,
				"register_id": sale.RegisterID, "customer_id": sale.CustomerID,
				"resolved_by_claim": claimID,
			}}); err != nil {
			return nil, fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	branch := sale.BranchID
	if err := s.recordEvent(ctx, outbox.Event{Type: EventSaleVoided, EntityType: "pos_transaction", EntityID: sale.ID,
		BranchID: &branch, Data: eventJSON(map[string]any{
			"number": sale.Number, "customer_id": sale.CustomerID, "status": StatusVoided.Status(),
			"from_status": StatusCompleted.Status(), "revision": sale.Revision + 1, "currency": sale.Currency,
			"total_cents": int64(sale.TotalCents), "reason": reason,
		})}); err != nil {
		return nil, err
	}
	return fxAll, nil
}
