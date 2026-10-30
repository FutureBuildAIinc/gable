// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
)

// stubTerminal stands in for the Run driver: the wiring is what is pinned.
type stubTerminal struct{}

func (stubTerminal) Dispatch(context.Context, payment.TerminalDispatch) (payment.TerminalReceipt, error) {
	return payment.TerminalReceipt{}, nil
}

func (stubTerminal) SettlementBatch(context.Context, string, time.Time, time.Time) ([]payment.TerminalBatchLine, error) {
	return nil, nil
}

// RULE (CP-05 B): a wired card terminal always has the gateway that takes a
// card back, so a card sale can be returned or voided to the card; with no
// terminal the counter has neither, and refuses a card (CP-05 A1).
func TestWirePOSCardPath_ATerminalAlwaysHasItsWayBack(t *testing.T) {
	runBack := payment.NewSimulatedTerminalGateway()
	cases := []struct {
		name        string
		simulator   string
		run         payment.TerminalProvider
		wantState   pos.CardTerminalState
		wantGateway bool
	}{
		{"nothing configured", "", nil, pos.CardTerminalNone, false},
		{"an unknown simulator script", "nonsense", nil, pos.CardTerminalNone, false},
		{"the Run driver", "", stubTerminal{}, pos.CardTerminalRun, true},
		{"the simulator", "approve", nil, pos.CardTerminalSimulator, true},
		{"the simulator wins over Run", "approve", stubTerminal{}, pos.CardTerminalSimulator, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := pos.NewService(nil, nil, nil)
			got := wirePOSCardPath(svc, payment.NewTerminalLedger(nil), c.simulator, c.run, runBack)
			state, gateway := svc.CardPath()
			if got != c.wantState || state != c.wantState {
				t.Fatalf("card terminal = %q (answered %q), wanted %q", state, got, c.wantState)
			}
			if gateway != c.wantGateway {
				t.Fatalf("gateway wired = %v, wanted %v", gateway, c.wantGateway)
			}
		})
	}
}
