// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// The transaction lookup of the Run driver (CP-05), proved against the
// scripted fake: it answers three things, found, not found and could not
// ask. A report that could not answer (a 500, a refused connection, an
// expired token) is not a report that holds no charge: the dispatch is
// refused, never charged blind, and the leg is left for reconciliation.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
)

// brokenReportDriver wires the driver on the fake with a reporting base that
// cannot answer: a 500 on the transaction list, a 401 on the token, or a
// refused connection.
func brokenReportDriver(t *testing.T, fs *runFakeServer, sink payment.AnswerSink,
	reportStatus, tokenStatus int, unreachable bool) *payment.RunTerminal {
	t.Helper()
	base := fs.server.URL
	if unreachable {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		base = dead.URL
		dead.Close()
	}
	fs.fake.breakReporting(reportStatus, tokenStatus)
	return payment.NewRunTerminal(func() payment.GatewayConfig {
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: fs.server.URL}
	}, sink, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithPollInterval(5*time.Millisecond).WithInteractionWindow(50*time.Millisecond).
		WithReporting(base, "clientid-teststub", "clientsecret-teststub")
}

// TestRunTerminalRefusesTheDispatchWhenTheTransactionLookupCannotAsk: the
// transaction lookup answers three things, found, not found and could not
// ask. A report that could not answer (a 500, a refused connection, an
// expired token) is not a report that holds no charge: the dispatch is
// refused, no charge-card POST is sent, and the leg stays pending at the
// device for reconciliation.
func TestRunTerminalRefusesTheDispatchWhenTheTransactionLookupCannotAsk(t *testing.T) {
	for _, tc := range []struct {
		name         string
		reportStatus int
		tokenStatus  int
		unreachable  bool
	}{
		{"the report answers 500", 500, 0, false},
		{"the reporting token is refused", 0, 401, false},
		{"the report is unreachable", 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, branch := newRunLedger(t)
			runUntilItSticks(t, func(t *testing.T) bool {
				sale := runSaleRow(t, branch)
				fs := newRunFake(t)
				term := brokenReportDriver(t, fs, ledger, tc.reportStatus, tc.tokenStatus, tc.unreachable)
				in := claimRunLegAt(t, ledger, sale, branch, 2500)
				_, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
					IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents, RegisterID: in.RegisterID,
					DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey})
				if err == nil {
					t.Fatal("the dispatch was answered: the driver must never charge blind when the report cannot answer")
				}
				if posts := fs.fake.chargePostCount(); posts != 0 {
					t.Fatalf("the driver posted %d charges with a report that could not answer, want 0", posts)
				}
				got, err := ledger.Get(context.Background(), in.ID)
				if err != nil {
					t.Fatalf("get intent: %v", err)
				}
				if got.State != payment.TerminalPendingAtDevice {
					t.Fatalf("the intent is %s, want it left pending at the device for reconciliation", got.State)
				}
				return true
			})
		})
	}
}

// TestRunTerminalARefusedLookupNeverChargesAgainAfterARestart: the first
// dispatch charges; the report then breaks; a fresh driver (a process
// restart, with a guard of its own still empty) re dispatches the same
// idempotency key. The report could not ask, so the re dispatch is refused
// and the leg is never charged twice.
func TestRunTerminalARefusedLookupNeverChargesAgainAfterARestart(t *testing.T) {
	ledger, branch := newRunLedger(t)
	runUntilItSticks(t, func(t *testing.T) bool {
		sale := runSaleRow(t, branch)
		fs := newRunFake(t)
		fs.fake.pollStep = func(string, int) (int, map[string]any) {
			return 200, map[string]any{"status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
		}
		term := newRunDriver(t, fs, ledger)
		in := claimRunLegAt(t, ledger, sale, branch, 2500)
		d := payment.TerminalDispatch{IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents,
			RegisterID: in.RegisterID, DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}
		if _, err := term.Dispatch(context.Background(), d); err != nil {
			t.Fatalf("first dispatch: %v", err)
		}
		if posts := fs.fake.chargePostCount(); posts != 1 {
			t.Fatalf("the first dispatch posted %d charges, want 1", posts)
		}
		// the report breaks: it can no longer say whether this order id
		// carries a charge
		fs.fake.breakReporting(500, 0)
		// a fresh driver, as after a process restart: its own guard is empty
		restarted := newRunDriver(t, fs, ledger)
		if _, err := restarted.Dispatch(context.Background(), d); err == nil {
			t.Fatal("the re dispatch was answered: the driver must never charge blind when the report cannot answer")
		}
		if posts := fs.fake.chargePostCount(); posts != 1 {
			t.Fatalf("the re dispatch posted a second charge (%d posts): the leg would be charged twice", posts)
		}
		return true
	})
}

// TestRunTerminalTheReportThatCannotAnswerNeverDeclinesTheLeg: an
// interaction that ends with no verdict is uncertain, not a decline. When the
// report cannot answer, the driver leaves the leg pending at the device for
// reconciliation: it never books a decline with the money moved.
func TestRunTerminalTheReportThatCannotAnswerNeverDeclinesTheLeg(t *testing.T) {
	ledger, branch := newRunLedger(t)
	runUntilItSticks(t, func(t *testing.T) bool {
		sale := runSaleRow(t, branch)
		fs := newRunFake(t)
		fs.fake.pollStep = func(_ string, poll int) (int, map[string]any) {
			if poll < 1 {
				return 200, map[string]any{"status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
			}
			// the report breaks: from here the driver cannot ask what
			// happened to this order id
			fs.fake.breakReporting(500, 0)
			return 200, map[string]any{"status": "failed", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
		}
		term := newRunDriver(t, fs, ledger)
		in := claimRunLegAt(t, ledger, sale, branch, 2500)
		if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
			IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents, RegisterID: in.RegisterID,
			DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		got, err := ledger.Get(context.Background(), in.ID)
		if err != nil {
			t.Fatalf("get intent: %v", err)
		}
		if got.State != payment.TerminalPendingAtDevice {
			t.Fatalf("the intent is %s, want it left pending at the device for reconciliation", got.State)
		}
		return true
	})
}
