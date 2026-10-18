// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The card terminal at the counter (CP-05, IX-01): a card leg goes through
// the intent ledger, not straight at the gateway. The claim is committed
// before any device call, the move to pending_at_device is committed before
// the device call too (so claimed means "never reached the device" and
// anything that may have reached it is pending_at_device), the answer is a
// guarded state move, the sweep turns a lease that ran out into a manager
// item, and the C2-5 compensation and the refund claims keep their own
// machinery and bind to the intents: every refund or void the counter takes
// at the gateway is one terminal intent of its own beside the refund claim
// (its leg key is the claim's id), so a lost answer is one row a manager
// resolves, never a guess. The payment package's terminal.go comment block
// writes the crash windows; the tests that stop the counter act at each of
// them are terminal_crash_test.go (the claim, the dispatch move, the device
// call and the answer) and terminal_counter_test.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
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

// The crash points of a card leg's act, in order. A test seam
// (WithTerminalCrash) may stop the act at any of them to prove where the
// intent, the sale and the money end.
const (
	// TerminalPointAfterClaim: the claim is committed, nothing else happened.
	TerminalPointAfterClaim = "after_claim"
	// TerminalPointAfterDispatchMove: the intent is pending_at_device, the
	// device not yet called.
	TerminalPointAfterDispatchMove = "after_dispatch_move"
	// TerminalPointAfterDeviceCall: the device took the leg, its reference
	// is stamped, no answer read yet.
	TerminalPointAfterDeviceCall = "after_device_call"
	// TerminalPointAfterAnswer: the device authorized the leg, the sale's
	// completion transaction not yet opened.
	TerminalPointAfterAnswer = "after_answer"
)

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

// WithTerminalCrash wires the test seam that stops a card leg's act at one
// of the crash points: the hook's error ends the act right there, exactly
// as a crash would, leaving the intent, the sale and the money as they stand.
// Production wires no hook.
func (s *Service) WithTerminalCrash(hook func(point string) error) *Service {
	s.terminalCrash = hook
	return s
}

// crashAt runs the crash seam at a point of the act.
func (s *Service) crashAt(point string) error {
	if s.terminalCrash == nil {
		return nil
	}
	return s.terminalCrash(point)
}

// terminalWired says the counter runs its card legs through the ledger.
func (s *Service) terminalWired() bool { return s.terminal != nil && s.terminalLedger != nil }

// firstNonEmpty answers the first non empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// resultOfLeg reads an authorized leg as the charge the completion records.
func resultOfLeg(in *payment.TerminalIntent) *chargeResult {
	return &chargeResult{GatewayTxID: firstNonEmpty(in.AnswerProviderRef, in.ProviderRef), AuthCode: in.AnswerAuthCode,
		Last4: in.AnswerLast4, Brand: in.AnswerBrand, IntentID: in.ID}
}

// attemptOf reads the attempt number of a leg key (card-0 is attempt 0,
// card-0#2 is attempt 2).
func attemptOf(legKey string) int {
	if i := strings.IndexByte(legKey, '#'); i >= 0 {
		n := 0
		fmt.Sscanf(legKey[i+1:], "%d", &n)
		return n
	}
	return 0
}

// nextCardAttempt settles which leg key a card tender claims: the first
// attempt, a retry after a decline or a closed claim, or the authorized leg
// a manager's resolution left on the open sale (reused, never charged
// again). A leg that is claimed or at the device, or lost with its manager
// item unresolved, refuses a second charge.
func (s *Service) nextCardAttempt(ctx context.Context, sale *Sale, base string, amountCents int64) (legKey string, reuse *payment.TerminalIntent, err error) {
	prior, err := s.terminalLedger.LatestLeg(ctx, sale.ID, base, payment.TerminalKindSale)
	if err != nil {
		return "", nil, err
	}
	if prior == nil {
		return base, nil, nil
	}
	switch prior.State {
	case payment.TerminalClaimed, payment.TerminalPendingAtDevice:
		return "", nil, conflict("terminal_leg_in_flight",
			"a terminal intent already holds this card leg: settle it before charging the card again")
	case payment.TerminalAuthorized:
		if prior.SpentAt == nil {
			if prior.AmountCents != amountCents {
				return "", nil, conflict("terminal_leg_amount",
					fmt.Sprintf("the terminal already authorized %d cents for this card leg: complete the sale with that amount", prior.AmountCents))
			}
			return "", prior, nil
		}
	case payment.TerminalLost:
		item, err := s.terminalLedger.LostItemOfIntent(ctx, prior.ID)
		if err != nil {
			return "", nil, err
		}
		if item != nil && (item.State == "open" || item.State == "resolving") {
			return "", nil, conflict("terminal_answer_unresolved",
				"the terminal's answer for this card leg was lost and awaits a manager: resolve it before charging the card again")
		}
	}
	return fmt.Sprintf("%s#%d", base, attemptOf(prior.LegKey)+1), nil, nil
}

// chargeCardAtTerminal takes one card tender through the intent: claim
// (committed before any device call), the dispatch move (committed before the
// device call too), the device call, the wait for the answer. The verdict
// comes back as the charge result the completion's transaction records; a
// decline answers the card_declined refusal; a wait that runs out answers
// terminal_answer_pending naming the intent, and the intent stays
// pending_at_device for the sweep and the desk's waiting state.
func (s *Service) chargeCardAtTerminal(ctx context.Context, sale *Sale, legIndex int, t *TenderIn) (*chargeResult, error) {
	amount := int64(t.AmountCents)
	legKey, reuse, err := s.nextCardAttempt(ctx, sale, fmt.Sprintf("card-%d", legIndex), amount)
	if err != nil {
		return nil, err
	}
	if reuse != nil {
		return resultOfLeg(reuse), nil
	}
	intent := &payment.TerminalIntent{
		ID: uuid.New(), TransactionID: sale.ID, LegKey: legKey, RegisterID: sale.RegisterID,
		BranchID: sale.BranchID, AmountCents: amount, Kind: payment.TerminalKindSale,
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
	if err := s.crashAt(TerminalPointAfterClaim); err != nil {
		return nil, err
	}
	// The dispatch move is committed BEFORE the device is called: from here
	// the intent is pending_at_device whatever the call does, so the sweep
	// files a manager item for anything that may have reached the device,
	// and a device answer that lands at once finds an intent it can move.
	moved, err := s.terminalLedger.MarkDispatched(ctx, intent.ID, "", s.now().Add(terminalAnswerLease))
	if err != nil {
		return nil, err
	}
	if !moved {
		// the sweep or a cancel closed the claim before the device was
		// reached: the device is not called
		return nil, conflict("terminal_leg_closed",
			"the card leg was closed before the terminal was reached: charge the card again")
	}
	if err := s.crashAt(TerminalPointAfterDispatchMove); err != nil {
		return nil, err
	}
	receipt, derr := s.terminal.Dispatch(ctx, payment.TerminalDispatch{
		IntentID: intent.ID, Kind: intent.Kind, AmountCents: intent.AmountCents, Currency: sale.Currency,
		RegisterID: intent.RegisterID, DeviceID: intent.DeviceID, IdempotencyKey: intent.IdempotencyKey,
	})
	if serr := s.terminalLedger.StampProviderRef(context.WithoutCancel(ctx), intent.ID, receipt.ProviderRef); serr != nil {
		s.logger.Error("the device's reference was not stamped on the terminal intent",
			"intent_id", intent.ID, "error", serr)
	}
	if err := s.crashAt(TerminalPointAfterDeviceCall); err != nil {
		return nil, err
	}
	if derr != nil {
		// the dispatch failed and no verdict exists: wait the bound anyway,
		// because a device that took the leg before failing may still answer;
		// the wait below lands in the same terminal_answer_pending refusal.
		s.logger.Warn("the terminal dispatch failed after the claim; the leg stays pending_at_device",
			"intent_id", intent.ID, "error", derr)
	}
	deadline := s.now().Add(s.terminalWait)
	for {
		fresh, gerr := s.terminalLedger.Get(ctx, intent.ID)
		if gerr != nil {
			return nil, gerr
		}
		switch fresh.State {
		case payment.TerminalAuthorized:
			if err := s.crashAt(TerminalPointAfterAnswer); err != nil {
				return nil, err
			}
			return resultOfLeg(fresh), nil
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

// refuseStrayAuthorizedLegs refuses a completion that leaves behind an
// authorized card leg of its sale (the terminal took the money, a manager's
// resolution left the leg on the open sale): the completion must use it
// (same tender slot, same amount), never complete the sale around it.
func (s *Service) refuseStrayAuthorizedLegs(ctx context.Context, saleID uuid.UUID, tenders []TenderIn) error {
	legs, err := s.terminalLedger.UnspentAuthorizedLegs(ctx, saleID)
	if err != nil {
		return err
	}
	for _, leg := range legs {
		base := leg.LegKey
		if i := strings.IndexByte(base, '#'); i >= 0 {
			base = base[:i]
		}
		idx := -1
		if _, err := fmt.Sscanf(base, "card-%d", &idx); err != nil {
			idx = -1
		}
		if idx < 0 || idx >= len(tenders) || tenders[idx].Method != TenderCard || int64(tenders[idx].AmountCents) != leg.AmountCents {
			return conflict("authorized_leg_unused",
				fmt.Sprintf("the terminal holds an authorized card leg of %d cents for this sale (%s): complete the sale with that card tender in the same slot", leg.AmountCents, leg.LegKey))
		}
	}
	return nil
}

// terminalRefundLeg binds one gateway refund or void the counter takes to
// the intent ledger: the claim beside the refund claim's own, dispatched
// before the call, the verdict applied after. A nil answer means no terminal
// is wired and the call runs exactly as before. The leg key and the
// idempotency key name the refund claim's own id, so a retried call is the
// same leg at the device and a resolution finds its claim by the leg.
func (s *Service) terminalRefundLeg(ctx context.Context, saleID, claimID uuid.UUID, kind string, amountCents int64, register string) *payment.TerminalIntent {
	if !s.terminalWired() {
		return nil
	}
	sale, err := s.repo.GetSale(ctx, saleID)
	if err != nil {
		return nil
	}
	intent := &payment.TerminalIntent{
		ID: uuid.New(), TransactionID: saleID, LegKey: claimID.String(), RegisterID: sale.RegisterID,
		BranchID: sale.BranchID, AmountCents: amountCents, Kind: kind, DeviceID: sale.RegisterID,
		IdempotencyKey: fmt.Sprintf("%s-%s", kind, claimID), LeaseUntil: s.now().Add(terminalAnswerLease),
	}
	if register != "" {
		intent.RegisterID = register
		intent.DeviceID = register
	}
	if err := s.terminalLedger.Claim(ctx, intent); err != nil {
		return nil
	}
	if ok, err := s.terminalLedger.MarkDispatched(ctx, intent.ID, "", s.now().Add(terminalAnswerLease)); err != nil || !ok {
		return nil
	}
	return intent
}

// compensationLegPrefix marks the leg key of a C2-5 compensation void: a
// reversal of a sale's authorized card charge when the sale's transaction
// refused. It names a sale intent, never a refund claim.
const compensationLegPrefix = "comp-"

// terminalCompensationLeg binds the C2-5 compensation void of one authorized
// sale leg to the ledger: a void intent beside the sale's, keyed on the sale
// leg. A retried compensation finds its leg again.
func (s *Service) terminalCompensationLeg(ctx context.Context, saleLeg *payment.TerminalIntent) *payment.TerminalIntent {
	if !s.terminalWired() {
		return nil
	}
	intent := &payment.TerminalIntent{
		ID: uuid.New(), TransactionID: saleLeg.TransactionID, LegKey: compensationLegPrefix + saleLeg.LegKey,
		RegisterID: saleLeg.RegisterID, BranchID: saleLeg.BranchID, AmountCents: saleLeg.AmountCents,
		Kind: payment.TerminalKindVoid, DeviceID: saleLeg.DeviceID,
		IdempotencyKey: "void-" + saleLeg.IdempotencyKey, LeaseUntil: s.now().Add(terminalAnswerLease),
	}
	if err := s.terminalLedger.Claim(ctx, intent); err != nil {
		existing, gerr := s.terminalLedger.GetByKey(ctx, intent.TransactionID, intent.LegKey, intent.Kind)
		if gerr != nil {
			return nil
		}
		return existing
	}
	if ok, err := s.terminalLedger.MarkDispatched(ctx, intent.ID, "", s.now().Add(terminalAnswerLease)); err != nil || !ok {
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

// chargeMade is one card charge the counter took before the completion's
// transaction opened.
type chargeMade struct {
	result      *chargeResult
	amountCents int64
}

// reverseCharges puts every charge made back after the completion refused or
// a later tender failed (C2-5): a gateway charge goes back through the
// gateway (void first, the refund when the void is refused); a terminal leg
// goes back the same way when a gateway is wired, bound to a compensation
// void intent of its own, and the sale leg is then spent. With no gateway
// wired a terminal leg is left authorized on the open sale: the retried
// completion reuses it, and the stray leg guard refuses a completion that
// would leave it behind. The first failure of a reversal is answered, the
// rest are still attempted.
func (s *Service) reverseCharges(ctx context.Context, made []chargeMade, saleID uuid.UUID, actor string, cause error) error {
	var firstErr error
	for _, c := range made {
		var rerr error
		if c.result.IntentID != uuid.Nil {
			rerr = s.reverseTerminalLeg(ctx, c, saleID, actor, cause)
		} else {
			rerr = s.reverseCharge(ctx, c.result.GatewayTxID, c.amountCents, saleID, actor, cause, "")
		}
		if rerr != nil && firstErr == nil {
			firstErr = rerr
		}
	}
	return firstErr
}

// reverseTerminalLeg reverses one authorized terminal sale leg.
func (s *Service) reverseTerminalLeg(ctx context.Context, c chargeMade, saleID uuid.UUID, actor string, cause error) error {
	if s.gateway == nil {
		s.logger.Warn("the terminal leg stays authorized on the open sale: no gateway is wired to reverse it",
			"intent_id", c.result.IntentID, "sale_id", saleID, "amount_cents", c.amountCents, "error", cause)
		return nil
	}
	leg, err := s.terminalLedger.Get(ctx, c.result.IntentID)
	if err != nil {
		return err
	}
	comp := s.terminalCompensationLeg(ctx, leg)
	rerr := s.reverseCharge(ctx, c.result.GatewayTxID, c.amountCents, saleID, actor, cause, terminalRefundKey(comp))
	if rerr != nil {
		// the reversal's outcome is unknown or failed: the compensation
		// intent stays pending_at_device, so the sweep files its manager item
		return rerr
	}
	s.terminalRefundVerdict(ctx, comp, true, c.result.GatewayTxID)
	if _, err := s.terminalLedger.MarkSpent(context.WithoutCancel(ctx), leg.ID); err != nil {
		s.logger.Error("the reversed terminal leg was not marked spent", "intent_id", leg.ID, "error", err)
	}
	return nil
}

// SweepTerminalIntents is the sweep act: the worker or the guarded route
// runs it, and it turns intents past their lease into lost, filing one
// manager item per dispatched leg (a claim that never reached the device is
// simply closed). The sweep reaches the caller's branch wall: from an HTTP
// caller it sweeps only the caller's branches; the worker's context carries
// no branch and sweeps every branch. Audit row and log line name both counts.
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

// holdIntentReach holds a terminal row's branch to the caller's reach before
// any act on it: a caller whose context names another branch finds a missing
// row, and a caller with no branch context is held to the grants it holds.
func (s *Service) holdIntentReach(ctx context.Context, what string, branch uuid.UUID, id uuid.UUID) error {
	if held := middleware.BranchIDForQuery(ctx); held != nil && *held != branch {
		return httpx.NotFound(what + " not found")
	}
	return s.holdRecordBranch(ctx, what, &branch, id)
}

// TerminalIntentView is the desk's waiting state: where the leg stands.
type TerminalIntentView struct {
	ID            uuid.UUID `json:"id"`
	TransactionID uuid.UUID `json:"transaction_id"`
	LegKey        string    `json:"leg_key"`
	Kind          string    `json:"kind"`
	State         string    `json:"state"`
	AmountCents   int64     `json:"amount_cents"`
	ProviderRef   string    `json:"provider_ref"`
	AnswerVerdict string    `json:"answer_verdict"`
}

func viewOfIntent(in *payment.TerminalIntent) *TerminalIntentView {
	return &TerminalIntentView{ID: in.ID, TransactionID: in.TransactionID, LegKey: in.LegKey, Kind: in.Kind,
		State: string(in.State), AmountCents: in.AmountCents, ProviderRef: in.ProviderRef, AnswerVerdict: in.AnswerVerdict}
}

// GetTerminalIntent reads one intent for the desk's waiting state, held to
// the caller's branch wall.
func (s *Service) GetTerminalIntent(ctx context.Context, id uuid.UUID) (*TerminalIntentView, error) {
	if !s.terminalWired() {
		return nil, errTerminalNotConfigured
	}
	in, err := s.terminalLedger.Get(ctx, id)
	if err != nil {
		return nil, httpx.NotFound("terminal intent not found")
	}
	if err := s.holdIntentReach(ctx, "terminal intent", in.BranchID, id); err != nil {
		return nil, err
	}
	return viewOfIntent(in), nil
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
	if err := s.holdIntentReach(ctx, "terminal intent", in.BranchID, id); err != nil {
		return nil, err
	}
	switch in.State {
	case payment.TerminalClaimed:
		if err := s.terminalLedger.CloseClaimedIntent(ctx, id); err != nil {
			if errors.Is(err, payment.ErrTerminalIntentConflict) {
				return nil, conflict("terminal_leg_in_flight",
					"the terminal took the card leg while it was cancelled: read its state again")
			}
			return nil, err
		}
		fresh, err := s.terminalLedger.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.terminal_intent.cancelled",
				EntityType: "pos_transaction", EntityID: in.TransactionID, UserID: actor,
				Changes: map[string]any{"intent_id": in.ID, "kind": in.Kind, "amount_cents": in.AmountCents}}); err != nil {
				return nil, fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		return viewOfIntent(fresh), nil
	case payment.TerminalPendingAtDevice:
		return nil, conflict("terminal_at_device",
			"the terminal has the card leg: a cancel cannot recall it. The money may still move; the sweep files the manager item once the lease runs out")
	case payment.TerminalAuthorized:
		if in.SpentAt == nil {
			return nil, conflict("terminal_authorized",
				"the terminal authorized this card leg and the sale has not used it: complete the sale with that card tender")
		}
	}
	return nil, conflict("terminal_intent_settled",
		fmt.Sprintf("the terminal intent is %s: it has already been settled", in.State))
}

// TerminalLostItemView is one manager item of the lost answer queue.
type TerminalLostItemView struct {
	ID              uuid.UUID `json:"id"`
	IntentID        uuid.UUID `json:"intent_id"`
	BranchID        uuid.UUID `json:"branch_id"`
	RegisterID      string    `json:"register_id"`
	TransactionID   uuid.UUID `json:"transaction_id"`
	Kind            string    `json:"kind"`
	AmountCents     int64     `json:"amount_cents"`
	DeviceID        string    `json:"device_id"`
	ProviderRef     string    `json:"provider_ref"`
	State           string    `json:"state"`
	LateVerdict     string    `json:"late_verdict"`
	LateProviderRef string    `json:"late_provider_ref"`
}

func viewOfItem(it *payment.TerminalLostItem) TerminalLostItemView {
	return TerminalLostItemView{ID: it.ID, IntentID: it.IntentID, BranchID: it.BranchID, RegisterID: it.RegisterID,
		TransactionID: it.TransactionID, Kind: it.Kind, AmountCents: it.AmountCents, DeviceID: it.DeviceID,
		ProviderRef: it.ProviderRef, State: it.State, LateVerdict: it.LateVerdict, LateProviderRef: it.LateProviderRef}
}

// ListTerminalLostItems answers the open manager items of the lost answer
// queue, held to the caller's branch wall in the query itself: with a context
// branch only that branch's rows, with none and a user only the branches the
// user is granted.
func (s *Service) ListTerminalLostItems(ctx context.Context) ([]TerminalLostItemView, error) {
	if !s.terminalWired() {
		return nil, errTerminalNotConfigured
	}
	items, err := s.terminalLedger.OpenLostItems(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]TerminalLostItemView, 0, len(items))
	for i := range items {
		out = append(out, viewOfItem(&items[i]))
	}
	return out, nil
}

// errTerminalNotConfigured is the refusal of the terminal routes when the
// counter runs no terminal.
var errTerminalNotConfigured = httpx.Unavailable("this counter runs no card terminal")

// reversalFailure is the 502 of a charge the terminal took and the gateway
// could not put back.
func reversalFailure(ref string) *httpx.Error {
	return &httpx.Error{Status: http.StatusBadGateway, Code: httpx.CodeChargeNotReversed, Operator: true,
		Message: fmt.Sprintf("the terminal charged and the reversal failed; finance must reconcile terminal reference %s", ref)}
}

// derefUUID answers the pointed id or the nil uuid.
func derefUUID(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
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
