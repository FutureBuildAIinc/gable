// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The manager's resolve acts for a lost terminal answer (CP-05): the
// terminal's own record is the authority and the manager reads it. Every act
// claims the lost item FIRST with one guarded statement (open to its close,
// inside the act's transaction, or open to resolving in a committed write of
// its own when the act calls the gateway), so a second or racing resolve gets
// a conflict and calls nothing. The money follows today's paths:
//
//   - a sale leg authorized: on a sale still open the authorized leg stays
//     on it for the completion to use; on a completed sale whose invoice has
//     an open amount the money applies up to that amount and the rest goes
//     back to the card; on a completed sale paid in full, or a voided sale,
//     the whole charge goes back (void first, the refund when the void is
//     refused, under the intent's idempotency key, as C2-5 does);
//   - a refund or void leg authorized: the refund claim the leg names settles
//     through the refund claim's own resolution (the void resolution, the
//     return posting, the payback), so the till close and a second resolve
//     see it settled; a C2-5 compensation void books nothing;
//   - a leg declined: the item closes and nothing moves.

import (
	"context"
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// ResolveTerminalLostItemIn is a manager's resolution of one lost answer.
type ResolveTerminalLostItemIn struct {
	Outcome   string // "authorized" or "declined"
	Reference string // the terminal's own record the manager read
	Reason    string
}

// reversalLease is how long a resolution that called the gateway holds its
// item in the resolving state before a second resolution may take over (a
// crashed resolution); the gateway calls are idempotent under the intent's
// key, so the takeover repeats them safely.
const reversalLease = 2 * reversalTimeout

// ResolveTerminalLostItem settles one lost answer the way the terminal's own
// record says (a manager only). Both outcomes write one audit row naming the
// person, the item, the outcome and the reference.
func (s *Service) ResolveTerminalLostItem(ctx context.Context, itemID uuid.UUID, in ResolveTerminalLostItemIn, actor string) (*TerminalLostItemView, error) {
	if !s.terminalWired() || s.ar == nil {
		return nil, errTerminalNotConfigured
	}
	if in.Outcome != "authorized" && in.Outcome != "declined" {
		return nil, invalid("outcome", "must be authorized or declined")
	}
	item, err := s.terminalLedger.LostItem(ctx, itemID)
	if err != nil {
		return nil, httpx.NotFound("lost item not found")
	}
	// the branch wall: the item's branch is held to the caller's reach before
	// either outcome runs
	if err := s.holdIntentReach(ctx, "lost item", item.BranchID, itemID); err != nil {
		return nil, err
	}
	intent, err := s.terminalLedger.Get(ctx, item.IntentID)
	if err != nil {
		return nil, err
	}
	entry := audit.Entry{Action: "pos.terminal_lost_item.resolved", EntityType: "pos_transaction",
		EntityID: item.TransactionID, UserID: actor,
		Changes: map[string]any{"item_id": item.ID, "intent_id": item.IntentID, "kind": item.Kind,
			"amount_cents": item.AmountCents, "outcome": in.Outcome, "reference": in.Reference, "reason": in.Reason}}
	if in.Outcome == "declined" {
		return s.resolveDeclined(ctx, item, entry)
	}
	ref := firstNonEmpty(in.Reference, item.LateProviderRef, intent.AnswerProviderRef, intent.ProviderRef, item.ProviderRef)
	switch intent.Kind {
	case payment.TerminalKindSale:
		return s.resolveSaleAuthorized(ctx, item, intent, in, ref, entry)
	case payment.TerminalKindRefund, payment.TerminalKindVoid:
		return s.resolveRefundAuthorized(ctx, item, intent, in, ref, entry)
	}
	return nil, conflict("terminal_kind", "the intent names no kind the counter can resolve")
}

// settleItemView is the view of an item the act just closed.
func settleItemView(item *payment.TerminalLostItem, state string) *TerminalLostItemView {
	v := viewOfItem(item)
	v.State = state
	return &v
}

// resolveDeclined closes the item when the terminal's own record said no:
//   - if the intent is already authorized and booked (spent_at IS NOT NULL),
//     refuses with 409 terminal_leg_already_booked naming the booked tender or
//     refund claim so the manager voids or returns through the sale;
//   - if the intent is authorized and unspent (spent_at IS NULL), moves the
//     intent to declined by a guarded statement and closes the item so the
//     open sale can charge again;
//   - if the intent is lost or already declined, closes the item and moves
//     nothing.
func (s *Service) resolveDeclined(ctx context.Context, item *payment.TerminalLostItem, entry audit.Entry) (*TerminalLostItemView, error) {
	if err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, item.TransactionID); err != nil {
			return err
		}
		intent, err := s.terminalLedger.Get(ctx, item.IntentID)
		if err != nil {
			return err
		}
		if intent.State == payment.TerminalAuthorized {
			if intent.SpentAt != nil {
				return s.refuseDeclinedBookedLeg(ctx, item, intent)
			}
			moved, err := s.terminalLedger.DeclineUnspentAuthorized(ctx, intent.ID, entryRef(entry))
			if err != nil {
				return err
			}
			if !moved {
				return conflict("terminal_intent_moved", "the card leg moved while the resolution ran: read the item again")
			}
		}
		ok, err := s.terminalLedger.ResolveLostItemDeclined(ctx, item.ID, entry.UserID, entryRef(entry))
		if err != nil {
			return err
		}
		if !ok {
			return conflict("lost_item_settled", "the lost item was already resolved or is being resolved")
		}
		return s.writeResolveAudit(ctx, entry)
	}); err != nil {
		return nil, err
	}
	return settleItemView(item, "resolved_declined"), nil
}

// refuseDeclinedBookedLeg refuses a declined resolution when the intent's
// money was already booked on the sale or refund claim, naming the booked
// tender or refund claim so the manager voids or returns through the sale.
func (s *Service) refuseDeclinedBookedLeg(ctx context.Context, item *payment.TerminalLostItem, intent *payment.TerminalIntent) error {
	if intent.Kind == payment.TerminalKindRefund || intent.Kind == payment.TerminalKindVoid {
		return conflict("terminal_leg_already_booked",
			fmt.Sprintf("the %s leg %s is already booked on sale %s (refund claim %s): reconcile it through the sale instead of closing the item declined",
				intent.Kind, intent.ID, item.TransactionID, intent.LegKey))
	}
	tenders, err := s.repo.GetTenders(ctx, item.TransactionID)
	if err != nil {
		return err
	}
	ref := firstNonEmpty(intent.AnswerProviderRef, intent.ProviderRef)
	var tenderName string
	for _, t := range tenders {
		if t.Method != TenderCard {
			continue
		}
		if tenderName == "" {
			tenderName = t.ID.String()
		}
		if t.GatewayTxID != nil && ref != "" && *t.GatewayTxID == ref {
			tenderName = t.ID.String()
			break
		}
	}
	if tenderName == "" {
		tenderName = intent.LegKey
	}
	return conflict("terminal_leg_already_booked",
		fmt.Sprintf("the card leg %s is already booked on sale %s (tender %s): void or return the sale instead of closing the item declined",
			intent.LegKey, item.TransactionID, tenderName))
}

// entryRef reads the reference an audit entry carries.
func entryRef(entry audit.Entry) string {
	ref, _ := entry.Changes["reference"].(string)
	return ref
}

// salePlan is what an authorized sale leg's resolution does with the money.
type salePlan struct {
	record  bool  // the sale is open: the leg stays on it for the completion
	applyC  int64 // cents applied to the sale's invoice
	reverse int64 // cents that go back to the card
	invoice *uuid.UUID
}

// planSaleLeg reads the sale under its lock and settles the plan.
func (s *Service) planSaleLeg(ctx context.Context, sale *Sale, amountCents int64) (salePlan, error) {
	switch sale.Status {
	case StatusOpen, StatusHeld:
		return salePlan{record: true}, nil
	case StatusCompleted:
		if sale.InvoiceID != nil {
			var open int64
			if err := s.db.GetExecutor(ctx).QueryRow(ctx,
				`SELECT ROUND(amount_open * 100)::bigint FROM invoices WHERE id = $1`, *sale.InvoiceID).Scan(&open); err != nil {
				return salePlan{}, err
			}
			if open < 0 {
				open = 0
			}
			apply := amountCents
			if apply > open {
				apply = open
			}
			return salePlan{applyC: apply, reverse: amountCents - apply, invoice: sale.InvoiceID}, nil
		}
	}
	return salePlan{reverse: amountCents}, nil
}

// resolveSaleAuthorized settles an authorized sale leg:
//   - if the intent is already authorized and spent (spent_at IS NOT NULL),
//     the books already agree with the resolution, so the item closes and
//     nothing moves (no reversal, no second booking);
//   - if the intent is lost, declined, or authorized and unspent, moves the
//     intent to authorized (unspent) by a guarded statement in the first
//     transaction before BeginReversal commits, and marks spent_at only after
//     the reversal or booking succeeds.
func (s *Service) resolveSaleAuthorized(ctx context.Context, item *payment.TerminalLostItem, intent *payment.TerminalIntent,
	in ResolveTerminalLostItemIn, ref string, entry audit.Entry) (*TerminalLostItemView, error) {
	var plan salePlan
	// First transaction: the sale row, then the intent's actual state check,
	// then the item's claim (one guarded statement), then the intent's move to
	// authorized (unspent) before BeginReversal commits. A leg already spent
	// or a leg that books or records needs no gateway call and finishes here;
	// a leg that reverses commits the claim and goes on to the gateway.
	var finished bool
	if err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, item.TransactionID); err != nil {
			return err
		}
		fresh, err := s.terminalLedger.Get(ctx, item.IntentID)
		if err != nil {
			return err
		}
		*intent = *fresh
		intent.AnswerBrand = firstNonEmpty(intent.AnswerBrand, item.LateBrand)
		intent.AnswerLast4 = firstNonEmpty(intent.AnswerLast4, item.LateLast4)
		intent.AnswerAuthCode = firstNonEmpty(intent.AnswerAuthCode, item.LateAuthCode)
		if intent.State == payment.TerminalAuthorized && intent.SpentAt != nil {
			ok, err := s.terminalLedger.ResolveLostItemAuthorized(ctx, item.ID, entry.UserID, in.Reference)
			if err != nil {
				return err
			}
			if !ok {
				return conflict("lost_item_settled", "the lost item was already resolved or is being resolved")
			}
			if err := s.writeResolveAudit(ctx, entry); err != nil {
				return err
			}
			finished = true
			return nil
		}
		if ref == "" {
			return conflict("no_provider_reference",
				"the intent carries no provider reference to book or reverse: name the terminal's reference")
		}
		sale, err := s.repo.GetSale(ctx, item.TransactionID)
		if err != nil {
			return err
		}
		plan, err = s.planSaleLeg(ctx, sale, intent.AmountCents)
		if err != nil {
			return err
		}
		if plan.reverse > 0 {
			if s.gateway == nil {
				return conflict("no_reversal_gateway",
					"this register wires no payment gateway to put the charge back: reverse it at the terminal and record it by hand")
			}
			ok, err := s.terminalLedger.BeginReversal(ctx, item.ID, entry.UserID, s.now().Add(reversalLease), s.now())
			if err != nil {
				return err
			}
			if !ok {
				return conflict("lost_item_settled", "the lost item was already resolved or is being resolved")
			}
			moved, err := s.terminalLedger.AuthorizeForResolution(ctx, intent.ID, ref)
			if err != nil {
				return err
			}
			if !moved {
				return conflict("terminal_intent_moved", "the intent is no longer unspent: read the item again")
			}
			return nil
		}
		ok, err := s.terminalLedger.ResolveLostItemAuthorized(ctx, item.ID, entry.UserID, in.Reference)
		if err != nil {
			return err
		}
		if !ok {
			return conflict("lost_item_settled", "the lost item was already resolved or is being resolved")
		}
		moved, err := s.terminalLedger.AuthorizeForResolution(ctx, intent.ID, ref)
		if err != nil {
			return err
		}
		if !moved {
			return conflict("terminal_intent_moved", "the intent is no longer unspent: read the item again")
		}
		if err := s.settleSaleLeg(ctx, sale, intent, item, plan, ref, in, entry); err != nil {
			return err
		}
		finished = true
		return nil
	}); err != nil {
		return nil, err
	}
	if finished {
		return settleItemView(item, "resolved_authorized"), nil
	}
	// The gateway reversal, outside any transaction, after the item's claim
	// committed: a second resolve now gets a conflict and calls nothing.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
	defer cancel()
	outcome, rerr := s.reverseTerminalCharge(rctx, ref, plan, intent)
	if rerr != nil {
		if ok, err := s.terminalLedger.ReleaseReversal(context.WithoutCancel(ctx), item.ID, entry.UserID); err != nil || !ok {
			s.logger.Error("CRITICAL: the failed reversal did not release the lost item",
				"item_id", item.ID, "released", ok, "error", err)
		}
		s.logger.Error("CRITICAL: the terminal charged and the manager's reversal failed",
			"item_id", item.ID, "reference", ref, "amount_cents", plan.reverse, "error", rerr)
		return nil, reversalFailure(ref)
	}
	entry.Changes["reversal"] = outcome
	entry.Changes["reversed_cents"] = plan.reverse
	entry.Changes["applied_cents"] = plan.applyC
	if err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, item.TransactionID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, item.TransactionID)
		if err != nil {
			return err
		}
		ok, err := s.terminalLedger.FinishReversal(ctx, item.ID, entry.UserID, in.Reference)
		if err != nil {
			return err
		}
		if !ok {
			return conflict("lost_item_settled", "the lost item was taken over by another resolution")
		}
		return s.settleSaleLeg(ctx, sale, intent, item, plan, ref, in, entry)
	}); err != nil {
		return nil, err
	}
	return settleItemView(item, "resolved_authorized"), nil
}

// reverseTerminalCharge puts the reversed part of the charge back through the
// gateway under the intent's idempotency key: the whole charge goes void
// first, then refund when the void is refused; a part goes as a refund of
// that part.
func (s *Service) reverseTerminalCharge(ctx context.Context, ref string, plan salePlan, intent *payment.TerminalIntent) (string, error) {
	if plan.applyC == 0 {
		if _, voidErr := payment.VoidUnderKey(ctx, s.gateway, ref, intent.IdempotencyKey); voidErr == nil {
			return string(chargeVoided), nil
		}
		if _, refundErr := payment.RefundUnderKey(ctx, s.gateway, ref, plan.reverse, intent.IdempotencyKey); refundErr != nil {
			return "", refundErr
		}
		return string(chargeRefunded), nil
	}
	if _, err := payment.RefundUnderKey(ctx, s.gateway, ref, plan.reverse, intent.IdempotencyKey+"-rest"); err != nil {
		return "", err
	}
	return string(chargeRefunded), nil
}

// settleSaleLeg writes everything a sale leg's resolution books, inside the
// transaction that closed (or finishes) the item: marking the intent spent
// when its money is booked or reversed, the payment and tender of the applied
// part, the audit row and the events.
func (s *Service) settleSaleLeg(ctx context.Context, sale *Sale, intent *payment.TerminalIntent, item *payment.TerminalLostItem,
	plan salePlan, ref string, in ResolveTerminalLostItemIn, entry audit.Entry) error {
	// an open sale keeps the authorized leg unspent for its completion; every
	// other plan has booked or reversed the money, so the leg is marked spent
	if !plan.record {
		spent, err := s.terminalLedger.MarkSpent(ctx, intent.ID)
		if err != nil {
			return err
		}
		if !spent {
			return conflict("terminal_intent_moved", "the intent was already booked or reversed by another act")
		}
	}
	var fx *account.Effects
	if plan.applyC > 0 {
		date, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
		if err != nil {
			return err
		}
		// what the invoice owes now bounds the application; a payment the
		// plan sized against an invoice that has since been paid down keeps
		// the rest as unapplied credit on the account (the money is recorded,
		// never lost)
		var open int64
		if err := s.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT ROUND(amount_open * 100)::bigint FROM invoices WHERE id = $1`, *plan.invoice).Scan(&open); err != nil {
			return err
		}
		apply := plan.applyC
		if apply > open {
			apply = open
		}
		var applications []account.ApplyLine
		if apply > 0 {
			applications = append(applications, account.ApplyLine{InvoiceID: *plan.invoice, AmountCents: apply})
		}
		customerID := derefUUID(sale.CustomerID)
		if sale.CustomerID == nil {
			// the walk-in customer backs a sale that names none, the way the
			// completion's own transaction does
			walkIn, _, err := s.repo.WalkInCustomer(ctx)
			if err != nil {
				return err
			}
			customerID = walkIn
		}
		payID, pfx, err := s.ar.RecordPayment(ctx, account.RecordPaymentIn{
			CustomerID: customerID, BranchID: sale.BranchID, Currency: sale.Currency,
			Method: "CARD", AmountCents: plan.applyC, Reference: in.Reference, ReceivedOn: date,
			Actor: entry.UserID, Card: &account.CardFacts{GatewayTxID: ref, GatewayStatus: "SETTLED",
				Last4: intent.AnswerLast4, Brand: intent.AnswerBrand, AuthCode: intent.AnswerAuthCode},
			Applications: applications,
		})
		if err != nil {
			return err
		}
		fx = pfx
		tender := &Tender{SaleID: sale.ID, Method: TenderCard, AmountCents: httpx.Cents(plan.applyC), PaymentID: &payID}
		g, a, l, b := ref, intent.AnswerAuthCode, intent.AnswerLast4, intent.AnswerBrand
		tender.GatewayTxID, tender.AuthCode, tender.CardLast4, tender.CardBrand = &g, &a, &l, &b
		if err := s.repo.AddTender(ctx, tender); err != nil {
			return err
		}
	}
	if err := s.writeResolveAudit(ctx, entry); err != nil {
		return err
	}
	if fx != nil {
		for _, ev := range fx.Events() {
			if err := s.recordEvent(ctx, ev); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveRefundAuthorized settles an authorized refund or void leg the books
// never took, through the refund claim's own resolution.
func (s *Service) resolveRefundAuthorized(ctx context.Context, item *payment.TerminalLostItem, intent *payment.TerminalIntent,
	in ResolveTerminalLostItemIn, ref string, entry audit.Entry) (*TerminalLostItemView, error) {
	compensation := strings.HasPrefix(intent.LegKey, compensationLegPrefix)
	var claimID uuid.UUID
	if !compensation {
		id, err := uuid.Parse(intent.LegKey)
		if err != nil {
			return nil, conflict("terminal_leg", "the intent's leg names no refund claim")
		}
		claimID = id
	}
	if err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, item.TransactionID); err != nil {
			return err
		}
		fresh, err := s.terminalLedger.Get(ctx, item.IntentID)
		if err != nil {
			return err
		}
		*intent = *fresh
		ok, err := s.terminalLedger.ResolveLostItemAuthorized(ctx, item.ID, entry.UserID, in.Reference)
		if err != nil {
			return err
		}
		if !ok {
			return conflict("lost_item_settled", "the lost item was already resolved or is being resolved")
		}
		if intent.State == payment.TerminalAuthorized && intent.SpentAt != nil {
			return s.writeResolveAudit(ctx, entry)
		}
		moved, err := s.terminalLedger.AuthorizeForResolution(ctx, intent.ID, ref)
		if err != nil {
			return err
		}
		if !moved {
			return conflict("terminal_intent_moved", "the intent is no longer unspent: read the item again")
		}
		if compensation {
			// the C2-5 compensation void of a sale's charge: it names a sale
			// leg, never a refund claim. The charge is back at the device, so
			// the sale leg it reversed is spent; nothing is booked.
			saleKey := strings.TrimPrefix(intent.LegKey, compensationLegPrefix)
			if saleLeg, err := s.terminalLedger.GetByKey(ctx, intent.TransactionID, saleKey, payment.TerminalKindSale); err == nil {
				if _, err := s.terminalLedger.MarkSpent(ctx, saleLeg.ID); err != nil {
					return err
				}
			}
		} else {
			rec, err := s.repo.GetRefundIntent(ctx, claimID)
			if err != nil {
				return err
			}
			if rec.SaleID != item.TransactionID {
				return conflict("terminal_leg", "the refund claim the leg names belongs to another sale")
			}
			switch rec.State {
			case "PENDING":
				claimRef := firstNonEmpty(rec.RefundTxID, ref)
				if claimRef == "" {
					return conflict("no_provider_reference",
						"the intent carries no provider reference to book: name the terminal's reference")
				}
				if _, err := s.ResolveRefundIntent(ctx, claimID, ResolveIntentIn{
					Outcome: "refund", RefundTxID: claimRef, Reason: in.Reason}, entry.UserID); err != nil {
					return err
				}
			case "DONE":
				// the claim already booked through its own resolution: the
				// item only closes
			default:
				return conflict("claim_released",
					fmt.Sprintf("the refund claim is %s: the books released it, reconcile the terminal's refund by hand", rec.State))
			}
		}
		spent, err := s.terminalLedger.MarkSpent(ctx, intent.ID)
		if err != nil {
			return err
		}
		if !spent {
			return conflict("terminal_intent_moved", "the intent was already marked spent")
		}
		return s.writeResolveAudit(ctx, entry)
	}); err != nil {
		return nil, err
	}
	return settleItemView(item, "resolved_authorized"), nil
}
