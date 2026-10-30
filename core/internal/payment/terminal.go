// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package payment: the card terminal seam (CP-05, IX-01).
//
// The crash windows this ledger closes. A card leg at the counter is one
// payment_terminal_intents row whose state moves claimed -> pending_at_device
// -> authorized or declined, each move ONE guarded statement (UPDATE ... SET
// state = $new WHERE id = $id AND state = $expected RETURNING ...): zero rows
// answers a conflict, never a second move. The dispatch move to
// pending_at_device is committed BEFORE the device is called, so claimed
// means "the device was never reached" and everything that may have reached
// the device is pending_at_device. The windows, each driven through the
// counter act with a seam that stops it between two statements
// (core/internal/pos/terminal_crash_test.go) and, at the ledger, by
// terminal_intent_test.go and terminal_fix_test.go:
//
//   claimed but never dispatched: the act died between the committed claim
//   and the dispatch move. No money moved. The intent holds its register's
//   drawer open until its lease expires; the sweep then closes it lost with
//   no manager item (TestCrashAfterClaimNeverReachesTheDevice,
//   TestClaimedNeverDispatchedSweepCloses). An act still alive when the
//   sweep closes its claim finds the dispatch move refused and never calls
//   the device (TestSweepClosingTheClaimStopsTheDispatch).
//
//   dispatched but never answered: the dispatch move committed and the
//   device took the leg, or the act died before the call, or the call
//   failed after the device took it. The money may have moved. The intent
//   stays pending_at_device until its lease expires; the sweep turns it lost
//   and files one manager item in the same statement (who resolves it, what
//   amount, which register, the device reference)
//   (TestCrashAfterDispatchMoveIsAManagerItem,
//   TestSimulatedCrashAfterDispatchEndToEnd,
//   TestDispatchedNeverAnsweredSweepQueues, TestSweepIsOneStatement).
//
//   answered, the act died before it booked: the device authorized the leg
//   and the sale's transaction never opened. The leg stays authorized and
//   unspent on the open sale; the retried completion uses it and never
//   charges the card again (TestCrashAfterDeviceCallKeepsTheAuthorizedLeg,
//   TestCrashAfterAnswerBeforeTheBookingKeepsTheAuthorizedLeg).
//
//   answered twice: the device (or a retry) delivered the same answer again.
//   The answer is idempotent by provider reference: the same verdict twice is
//   a no-op, the intent never moves twice (TestSimulatedDuplicateAnswerEndToEnd,
//   TestDuplicateAnswerIsNoOp).
//
//   answered after the sweep: a late answer arrived on an intent the sweep
//   already turned lost. The guarded move refuses it (lost is not
//   pending_at_device) and the single statement that follows classes it late,
//   audits it and files or updates the manager item with the device's
//   verdict and reference; a late authorized answer after a declined
//   resolution reopens the item (TestSimulatedLateAnswerEndToEnd,
//   TestLateAnswerKeepsVerdictAndReferenceOnTheItem,
//   TestLateAuthorizedAfterDeclinedResolutionReopens).
//
// An answer for an intent still claimed is a defect (the device cannot have
// answered a leg it was never called for) and is refused loudly
// (TestAnswerForClaimedIntentIsRefused).
//
// A conflicting verdict (authorized then declined under one provider
// reference) is audited and becomes a manager item too, never a second state
// move (TestConflictingAnswerBecomesManagerItem,
// TestLateAnswerAfterAuthorizedResolutionIsFlaggedNotReopened). The row carries only
// the answer's safe fields: brand, last four, auth code, the provider's own
// reference. A card number, track data or a CVV is never stored, written or
// logged, not even in a test fixture.

package payment

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TerminalAnswerLease is how long a card leg's answer may take at the device
// before the sweep may turn the intent lost. It exceeds the counter's wait
// for the answer, so a waited out act leaves the intent inside its lease and
// only the sweep (past the lease) decides it is lost.
//
// The number lives here, beside the seam, because every driver's own window
// has to fit inside it: a poller that keeps working longer than the lease is
// closed by the sweep while the device is still answering, and the leg is
// filed as a manager item for an answer that is about to land. The counter
// writes this lease onto the leg it claims (core/internal/pos/terminal.go)
// and the Run driver bounds its poller by it
// (core/internal/payment/terminal_run.go).
const TerminalAnswerLease = 90 * time.Second

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
	ID             uuid.UUID
	TransactionID  uuid.UUID
	LegKey         string
	RegisterID     string
	BranchID       uuid.UUID
	AmountCents    int64
	Kind           string
	State          TerminalIntentState
	DeviceID       string
	ProviderRef    string
	IdempotencyKey string
	LeaseUntil     time.Time
	ClaimedAt      time.Time
	DispatchedAt   *time.Time
	AnsweredAt     *time.Time
	AnswerBrand    string
	// AnswerFeeCents is the card surcharge the device's answer carried (CP-15).
	AnswerFeeCents    int64
	AnswerLast4       string
	AnswerAuthCode    string
	AnswerProviderRef string
	AnswerVerdict     string
	// SpentAt: an authorized leg whose money the books took or the device
	// gave back (nil while it still backs an open sale).
	SpentAt *time.Time
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
	IntentID    uuid.UUID
	ProviderRef string
	Verdict     string // "authorized" or "declined"
	Brand       string
	Last4       string
	AuthCode    string
	// FeeCents is the card surcharge the processor added on top of the leg's
	// amount (CP-15); zero when none. The ledger stores it on the intent.
	FeeCents int64
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
