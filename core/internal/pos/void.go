// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"
	"sort"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
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
		intents = append(intents, RefundIntent{SaleID: saleID, TenderID: t.ID, GatewayTxID: *t.GatewayTxID,
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
		res, err := s.gateway.Refund(ctx, intents[i].GatewayTxID, intents[i].AmountCents)
		refundErr := err
		if refundErr == nil && res.Status != payment.GatewayStatusRefunded && res.Status != payment.GatewayStatusApproved {
			refundErr = conflict("card_refund", fmt.Sprintf("the card refund was not accepted (%s)", res.Status))
		}
		if refundErr != nil {
			// this tender's refund failed: its claim is released (no money
			// moved for it), and the refunds already taken are recorded in
			// committed rows with their claims left standing, because the
			// money transaction never opens now
			s.failRefundIntent(ctx, intents[i])
			for _, r := range refunds {
				s.recordOrphanRefund(ctx, r, refundErr)
			}
			if err != nil {
				return nil, fmt.Errorf("card refund failed: %w", err)
			}
			return nil, refundErr
		}
		refunds = append(refunds, gatewayRefund{gatewayTxID: intents[i].GatewayTxID, refundTxID: res.TransactionID,
			amountCents: intents[i].AmountCents, tenderID: intents[i].TenderID, act: "void", entity: saleID, actor: actor})
	}
	var out *Sale
	err = s.inTx(ctx, func(ctx context.Context) error {
		// Step 1: the sale row.
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(sale.Revision, ifMatch, bodyRevision); err != nil {
			return err
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
		for i := range lines {
			l := &lines[i]
			if !l.IsStocked() || l.Quantity == nil {
				continue
			}
			if err := s.inventory.RestockQty(ctx, *l.ProductID, sale.BranchID, *l.Quantity); err != nil {
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

const refundApproved = "REFUNDED"

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
