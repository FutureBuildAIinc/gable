// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package payment: the card terminal seam (CP-05, IX-01).
//
// The crash windows this ledger closes. A card leg at the counter is one
// payment_terminal_intents row whose state moves claimed -> pending_at_device
// -> authorized or declined, each move ONE guarded statement (UPDATE ... SET
// state = $new WHERE id = $id AND state = $expected RETURNING ...): zero rows
// answers a conflict, never a second move. The windows, each with the test
// that crashes at that point (terminal_intent_test.go):
//
//   claimed but never dispatched: the act died between the committed claim
//   and the device call. No money moved. The intent holds its register's
//   drawer open until its lease expires; the sweep then closes it lost with
//   no manager item (test TestClaimedNeverDispatchedSweepCloses).
//
//   dispatched but never answered: the device took the leg and the act (or
//   the device) died before the answer landed. The money may have moved. The
//   intent stays pending_at_device until its lease expires; the sweep turns
//   it lost and files one manager item (who resolves it, what amount, which
//   register, the device reference) (test TestDispatchedNeverAnsweredSweepQueues).
//
//   answered twice: the device (or a retry) delivered the same answer again.
//   The answer is idempotent by provider reference: the same verdict twice is
//   a no-op, the intent never moves twice (test TestDuplicateAnswerIsNoOp).
//
//   answered after the sweep: a late answer arrived on an intent the sweep
//   already turned lost. The guarded move refuses it (lost is not
//   pending_at_device); the answer is audited and filed as a manager item
//   beside the sweep's own, so the manager reads the terminal's record with
//   the late answer in hand (test TestLateAnswerAfterSweepBecomesManagerItem).
//
// A conflicting verdict (authorized then declined under one provider
// reference) is audited and becomes a manager item too, never a second state
// move (test TestConflictingAnswerBecomesManagerItem). The row carries only
// the answer's safe fields: brand, last four, auth code, the provider's own
// reference. A card number, track data or a CVV is never stored, written or
// logged, not even in a test fixture.

package payment

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TerminalIntentState is one state of a card terminal intent. Every move is
// one guarded statement; zero rows moved answers a conflict.
type TerminalIntentState string

const (
	// TerminalClaimed: the claim is committed, the device not yet reached.
	TerminalClaimed TerminalIntentState = "claimed"
	// TerminalPendingAtDevice: dispatched, the answer not yet landed.
	TerminalPendingAtDevice TerminalIntentState = "pending_at_device"
	// TerminalAuthorized: the device approved the leg.
	TerminalAuthorized TerminalIntentState = "authorized"
	// TerminalDeclined: the device refused the leg.
	TerminalDeclined TerminalIntentState = "declined"
	// TerminalLost: the lease expired before an answer; a pending_at_device
	// loss carries a manager item.
	TerminalLost TerminalIntentState = "lost"
)

// Terminal kinds: a sale's charge, a refund of a charge, a void of a charge.
const (
	TerminalKindSale   = "sale"
	TerminalKindRefund = "refund"
	TerminalKindVoid   = "void"
)

// TerminalIntent is one card leg the counter sends to a terminal.
type TerminalIntent struct {
	ID                uuid.UUID
	TransactionID     uuid.UUID
	LegKey            string
	RegisterID        string
	BranchID          uuid.UUID
	AmountCents       int64
	Kind              string
	State             TerminalIntentState
	DeviceID          string
	ProviderRef       string
	IdempotencyKey    string
	LeaseUntil        time.Time
	ClaimedAt         time.Time
	DispatchedAt      *time.Time
	AnsweredAt        *time.Time
	AnswerBrand       string
	AnswerLast4       string
	AnswerAuthCode    string
	AnswerProviderRef string
	AnswerVerdict     string
}

// TerminalDispatch is one leg handed to a device.
type TerminalDispatch struct {
	IntentID       uuid.UUID
	Kind           string
	AmountCents    int64
	Currency       string
	RegisterID     string
	DeviceID       string
	IdempotencyKey string
	// Reference is the charge's provider reference when the kind is a refund
	// or a void of an earlier sale leg.
	Reference string
}

// TerminalReceipt is the device's immediate take of a dispatch: its own
// reference for the leg. The verdict arrives asynchronously.
type TerminalReceipt struct {
	ProviderRef string
	DeviceID    string
}

// TerminalAnswer is the asynchronous verdict a device delivers. It carries
// only safe fields.
type TerminalAnswer struct {
	IntentID     uuid.UUID
	ProviderRef  string
	Verdict      string // "authorized" or "declined"
	Brand        string
	Last4        string
	AuthCode     string
}

// TerminalBatchLine is one leg of a device's settlement batch.
type TerminalBatchLine struct {
	ProviderRef string
	Verdict     string
	AmountCents int64
	Brand       string
	Last4       string
	AuthCode    string
}

// TerminalProvider is the terminal device seam beside PaymentGateway: it
// dispatches a card leg, delivers no verdict itself (the answer arrives
// asynchronously, through the provider's own callback into ApplyDeviceAnswer
// or the settlement batch), and reads what a device settled.
type TerminalProvider interface {
	// Dispatch hands one leg to the device. The idempotency key makes a
	// retried dispatch the same leg at the device, never a second charge.
	Dispatch(ctx context.Context, d TerminalDispatch) (TerminalReceipt, error)
	// SettlementBatch reads the legs the device settled between from and to.
	SettlementBatch(ctx context.Context, deviceID string, from, to time.Time) ([]TerminalBatchLine, error)
}
