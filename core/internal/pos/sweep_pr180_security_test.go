// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// PR 180 sweep: the terminal's own recorded device reference is the authority
// on a lost item, not the manager's typed string. A resolution that names a
// different reference is refused before any gateway call, and an empty or
// matching submitted reference settles under the recorded one.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/google/uuid"
)

// conflictAnswerItem completes a card sale the device declines, then delivers
// a conflicting authorized answer that records the device's own reference and
// files an open manager item. It answers the intent and the item.
func conflictAnswerItem(t *testing.T, f *fixture, recordedRef string) (intentID, itemID uuid.UUID) {
	t.Helper()
	saleID := f.openSale(t, nil)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusConflict {
		t.Fatalf("declined completion = %d, wanted 409: %s", r.status, r.raw)
	}
	intentID, state, _ := f.terminalIntentOf(t, saleID)
	if state != "declined" {
		t.Fatalf("the declined leg is %s, wanted declined", state)
	}
	ledger := payment.NewTerminalLedger(f.db)
	if out, err := ledger.ApplyDeviceAnswer(context.Background(), payment.TerminalAnswer{
		IntentID: intentID, ProviderRef: recordedRef, Verdict: "authorized",
		Brand: "VISA", Last4: "4242", AuthCode: "AUTH1",
	}); err != nil || out != payment.AnswerConflictQueued {
		t.Fatalf("conflicting authorized answer = %s %v, wanted conflict_queued", out, err)
	}
	return intentID, f.itemOfSale(t, saleID)
}

// TestSweepPR180ResolveRefusesMismatchedReferenceAndUsesRecordedDeviceRef: a
// lost item that carries the device's recorded reference cannot be settled
// under a different manager typed reference: the resolve refuses with 409
// reference_mismatch naming both references, calls no gateway, and leaves the
// item open; an empty or matching submitted reference settles under the
// recorded one.
func TestSweepPR180ResolveRefusesMismatchedReferenceAndUsesRecordedDeviceRef(t *testing.T) {
	gw := &fakeGateway{}
	f := terminalFixture(t, payment.SimDecline, gw)

	// A mismatched reference is refused before anything moves.
	intentA, itemA := conflictAnswerItem(t, f, "tx-actual-customer")
	r := f.resolveItem(itemA, "authorized", "tx-other-customer")
	requireBlocker(t, r, "reference_mismatch")
	if !strings.Contains(string(r.raw), "tx-actual-customer") {
		t.Fatalf("the refusal does not name the recorded reference: %s", r.raw)
	}
	if !strings.Contains(string(r.raw), "tx-other-customer") {
		t.Fatalf("the refusal does not name the submitted reference: %s", r.raw)
	}
	if len(gw.voids) != 0 || len(gw.refunds) != 0 {
		t.Fatalf("the refused resolve called the gateway: voids %v refunds %v", gw.voids, gw.refunds)
	}
	if got := itemStateOf(t, f, itemA); got != "open" {
		t.Fatalf("the refused item is %s, wanted open", got)
	}
	if state, _, ref := f.intentFacts(t, intentA); state != "declined" || ref != "" {
		t.Fatalf("the intent moved to %s under %q, wanted declined with no answer reference", state, ref)
	}

	// A matching reference settles under the recorded one.
	intentB, itemB := conflictAnswerItem(t, f, "tx-actual-customer")
	if r := f.resolveItem(itemB, "authorized", "tx-actual-customer"); r.status != http.StatusOK {
		t.Fatalf("the matching resolve = %d: %s", r.status, r.raw)
	}
	if state, spent, ref := f.intentFacts(t, intentB); state != "authorized" || spent || ref != "tx-actual-customer" {
		t.Fatalf("the leg after the matching resolve is %s spent=%t ref=%q, wanted authorized unspent under the recorded reference", state, spent, ref)
	}

	// An empty submitted reference settles under the recorded one too.
	intentC, itemC := conflictAnswerItem(t, f, "tx-actual-customer")
	if r := f.resolveItem(itemC, "authorized", ""); r.status != http.StatusOK {
		t.Fatalf("the empty reference resolve = %d: %s", r.status, r.raw)
	}
	if state, spent, ref := f.intentFacts(t, intentC); state != "authorized" || spent || ref != "tx-actual-customer" {
		t.Fatalf("the leg after the empty resolve is %s spent=%t ref=%q, wanted authorized unspent under the recorded reference", state, spent, ref)
	}
	f.assertARInvariants(t)
}
