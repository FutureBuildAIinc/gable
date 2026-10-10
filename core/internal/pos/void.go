// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// VoidSale voids a completed counter sale while its till session is open,
// in ONE transaction in section 11's order (the sale, its payments, its
// invoice): each cash or check payment voided; each card payment, whose
// gateway refund is made before the transaction (as every gateway call is),
// has its application reversed and the amount recorded as a refund from its
// unapplied cash; then the invoice void, and the stock returns. After the
// session closes it is a return (ADR 0005 section 14.2 C2-5).
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
	tenders, err := s.repo.GetTenders(ctx, saleID)
	if err != nil {
		return nil, err
	}
	// Card refunds go through the gateway before the transaction, so a
	// decline aborts with nothing persisted (as every gateway call is).
	for i := range tenders {
		t := &tenders[i]
		if t.Method != TenderCard || t.PaymentID == nil {
			continue
		}
		var gatewayTxID string
		if t.GatewayTxID != nil {
			gatewayTxID = *t.GatewayTxID
		}
		if gatewayTxID == "" {
			continue
		}
		if s.gateway == nil {
			return nil, conflict("card_terminal", "this register has no card terminal gateway to refund the card tender")
		}
		res, err := s.gateway.Refund(ctx, gatewayTxID, int64(t.AmountCents))
		if err != nil {
			return nil, fmt.Errorf("card refund failed: %w", err)
		}
		if res.Status != payment.GatewayStatusRefunded && res.Status != payment.GatewayStatusApproved {
			return nil, conflict("card_refund", fmt.Sprintf("the card refund was not accepted (%s)", res.Status))
		}
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
		// void and won leaves the sale with returns, and this void refuses.
		if has, err := s.repo.SaleHasReturns(ctx, saleID); err != nil {
			return err
		} else if has {
			return conflict("has_returns",
				"this sale has been returned against: return the rest through POST /pos/returns instead of voiding it")
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
				fx, err := s.ar.Reverse(ctx, account.ReverseIn{
					ApplicationID: cardApplicationOf(ctx, s, *t.PaymentID), Reason: reason, Actor: actor, On: date})
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

const refundApproved = "REFUNDED"

// tendersInIDOrder sorts the tenders by their payment ids (section 11,
// step 2: payments in id order).
func tendersInIDOrder(tenders []Tender) []Tender {
	out := append([]Tender(nil), tenders...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j], out[j-1]
			if a.PaymentID != nil && b.PaymentID != nil && a.PaymentID.String() < b.PaymentID.String() {
				out[j], out[j-1] = out[j-1], out[j]
			} else {
				break
			}
		}
	}
	return out
}

// cardApplicationOf finds the payment's live application on the invoice.
func cardApplicationOf(ctx context.Context, s *Service, paymentID uuid.UUID) uuid.UUID {
	var id uuid.UUID
	err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM ar_applications WHERE payment_id = $1 AND reversed_at IS NULL ORDER BY created_at, id LIMIT 1`,
		paymentID).Scan(&id)
	if err != nil {
		return uuid.Nil
	}
	return id
}
