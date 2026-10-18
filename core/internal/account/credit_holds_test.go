// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The credit holds (M25): the sweep in both modes, the manual hold and its
// idempotency, the time-bounded override, the release, and the credit limit
// change workflow. Every executed act is checked against the books it moves:
// the customers.credit_hold flag and, after every money act, the world's own
// invariants.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

func (w *arWorld) setCreditLimit(cents int64) {
	w.t.Helper()
	w.exec(`UPDATE customers SET credit_limit = $2::bigint::numeric / 100 WHERE id = $1`, w.customerID, cents)
}

func (w *arWorld) creditHoldRows() int64 {
	return w.queryInt(`SELECT count(*) FROM credit_holds WHERE customer_id = $1`, w.customerID)
}

func (w *arWorld) creditFlag() bool {
	w.t.Helper()
	var flag bool
	if err := w.db.Pool.QueryRow(context.Background(),
		`SELECT credit_hold FROM customers WHERE id = $1`, w.customerID).Scan(&flag); err != nil {
		w.t.Fatalf("read the credit hold flag: %v", err)
	}
	return flag
}

func (w *arWorld) standingHold() *account.CreditHold {
	w.t.Helper()
	holds, err := w.svc.ListCreditHolds(context.Background(), account.CreditHoldFilter{CustomerID: &w.customerID, Limit: 10})
	if err != nil {
		w.t.Fatalf("list the credit holds: %v", err)
	}
	for i := range holds {
		if holds[i].Status == "active" || holds[i].Status == "overridden" {
			return &holds[i]
		}
	}
	return nil
}

// sweepC is the sweep narrowed to the world's own customer, so a shared test
// database's other accounts never enter the answer.
func sweepC(w *arWorld, dryRun bool) *account.HoldEvaluation {
	w.t.Helper()
	ev, err := w.svc.EvaluateHolds(context.Background(), dryRun, []uuid.UUID{w.customerID}, "u-test", "")
	if err != nil {
		w.t.Fatalf("evaluate the holds: %v", err)
	}
	return ev
}

// The rule itself lives in credit_hold_rules_internal_test.go (the pure
// functions are unexported); this file holds the database half: the sweep in
// both modes, the manual hold and its idempotency, the override window, the
// release, and the credit limit change workflow.

// TestHoldSweepDryRun previews the holds and writes nothing.
func TestHoldSweepDryRun(t *testing.T) {
	w := newARWorld(t)
	w.setCreditLimit(10_000)
	w.invoice(50_000, daysFromNow(-40)) // past its limit and 10 days past due

	ev := sweepC(w, true)
	if !ev.DryRun || ev.Placed != 0 || ev.Released != 0 {
		t.Fatalf("a dry run reported %+v, want a preview that placed nothing", ev)
	}
	if ev.Evaluated != 1 {
		t.Fatalf("evaluated %d accounts, want the world's one", ev.Evaluated)
	}
	if len(ev.WouldPlace) != 1 || ev.WouldPlace[0].HoldType != "over_limit" {
		t.Fatalf("would place %+v, want one over_limit hold", ev.WouldPlace)
	}
	if ev.WouldPlace[0].CustomerID != w.customerID || ev.WouldPlace[0].Reason == "" {
		t.Fatalf("the placement names the wrong account or no reason: %+v", ev.WouldPlace[0])
	}
	if w.creditHoldRows() != 0 || w.creditFlag() {
		t.Fatal("a dry run wrote a hold or moved the flag")
	}
}

// TestHoldSweepPlacesAndLifts converges the hold set: places on the breach,
// never doubles, lifts when the trigger clears, and keeps the flag true to
// the books.
func TestHoldSweepPlacesAndLifts(t *testing.T) {
	w := newARWorld(t)
	w.setCreditLimit(10_000)
	w.invoice(50_000, daysFromNow(-40))

	if ev := sweepC(w, false); ev.Placed != 1 || len(ev.WouldPlace) != 1 || ev.WouldPlace[0].HoldID == "" {
		t.Fatalf("the executed sweep placed %+v, want one hold with its id", ev.WouldPlace)
	}
	if w.creditHoldRows() != 1 || !w.creditFlag() {
		t.Fatalf("after the sweep: %d holds, flag %v", w.creditHoldRows(), w.creditFlag())
	}
	// The second run converges: nothing new to place.
	if ev := sweepC(w, false); ev.Placed != 0 || len(ev.WouldPlace) != 0 {
		t.Fatalf("the second sweep placed %+v, want nothing", ev.WouldPlace)
	}
	if w.creditHoldRows() != 1 {
		t.Fatalf("the second sweep left %d holds, want one", w.creditHoldRows())
	}
	// Raising the limit clears the trigger; the sweep lifts its own hold.
	w.setCreditLimit(1_000_000)
	if ev := sweepC(w, true); len(ev.WouldRelease) != 1 {
		t.Fatalf("the dry sweep would release %+v, want the one hold", ev.WouldRelease)
	}
	if ev := sweepC(w, false); ev.Released != 1 {
		t.Fatalf("the sweep released %d, want one", ev.Released)
	}
	if h := w.standingHold(); h != nil {
		t.Fatalf("the hold still stands: %+v", h)
	}
	if w.creditFlag() {
		t.Fatal("the flag stayed true after the lift")
	}
}

// TestHoldSweepSparesManualAndOverriddenHolds never lifts a hold a person
// placed or one carrying a manager's override, whatever the books say.
func TestHoldSweepSparesManualAndOverriddenHolds(t *testing.T) {
	w := newARWorld(t)
	w.setCreditLimit(10_000)
	w.invoice(50_000, daysFromNow(-40))

	hold, _, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "the owner asked", Actor: "u-manager", Role: "finance"})
	if err != nil || hold == nil {
		t.Fatalf("place the manual hold: %v", err)
	}
	if ev := sweepC(w, true); len(ev.WouldRelease) != 0 {
		t.Fatalf("the sweep would lift a person's hold: %+v", ev.WouldRelease)
	}
	if ev := sweepC(w, false); ev.Released != 0 {
		t.Fatalf("the sweep lifted %d person's holds", ev.Released)
	}
	// An overridden automatic hold stands its window.
	w.exec(`DELETE FROM credit_holds WHERE customer_id = $1`, w.customerID)
	if ev := sweepC(w, false); ev.Placed != 1 {
		t.Fatalf("the sweep placed %d, want the automatic hold", ev.Placed)
	}
	until := time.Now().Add(48 * time.Hour)
	if _, err := w.svc.OverrideHold(context.Background(), w.standingHold().ID,
		account.HoldActIn{Reason: "promised by Friday", OverrideUntil: &until, Actor: "u-manager", Role: "finance"}); err != nil {
		t.Fatalf("override the hold: %v", err)
	}
	w.setCreditLimit(1_000_000)
	if ev := sweepC(w, true); len(ev.WouldRelease) != 0 {
		t.Fatalf("the sweep would lift an overridden hold: %+v", ev.WouldRelease)
	}
	if ev := sweepC(w, false); ev.Released != 0 {
		t.Fatalf("the sweep lifted %d overridden holds", ev.Released)
	}
}

// TestManualHoldIsIdempotent answers the standing hold twice and never
// writes a second one.
func TestManualHoldIsIdempotent(t *testing.T) {
	w := newARWorld(t)
	first, placed, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "credit office", Actor: "u-test", Role: "finance"})
	if err != nil || !placed || first == nil {
		t.Fatalf("place the hold: placed %v, %v", placed, err)
	}
	if first.Status != "active" || first.HoldType != "manual" || first.PlacedBy == nil || *first.PlacedBy != "u-test" {
		t.Fatalf("the placed hold reads %+v", first)
	}
	if !w.creditFlag() {
		t.Fatal("the flag is false behind a standing hold")
	}
	second, placed, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "again", Actor: "u-test", Role: "finance"})
	if err != nil {
		t.Fatalf("place again: %v", err)
	}
	if placed {
		t.Fatal("the second place claims to have placed")
	}
	if second == nil || second.ID != first.ID {
		t.Fatalf("the second place answered %+v, want the first hold %s", second, first.ID)
	}
	if w.creditHoldRows() != 1 {
		t.Fatalf("%d holds stand, want one", w.creditHoldRows())
	}
	// A reason is not optional, and an unknown kind is refused.
	if _, _, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID, Actor: "u-test", Role: "finance"}); err == nil {
		t.Fatal("a hold without a reason was placed")
	}
	if _, _, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "x", HoldType: "mystery", Actor: "u-test", Role: "finance"}); err == nil {
		t.Fatal("a hold of an unknown kind was placed")
	}
}

// TestHoldOverrideAndRelease walks a hold through its window and out the
// other side: the override stamps who and until, refuses a window in the
// past, and the release ends the hold with its reason and the flag.
func TestHoldOverrideAndRelease(t *testing.T) {
	w := newARWorld(t)
	hold, placed, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "past due", Actor: "u-test", Role: "finance"})
	if err != nil || !placed {
		t.Fatalf("place: %v", err)
	}
	// A window that is already shut is refused.
	past := time.Now().Add(-time.Hour)
	if _, err := w.svc.OverrideHold(context.Background(), hold.ID,
		account.HoldActIn{Reason: "no", OverrideUntil: &past, Actor: "u-manager", Role: "finance"}); err == nil {
		t.Fatal("an override into the past was taken")
	}
	if _, err := w.svc.OverrideHold(context.Background(), hold.ID,
		account.HoldActIn{OverrideUntil: &past, Actor: "u-manager", Role: "finance"}); err == nil {
		t.Fatal("an override without a reason was taken")
	}
	until := time.Now().Add(48 * time.Hour)
	maxOrder := int64(250_000)
	overridden, err := w.svc.OverrideHold(context.Background(), hold.ID,
		account.HoldActIn{Reason: "the cheque is in the mail", OverrideUntil: &until, MaxOrderCents: &maxOrder,
			Actor: "u-manager", Role: "finance"})
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if overridden.Status != "overridden" || overridden.OverrideUntil == nil || overridden.OverrideBy == nil ||
		overridden.OverrideReason == nil || overridden.MaxOrderCents == nil || *overridden.MaxOrderCents != 250_000 {
		t.Fatalf("the overridden hold reads %+v", overridden)
	}
	// The live window is what the override buys: the counter and the order
	// paths see the account as open while it stands.
	if w.creditFlag() {
		t.Fatal("the flag stayed true behind a live override")
	}
	// The release ends it, whatever state it stands in.
	released, err := w.svc.ReleaseHold(context.Background(), hold.ID,
		account.HoldActIn{Reason: "paid in full", Actor: "u-test", Role: "finance"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.Status != "released" || released.ReleasedAt == nil || released.ReleaseReason == nil ||
		released.OverrideUntil != nil {
		t.Fatalf("the released hold reads %+v", released)
	}
	if w.creditFlag() || w.standingHold() != nil {
		t.Fatal("the hold or its flag outlived the release")
	}
	if _, err := w.svc.ReleaseHold(context.Background(), hold.ID,
		account.HoldActIn{Reason: "again", Actor: "u-test", Role: "finance"}); err == nil {
		t.Fatal("a released hold was released again")
	}
	// A role outside the finance roles is refused when the caller names one.
	if _, _, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "x", Actor: "u-test", Role: "sales"}); err == nil {
		t.Fatal("a sales role placed a hold")
	}
}

// TestCreditLimitChangeWorkflow records a pending change, applies an approved
// one to the customer, and answers the history newest first.
func TestCreditLimitChangeWorkflow(t *testing.T) {
	w := newARWorld(t)
	w.setCreditLimit(50_000)

	pending, err := w.svc.RequestCreditLimitChange(context.Background(), w.customerID, account.CreditLimitChangeIn{
		NewLimitCents: 75_000, Reason: "asked for more room", Actor: "u-clerk"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if pending.Status != "pending" || pending.OldLimitCents != 50_000 || pending.NewLimitCents != 75_000 ||
		pending.DecidedAt != nil || pending.RequestedBy == nil || *pending.RequestedBy != "u-clerk" {
		t.Fatalf("the pending change reads %+v", pending)
	}
	if got := w.queryInt(`SELECT ROUND(credit_limit * 100)::bigint FROM customers WHERE id = $1`, w.customerID); got != 50_000 {
		t.Fatalf("a pending request moved the limit to %d", got)
	}
	approved, err := w.svc.RequestCreditLimitChange(context.Background(), w.customerID, account.CreditLimitChangeIn{
		NewLimitCents: 80_000, Reason: "approved by the office", Approve: true, Actor: "u-finance"})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != "approved" || approved.ApprovedBy == nil || *approved.ApprovedBy != "u-finance" ||
		approved.DecidedAt == nil {
		t.Fatalf("the approved change reads %+v", approved)
	}
	if got := w.queryInt(`SELECT ROUND(credit_limit * 100)::bigint FROM customers WHERE id = $1`, w.customerID); got != 80_000 {
		t.Fatalf("the limit did not move: %d", got)
	}
	history, err := w.svc.ListCreditLimitChanges(context.Background(), account.CreditLimitChangeFilter{
		CustomerID: w.customerID, Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("%d changes in the history, want 2", len(history))
	}
	if history[0].NewLimitCents != 80_000 || history[1].Status != "pending" {
		t.Fatalf("the history is not newest first with the right rows: %+v", history)
	}
	// The refusals: a negative limit, a missing reason, and a caller whose
	// role is not one of the finance roles.
	if _, err := w.svc.RequestCreditLimitChange(context.Background(), w.customerID, account.CreditLimitChangeIn{
		NewLimitCents: -1, Reason: "x", Actor: "u-test"}); err == nil {
		t.Fatal("a negative limit was recorded")
	}
	if _, err := w.svc.RequestCreditLimitChange(context.Background(), w.customerID, account.CreditLimitChangeIn{
		NewLimitCents: 100, Actor: "u-test"}); err == nil {
		t.Fatal("a change without a reason was recorded")
	}
	if _, err := w.svc.RequestCreditLimitChange(context.Background(), w.customerID, account.CreditLimitChangeIn{
		NewLimitCents: 100, Reason: "x", Actor: "u-test", Role: "sales"}); err == nil {
		t.Fatal("a sales role changed a limit")
	}
}

// The sweep's accounts summary carries what the controller reads beside a
// hold: the limit, the balance and the past due figure.
func TestHoldListCarriesCreditStanding(t *testing.T) {
	w := newARWorld(t)
	w.setCreditLimit(20_000)
	w.invoice(30_000, daysFromNow(-40)) // 10 days past due and over the limit

	if _, _, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "watch", Actor: "u-test", Role: "finance"}); err != nil {
		t.Fatalf("place: %v", err)
	}
	holds, err := w.svc.ListCreditHolds(context.Background(), account.CreditHoldFilter{CustomerID: &w.customerID, Limit: 10})
	if err != nil || len(holds) != 1 {
		t.Fatalf("list: %d holds, %v", len(holds), err)
	}
	st := holds[0].Credit
	if st == nil || st.CreditLimitCents == nil || *st.CreditLimitCents != 20_000 ||
		st.CurrentBalanceCents != 30_000 || st.PastDueCents != 30_000 {
		t.Fatalf("the standing reads %+v", st)
	}
}

// The list of a released hold keeps its history and its summary, and the
// standing list drops it.
func TestReleasedHoldListKeepsHistory(t *testing.T) {
	w := newARWorld(t)
	hold, placed, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "temp", Actor: "u-test", Role: "finance"})
	if err != nil || !placed {
		t.Fatalf("place: %v", err)
	}
	if _, err := w.svc.ReleaseHold(context.Background(), hold.ID,
		account.HoldActIn{Reason: "ok now", Actor: "u-test", Role: "finance"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	standing, err := w.svc.ListCreditHolds(context.Background(), account.CreditHoldFilter{CustomerID: &w.customerID, Limit: 10})
	if err != nil || len(standing) != 0 {
		t.Fatalf("the standing list answered %d holds, want none", len(standing))
	}
	all, err := w.svc.ListCreditHolds(context.Background(), account.CreditHoldFilter{
		CustomerID: &w.customerID, Status: "all", Limit: 10})
	if err != nil || len(all) != 1 {
		t.Fatalf("the all list answered %d holds, %v", len(all), err)
	}
	if all[0].Status != "released" {
		t.Fatalf("the hold reads %q, want released", all[0].Status)
	}
}

// TestCreditLimitChangeOnAFreshCustomer records and approves a change on a
// customer that names no limit at all (credit_limit NULL): the old limit
// reads as zero, never a scan failure.
func TestCreditLimitChangeOnAFreshCustomer(t *testing.T) {
	w := newARWorld(t)
	change, err := w.svc.RequestCreditLimitChange(context.Background(), w.customerID, account.CreditLimitChangeIn{
		NewLimitCents: 80_000, Reason: "first limit", Approve: true, Actor: "u-finance"})
	if err != nil {
		t.Fatalf("approve a change on a customer with no limit: %v", err)
	}
	if change.OldLimitCents != 0 || change.Status != "approved" {
		t.Fatalf("the change reads %+v, want old 0 approved", change)
	}
	if got := w.queryInt(`SELECT ROUND(credit_limit * 100)::bigint FROM customers WHERE id = $1`, w.customerID); got != 80_000 {
		t.Fatalf("the limit did not move: %d", got)
	}
}

// TestHoldActsWorkWithoutACallerName: a machine key, the dev mode and the
// automated sweep carry no user claims, so the acts that must name an actor
// stamp system (or sweep) instead of failing a check constraint.
func TestHoldActsWorkWithoutACallerName(t *testing.T) {
	w := newARWorld(t)
	hold, placed, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID, Reason: "watch"})
	if err != nil || !placed {
		t.Fatalf("place with no actor: %v", err)
	}
	until := time.Now().Add(48 * time.Hour)
	overridden, err := w.svc.OverrideHold(context.Background(), hold.ID,
		account.HoldActIn{Reason: "the window", OverrideUntil: &until})
	if err != nil {
		t.Fatalf("override with no actor: %v", err)
	}
	if overridden.OverrideBy == nil || *overridden.OverrideBy == "" {
		t.Fatalf("the override names no actor: %+v", overridden)
	}
	released, err := w.svc.ReleaseHold(context.Background(), hold.ID, account.HoldActIn{Reason: "done"})
	if err != nil {
		t.Fatalf("release with no actor: %v", err)
	}
	if released.ReleasedBy == nil || *released.ReleasedBy == "" {
		t.Fatalf("the release names no actor: %+v", released)
	}
	if _, err := w.svc.RequestCreditLimitChange(context.Background(), w.customerID, account.CreditLimitChangeIn{
		NewLimitCents: 1_000, Reason: "ok", Approve: true}); err != nil {
		t.Fatalf("approve a limit change with no actor: %v", err)
	}
	// And the sweep itself, which never carries a caller's name.
	w.setCreditLimit(10_000)
	w.invoice(50_000, daysFromNow(-40))
	if ev := sweepC(w, false); ev.Placed != 1 {
		t.Fatalf("the sweep placed %d, want one", ev.Placed)
	}
	h := w.standingHold()
	if h == nil {
		t.Fatal("the sweep's hold is missing")
	}
	w.setCreditLimit(1_000_000)
	if ev := sweepC(w, false); ev.Released != 1 {
		t.Fatalf("the sweep released %d, want one", ev.Released)
	}
	all, err := w.svc.ListCreditHolds(context.Background(), account.CreditHoldFilter{
		CustomerID: &w.customerID, Status: "released", Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, h := range all {
		if h.ReleasedBy == nil || *h.ReleasedBy == "" || h.ReleaseReason == nil {
			t.Fatalf("a released hold names no actor or reason: %+v", h)
		}
	}
}

// TestOverrideWindowExpiry: a live override lets orders through (the flag
// reads false), an elapsed one blocks again, and the sweep expires it back
// to active and lifts it once its trigger is gone.
func TestOverrideWindowExpiry(t *testing.T) {
	w := newARWorld(t)
	w.setCreditLimit(10_000)
	w.invoice(50_000, daysFromNow(-40)) // over the limit, and it stays there

	if ev := sweepC(w, false); ev.Placed != 1 {
		t.Fatalf("the sweep placed %d, want one", ev.Placed)
	}
	hold := w.standingHold()
	if hold == nil || hold.Status != "active" {
		t.Fatalf("the automatic hold reads %+v", hold)
	}
	// The window buys an hour; then the clock moves past it.
	until := time.Now().Add(time.Hour)
	overridden, err := w.svc.OverrideHold(context.Background(), hold.ID,
		account.HoldActIn{Reason: "promised", OverrideUntil: &until, Actor: "u-manager", Role: "finance"})
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if overridden.Status != "overridden" {
		t.Fatalf("the overridden hold reads %q", overridden.Status)
	}
	if w.creditFlag() {
		t.Fatal("the flag is true behind a live override: the window lets orders through")
	}
	// The sweep does not lift a live override's hold even if the books
	// cleared (they have not here, but the plan must say nothing to lift).
	if ev := sweepC(w, true); len(ev.WouldRelease) != 0 {
		t.Fatalf("the sweep would lift a hold under a live override: %+v", ev.WouldRelease)
	}
	// The window elapses. A dry run still plans nothing (it writes
	// nothing); the live sweep expires the override back to active, and the
	// account blocks again.
	w.svc.WithClockForTest(func() time.Time { return time.Now().Add(2 * time.Hour) })
	if ev := sweepC(w, true); len(ev.WouldRelease) != 0 {
		t.Fatalf("a breached account is not a lift candidate: %+v", ev.WouldRelease)
	}
	if ev := sweepC(w, false); ev.Placed != 0 || ev.Released != 0 {
		t.Fatalf("the sweep moved nothing: %+v", ev)
	}
	expired := w.standingHold()
	if expired == nil || expired.Status != "active" || expired.OverrideUntil != nil {
		t.Fatalf("the elapsed override did not expire back to active: %+v", expired)
	}
	if !w.creditFlag() {
		t.Fatal("the flag stayed false after the override window elapsed")
	}
	// With the trigger gone the same sweep lifts the hold it placed.
	w.setCreditLimit(1_000_000)
	if ev := sweepC(w, false); ev.Released != 1 {
		t.Fatalf("the sweep released %d, want the expired hold", ev.Released)
	}
	if h := w.standingHold(); h != nil {
		t.Fatalf("the hold outlived the lift: %+v", h)
	}
	if w.creditFlag() {
		t.Fatal("the flag stayed true after the lift")
	}
}

// TestHoldRoutesCarryTheCustomerWall: a caller pinned to a branch the
// customer does not hold finds the hold missing, on the list and on the acts
// alike, and the wide caller still sees it.
func TestHoldRoutesCarryTheCustomerWall(t *testing.T) {
	w := newARWorld(t)
	hold, placed, err := w.svc.PlaceHold(context.Background(), account.PlaceHoldIn{CustomerID: w.customerID,
		Reason: "watch", Actor: "u-test", Role: "finance"})
	if err != nil || !placed {
		t.Fatalf("place: %v", err)
	}
	other := uuid.New()
	w.exec(`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, other, "wb-"+other.String()[:8])
	t.Cleanup(func() {
		_, _ = w.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, other)
	})
	pinned := branchctx.With(context.Background(), &branchctx.Context{BranchID: &other})

	holds, err := w.svc.ListCreditHolds(pinned, account.CreditHoldFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list pinned: %v", err)
	}
	if len(holds) != 0 {
		t.Fatalf("a caller pinned elsewhere sees %d holds, want none", len(holds))
	}
	if _, err := w.svc.ReleaseHold(pinned, hold.ID, account.HoldActIn{Reason: "x", Actor: "u-test", Role: "finance"}); !notFound(err) {
		t.Fatalf("a release pinned elsewhere answered %v, want not found", err)
	}
	until := time.Now().Add(time.Hour)
	if _, err := w.svc.OverrideHold(pinned, hold.ID,
		account.HoldActIn{Reason: "x", OverrideUntil: &until, Actor: "u-test", Role: "finance"}); !notFound(err) {
		t.Fatalf("an override pinned elsewhere answered %v, want not found", err)
	}
	// The wide caller still sees and may lift it.
	wide, err := w.svc.ListCreditHolds(context.Background(), account.CreditHoldFilter{CustomerID: &w.customerID, Limit: 10})
	if err != nil || len(wide) != 1 {
		t.Fatalf("the wide list answered %d holds, %v", len(wide), err)
	}
	if _, err := w.svc.ReleaseHold(context.Background(), hold.ID,
		account.HoldActIn{Reason: "ok now", Actor: "u-test", Role: "finance"}); err != nil {
		t.Fatalf("the wide release: %v", err)
	}
}

// notFound answers whether the error is the wire's not found.
func notFound(err error) bool {
	e, ok := err.(*httpx.Error)
	return ok && e.Status == http.StatusNotFound
}
