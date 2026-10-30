// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"github.com/google/uuid"

	"github.com/gablelbm/gable/internal/payment"
)

// CardTerminalState names the card path a register's counter has (CP-05
// A1): the Run terminal driver, the simulated terminal of a demo stack, or
// none. With none the counter takes no card: a card that ran nowhere is never
// booked as paid.
type CardTerminalState string

const (
	CardTerminalRun       CardTerminalState = "run"
	CardTerminalSimulator CardTerminalState = "simulator"
	CardTerminalNone      CardTerminalState = "none"
)

// codeCardTerminalNotWired is the refusal of a card where no terminal is
// wired: the sale, the return and the void answer it alike.
const codeCardTerminalNotWired = "card_terminal_not_wired"

// cardTerminalNotWiredWords is the sale's refusal, and the Pay screen says
// the same sentence on its disabled Card.
const cardTerminalNotWiredWords = "This register has no card terminal: take cash, check or the account"

// cardTerminalAt answers the card path of a branch's counter. Today it is the
// one the server wires; a per branch "standalone card machine" switch (A2,
// Colton's decision, not built) would answer its own state here, and the
// refusal below would let that state's tender through.
func (s *Service) cardTerminalAt(_ uuid.UUID) CardTerminalState {
	if !s.terminalWired() {
		return CardTerminalNone
	}
	if _, ok := s.terminal.(*payment.SimulatedTerminal); ok {
		return CardTerminalSimulator
	}
	return CardTerminalRun
}

// refuseCardWithoutTerminal refuses a sale's card tender that nothing would
// run: no terminal is wired, and the tender carries no token a wired gateway
// could charge. It runs in the completion's pre-check, before any charge, so
// nothing is booked.
func (s *Service) refuseCardWithoutTerminal(branchID uuid.UUID, tenders []TenderIn) error {
	if s.cardTerminalAt(branchID) != CardTerminalNone {
		return nil
	}
	for i := range tenders {
		t := &tenders[i]
		if t.Method == TenderCard && (t.TokenID == "" || s.gateway == nil) {
			return conflict(codeCardTerminalNotWired, cardTerminalNotWiredWords)
		}
	}
	return nil
}

// CardPath answers the counter's wired card path: the terminal state and
// whether a gateway is wired to take a card back (serve's twin test pins
// that a wired terminal always has one, CP-05 B).
func (s *Service) CardPath() (CardTerminalState, bool) {
	return s.cardTerminalAt(uuid.Nil), s.gateway != nil
}
