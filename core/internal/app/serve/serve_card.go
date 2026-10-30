// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
)

// wirePOSCardPath wires the counter's card terminal and, with it and only
// with it, the gateway that takes a card back (CP-05 A1 and B). A demo
// stack's simulator wins over the Run driver (both are never wired) and gets
// the simulated gateway beside it; the Run driver gets runBack, Run's
// void-or-refund on the leg's trans_id (the reference the sale answer stored
// on the tender). With neither the counter has no card terminal: it refuses
// a card (card_terminal_not_wired) and wires no gateway. run is nil unless
// the configuration selects the Run driver.
func wirePOSCardPath(posSvc *pos.Service, ledger *payment.TerminalLedger, simulator string,
	run payment.TerminalProvider, runBack payment.PaymentGateway) pos.CardTerminalState {
	if sim := payment.NewSimulatedTerminalFromEnv(simulator, payment.NewSimulatedSurchargeSink(ledger)); sim != nil {
		posSvc.WithTerminal(sim, ledger).WithGateway(payment.NewSimulatedTerminalGateway())
	} else if run != nil {
		posSvc.WithTerminal(run, ledger).WithGateway(runBack)
	}
	state, _ := posSvc.CardPath()
	return state
}
