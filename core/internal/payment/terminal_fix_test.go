// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// Round 2 proofs for the intent ledger: an answer never lands on a claim the
// device was not reached from (F2), a move and its manager item are one
// statement (F3), a late answer is classified late and its verdict and
// reference are kept on the item, and a late authorized answer after a
// declined resolution reopens the item (F3).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// TestAnswerForClaimedIntentIsRefused: claimed means the device was never
// reached, so an answer for it is a defect to refuse loudly, never a case to
// queue as late.
func TestAnswerForClaimedIntentIsRefused(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in := claimIntent(t, l, sale, time.Minute)
	out, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-claimed", Verdict: "authorized"})
	if err == nil {
		t.Fatalf("an answer for a claimed intent was accepted as %q, wanted a refusal", out)
	}
	if !strings.Contains(err.Error(), "claimed") {
		t.Fatalf("the refusal does not name the claimed state: %v", err)
	}
	items, qerr := l.OpenLostItems(ctx, nil)
	if qerr != nil {
		t.Fatalf("queue: %v", qerr)
	}
	for _, it := range items {
		if it.IntentID == in.ID {
			t.Fatal("an answer for a claimed intent filed a manager item")
		}
	}
	fresh, _ := l.Get(ctx, in.ID)
	if fresh.State != payment.TerminalClaimed {
		t.Fatalf("the refused answer moved the claim to %s", fresh.State)
	}
}

// TestSweepIsOneStatement: the move to lost and the manager item are one
// statement. A manager item insert that fails (a constraint on one amount
// only this test uses) leaves the intent exactly where it was, never lost
// without its item.
func TestSweepIsOneStatement(t *testing.T) {
	l, sale := newTerminalFixture(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `ALTER TABLE payment_terminal_lost_items
		ADD CONSTRAINT m29_sweep_atomic_probe CHECK (amount_cents <> 7777) NOT VALID`); err != nil {
		t.Fatalf("probe constraint: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `ALTER TABLE payment_terminal_lost_items DROP CONSTRAINT IF EXISTS m29_sweep_atomic_probe`)
	})
	in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: "card-0", RegisterID: "REG-" + sale.String()[:12],
		BranchID: m29BranchOf(t, l), AmountCents: 7777, Kind: payment.TerminalKindSale,
		IdempotencyKey: "sale-" + sale.String() + "-card-0", LeaseUntil: time.Now().Add(time.Minute)}
	ctx = branchctx.With(ctx, &branchctx.Context{BranchID: &in.BranchID})
	if err := l.Claim(ctx, in); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok, err := l.MarkDispatched(ctx, in.ID, "devref-atomic", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("dispatch: %t %v", ok, err)
	}
	if _, err := l.Sweep(ctx, time.Now()); err == nil {
		t.Fatal("the sweep answered no error though its item insert was refused")
	}
	fresh, err := l.Get(ctx, in.ID)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if fresh.State != payment.TerminalPendingAtDevice {
		t.Fatalf("the failed sweep left the intent %s, wanted pending_at_device (no lost intent without its item)", fresh.State)
	}
}

// lostItemOf sweeps one dispatched leg and answers its manager item.
func lostItemOf(t *testing.T, l *payment.TerminalLedger, sale uuid.UUID, ref string) (*payment.TerminalIntent, *payment.TerminalLostItem) {
	t.Helper()
	in := claimIntent(t, l, sale, time.Minute)
	ctx := branchctx.With(context.Background(), &branchctx.Context{BranchID: &in.BranchID})
	if ok, err := l.MarkDispatched(ctx, in.ID, ref, time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("dispatch: %t %v", ok, err)
	}
	if _, err := l.Sweep(ctx, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	items, err := l.OpenLostItems(ctx, nil)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	for i := range items {
		if items[i].IntentID == in.ID {
			return in, &items[i]
		}
	}
	t.Fatal("the sweep filed no manager item")
	return nil, nil
}

// TestLateAnswerKeepsVerdictAndReferenceOnTheItem: the answer after the sweep
// is classed late and the item keeps the device's own verdict and reference.
func TestLateAnswerKeepsVerdictAndReferenceOnTheItem(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in, item := lostItemOf(t, l, sale, "")
	out, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-late-kept",
		Verdict: "authorized", Brand: "VISA", Last4: "4242", AuthCode: "LATE1"})
	if err != nil || out != payment.AnswerLateQueued {
		t.Fatalf("the late answer = %s %v, wanted late_queued", out, err)
	}
	db := testutil.RequireDB(t)
	var verdict, ref, brand, last4, auth, providerRef string
	if err := db.Pool.QueryRow(ctx, `SELECT late_verdict, late_provider_ref, late_brand, late_last4, late_auth_code, provider_ref
		FROM payment_terminal_lost_items WHERE id = $1`, item.ID).Scan(&verdict, &ref, &brand, &last4, &auth, &providerRef); err != nil {
		t.Fatalf("item reread: %v", err)
	}
	if verdict != "authorized" || ref != "devref-late-kept" || brand != "VISA" || last4 != "4242" || auth != "LATE1" {
		t.Fatalf("the item kept verdict %q ref %q brand %q last4 %q auth %q", verdict, ref, brand, last4, auth)
	}
	if providerRef != "devref-late-kept" {
		t.Fatalf("the item that carried no device reference kept %q, wanted the late answer's", providerRef)
	}
}

// TestLateAuthorizedAfterDeclinedResolutionReopens: a manager resolved the
// item declined, then the device's late answer says authorized. The item
// reopens for the manager with the late verdict and reference on it.
func TestLateAuthorizedAfterDeclinedResolutionReopens(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in, item := lostItemOf(t, l, sale, "devref-reopen")
	if ok, err := l.ResolveLostItemDeclined(ctx, item.ID, "manager", "the terminal showed no sale"); err != nil || !ok {
		t.Fatalf("declined resolution: %t %v", ok, err)
	}
	out, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-reopen", Verdict: "authorized"})
	if err != nil || out != payment.AnswerReopened {
		t.Fatalf("the late authorized answer = %s %v, wanted reopened", out, err)
	}
	fresh, err := l.LostItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("item reread: %v", err)
	}
	if fresh.State != "open" {
		t.Fatalf("the item is %s after a late authorized answer on a declined resolution, wanted open", fresh.State)
	}
	db := testutil.RequireDB(t)
	var verdict, ref string
	if err := db.Pool.QueryRow(ctx, `SELECT late_verdict, late_provider_ref FROM payment_terminal_lost_items WHERE id = $1`, item.ID).Scan(&verdict, &ref); err != nil {
		t.Fatalf("late fields: %v", err)
	}
	if verdict != "authorized" || ref != "devref-reopen" {
		t.Fatalf("the reopened item keeps verdict %q ref %q", verdict, ref)
	}
}

// TestLateDeclinedAfterAuthorizedResolutionReopens: a manager resolved the
// item authorized, then the device's late answer says declined. The item
// reopens for the manager with the contradicting verdict and reference on it.
func TestLateDeclinedAfterAuthorizedResolutionReopens(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in, item := lostItemOf(t, l, sale, "devref-flag")
	if ok, err := l.ResolveLostItemAuthorized(ctx, item.ID, "manager", "devref-flag"); err != nil || !ok {
		t.Fatalf("authorized resolution: %t %v", ok, err)
	}
	// the resolving act books the money and moves the intent with it
	if ok, err := l.AuthorizeLost(ctx, in.ID, "devref-flag", true); err != nil || !ok {
		t.Fatalf("the intent's move: %t %v", ok, err)
	}
	out, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-flag", Verdict: "declined"})
	if err != nil || out != payment.AnswerReopened {
		t.Fatalf("the contradicting late answer = %s %v, wanted reopened", out, err)
	}
	fresh, _ := l.LostItem(ctx, item.ID)
	if fresh.State != "open" {
		t.Fatalf("the contradicting answer left the item %s, wanted open", fresh.State)
	}
	db := testutil.RequireDB(t)
	var verdict string
	if err := db.Pool.QueryRow(ctx, `SELECT late_verdict FROM payment_terminal_lost_items WHERE id = $1`, item.ID).Scan(&verdict); err != nil || verdict != "declined" {
		t.Fatalf("the contradicting verdict kept on the item: %q %v", verdict, err)
	}
}
