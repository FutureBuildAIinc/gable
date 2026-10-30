// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/google/uuid"
)

// The answers the driver may and may not land: an uncertain end it cannot
// look up, and an approval for a figure other than the leg's, are never
// booked; the leg stays at the device and the sweep files the manager item.

// newSinkDriver wires the driver on the fake with a recording sink, with or
// without the reporting credential set.
func newSinkDriver(fs *runFakeServer, sink payment.AnswerSink, reporting bool) *payment.RunTerminal {
	term := payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: fs.server.URL}
	}, sink, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithPollInterval(5 * time.Millisecond).WithInteractionWindow(2 * time.Second)
	if reporting {
		term = term.WithReporting(fs.server.URL, "clientid-teststub", "clientsecret-teststub")
	}
	return term
}

// pollsServed reads how many status polls the fake served for one order.
func (f *runFakeTerminal) pollsServed(order string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pollsByOrder[order]
}

// dispatchSaleLeg dispatches one sale leg of 25.00 and answers its order id.
func dispatchSaleLeg(t *testing.T, term *payment.RunTerminal) string {
	t.Helper()
	order := "sale-" + uuid.NewString() + "-card-0"
	if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
		IntentID: uuid.New(), Kind: payment.TerminalKindSale, AmountCents: 2500, Currency: "USD",
		RegisterID: "REG-1", DeviceID: "HSN-TESTSTUB", IdempotencyKey: order}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return order
}

// requireNoAnswerAfter waits until the fake served the polls that end the
// interaction, then well past the driver's delivery timer, and fails on any
// answer the driver landed.
func requireNoAnswerAfter(t *testing.T, fs *runFakeServer, sink *recordingSink, order string, polls int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for fs.fake.pollsServed(order) < polls {
		if time.Now().After(deadline) {
			t.Fatalf("the fake served %d polls, wanted %d", fs.fake.pollsServed(order), polls)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.answers) != 0 {
		t.Fatalf("the driver landed %+v; the leg must stay at the device for the manager item", sink.answers)
	}
}

// endStep scripts a pending first poll, then the given end on every later one.
func endStep(status string, result map[string]any) func(order string, poll int) (int, map[string]any) {
	return func(_ string, poll int) (int, map[string]any) {
		if poll < 1 {
			return 200, map[string]any{"status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
		}
		var res any
		if result != nil {
			res = result
		}
		return 200, map[string]any{"status": status, "result": res, "updated_at": "2026-01-01T00:00:00Z"}
	}
}

// TestRunTerminalUncertainEndWithoutReportingDeliversNothing: without the
// reporting credential set the lookup cannot run, so an end that may have
// moved money (a failed or cancelled interaction past the card read, a
// succeeded one with no result object, a charge stage fault) is never booked
// declined: the cashier would take the card again on a new leg while the
// first charge stands in no book.
func TestRunTerminalUncertainEndWithoutReportingDeliversNothing(t *testing.T) {
	cases := []struct {
		name   string
		status string
		result map[string]any
	}{
		{"failed with no result", "failed", nil},
		{"cancelled with no result", "cancelled", nil},
		{"succeeded with no result", "succeeded", nil},
		{"failed at the charge stage", "failed", map[string]any{"stage_errors": map[string]any{"charge": "gateway timeout"}}},
		{"cancelled past the read", "cancelled", map[string]any{"stage_errors": map[string]any{"signature": "not supported"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newRunFake(t)
			fs.fake.pollStep = endStep(tc.status, tc.result)
			var sink recordingSink
			order := dispatchSaleLeg(t, newSinkDriver(fs, &sink, false))
			requireNoAnswerAfter(t, fs, &sink, order, 2)
		})
	}
}

// TestRunTerminalReadStageFailureDeclines: a failed or cancelled interaction
// whose error is at the card read ran no charge, so it declines with or
// without the reporting credential set.
func TestRunTerminalReadStageFailureDeclines(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		for _, reporting := range []bool{false, true} {
			t.Run(status, func(t *testing.T) {
				fs := newRunFake(t)
				fs.fake.pollStep = endStep(status, map[string]any{"stage_errors": map[string]any{"read": "card removed"}})
				var sink recordingSink
				dispatchSaleLeg(t, newSinkDriver(fs, &sink, reporting))
				if got := sink.waitForAnswer(t); got.Verdict != "declined" {
					t.Fatalf("verdict %s, want declined", got.Verdict)
				}
			})
		}
	}
}

// TestRunTerminalAuthorizedAnswerBooksOnlyTheLegAmount: an approval is booked
// only when the card was charged exactly the leg's amount with no fee. A
// surcharged answer (the card charged subtotal plus fee) and a short approval
// land nothing until surcharge booking is built: the sweep carries the leg to
// a person. A waived fee and an equal amount authorize.
func TestRunTerminalAuthorizedAnswerBooksOnlyTheLegAmount(t *testing.T) {
	approved := func(amount any, fee any) map[string]any {
		r := chargeResult("A", "TRX-AMT", "00", "VISA")
		r["amount"] = amount
		if fee != nil {
			r["fee_amount"] = fee
		}
		return r
	}
	held := []struct {
		name   string
		result map[string]any
	}{
		{"surcharged", approved("25.75", "0.75")},
		{"surcharged with a numeric fee", approved("25.75", 0.75)},
		{"short approval", approved("20.00", nil)},
		{"over approval", approved("25.01", nil)},
		{"no amount", approved("", nil)},
	}
	for _, tc := range held {
		t.Run(tc.name, func(t *testing.T) {
			fs := newRunFake(t)
			fs.fake.pollStep = endStep("succeeded", tc.result)
			var sink recordingSink
			order := dispatchSaleLeg(t, newSinkDriver(fs, &sink, true))
			requireNoAnswerAfter(t, fs, &sink, order, 2)
		})
	}
	booked := []struct {
		name   string
		result map[string]any
	}{
		{"equal amount", approved("25.00", nil)},
		{"waived fee", approved("25.00", "0.00")},
		{"numeric amount", approved(25, nil)},
	}
	for _, tc := range booked {
		t.Run(tc.name, func(t *testing.T) {
			fs := newRunFake(t)
			fs.fake.pollStep = endStep("succeeded", tc.result)
			var sink recordingSink
			dispatchSaleLeg(t, newSinkDriver(fs, &sink, true))
			if got := sink.waitForAnswer(t); got.Verdict != "authorized" || got.ProviderRef != "TRX-AMT" {
				t.Fatalf("answer %+v, want authorized on TRX-AMT", got)
			}
		})
	}
}

// TestRunTerminalLookupBooksOnlyTheLegAmount: a charge the transaction lookup
// finds for a figure other than the leg's lands nothing, on the poll's own
// lookup and on the lookup before the dispatch (which then never prompts the
// device either).
func TestRunTerminalLookupBooksOnlyTheLegAmount(t *testing.T) {
	row := func(order string, cents float64) map[string]any {
		return map[string]any{"order_id": order, "trans_id": "TRX-ROW", "result": "A", "auth_code": "OK11",
			"card_number": "XXXXXXXXXXXX4242", "amount": cents, "resp_text": "APPROVED"}
	}
	t.Run("the poll's lookup after the interaction vanished", func(t *testing.T) {
		fs := newRunFake(t)
		fs.fake.pollStep = func(string, int) (int, map[string]any) { return 404, map[string]any{"error": "not found"} }
		fs.fake.deferredReport = 1
		var sink recordingSink
		term := newSinkDriver(fs, &sink, true)
		order := "sale-" + uuid.NewString() + "-card-0"
		fs.fake.mu.Lock()
		fs.fake.reporting[order] = row(order, 2400)
		fs.fake.mu.Unlock()
		if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
			IntentID: uuid.New(), Kind: payment.TerminalKindSale, AmountCents: 2500, Currency: "USD",
			RegisterID: "REG-1", DeviceID: "HSN-TESTSTUB", IdempotencyKey: order}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		requireNoAnswerAfter(t, fs, &sink, order, 1)
		if fs.fake.lookupCount() < 2 {
			t.Fatalf("the poll never looked the charge up (%d lookups)", fs.fake.lookupCount())
		}
	})
	t.Run("the lookup before the dispatch", func(t *testing.T) {
		fs := newRunFake(t)
		var sink recordingSink
		term := newSinkDriver(fs, &sink, true)
		order := "sale-" + uuid.NewString() + "-card-0"
		fs.fake.mu.Lock()
		fs.fake.reporting[order] = row(order, 2575)
		fs.fake.mu.Unlock()
		if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
			IntentID: uuid.New(), Kind: payment.TerminalKindSale, AmountCents: 2500, Currency: "USD",
			RegisterID: "REG-1", DeviceID: "HSN-TESTSTUB", IdempotencyKey: order}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
		if n := fs.fake.chargePostCount(); n != 0 {
			t.Fatalf("the device was prompted %d times for a charge the report already holds", n)
		}
		sink.mu.Lock()
		defer sink.mu.Unlock()
		if len(sink.answers) != 0 {
			t.Fatalf("the driver landed %+v for a charge of another figure", sink.answers)
		}
	})
}
