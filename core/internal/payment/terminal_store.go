// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// isUniqueViolation reads the SQLSTATE of a refused insert.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ErrTerminalIntentNotFound is the read of an intent that is not there (or
// that the caller's branch context hides).
var ErrTerminalIntentNotFound = errors.New("the terminal intent was not found")

// ErrTerminalIntentConflict is the claim of a leg another standing intent
// already holds.
var ErrTerminalIntentConflict = errors.New("a terminal intent already holds this leg")

// AnswerOutcome says what ApplyDeviceAnswer did with an answer.
type AnswerOutcome string

const (
	// AnswerApplied: the verdict moved the intent, once.
	AnswerApplied AnswerOutcome = "applied"
	// AnswerDuplicate: the same verdict had already landed; a no-op.
	AnswerDuplicate AnswerOutcome = "duplicate"
	// AnswerConflictQueued: a verdict contradicts the one already recorded;
	// audited and queued for a manager, the intent never moved twice.
	AnswerConflictQueued AnswerOutcome = "conflict_queued"
	// AnswerLateQueued: the answer arrived on an intent the sweep already
	// turned lost; audited and queued for a manager beside the sweep's item.
	AnswerLateQueued AnswerOutcome = "late_queued"
	// AnswerReopened: a late authorized answer contradicted a declined
	// resolution; the item is open again for a manager, carrying the verdict.
	AnswerReopened AnswerOutcome = "reopened"
)

// TerminalLostItem is one manager item of the lost answer queue.
type TerminalLostItem struct {
	ID             uuid.UUID
	IntentID       uuid.UUID
	BranchID       uuid.UUID
	RegisterID     string
	TransactionID  uuid.UUID
	Kind           string
	AmountCents    int64
	DeviceID       string
	ProviderRef    string
	State          string
	ResolvedBy     string
	ResolutionNote string
	ResolvedAt     *time.Time
	// The device's own late word, kept when an answer arrived after the sweep
	// or against a recorded verdict.
	LateVerdict     string
	LateProviderRef string
	LateBrand       string
	LateLast4       string
	LateAuthCode    string
	LateAt          *time.Time
	ResolvingUntil  *time.Time
}

// ErrAnswerForClaimedIntent is the refusal of a device answer for an intent
// still claimed: a claimed intent never reached the device (the dispatch move
// is committed before the device call), so an answer for it is a defect.
var ErrAnswerForClaimedIntent = errors.New("the device answered an intent that is still claimed: a claimed intent never reached the device")

// TerminalLedger is the intent ledger's store: the guarded state moves, the
// device answers and the sweep. Its statements run through the context's own
// executor, so a caller inside a transaction moves the intent inside it.
// Reads and moves that serve an HTTP caller carry the caller's branch wall
// (the context's reach); a context with no branch context (the worker, a
// device answer) reaches every branch.
type TerminalLedger struct {
	db       *database.DB
	auditLog AuditSink
	logger   *slog.Logger
}

// NewTerminalLedger builds the ledger on the database.
func NewTerminalLedger(db *database.DB) *TerminalLedger {
	return &TerminalLedger{db: db, logger: slog.Default()}
}

// WithAuditLog sets the audit sink the conflicting and late answers write.
func (l *TerminalLedger) WithAuditLog(a AuditSink) *TerminalLedger { l.auditLog = a; return l }

// reachArgs are the two wall parameters of a branch scoped statement.
func reachArgs(ctx context.Context) (branch *uuid.UUID, grants *string) {
	return middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)
}

// Claim commits the claim before any device call: one insert, refused when a
// standing intent already holds the leg (the unique constraint) or the leg's
// key was reused under another kind's constraint.
func (l *TerminalLedger) Claim(ctx context.Context, in *TerminalIntent) error {
	_, err := l.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO payment_terminal_intents
			(id, transaction_id, leg_key, register_id, branch_id, amount_cents, kind, state,
			 device_id, idempotency_key, lease_until)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'claimed', $8, $9, $10)`,
		in.ID, in.TransactionID, in.LegKey, in.RegisterID, in.BranchID, in.AmountCents, in.Kind,
		in.DeviceID, in.IdempotencyKey, in.LeaseUntil)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrTerminalIntentConflict
		}
		return err
	}
	return nil
}

// Get reads one intent by id, with no wall (an internal read: a device
// answer, a worker pass, an act that already held the record's branch).
func (l *TerminalLedger) Get(ctx context.Context, id uuid.UUID) (*TerminalIntent, error) {
	return l.scanIntent(l.db.GetExecutor(ctx).QueryRow(ctx, intentColumns+` WHERE i.id = $1`, id))
}

// GetInReach reads one intent held to the caller's branch wall: an intent of
// a branch outside the caller's reach is a missing row.
func (l *TerminalLedger) GetInReach(ctx context.Context, id uuid.UUID) (*TerminalIntent, error) {
	branch, grants := reachArgs(ctx)
	return l.scanIntent(l.db.GetExecutor(ctx).QueryRow(ctx,
		intentColumns+` WHERE i.id = $1 AND `+middleware.Wall("i", 2, 3), id, branch, grants))
}

// GetByKey reads the intent of one leg and kind.
func (l *TerminalLedger) GetByKey(ctx context.Context, transactionID uuid.UUID, legKey, kind string) (*TerminalIntent, error) {
	return l.scanIntent(l.db.GetExecutor(ctx).QueryRow(ctx,
		intentColumns+` WHERE i.transaction_id = $1 AND i.leg_key = $2 AND i.kind = $3`, transactionID, legKey, kind))
}

// LatestLeg reads the newest attempt at a card leg of a sale: the leg's base
// key (card-0) and its retries (card-0#1, card-0#2). Nil when no attempt was
// ever claimed.
func (l *TerminalLedger) LatestLeg(ctx context.Context, transactionID uuid.UUID, legBase, kind string) (*TerminalIntent, error) {
	in, err := l.scanIntent(l.db.GetExecutor(ctx).QueryRow(ctx,
		intentColumns+` WHERE i.transaction_id = $1 AND i.kind = $3 AND (i.leg_key = $2 OR i.leg_key LIKE $2 || '#%')
		ORDER BY i.claimed_at DESC, i.id LIMIT 1`, transactionID, legBase, kind))
	if errors.Is(err, ErrTerminalIntentNotFound) {
		return nil, nil
	}
	return in, err
}

// UnspentAuthorizedLegs lists the sale legs of a sale that the device
// authorized and the books have neither taken nor put back.
func (l *TerminalLedger) UnspentAuthorizedLegs(ctx context.Context, transactionID uuid.UUID) ([]TerminalIntent, error) {
	rows, err := l.db.GetExecutor(ctx).Query(ctx,
		intentColumns+` WHERE i.transaction_id = $1 AND i.kind = 'sale' AND i.state = 'authorized' AND i.spent_at IS NULL
		ORDER BY i.claimed_at, i.id`, transactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TerminalIntent
	for rows.Next() {
		in, err := l.scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	return out, rows.Err()
}

const intentColumns = `
SELECT i.id, i.transaction_id, i.leg_key, i.register_id, i.branch_id, i.amount_cents, i.kind, i.state,
       i.device_id, i.provider_ref, i.idempotency_key, i.lease_until, i.claimed_at, i.dispatched_at, i.answered_at,
       i.answer_brand, i.answer_last4, i.answer_auth_code, i.answer_provider_ref, i.answer_verdict, i.spent_at
FROM payment_terminal_intents i`

func (l *TerminalLedger) scanIntent(row interface {
	Scan(dest ...any) error
}) (*TerminalIntent, error) {
	var in TerminalIntent
	var state string
	if err := row.Scan(&in.ID, &in.TransactionID, &in.LegKey, &in.RegisterID, &in.BranchID, &in.AmountCents,
		&in.Kind, &state, &in.DeviceID, &in.ProviderRef, &in.IdempotencyKey, &in.LeaseUntil, &in.ClaimedAt,
		&in.DispatchedAt, &in.AnsweredAt, &in.AnswerBrand, &in.AnswerLast4, &in.AnswerAuthCode,
		&in.AnswerProviderRef, &in.AnswerVerdict, &in.SpentAt); err != nil {
		return nil, ErrTerminalIntentNotFound
	}
	in.State = TerminalIntentState(state)
	return &in, nil
}

// MarkDispatched is the claimed -> pending_at_device move, committed BEFORE
// the device call: one guarded statement with RETURNING, the restamped lease
// written with it. After it, a claimed intent means "never reached the
// device" and the sweep may close it; anything that may have reached the
// device is pending_at_device. False answers that the intent was not claimed
// (the sweep or a cancel closed it): the caller must not call the device.
// providerRef is optional and kept when empty.
func (l *TerminalLedger) MarkDispatched(ctx context.Context, id uuid.UUID, providerRef string, leaseUntil time.Time) (bool, error) {
	var got uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_intents
		SET state = 'pending_at_device', provider_ref = COALESCE(NULLIF($2, ''), provider_ref), lease_until = $3,
		    dispatched_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND state = 'claimed'
		RETURNING id`, id, providerRef, leaseUntil).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// StampProviderRef records the device's own reference for a leg once the
// device took it, whatever state the leg is in by then (an answer may have
// landed first). A reference already stamped is never overwritten.
func (l *TerminalLedger) StampProviderRef(ctx context.Context, id uuid.UUID, providerRef string) error {
	if providerRef == "" {
		return nil
	}
	_, err := l.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE payment_terminal_intents SET provider_ref = $2, updated_at = NOW()
		WHERE id = $1 AND provider_ref = ''`, id, providerRef)
	return err
}

// CloseClaimedIntent is the desk's cancel of a claim the device never
// reached: one guarded move to lost, no manager item (no money moved). The
// lease is not waited out: a claim still claimed is a claim whose dispatch
// never began, so closing it immediately is safe.
func (l *TerminalLedger) CloseClaimedIntent(ctx context.Context, id uuid.UUID) error {
	var got uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_intents
		SET state = 'lost', updated_at = NOW()
		WHERE id = $1 AND state = 'claimed'
		RETURNING id`, id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTerminalIntentConflict
	}
	return err
}

// MarkSpent records that an authorized leg's money is booked on the sale or
// put back at the device. One guarded statement; false answers the leg was
// not an unspent authorized one.
func (l *TerminalLedger) MarkSpent(ctx context.Context, id uuid.UUID) (bool, error) {
	var got uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_intents SET spent_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND state = 'authorized' AND spent_at IS NULL
		RETURNING id`, id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AuthorizeLost is the lost -> authorized move a manager's resolution makes
// on the word of the terminal's own record: the answer's reference is the
// one the manager read. spent says the resolution booked or reversed the
// money (the leg is finished); unspent leaves the authorized leg on its open
// sale for the completion to use. One guarded statement.
func (l *TerminalLedger) AuthorizeLost(ctx context.Context, id uuid.UUID, providerRef string, spent bool) (bool, error) {
	var got uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_intents
		SET state = 'authorized', answer_verdict = 'authorized', answered_at = NOW(),
		    answer_provider_ref = $2, spent_at = CASE WHEN $3 THEN NOW() ELSE NULL END, updated_at = NOW()
		WHERE id = $1 AND state = 'lost'
		RETURNING id`, id, providerRef, spent).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ApplyDeviceAnswer lands one asynchronous verdict, idempotent by provider
// reference: the same verdict twice is a no-op; a conflicting verdict is
// audited and queued for a manager; an answer on an intent the sweep already
// turned lost is classed late, audited and queued beside the sweep's own
// item, the device's verdict and reference kept on the item. The first move
// is ONE guarded statement (pending_at_device -> authorized or declined with
// RETURNING); only a zero row result reads the intent to classify why, so a
// sweep racing the answer is classed late, never conflicting. The intent
// itself never moves twice. An answer for a claimed intent is a defect and
// is refused.
func (l *TerminalLedger) ApplyDeviceAnswer(ctx context.Context, a TerminalAnswer) (AnswerOutcome, error) {
	verdictState := TerminalDeclined
	if a.Verdict == "authorized" {
		verdictState = TerminalAuthorized
	} else if a.Verdict != "declined" {
		return "", fmt.Errorf("the answer's verdict %q is neither authorized nor declined", a.Verdict)
	}
	var moved uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_intents
		SET state = $2, answered_at = NOW(), answer_verdict = $3, answer_brand = $4,
		    answer_last4 = $5, answer_auth_code = $6, answer_provider_ref = $7,
		    provider_ref = COALESCE(NULLIF(provider_ref, ''), NULLIF($7, '')), updated_at = NOW()
		WHERE id = $1 AND state = 'pending_at_device'
		RETURNING id`,
		a.IntentID, string(verdictState), a.Verdict, a.Brand, a.Last4, a.AuthCode, a.ProviderRef).Scan(&moved)
	if err == nil {
		return AnswerApplied, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	in, err := l.Get(ctx, a.IntentID)
	if err != nil {
		return "", err
	}
	switch in.State {
	case TerminalAuthorized, TerminalDeclined:
		if in.AnswerVerdict == a.Verdict {
			return AnswerDuplicate, nil
		}
		return l.queueAnswer(ctx, in, a, "conflicting")
	case TerminalLost:
		return l.queueAnswer(ctx, in, a, "late")
	case TerminalClaimed:
		return "", ErrAnswerForClaimedIntent
	}
	return "", fmt.Errorf("the intent is in no state an answer can land on: %s", in.State)
}

// queueAnswer files the manager item of an answer that cannot land (a
// verdict contradicting the recorded one, or an answer after the sweep) in
// ONE statement: the item is inserted or, when the sweep or an earlier
// answer filed it, updated to carry the device's verdict, reference and
// safe fields. A late authorized answer reopens an item a manager resolved
// declined; an answer against any other resolution is kept on the item and
// audited, never reopening it (the books already moved).
func (l *TerminalLedger) queueAnswer(ctx context.Context, in *TerminalIntent, a TerminalAnswer, why string) (AnswerOutcome, error) {
	var state string
	var reopened bool
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO payment_terminal_lost_items
			(intent_id, branch_id, register_id, transaction_id, kind, amount_cents, device_id, provider_ref,
			 late_verdict, late_provider_ref, late_brand, late_last4, late_auth_code, late_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $8, $10, $11, $12, NOW())
		ON CONFLICT (intent_id) DO UPDATE SET
			late_verdict = EXCLUDED.late_verdict, late_provider_ref = EXCLUDED.late_provider_ref,
			late_brand = EXCLUDED.late_brand, late_last4 = EXCLUDED.late_last4,
			late_auth_code = EXCLUDED.late_auth_code, late_at = NOW(),
			provider_ref = COALESCE(NULLIF(payment_terminal_lost_items.provider_ref, ''), EXCLUDED.provider_ref),
			state = CASE WHEN payment_terminal_lost_items.state = 'resolved_declined' AND EXCLUDED.late_verdict = 'authorized'
			             THEN 'open' ELSE payment_terminal_lost_items.state END,
			resolved_by = CASE WHEN payment_terminal_lost_items.state = 'resolved_declined' AND EXCLUDED.late_verdict = 'authorized'
			                   THEN '' ELSE payment_terminal_lost_items.resolved_by END,
			resolved_at = CASE WHEN payment_terminal_lost_items.state = 'resolved_declined' AND EXCLUDED.late_verdict = 'authorized'
			                   THEN NULL ELSE payment_terminal_lost_items.resolved_at END,
			reopened_at = CASE WHEN payment_terminal_lost_items.state = 'resolved_declined' AND EXCLUDED.late_verdict = 'authorized'
			                   THEN NOW() ELSE payment_terminal_lost_items.reopened_at END
		RETURNING state, COALESCE(reopened_at = NOW(), FALSE)`,
		in.ID, in.BranchID, in.RegisterID, in.TransactionID, in.Kind, in.AmountCents, in.DeviceID, a.ProviderRef,
		a.Verdict, a.Brand, a.Last4, a.AuthCode).Scan(&state, &reopened)
	if err != nil {
		return "", err
	}
	outcome := AnswerLateQueued
	if why == "conflicting" {
		outcome = AnswerConflictQueued
	}
	if reopened {
		outcome = AnswerReopened
	}
	if l.auditLog != nil {
		action := "payment.terminal_answer_" + why
		if outcome == AnswerReopened {
			action = "payment.terminal_answer_reopened"
		}
		if err := l.auditLog.Log(ctx, audit.Entry{
			Action: action, EntityType: "pos_transaction", EntityID: in.TransactionID,
			Changes: map[string]any{
				"intent_id": in.ID, "kind": in.Kind, "amount_cents": in.AmountCents,
				"recorded_verdict": in.AnswerVerdict, "answer_verdict": a.Verdict, "answer_provider_ref": a.ProviderRef,
				"item_state": state,
			}}); err != nil {
			l.logger.Error("the terminal answer audit row was not written", "intent_id", in.ID, "error", err)
		}
	}
	return outcome, nil
}

// SweepResult says what one sweep pass closed and queued.
type SweepResult struct {
	ClaimedClosed  int64
	DispatchedLost int64
}

// Sweep turns intents past their lease into lost, in ONE statement: a claimed
// one never reached the device and is simply closed; a pending_at_device one
// may have moved money, so its move to lost and its manager item (who must
// resolve it, the amount, the register, the device reference) are one CTE:
// no lost intent exists without its item. The sweep reaches the caller's
// branch wall; a context with no branch context (the worker) sweeps every
// branch.
func (l *TerminalLedger) Sweep(ctx context.Context, now time.Time) (*SweepResult, error) {
	branch, grants := reachArgs(ctx)
	var res SweepResult
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		WITH closed AS (
			UPDATE payment_terminal_intents i
			SET state = 'lost', updated_at = NOW()
			WHERE i.state = 'claimed' AND i.lease_until < $1 AND `+middleware.Wall("i", 2, 3)+`
			RETURNING i.id
		), moved AS (
			UPDATE payment_terminal_intents i
			SET state = 'lost', updated_at = NOW()
			WHERE i.state = 'pending_at_device' AND i.lease_until < $1 AND `+middleware.Wall("i", 2, 3)+`
			RETURNING i.id, i.branch_id, i.register_id, i.transaction_id, i.kind, i.amount_cents, i.device_id, i.provider_ref
		), filed AS (
			INSERT INTO payment_terminal_lost_items
				(intent_id, branch_id, register_id, transaction_id, kind, amount_cents, device_id, provider_ref)
			SELECT id, branch_id, register_id, transaction_id, kind, amount_cents, device_id, provider_ref FROM moved
			ON CONFLICT (intent_id) DO NOTHING
			RETURNING id
		)
		SELECT (SELECT count(*) FROM closed), (SELECT count(*) FROM moved), (SELECT count(*) FROM filed)`,
		now, branch, grants).Scan(&res.ClaimedClosed, &res.DispatchedLost, new(int64))
	if err != nil {
		return nil, err
	}
	return &res, nil
}

const lostItemColumns = `
SELECT i.id, i.intent_id, i.branch_id, i.register_id, i.transaction_id, i.kind, i.amount_cents, i.device_id,
       i.provider_ref, i.state, i.resolved_by, i.resolution_reference, i.resolved_at,
       i.late_verdict, i.late_provider_ref, i.late_brand, i.late_last4, i.late_auth_code, i.late_at, i.resolving_until
FROM payment_terminal_lost_items i`

func scanLostItem(row interface {
	Scan(dest ...any) error
}) (*TerminalLostItem, error) {
	var it TerminalLostItem
	if err := row.Scan(&it.ID, &it.IntentID, &it.BranchID, &it.RegisterID, &it.TransactionID, &it.Kind,
		&it.AmountCents, &it.DeviceID, &it.ProviderRef, &it.State, &it.ResolvedBy, &it.ResolutionNote, &it.ResolvedAt,
		&it.LateVerdict, &it.LateProviderRef, &it.LateBrand, &it.LateLast4, &it.LateAuthCode, &it.LateAt,
		&it.ResolvingUntil); err != nil {
		return nil, err
	}
	return &it, nil
}

// OpenLostItems lists the queue's items that still wait on a manager (open
// and being resolved), oldest first, held to the caller's branch wall: with a
// context branch only that branch's rows, with none and a user only the
// branches the user is granted, and every branch for the reach that has no
// wall (an administrator without a header, the worker). branchID is an extra
// filter on top of the wall (nil for none).
func (l *TerminalLedger) OpenLostItems(ctx context.Context, branchID *uuid.UUID) ([]TerminalLostItem, error) {
	branch, grants := reachArgs(ctx)
	rows, err := l.db.GetExecutor(ctx).Query(ctx, lostItemColumns+`
		WHERE i.state IN ('open', 'resolving') AND ($3::uuid IS NULL OR i.branch_id = $3) AND `+middleware.Wall("i", 1, 2)+`
		ORDER BY i.created_at, i.id`, branch, grants, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TerminalLostItem
	for rows.Next() {
		it, err := scanLostItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

// LostItem reads one queue item, with no wall (an internal read).
func (l *TerminalLedger) LostItem(ctx context.Context, id uuid.UUID) (*TerminalLostItem, error) {
	it, err := scanLostItem(l.db.GetExecutor(ctx).QueryRow(ctx, lostItemColumns+` WHERE i.id = $1`, id))
	if err != nil {
		return nil, ErrTerminalIntentNotFound
	}
	return it, nil
}

// LostItemInReach reads one queue item held to the caller's branch wall: an
// item of a branch outside the caller's reach is a missing row.
func (l *TerminalLedger) LostItemInReach(ctx context.Context, id uuid.UUID) (*TerminalLostItem, error) {
	branch, grants := reachArgs(ctx)
	it, err := scanLostItem(l.db.GetExecutor(ctx).QueryRow(ctx,
		lostItemColumns+` WHERE i.id = $1 AND `+middleware.Wall("i", 2, 3), id, branch, grants))
	if err != nil {
		return nil, ErrTerminalIntentNotFound
	}
	return it, nil
}

// LostItemOfIntent reads the queue item an intent filed, nil when none.
func (l *TerminalLedger) LostItemOfIntent(ctx context.Context, intentID uuid.UUID) (*TerminalLostItem, error) {
	it, err := scanLostItem(l.db.GetExecutor(ctx).QueryRow(ctx, lostItemColumns+` WHERE i.intent_id = $1`, intentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return it, err
}

// resolveLostItem is the guarded close of one open item.
func (l *TerminalLedger) resolveLostItem(ctx context.Context, id uuid.UUID, to, by, reference string) (bool, error) {
	var got uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_lost_items
		SET state = $2, resolved_by = $3, resolution_reference = $4, resolved_at = NOW(),
		    resolving_by = '', resolving_until = NULL
		WHERE id = $1 AND state = 'open'
		RETURNING id`, id, to, by, reference).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ResolveLostItemDeclined closes a queue item as declined: the terminal's own
// record said no, so nothing moved and nothing is booked. One guarded
// statement: only an open item resolves.
func (l *TerminalLedger) ResolveLostItemDeclined(ctx context.Context, id uuid.UUID, by, reference string) (bool, error) {
	return l.resolveLostItem(ctx, id, "resolved_declined", by, reference)
}

// ResolveLostItemAuthorized closes a queue item as authorized as the first
// statement of the resolving transaction: a second or racing resolution finds
// the item no longer open and moves nothing. One guarded statement.
func (l *TerminalLedger) ResolveLostItemAuthorized(ctx context.Context, id uuid.UUID, by, reference string) (bool, error) {
	return l.resolveLostItem(ctx, id, "resolved_authorized", by, reference)
}

// BeginReversal claims an item for a resolution that calls the gateway: open
// (or a resolving item whose lease ran out, a crashed resolution) moves to
// resolving, committed before any gateway call, so a second or racing
// resolution is refused and calls nothing. One guarded statement; false
// answers another resolution holds the item or it is settled.
func (l *TerminalLedger) BeginReversal(ctx context.Context, id uuid.UUID, by string, leaseUntil, now time.Time) (bool, error) {
	var got uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_lost_items
		SET state = 'resolving', resolving_by = $2, resolving_until = $3
		WHERE id = $1 AND (state = 'open' OR (state = 'resolving' AND resolving_until < $4))
		RETURNING id`, id, by, leaseUntil, now).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// FinishReversal closes a resolving item authorized once the gateway
// reversal answered, in the booking transaction. Only the holder finishes.
func (l *TerminalLedger) FinishReversal(ctx context.Context, id uuid.UUID, by, reference string) (bool, error) {
	var got uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_lost_items
		SET state = 'resolved_authorized', resolved_by = $2, resolution_reference = $3, resolved_at = NOW(),
		    resolving_by = '', resolving_until = NULL
		WHERE id = $1 AND state = 'resolving' AND resolving_by = $2
		RETURNING id`, id, by, reference).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ReleaseReversal puts a resolving item back to open when its gateway
// reversal failed (nothing moved), so the manager may try again.
func (l *TerminalLedger) ReleaseReversal(ctx context.Context, id uuid.UUID, by string) (bool, error) {
	var got uuid.UUID
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE payment_terminal_lost_items
		SET state = 'open', resolving_by = '', resolving_until = NULL
		WHERE id = $1 AND state = 'resolving' AND resolving_by = $2
		RETURNING id`, id, by).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// OpenIntentsAtRegister answers the open intents (claimed or at the device)
// of one register, so the till close can refuse while one stands.
func (l *TerminalLedger) OpenIntentsAtRegister(ctx context.Context, registerID string) ([]uuid.UUID, error) {
	rows, err := l.db.GetExecutor(ctx).Query(ctx, `
		SELECT id FROM payment_terminal_intents
		WHERE register_id = $1 AND state IN ('claimed', 'pending_at_device')`, registerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
