// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// VoidSale voids a completed counter sale while its till session is open,
// in ONE transaction in section 11's order (the sale, its payments, its
// invoice): each cash or check payment voided; each card payment, whose
// gateway refund is made BEFORE the transaction as every gateway call is
// (ADR 0005 section 11: "The card gateway is called before the transaction
// and its result written inside it"; section 14.2 C2-5), claimed first
// through a committed refund intent unique per sale and tender, so a second
// void of the sale, or a card return of the same tender, is refused before
// it reaches the terminal and no lock of the money transaction is held
// across the network call; the transaction then reverses the payment's
// application, records the amount as a refund from its unapplied cash and
// marks the intent done; then the invoice void, and the stock returns.
// After the session closes it is a return (ADR 0005 section 14.2 C2-5).
func (s *Service) VoidSale(ctx context.Context, saleID uuid.UUID, ifMatch string, bodyRevision *int64, reason, actor string) (*Sale, error) {
	if s.ar == nil || s.inventory == nil {
		return nil, errNotConfigured
	}
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	pre, err := s.repo.GetSale(ctx, saleID)
	if err != nil {
		return nil, err
	}
	if pre.Status != StatusCompleted {
		return nil, httpx.InvalidStateTransition(fmt.Sprintf("cannot void a %s sale: only a completed sale is voided", pre.Status.Status()))
	}
	// Every guard that needs no lock runs BEFORE the gateway is asked for
	// money (first review P1-2): a refusal must never move money at the
	// terminal. The guards are rechecked under the sale row lock inside the
	// transaction; the residual race is closed by recording what the gateway
	// did when the transaction then refuses, and by the intent the refund
	// leaves standing.
	// After any return of the sale the way back is another return: the
	// return already paid part of the money out and put its goods back, so a
	// void would pay the whole sale a second time.
	if has, err := s.repo.SaleHasReturns(ctx, saleID); err != nil {
		return nil, err
	} else if has {
		return nil, conflict("has_returns",
			"this sale has been returned against: return the rest through POST /pos/returns instead of voiding it")
	}
	// A void lives inside its till session; once the drawer closes, the
	// money has been counted and the way back is a return.
	if pre.TillSessionID != nil {
		session, err := s.repo.GetTillSession(ctx, *pre.TillSessionID)
		if err != nil {
			return nil, err
		}
		if session.Status != TillOpen {
			return nil, conflict("session_closed",
				"the till session this sale was made in is closed: return the goods through POST /pos/returns instead")
		}
	}
	if pre.InvoiceID == nil {
		return nil, conflict("no_invoice", "the sale carries no invoice to void")
	}
	if err := httpx.CheckRevision(pre.Revision, ifMatch, bodyRevision); err != nil {
		return nil, err
	}
	// The card refunds, claimed and taken BEFORE the transaction (ADR 0005
	// section 11 and 14.2 C2-5: the gateway is called before the
	// transaction, as every gateway call is). The claim is a committed
	// refund intent per card tender, opened in its own short transaction:
	// its unique (sale, tender) index is what refuses a second void, or a
	// card return of the same tender, before the gateway, because the sale
	// row lock cannot span the call. The intent's foreign keys take only FOR
	// KEY SHARE, so the claim never parks behind a money transaction.
	tenders, err := s.repo.GetTenders(ctx, saleID)
	if err != nil {
		return nil, err
	}
	var intents []RefundIntent
	for i := range tenders {
		t := &tenders[i]
		// only a card tender that became a payment is refunded (the AR legs
		// follow its payment), and only one behind a gateway transaction
		if t.Method != TenderCard || t.PaymentID == nil || t.GatewayTxID == nil || *t.GatewayTxID == "" {
			continue
		}
		intents = append(intents, RefundIntent{ID: uuid.New(), SaleID: saleID, TenderID: t.ID, GatewayTxID: *t.GatewayTxID,
			AmountCents: int64(t.AmountCents), Act: "void"})
	}
	if len(intents) > 0 && s.gateway == nil {
		return nil, conflict("card_terminal", "this register has no card terminal gateway to refund the card tender")
	}
	// The claims, committed in one short transaction of their own before
	// any terminal is reached. The sale row is locked and every guard
	// rechecked under it first: a contender whose earlier check read stale
	// must not claim and refund a sale the lock now sees voided (or
	// returned against, or closed); and in the window between one claim's
	// commit and its money transaction, the standing intent's unique index
	// is what refuses the next claim. No lock is held across the gateway
	// call: this transaction commits before the terminal is reached.
	claimErr := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		held, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(held.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if held.Status != StatusCompleted {
			return httpx.InvalidStateTransition("only a completed sale is voided")
		}
		if has, err := s.repo.SaleHasReturns(ctx, saleID); err != nil {
			return err
		} else if has {
			return conflict("has_returns",
				"this sale has been returned against: return the rest through POST /pos/returns instead of voiding it")
		}
		if held.TillSessionID != nil {
			if err := s.repo.LockTillSession(ctx, *held.TillSessionID, false); err != nil {
				return err
			}
			session, err := s.repo.GetTillSession(ctx, *held.TillSessionID)
			if err != nil {
				return err
			}
			if session.Status != TillOpen {
				return conflict("session_closed",
					"the till session this sale was made in is closed: return the goods through POST /pos/returns instead")
			}
		}
		for i := range intents {
			if err := s.checkCardClaim(ctx, saleID, intents[i].TenderID, intents[i].AmountCents, intents[i].AmountCents); err != nil {
				return err
			}
			if err := s.repo.InsertRefundIntent(ctx, intents[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if claimErr != nil {
		return nil, claimErr
	}
	// refunds collects each gateway refund taken outside the transaction: a
	// refusing transaction (a failed step, or a guard that lost a race)
	// records every refund already made in committed rows of its own, and
	// the claims it leaves standing keep the tender from being refunded
	// again until that is reconciled (fourth review P1-4).
	var refunds []gatewayRefund
	for i := range intents {
		// The lease is stamped per call (the sixteenth review's P1-2): a
		// committed write immediately before this claim's own terminal call
		// renews answer_due_at to now plus the lease, so a later tender's
		// call in this sequence never runs under a lease the earlier calls
		// already spent, and the resolve route cannot release a claim whose
		// call is still on the wire. A claim whose stamp fails is never
		// called: it is abandoned exactly as the tenders after a refusal
		// are, and the refunds already taken are recorded beside it.
		if err := s.repo.RestampRefundIntentLease(ctx, intents[i].ID); err != nil {
			for j := i; j < len(intents); j++ {
				s.abandonRefundIntent(ctx, intents[j])
			}
			for _, r := range refunds {
				s.recordOrphanRefund(ctx, r, err)
			}
			return nil, fmt.Errorf("failed to stamp the claim's in flight lease before its terminal call: %w", err)
		}
		// The call is bounded (fourteenth review P1-1): every path it can take ends inside
		// the bound, so the claim it holds never sits in flight past its own
		// settle path.
		callCtx, cancel := context.WithTimeout(ctx, refundCallTimeout)
		res, err := s.gateway.Refund(callCtx, intents[i].GatewayTxID, intents[i].AmountCents)
		cancel()
		refundErr := err
		verdict := refundAccepted
		if err == nil {
			verdict = refundVerdictOf(res)
			if verdict != refundAccepted {
				refundErr = conflict("card_refund", fmt.Sprintf("the card refund was not accepted (%s)", res.Status))
			}
		}
		if refundErr != nil {
			// this tender's refund did not go through: an error, or a status
			// that is neither a success nor the terminal's own no, carries no
			// verdict (the terminal may have moved the money: the claim
			// stands and the unknown outcome is recorded, seventh review P2-2
			// and tenth review P2-3), while a definite refusal status proves
			// nothing moved (the claim is released). The refunds already
			// taken are recorded in committed rows with their claims left
			// standing, because the money transaction never opens now. The
			// tenders after this one are never asked (fourteenth review P1-1): their
			// claims are abandoned, stranded from now rather than held on a
			// lease for calls that will never be made.
			if err != nil {
				s.unknownRefundOutcome(ctx, intents[i], err)
			} else if verdict == refundDeclined {
				s.failRefundIntent(ctx, intents[i])
			} else {
				s.unknownRefundOutcome(ctx, intents[i], noVerdictOf(res))
			}
			for j := i + 1; j < len(intents); j++ {
				s.abandonRefundIntent(ctx, intents[j])
			}
			for _, r := range refunds {
				s.recordOrphanRefund(ctx, r, refundErr)
			}
			if err != nil {
				return nil, fmt.Errorf("card refund failed: %w", err)
			}
			return nil, refundErr
		}
		// the gateway's own refund id, on the standing claim the moment the
		// terminal answers and in its own committed write (seventh review
		// P2-3), whatever state the claim is in (fourteenth review P1-1): a claim carrying
		// a refund id is a refund that moved
		if err := s.repo.MarkRefundIntentRefunded(ctx, intents[i].ID, res.TransactionID); err != nil {
			s.logger.Error("CRITICAL: the gateway's refund id was not written to the claim",
				"intent_id", intents[i].ID, "sale_id", saleID, "error", err)
		}
		refunds = append(refunds, gatewayRefund{gatewayTxID: intents[i].GatewayTxID, refundTxID: res.TransactionID,
			amountCents: intents[i].AmountCents, tenderID: intents[i].TenderID, act: "void", entity: saleID, actor: actor})
	}
	out, err := s.runVoidTx(ctx, saleID, ifMatch, bodyRevision, reason, actor, refunds, nil)
	if err != nil {
		// The gateway already refunded and the transaction refused: the
		// money is accounted for in committed rows of its own, one per
		// refund, carrying the gateway ids.
		for _, r := range refunds {
			s.recordOrphanRefund(ctx, r, err)
		}
		return nil, err
	}
	return out, nil
}

// runVoidTx runs the void's money transaction: the sale row lock, every guard
// restated under it, the payments reversed, the invoice voided, the stock
// back, the events last, all one transaction. The claims complete with the
// posting; a refusing transaction rolls them back to PENDING with everything
// else and the caller records the refunds that moved. operatorReplay, when
// set, lifts the client revision precondition (the operator's resolution is
// its own authority) and writes resolveAudit inside the transaction.
func (s *Service) runVoidTx(ctx context.Context, saleID uuid.UUID, ifMatch string, bodyRevision *int64,
	reason, actor string, refunds []gatewayRefund, resolveAudit *audit.Entry) (*Sale, error) {
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		// Step 1: the sale row.
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if resolveAudit == nil {
			if err := httpx.CheckRevision(sale.Revision, ifMatch, bodyRevision); err != nil {
				return err
			}
		}
		if sale.Status != StatusCompleted {
			return httpx.InvalidStateTransition("only a completed sale is voided")
		}
		// The returns check, again under the lock: a return that raced this
		// void and won leaves the sale with returns, and this void refuses
		// (the refund it already took at the gateway is recorded above, and
		// its intent stays standing).
		if has, err := s.repo.SaleHasReturns(ctx, saleID); err != nil {
			return err
		} else if has {
			return conflict("has_returns",
				"this sale has been returned against: return the rest through POST /pos/returns instead of voiding it")
		}
		// The session, rechecked under its FOR SHARE lock against the
		// close's FOR UPDATE: once the drawer is counted the way back is a
		// return, never a void into a closed session.
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
		lines, err := s.repo.GetLines(ctx, saleID)
		if err != nil {
			return err
		}
		tenders, err := s.repo.GetTenders(ctx, saleID)
		if err != nil {
			return err
		}
		// The claims complete with the posting: the transaction owns them
		// now, and a refusal rolls them back to PENDING with everything
		// else, recording the refunds that moved.
		for i := range refunds {
			if err := s.repo.CompleteRefundIntent(ctx, saleID, refunds[i].tenderID, refunds[i].refundTxID); err != nil {
				return err
			}
		}
		if resolveAudit != nil && s.auditLog != nil {
			if err := s.auditLog.Log(ctx, *resolveAudit); err != nil {
				return fmt.Errorf("failed to write the resolution audit row: %w", err)
			}
		}
		date, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
		if err != nil {
			return err
		}
		var fxAll *account.Effects
		// The payments, in id order (section 11, step 2 comes before the
		// invoice at step 4): cash and check void whole; card reverses its
		// application and refunds the amount from the unapplied cash the
		// reversal creates.
		ordered := tendersInIDOrder(tenders)
		for _, t := range ordered {
			if t.PaymentID == nil {
				continue
			}
			switch t.Method {
			case TenderCash, TenderCheck:
				fx, err := s.ar.VoidPayment(ctx, account.VoidPaymentIn{
					PaymentID: *t.PaymentID, Reason: reason, Actor: actor, On: date})
				if err != nil {
					return err
				}
				mergeEffects(&fxAll, fx)
			case TenderCard:
				applicationID, err := cardApplicationOf(ctx, s, *t.PaymentID)
				if err != nil {
					return err
				}
				fx, err := s.ar.Reverse(ctx, account.ReverseIn{
					ApplicationID: applicationID, Reason: reason, Actor: actor, On: date})
				if err != nil {
					return err
				}
				mergeEffects(&fxAll, fx)
				_, rfx, err := s.ar.RefundPayment(ctx, account.RefundPaymentIn{
					PaymentID: *t.PaymentID, AmountCents: int64(t.AmountCents), Reason: reason, Actor: actor, On: date})
				if err != nil {
					return err
				}
				mergeEffects(&fxAll, rfx)
			}
		}
		// The invoice void (step 4): reverses its whole entry, writes the
		// opposite subledger row, ends VOID.
		var entryID *uuid.UUID
		if err := s.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT gl_entry_id FROM invoices WHERE id = $1`, sale.InvoiceID).Scan(&entryID); err != nil {
			return err
		}
		fx, err := s.ar.VoidInvoice(ctx, account.VoidInvoiceIn{
			InvoiceID: *sale.InvoiceID, EntryID: entryID, Reason: reason, Actor: actor, On: date})
		if err != nil {
			return err
		}
		mergeEffects(&fxAll, fx)
		// The stock returns (step 6): each stocked line back on hand.
		if err := s.checkLineStockUnits(ctx, lines); err != nil {
			return err
		}
		for i := range lines {
			l := &lines[i]
			if !l.IsStocked() || l.Quantity == nil {
				continue
			}
			if err := s.inventory.RestockQty(ctx, *l.ProductID, sale.BranchID, salesdoc.StockQty(l)); err != nil {
				return err
			}
		}
		if err := s.repo.VoidSale(ctx, saleID); err != nil {
			return err
		}
		// The audit row, inside the transaction (R1-14): a rolled back void
		// leaves no row, and only a void that changed state reaches here.
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.transaction.voided", EntityType: "pos_transaction", EntityID: saleID, UserID: actor,
				Changes: map[string]any{
					"number": sale.Number, "total_cents": int64(sale.TotalCents), "reason": reason,
					"register_id": sale.RegisterID, "customer_id": sale.CustomerID,
				}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		// The events, last: the AR core's, then the sale's own.
		branch := sale.BranchID
		if fxAll != nil {
			for _, ev := range fxAll.Events() {
				if err := s.recordEvent(ctx, ev); err != nil {
					return err
				}
			}
		}
		if err := s.recordEvent(ctx, outbox.Event{Type: EventSaleVoided, EntityType: "pos_transaction", EntityID: saleID,
			BranchID: &branch, Data: eventJSON(map[string]any{
				"number": sale.Number, "customer_id": sale.CustomerID, "status": StatusVoided.Status(),
				"from_status": StatusCompleted.Status(), "revision": sale.Revision + 1, "currency": sale.Currency,
				"total_cents": int64(sale.TotalCents), "reason": reason,
			})}); err != nil {
			return err
		}
		out, err = s.GetSale(ctx, saleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// tendersInIDOrder answers the tenders that became payments, sorted by
// their payment ids (section 11, step 2: payments in id order). A tender
// with no payment (an ACCOUNT tender) locks nothing and is dropped here;
// the caller skips it anyway.
func tendersInIDOrder(tenders []Tender) []Tender {
	out := make([]Tender, 0, len(tenders))
	for i := range tenders {
		if tenders[i].PaymentID != nil {
			out = append(out, tenders[i])
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].PaymentID.String() < out[b].PaymentID.String() })
	return out
}

// cardApplicationOf finds the payment's live application on the invoice. A
// read failure is returned, never swallowed into a nil id that would
// surface as a confusing Reverse refusal.
func cardApplicationOf(ctx context.Context, s *Service, paymentID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM ar_applications WHERE payment_id = $1 AND reversed_at IS NULL ORDER BY created_at, id LIMIT 1`,
		paymentID).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to read the card payment's application: %w", err)
	}
	return id, nil
}

// cardApplicationLive answers the payment's live application when one stands:
// the per tender void resolution skips a tender whose leg an earlier
// resolution already reversed.
func cardApplicationLive(ctx context.Context, s *Service, paymentID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM ar_applications WHERE payment_id = $1 AND reversed_at IS NULL ORDER BY created_at, id LIMIT 1`,
		paymentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to read the card payment's application: %w", err)
	}
	return id, true, nil
}
