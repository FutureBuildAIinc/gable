// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// CP-15: a surcharged charge that reaches a manager item keeps its fee. An
// approval the driver holds keeps what the card was charged above the leg on
// the leg, and a late answer keeps the fee it carried, so the manager's
// authorized resolution books (or puts back) the money that moved.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/google/uuid"
)

// holdingSink records the answers landed and the figures held.
type holdingSink struct {
	recordingSink
	hmu  sync.Mutex
	held map[uuid.UUID]int64
}

func (s *holdingSink) HoldDeviceAnswer(_ context.Context, intentID uuid.UUID, feeCents int64) error {
	s.hmu.Lock()
	defer s.hmu.Unlock()
	if s.held == nil {
		s.held = map[uuid.UUID]int64{}
	}
	s.held[intentID] = feeCents
	return nil
}

func (s *holdingSink) heldFor(id uuid.UUID) (int64, bool) {
	s.hmu.Lock()
	defer s.hmu.Unlock()
	fee, ok := s.held[id]
	return fee, ok
}

// TestCP15_AHeldApprovalKeepsWhatTheCardWasChargedAboveTheLeg: the held
// answer lands nothing, and the driver keeps the figure charged above the leg
// on it; a short approval keeps nothing.
func TestCP15_AHeldApprovalKeepsWhatTheCardWasChargedAboveTheLeg(t *testing.T) {
	approved := func(amount any, fee any) map[string]any {
		r := chargeResult("A", "TRX-AMT", "00", "VISA")
		r["amount"] = amount
		if fee != nil {
			r["fee_amount"] = fee
		}
		return r
	}
	cases := []struct {
		name   string
		result map[string]any
		want   int64 // -1: nothing held
	}{
		{"fee that does not add up", approved("25.75", "0.50"), 75},
		{"fee the driver cannot read", approved("25.75", "0.755"), 75},
		{"over approval", approved("25.01", nil), 1},
		{"short approval", approved("20.00", nil), -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newRunFake(t)
			fs.fake.pollStep = endStep("succeeded", tc.result)
			sink := &holdingSink{}
			term := newSinkDriver(fs, sink, true)
			id := uuid.New()
			order := "sale-" + uuid.NewString() + "-card-0"
			if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
				IntentID: id, Kind: payment.TerminalKindSale, AmountCents: 2500, Currency: "USD",
				RegisterID: "REG-1", DeviceID: "HSN-TESTSTUB", IdempotencyKey: order}); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			requireNoAnswerAfter(t, fs, &sink.recordingSink, order, 2)
			got, ok := sink.heldFor(id)
			if tc.want < 0 {
				if ok {
					t.Fatalf("a short approval held %d cents", got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Fatalf("held %d (%t), wanted %d cents charged above the leg", got, ok, tc.want)
			}
		})
	}
	t.Run("the lookup before the dispatch", func(t *testing.T) {
		fs := newRunFake(t)
		sink := &holdingSink{}
		term := newSinkDriver(fs, sink, true)
		id := uuid.New()
		order := "sale-" + uuid.NewString() + "-card-0"
		fs.fake.mu.Lock()
		fs.fake.reporting[order] = map[string]any{"order_id": order, "trans_id": "TRX-ROW", "result": "A",
			"auth_code": "OK11", "card_number": "XXXXXXXXXXXX4242", "amount": 2575, "resp_text": "APPROVED"}
		fs.fake.mu.Unlock()
		if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
			IntentID: id, Kind: payment.TerminalKindSale, AmountCents: 2500, Currency: "USD",
			RegisterID: "REG-1", DeviceID: "HSN-TESTSTUB", IdempotencyKey: order}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if got, ok := sink.heldFor(id); ok {
				if got != 75 {
					t.Fatalf("held %d, wanted 75", got)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the lookup's approval above the leg held nothing")
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

// TestCP15_TheLedgerHoldsTheFeeOnAnUnansweredLeg: the held figure lands on a
// lost leg.
func TestCP15_TheLedgerHoldsTheFeeOnAnUnansweredLeg(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in, _ := lostItemOf(t, l, sale, "devref-held")
	if err := l.HoldDeviceAnswer(ctx, in.ID, 75); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if fresh, _ := l.Get(ctx, in.ID); fresh.AnswerFeeCents != 75 {
		t.Fatalf("the lost leg holds a fee of %d, wanted 75", fresh.AnswerFeeCents)
	}
}

// TestCP15_AHeldFeeNeverRewritesAnAnsweredLeg: the answer's own fee stands.
func TestCP15_AHeldFeeNeverRewritesAnAnsweredLeg(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	answered := claimIntent(t, l, sale, time.Minute)
	if ok, err := l.MarkDispatched(ctx, answered.ID, "devref-answered", time.Now().Add(time.Minute)); err != nil || !ok {
		t.Fatalf("dispatch: %t %v", ok, err)
	}
	if _, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: answered.ID, ProviderRef: "devref-answered",
		Verdict: "authorized", FeeCents: 30}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if err := l.HoldDeviceAnswer(ctx, answered.ID, 75); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if fresh, _ := l.Get(ctx, answered.ID); fresh.AnswerFeeCents != 30 {
		t.Fatalf("the answered leg's fee became %d, wanted the answer's 30", fresh.AnswerFeeCents)
	}
}

// TestCP15_ALateAnswerKeepsItsFeeOnTheLeg: the answer after the sweep is
// queued for the manager with its fee kept on the leg.
func TestCP15_ALateAnswerKeepsItsFeeOnTheLeg(t *testing.T) {
	l, sale := newTerminalFixture(t)
	ctx := context.Background()
	in, _ := lostItemOf(t, l, sale, "")
	out, err := l.ApplyDeviceAnswer(ctx, payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "devref-late-fee",
		Verdict: "authorized", FeeCents: 75})
	if err != nil || out != payment.AnswerLateQueued {
		t.Fatalf("the late answer = %s %v, wanted late_queued", out, err)
	}
	if fresh, _ := l.Get(ctx, in.ID); fresh.AnswerFeeCents != 75 || fresh.State != payment.TerminalLost {
		t.Fatalf("the leg is %s with a fee of %d, wanted lost with the late answer's 75", fresh.State, fresh.AnswerFeeCents)
	}
}
