// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// The simulated terminal (CP-05, IX-01): a TerminalProvider driver with
// scripted outcomes, used by the tests and selectable on a demo stack by
// configuration (NewSimulatedTerminalFromEnv reads POS_TERMINAL_SIMULATOR;
// an unset or unrecognized value answers nil, so it is never the default in
// a production configuration). It never touches a real device, and its
// answers carry only safe fields (brand, last four, auth code, reference).
//
// The scripted outcomes, one per idempotency key (the default when a key has
// no entry):
//
//	approve             the device answers authorized, once
//	decline             the device answers declined, once
//	no_answer           the device takes the leg and never answers (the
//	                    waiting state the desk shows and the sweep resolves)
//	crash_after_dispatch the dispatch itself fails after the device recorded
//	                    the leg (the act must treat the answer as unknown)
//	duplicate_answer    the device answers authorized twice (the second must
//	                    be a no-op)
//	late_answer         the device holds the answer until the test releases
//	                    it, after the sweep (DeliverLate answers it then)

// SimulatedOutcome is one scripted outcome of the simulated terminal.
type SimulatedOutcome string

const (
	SimApprove            SimulatedOutcome = "approve"
	SimDecline            SimulatedOutcome = "decline"
	SimNoAnswer           SimulatedOutcome = "no_answer"
	SimCrashAfterDispatch SimulatedOutcome = "crash_after_dispatch"
	SimDuplicateAnswer    SimulatedOutcome = "duplicate_answer"
	SimLateAnswer         SimulatedOutcome = "late_answer"
)

// AnswerSink receives the asynchronous answers the simulated device
// delivers. The terminal ledger's ApplyDeviceAnswer is the production sink.
type AnswerSink interface {
	ApplyDeviceAnswer(ctx context.Context, a TerminalAnswer) (AnswerOutcome, error)
}

// HeldAnswerSink is the optional half of a sink that keeps what a held
// approval charged above the leg (CP-15): the leg stays unanswered for the
// manager item, and the manager's authorized resolution books or puts back
// that figure as the card fee. The terminal ledger implements it.
type HeldAnswerSink interface {
	HoldDeviceAnswer(ctx context.Context, intentID uuid.UUID, feeCents int64) error
}

// SimulatedTerminal is the scripted TerminalProvider of tests and demo
// stacks. Safe for concurrent use.
type SimulatedTerminal struct {
	mu         sync.Mutex
	def        SimulatedOutcome
	script     map[string]SimulatedOutcome
	answers    AnswerSink
	dispatched []TerminalDispatch
	held       map[string]TerminalAnswer
	outcomes   []AnswerOutcome
	delivered  int
}

// NewSimulatedTerminal builds the driver with a default outcome and the sink
// its asynchronous answers go to.
func NewSimulatedTerminal(def SimulatedOutcome, answers AnswerSink) *SimulatedTerminal {
	return &SimulatedTerminal{def: def, script: map[string]SimulatedOutcome{},
		answers: answers, held: map[string]TerminalAnswer{}}
}

// NewSimulatedTerminalFromEnv answers the driver a demo stack selects by
// configuration, or nil when the configuration names none (a production
// configuration leaves it unset, so the simulator is never the default).
func NewSimulatedTerminalFromEnv(value string, answers AnswerSink) *SimulatedTerminal {
	switch SimulatedOutcome(value) {
	case SimApprove, SimDecline, SimNoAnswer, SimCrashAfterDispatch, SimDuplicateAnswer, SimLateAnswer:
		return NewSimulatedTerminal(SimulatedOutcome(value), answers)
	}
	return nil
}

// Script sets the outcome of one idempotency key.
func (s *SimulatedTerminal) Script(key string, outcome SimulatedOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script[key] = outcome
}

// Dispatched lists the legs the device took, in order.
func (s *SimulatedTerminal) Dispatched() []TerminalDispatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TerminalDispatch, len(s.dispatched))
	copy(out, s.dispatched)
	return out
}

// Dispatch hands the leg to the scripted device.
func (s *SimulatedTerminal) Dispatch(ctx context.Context, d TerminalDispatch) (TerminalReceipt, error) {
	s.mu.Lock()
	outcome := s.def
	if o, ok := s.script[d.IdempotencyKey]; ok {
		outcome = o
	}
	s.dispatched = append(s.dispatched, d)
	s.mu.Unlock()
	ref := simulatedRefPrefix + d.IdempotencyKey
	switch outcome {
	case SimCrashAfterDispatch:
		// the device recorded the leg and the connection died: the caller
		// sees the failure, never a verdict
		return TerminalReceipt{}, fmt.Errorf("the simulated terminal crashed after taking the leg")
	}
	switch outcome {
	case SimApprove, SimDuplicateAnswer, SimLateAnswer:
		answer := TerminalAnswer{IntentID: d.IntentID, ProviderRef: ref, Verdict: "authorized",
			Brand: "VISA", Last4: "4242", AuthCode: "SIMOK"}
		switch outcome {
		case SimApprove:
			s.deliver(ctx, answer)
		case SimDuplicateAnswer:
			s.deliver(ctx, answer)
			s.deliver(ctx, answer)
		case SimLateAnswer:
			s.mu.Lock()
			s.held[d.IdempotencyKey] = answer
			s.mu.Unlock()
		}
	case SimDecline:
		s.deliver(ctx, TerminalAnswer{IntentID: d.IntentID, ProviderRef: ref, Verdict: "declined"})
	}
	return TerminalReceipt{ProviderRef: ref, DeviceID: d.DeviceID}, nil
}

// deliver sends one answer to the sink, on the device's own time: a real
// device answers on its own connection after the dispatch returned, never
// inside it, so the simulated one schedules the answer off the caller's
// goroutine (the caller commits its dispatched move before the answer can
// land, exactly as at a real device).
func (s *SimulatedTerminal) deliver(ctx context.Context, a TerminalAnswer) {
	if s.answers == nil {
		return
	}
	time.AfterFunc(20*time.Millisecond, func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		out, err := s.answers.ApplyDeviceAnswer(dctx, a)
		if err != nil {
			out = AnswerOutcome("error: " + err.Error())
		}
		s.mu.Lock()
		s.outcomes = append(s.outcomes, out)
		s.mu.Unlock()
	})
}

// Outcomes lists what the sink answered for each answer the device delivered,
// in the order they landed (applied, duplicate, late_queued and so on).
func (s *SimulatedTerminal) Outcomes() []AnswerOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AnswerOutcome, len(s.outcomes))
	copy(out, s.outcomes)
	return out
}

// DeliverLate releases the answer a late_answer script holds, after the test
// ran the sweep: the answer lands on an intent already lost.
func (s *SimulatedTerminal) DeliverLate(ctx context.Context, key string) {
	s.mu.Lock()
	a, ok := s.held[key]
	delete(s.held, key)
	s.mu.Unlock()
	if ok {
		s.deliver(ctx, a)
	}
}

// SettlementBatch reads back the legs the scripted device settled: every
// dispatched leg whose outcome was an approve or a duplicate approve.
func (s *SimulatedTerminal) SettlementBatch(ctx context.Context, deviceID string, from, to time.Time) ([]TerminalBatchLine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TerminalBatchLine
	for _, d := range s.dispatched {
		outcome := s.def
		if o, ok := s.script[d.IdempotencyKey]; ok {
			outcome = o
		}
		if outcome != SimApprove && outcome != SimDuplicateAnswer && outcome != SimLateAnswer {
			continue
		}
		out = append(out, TerminalBatchLine{ProviderRef: simulatedRefPrefix + d.IdempotencyKey,
			Verdict: "authorized", AmountCents: d.AmountCents, Brand: "VISA", Last4: "4242"})
	}
	return out, nil
}

// SimulatedSurchargeSink is the answer sink of a demo stack standing in for a
// processor enrolled in surcharging (CP-15): it adds the branch's rate to an
// authorized sale answer when the branch's switch is on, rounded half up to
// the cent, and lands the answer in the ledger. It exists so a demo stack and
// the Playwright run can show a fee without a real processor; a production
// configuration never wires it, and the fee on a real answer is the
// processor's own figure.
type SimulatedSurchargeSink struct {
	ledger *TerminalLedger
}

// NewSimulatedSurchargeSink wraps the ledger the simulated device answers into.
func NewSimulatedSurchargeSink(ledger *TerminalLedger) SimulatedSurchargeSink {
	return SimulatedSurchargeSink{ledger: ledger}
}

// ApplyDeviceAnswer adds the branch's fee to an authorized sale answer and
// lands it in the ledger.
func (s SimulatedSurchargeSink) ApplyDeviceAnswer(ctx context.Context, a TerminalAnswer) (AnswerOutcome, error) {
	if a.Verdict == "authorized" && a.FeeCents == 0 {
		if in, err := s.ledger.Get(ctx, a.IntentID); err == nil && in.Kind == TerminalKindSale {
			var enabled bool
			var bps int64
			if err := s.ledger.db.GetExecutor(ctx).QueryRow(ctx,
				`SELECT card_surcharge_enabled, card_surcharge_bps FROM locations WHERE id = $1`, in.BranchID).Scan(&enabled, &bps); err == nil && enabled {
				a.FeeCents = (in.AmountCents*bps + 5000) / 10000
			}
		}
	}
	return s.ledger.ApplyDeviceAnswer(ctx, a)
}
