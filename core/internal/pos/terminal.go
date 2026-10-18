// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The card terminal at the counter (CP-05, IX-01): a card leg goes through
// the intent ledger, not straight at the gateway. The claim is committed
// before any device call, the dispatch and the answer are guarded state
// moves, the sweep turns a lease that ran out into a manager item, and the
// C2-5 compensation and the refund claims keep their own machinery and bind
// to the intents: every refund or void the counter takes at the gateway is
// one terminal intent of its own beside the refund claim, so a lost answer
// is one row a manager resolves, never a guess. The payment package's
// terminal.go comment block writes the crash windows; the tests live in
// terminal_counter_test.go and terminal_window_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// terminalAnswerLease is how long a card leg's answer may take at the device
// before the sweep may turn the intent lost. It exceeds the counter's wait
// below, so a waited out act leaves the intent inside its lease and only the
// sweep (past the lease) decides it is lost.
const terminalAnswerLease = 90 * time.Second

// terminalWaitDefault is how long the counter waits for the device's answer
// before it answers the desk a waiting state. The intent stays
// pending_at_device; the cancel the desk offers is honest about the device
// (it never claims money back), and the sweep files the manager item.
const terminalWaitDefault = 30 * time.Second

// WithTerminal wires the terminal device seam and its ledger. Without it the
// counter keeps the plain gateway path (a token bearing charge through the
// payment gateway), exactly as before.
func (s *Service) WithTerminal(p payment.TerminalProvider, ledger *payment.TerminalLedger) *Service {
	s.terminal = p
	s.terminalLedger = ledger
	if s.terminalWait == 0 {
		s.terminalWait = terminalWaitDefault
	}
	return s
}

// WithTerminalWait bounds how long the counter waits for one device answer.
func (s *Service) WithTerminalWait(d time.Duration) *Service {
	s.terminalWait = d
	return s
}

// terminalWired says the counter runs its card legs through the ledger.
func (s *Service) terminalWired() bool { return s.terminal != nil && s.terminalLedger != nil }

// chargeCardAtTerminal takes one card tender through the intent: claim
// (committed before any device call), dispatch, wait for the answer. The
// verdict comes back as the charge result the completion's transaction
// records; a decline answers the card_declined refusal; a wait that runs out
// answers terminal_answer_pending naming the intent, and the intent stays
// pending_at_device for the sweep and the desk's waiting state.
func (s *Service) chargeCardAtTerminal(ctx context.Context, sale *Sale, legIndex int, t *TenderIn) (*chargeResult, error) {
	legKey := fmt.Sprintf("card-%d", legIndex)
	intent := &payment.TerminalIntent{
		ID: uuid.New(), TransactionID: sale.ID, LegKey: legKey, RegisterID: sale.RegisterID,
		BranchID: sale.BranchID, AmountCents: int64(t.AmountCents), Kind: payment.TerminalKindSale,
		DeviceID: sale.RegisterID, IdempotencyKey: fmt.Sprintf("sale-%s-%s", sale.ID, legKey),
		LeaseUntil: s.now().Add(terminalAnswerLease),
	}
	if err := s.terminalLedger.Claim(ctx, intent); err != nil {
		if errors.Is(err, payment.ErrTerminalIntentConflict) {
			return nil, conflict("terminal_leg_in_flight",
				"a terminal intent already holds this card leg: settle it before charging the card again")
		}
		return nil, err
	}
	receipt, err := s.terminal.Dispatch(ctx, payment.TerminalDispatch{
		IntentID: intent.ID, Kind: intent.Kind, AmountCents: intent.AmountCents, Currency: sale.Currency,
		RegisterID: intent.RegisterID, DeviceID: intent.DeviceID, IdempotencyKey: intent.IdempotencyKey,
	})
	// The dispatched move happens on both paths: a receipt carries the
	// device's reference, and a failed dispatch carries none, but the device
	// may still have taken the leg (a crash after the take), so the intent
	// never stays claimed behind a dispatch that may have reached it.
	if _, merr := s.terminalLedger.MarkDispatched(ctx, intent.ID, receipt.ProviderRef, s.now().Add(terminalAnswerLease)); merr != nil {
		return nil, merr
	}
	if err != nil {
		// the dispatch failed and no verdict exists: wait the bound anyway,
		// because a device that took the leg before failing may still answer;
		// the wait below lands in the same terminal_answer_pending refusal.
		_ = err
	}
	deadline := s.now().Add(s.terminalWait)
	for {
		fresh, gerr := s.terminalLedger.Get(ctx, intent.ID)
		if gerr != nil {
			return nil, gerr
		}
		switch fresh.State {
		case payment.TerminalAuthorized:
			return &chargeResult{GatewayTxID: fresh.AnswerProviderRef, AuthCode: fresh.AnswerAuthCode,
				Last4: fresh.AnswerLast4, Brand: fresh.AnswerBrand}, nil
		case payment.TerminalDeclined:
			return nil, conflict("card_declined", "the card was declined at the terminal")
		}
		if !s.now().Before(deadline) {
			return nil, conflict("terminal_answer_pending",
				fmt.Sprintf("the terminal has the card leg and has not answered (intent %s): the sale is not completed; the desk shows the waiting state and the sweep files the manager item once the lease runs out", intent.ID))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// terminalRefundLeg binds one gateway refund or void the counter takes to
// the intent ledger: the claim beside the refund claim's own, dispatched
// before the call, the verdict applied after. A nil answer means no terminal
// is wired and the call runs exactly as before. The idempotency key is the
// refund claim's own id, so a retried call is the same leg at the device.
func (s *Service) terminalRefundLeg(ctx context.Context, saleID, tenderID uuid.UUID, kind string, amountCents int64, register string) *payment.TerminalIntent {
	if !s.terminalWired() {
		return nil
	}
	sale, err := s.repo.GetSale(ctx, saleID)
	if err != nil {
		return nil
	}
	intent := &payment.TerminalIntent{
		ID: uuid.New(), TransactionID: saleID, LegKey: tenderID.String(), RegisterID: sale.RegisterID,
		BranchID: sale.BranchID, AmountCents: amountCents, Kind: kind, DeviceID: sale.RegisterID,
		IdempotencyKey: fmt.Sprintf("%s-%s", kind, tenderID), LeaseUntil: s.now().Add(terminalAnswerLease),
	}
	if register != "" {
		intent.RegisterID = register
		intent.DeviceID = register
	}
	if err := s.terminalLedger.Claim(ctx, intent); err != nil {
		return nil
	}
	if _, err := s.terminalLedger.MarkDispatched(ctx, intent.ID, "", s.now().Add(terminalAnswerLease)); err != nil {
		return nil
	}
	return intent
}

// terminalRefundKey is the idempotency key of a bound refund or void call.
func terminalRefundKey(intent *payment.TerminalIntent) string {
	if intent == nil {
		return ""
	}
	return intent.IdempotencyKey
}

// terminalRefundVerdict applies the gateway call's outcome to the bound
// intent: accepted answers authorized, the terminal's own no answers
// declined, and no verdict leaves the intent pending for the sweep (the
// money may have moved).
func (s *Service) terminalRefundVerdict(ctx context.Context, intent *payment.TerminalIntent, accepted bool, providerRef string) {
	if intent == nil {
		return
	}
	verdict := "declined"
	if accepted {
		verdict = "authorized"
	}
	sctx := context.WithoutCancel(ctx)
	if _, err := s.terminalLedger.ApplyDeviceAnswer(sctx, payment.TerminalAnswer{
		IntentID: intent.ID, ProviderRef: providerRef, Verdict: verdict}); err != nil {
		s.logger.Error("the bound terminal intent took no verdict",
			"intent_id", intent.ID, "kind", intent.Kind, "error", err)
	}
}

// SweepTerminalIntents is the sweep act: the scheduler or the guarded route
// runs it, and it turns intents past their lease into lost, filing one
// manager item per dispatched leg (a claim that never reached the device is
// simply closed). Audit row and log line name both counts.
func (s *Service) SweepTerminalIntents(ctx context.Context, actor string) (*payment.SweepResult, error) {
	if !s.terminalWired() {
		return &payment.SweepResult{}, nil
	}
	res, err := s.terminalLedger.Sweep(ctx, s.now())
	if err != nil {
		return nil, err
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "pos.terminal_intents.swept", EntityType: "pos_transaction", EntityID: uuid.Nil, UserID: actor,
			Changes: map[string]any{"claimed_closed": res.ClaimedClosed, "dispatched_lost": res.DispatchedLost}}); err != nil {
			return nil, fmt.Errorf("failed to write the sweep audit row: %w", err)
		}
	}
	s.logger.Info("the terminal intent sweep ran",
		"claimed_closed", res.ClaimedClosed, "dispatched_lost", res.DispatchedLost)
	return res, nil
}

// TerminalIntentView is the desk's waiting state: where the leg stands.
type TerminalIntentView struct {
	ID           uuid.UUID `json:"id"`
	TransactionID uuid.UUID `json:"transaction_id"`
	LegKey       string    `json:"leg_key"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	AmountCents  int64     `json:"amount_cents"`
	ProviderRef  string    `json:"provider_ref"`
	AnswerVerdict string   `json:"answer_verdict"`
}

// GetTerminalIntent reads one intent for the desk's waiting state, held to
// the caller's branch wall through the sale the intent names.
func (s *Service) GetTerminalIntent(ctx context.Context, id uuid.UUID) (*TerminalIntentView, error) {
	if !s.terminalWired() {
		return nil, errTerminalNotConfigured
	}
	in, err := s.terminalLedger.Get(ctx, id)
	if err != nil {
		return nil, httpx.NotFound("terminal intent not found")
	}
	if _, err := s.repo.GetSale(ctx, in.TransactionID); err != nil {
		return nil, httpx.NotFound("terminal intent not found")
	}
	return &TerminalIntentView{ID: in.ID, TransactionID: in.TransactionID, LegKey: in.LegKey, Kind: in.Kind,
		State: string(in.State), AmountCents: in.AmountCents, ProviderRef: in.ProviderRef, AnswerVerdict: in.AnswerVerdict}, nil
}

// CancelTerminalIntent is the desk's cancel, honest about the device: a
// claim the device never reached closes lost with no manager item; a leg at
// the device refuses, because the device may still move the money and only
// the sweep and a manager may settle that.
func (s *Service) CancelTerminalIntent(ctx context.Context, id uuid.UUID, actor string) (*TerminalIntentView, error) {
	if !s.terminalWired() {
		return nil, errTerminalNotConfigured
	}
	in, err := s.terminalLedger.Get(ctx, id)
	if err != nil {
		return nil, httpx.NotFound("terminal intent not found")
	}
	if _, err := s.repo.GetSale(ctx, in.TransactionID); err != nil {
		return nil, httpx.NotFound("terminal intent not found")
	}
	switch in.State {
	case payment.TerminalClaimed:
		if err := s.terminalLedger.CloseClaimedIntent(ctx, id); err != nil {
			return nil, err
		}
		fresh, err := s.terminalLedger.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if fresh.State != payment.TerminalLost {
			return nil, conflict("terminal_leg_in_flight",
				"the terminal took the card leg while it was cancelled: read its state again")
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.terminal_intent.cancelled",
				EntityType: "pos_transaction", EntityID: in.TransactionID, UserID: actor,
				Changes: map[string]any{"intent_id": in.ID, "kind": in.Kind, "amount_cents": in.AmountCents}}); err != nil {
				return nil, fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		return &TerminalIntentView{ID: fresh.ID, TransactionID: fresh.TransactionID, LegKey: fresh.LegKey,
			Kind: fresh.Kind, State: string(fresh.State), AmountCents: fresh.AmountCents}, nil
	case payment.TerminalPendingAtDevice:
		return nil, conflict("terminal_at_device",
			"the terminal has the card leg: a cancel cannot recall it. The money may still move; the sweep files the manager item once the lease runs out")
	default:
		return nil, conflict("terminal_intent_settled",
			fmt.Sprintf("the terminal intent is %s: it has already been settled", in.State))
	}
}

// TerminalLostItemView is one manager item of the lost answer queue.
type TerminalLostItemView struct {
	ID            uuid.UUID `json:"id"`
	IntentID      uuid.UUID `json:"intent_id"`
	BranchID      uuid.UUID `json:"branch_id"`
	RegisterID    string    `json:"register_id"`
	TransactionID uuid.UUID `json:"transaction_id"`
	Kind          string    `json:"kind"`
	AmountCents   int64     `json:"amount_cents"`
	DeviceID      string    `json:"device_id"`
	ProviderRef   string    `json:"provider_ref"`
	State         string    `json:"state"`
}

// ListTerminalLostItems answers the open manager items of the lost answer
// queue, held to the caller's branch wall.
func (s *Service) ListTerminalLostItems(ctx context.Context) ([]TerminalLostItemView, error) {
	if !s.terminalWired() {
		return nil, errTerminalNotConfigured
	}
	items, err := s.terminalLedger.OpenLostItems(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []TerminalLostItemView
	for _, it := range items {
		// the branch wall: an item whose sale the caller's branch context
		// hides is not the caller's to see
		if _, err := s.repo.GetSale(ctx, it.TransactionID); err != nil {
			continue
		}
		out = append(out, TerminalLostItemView{ID: it.ID, IntentID: it.IntentID, BranchID: it.BranchID,
			RegisterID: it.RegisterID, TransactionID: it.TransactionID, Kind: it.Kind, AmountCents: it.AmountCents,
			DeviceID: it.DeviceID, ProviderRef: it.ProviderRef, State: it.State})
	}
	return out, nil
}

// ResolveTerminalLostItemIn is a manager's resolution of one lost answer.
type ResolveTerminalLostItemIn struct {
	Outcome   string // "authorized" or "declined"
	Reference string // the terminal's own record the manager read
	Reason    string
}

// errTerminalNotConfigured is the refusal of the terminal routes when the
// counter runs no terminal.
var errTerminalNotConfigured = httpx.Unavailable("this counter runs no card terminal")

// ResolveTerminalLostItem settles one lost answer the way the terminal's own
// record says (a manager only): authorized applies the tender to the sale it
// belongs to, or refunds it when the sale is gone; declined closes the item
// and moves nothing. Both legs write one audit row naming the person, the
// item, the outcome and the reference.
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
	// the branch wall, the record arm: the item's branch is held to the
	// caller's grants before either leg runs
	if err := s.holdRecordBranch(ctx, "lost item", &item.BranchID, itemID); err != nil {
		return nil, err
	}
	view := TerminalLostItemView{ID: item.ID, IntentID: item.IntentID, BranchID: item.BranchID,
		RegisterID: item.RegisterID, TransactionID: item.TransactionID, Kind: item.Kind,
		AmountCents: item.AmountCents, DeviceID: item.DeviceID, ProviderRef: item.ProviderRef}
	entry := audit.Entry{Action: "pos.terminal_lost_item.resolved", EntityType: "pos_transaction",
		EntityID: item.TransactionID, UserID: actor,
		Changes: map[string]any{"item_id": item.ID, "intent_id": item.IntentID, "kind": item.Kind,
			"amount_cents": item.AmountCents, "outcome": in.Outcome, "reference": in.Reference, "reason": in.Reason}}
	switch in.Outcome {
	case "declined":
		view.State = "resolved_declined"
		if err := s.inTx(ctx, func(ctx context.Context) error {
			ok, err := s.terminalLedger.ResolveLostItemDeclined(ctx, itemID, actor, in.Reference)
			if err != nil {
				return err
			}
			if !ok {
				return conflict("lost_item_settled", "the lost item was already resolved")
			}
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, entry); err != nil {
					return fmt.Errorf("failed to write the resolution audit row: %w", err)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return &view, nil
	case "authorized":
		view.State = "resolved_authorized"
		intent, err := s.terminalLedger.Get(ctx, item.IntentID)
		if err != nil {
			return nil, err
		}
		sale, err := s.repo.GetSale(ctx, item.TransactionID)
		if err != nil {
			return nil, err
		}
		switch intent.Kind {
		case payment.TerminalKindSale:
			return s.resolveSaleIntent(ctx, item, intent, sale, in, view, entry)
		case payment.TerminalKindRefund, payment.TerminalKindVoid:
			return s.resolveRefundIntent(ctx, item, intent, sale, in, view, entry)
		}
		return nil, conflict("terminal_kind", "the intent names no kind the counter can resolve")
	}
	return nil, invalid("outcome", "must be authorized or declined")
}

// resolveSaleIntent applies an authorized sale leg the completion never
// took: the sale still completed through other means keeps its invoice, and
// the money is recorded against it; a sale that never completed (or was
// voided) gives the money back through the gateway, the C2-5 way, and only
// then closes the item.
func (s *Service) resolveSaleIntent(ctx context.Context, item *payment.TerminalLostItem, intent *payment.TerminalIntent,
	sale *Sale, in ResolveTerminalLostItemIn, view TerminalLostItemView, entry audit.Entry) (*TerminalLostItemView, error) {
	if sale.Status != StatusCompleted || sale.InvoiceID == nil {
		// the sale is gone: the money goes back the way a refused charge goes
		// back (the same day void first, the refund when the void is
		// refused), outside the transaction, before the item closes
		if intent.AnswerProviderRef == "" && intent.ProviderRef == "" {
			return nil, conflict("no_provider_reference",
				"the intent carries no provider reference to reverse: reconcile it by hand")
		}
		ref := intent.AnswerProviderRef
		if ref == "" {
			ref = intent.ProviderRef
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reversalTimeout)
		defer cancel()
		if _, voidErr := payment.VoidUnderKey(rctx, s.gateway, ref, intent.IdempotencyKey); voidErr != nil {
			if _, refundErr := payment.RefundUnderKey(rctx, s.gateway, ref, intent.AmountCents, intent.IdempotencyKey); refundErr != nil {
				return nil, &httpx.Error{Status: http.StatusBadGateway, Code: httpx.CodeChargeNotReversed, Operator: true,
					Message: fmt.Sprintf("the terminal charged and the reversal failed; finance must reconcile terminal reference %s", ref)}
			}
		}
		if err := s.inTx(ctx, func(ctx context.Context) error {
			ok, err := s.terminalLedger.ResolveLostItemAuthorized(ctx, item.ID, entry.UserID, in.Reference)
			if err != nil || !ok {
				if err == nil {
					return conflict("lost_item_settled", "the lost item was already resolved")
				}
				return err
			}
			return s.writeResolveAudit(ctx, entry)
		}); err != nil {
			return nil, err
		}
		return &view, nil
	}
	// the sale completed: record the money against its invoice and the
	// tender row the completion never wrote, in one transaction with the
	// item's close, the audit row and the events
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, sale.ID); err != nil {
			return err
		}
		date, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
		if err != nil {
			return err
		}
		// what the invoice still owes bounds the application: the rest of
		// the money records as unapplied credit on the account (the sale
		// may have been paid another way while the answer was lost)
		var open int64
		if err := s.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT ROUND(amount_open * 100)::bigint FROM invoices WHERE id = $1`, *sale.InvoiceID).Scan(&open); err != nil {
			return err
		}
		apply := intent.AmountCents
		if apply > open {
			apply = open
		}
		var applications []account.ApplyLine
		if apply > 0 {
			applications = append(applications, account.ApplyLine{InvoiceID: *sale.InvoiceID, AmountCents: apply})
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
		payID, fx, err := s.ar.RecordPayment(ctx, account.RecordPaymentIn{
			CustomerID: customerID, BranchID: sale.BranchID, Currency: sale.Currency,
			Method: "CARD", AmountCents: intent.AmountCents, Reference: in.Reference, ReceivedOn: date,
			Actor: entry.UserID, Card: &account.CardFacts{GatewayTxID: intent.AnswerProviderRef,
				GatewayStatus: "SETTLED", Last4: intent.AnswerLast4, Brand: intent.AnswerBrand, AuthCode: intent.AnswerAuthCode},
			Applications: applications,
		})
		if err != nil {
			return err
		}
		tender := &Tender{SaleID: sale.ID, Method: TenderCard, AmountCents: httpx.Cents(intent.AmountCents), PaymentID: &payID}
		g, a, l, b := intent.AnswerProviderRef, intent.AnswerAuthCode, intent.AnswerLast4, intent.AnswerBrand
		tender.GatewayTxID, tender.AuthCode, tender.CardLast4, tender.CardBrand = &g, &a, &l, &b
		if err := s.repo.AddTender(ctx, tender); err != nil {
			return err
		}
		ok, err := s.terminalLedger.ResolveLostItemAuthorized(ctx, item.ID, entry.UserID, in.Reference)
		if err != nil {
			return err
		}
		if !ok {
			return conflict("lost_item_settled", "the lost item was already resolved")
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
	})
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// resolveRefundIntent books an authorized refund or void leg the books never
// took: the tender the leg names still holds a posted payment, and the
// refund is recorded against it under the provider's own reference, in one
// transaction with the item's close, the audit row and the events.
func (s *Service) resolveRefundIntent(ctx context.Context, item *payment.TerminalLostItem, intent *payment.TerminalIntent,
	sale *Sale, in ResolveTerminalLostItemIn, view TerminalLostItemView, entry audit.Entry) (*TerminalLostItemView, error) {
	var out *TerminalLostItemView
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, sale.ID); err != nil {
			return err
		}
		tenderID, err := uuid.Parse(intent.LegKey)
		if err != nil {
			return conflict("terminal_leg", "the intent's leg names no tender of the sale")
		}
		tenders, err := s.repo.GetTenders(ctx, sale.ID)
		if err != nil {
			return err
		}
		var tender *Tender
		for i := range tenders {
			if tenders[i].ID == tenderID {
				tender = &tenders[i]
			}
		}
		if tender == nil || tender.PaymentID == nil {
			return conflict("tender_refunded", "the intent names no card tender the sale still holds")
		}
		date, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
		if err != nil {
			return err
		}
		_, fx, err := s.ar.RefundPayment(ctx, account.RefundPaymentIn{
			PaymentID: *tender.PaymentID, AmountCents: intent.AmountCents, Reason: in.Reason,
			GatewayRefundID: intent.AnswerProviderRef, Actor: entry.UserID, On: date})
		if err != nil {
			return err
		}
		ok, err := s.terminalLedger.ResolveLostItemAuthorized(ctx, item.ID, entry.UserID, in.Reference)
		if err != nil {
			return err
		}
		if !ok {
			return conflict("lost_item_settled", "the lost item was already resolved")
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
		out = &view
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// writeResolveAudit writes the resolution audit row inside the leg's
// transaction.
func (s *Service) writeResolveAudit(ctx context.Context, entry audit.Entry) error {
	if s.auditLog == nil {
		return nil
	}
	if err := s.auditLog.Log(ctx, entry); err != nil {
		return fmt.Errorf("failed to write the resolution audit row: %w", err)
	}
	return nil
}

// derefUUID answers the pointed id or the nil uuid.
func derefUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}
