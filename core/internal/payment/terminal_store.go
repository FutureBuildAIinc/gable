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
	"github.com/google/uuid"
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
}

// TerminalLedger is the intent ledger's store: the guarded state moves, the
// device answers and the sweep. Its statements run through the context's own
// executor, so a caller inside a transaction moves the intent inside it.
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

// Get reads one intent by id.
func (l *TerminalLedger) Get(ctx context.Context, id uuid.UUID) (*TerminalIntent, error) {
	return l.scanIntent(l.db.GetExecutor(ctx).QueryRow(ctx, intentColumns+` WHERE i.id = $1`, id))
}

// GetByKey reads the intent of one leg and kind.
func (l *TerminalLedger) GetByKey(ctx context.Context, transactionID uuid.UUID, legKey, kind string) (*TerminalIntent, error) {
	return l.scanIntent(l.db.GetExecutor(ctx).QueryRow(ctx,
		intentColumns+` WHERE i.transaction_id = $1 AND i.leg_key = $2 AND i.kind = $3`, transactionID, legKey, kind))
}

const intentColumns = `
SELECT i.id, i.transaction_id, i.leg_key, i.register_id, i.branch_id, i.amount_cents, i.kind, i.state,
       i.device_id, i.provider_ref, i.idempotency_key, i.lease_until, i.claimed_at, i.dispatched_at, i.answered_at,
       i.answer_brand, i.answer_last4, i.answer_auth_code, i.answer_provider_ref, i.answer_verdict
FROM payment_terminal_intents i`

func (l *TerminalLedger) scanIntent(row interface {
	Scan(dest ...any) error
}) (*TerminalIntent, error) {
	var in TerminalIntent
	var state string
	if err := row.Scan(&in.ID, &in.TransactionID, &in.LegKey, &in.RegisterID, &in.BranchID, &in.AmountCents,
		&in.Kind, &state, &in.DeviceID, &in.ProviderRef, &in.IdempotencyKey, &in.LeaseUntil, &in.ClaimedAt,
		&in.DispatchedAt, &in.AnsweredAt, &in.AnswerBrand, &in.AnswerLast4, &in.AnswerAuthCode,
		&in.AnswerProviderRef, &in.AnswerVerdict); err != nil {
		return nil, ErrTerminalIntentNotFound
	}
	in.State = TerminalIntentState(state)
	return &in, nil
}

// MarkDispatched is the claimed -> pending_at_device move: one guarded
// statement, the device's own reference and the restamped lease written with
// it. False answers that the intent was not claimed (a contender moved it, or
// the sweep already closed it); the caller re-reads and decides.
func (l *TerminalLedger) MarkDispatched(ctx context.Context, id uuid.UUID, providerRef string, leaseUntil time.Time) (bool, error) {
	tag, err := l.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE payment_terminal_intents
		SET state = 'pending_at_device', provider_ref = $2, lease_until = $3,
		    dispatched_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND state = 'claimed'`, id, providerRef, leaseUntil)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// CloseClaimedIntent is the desk's cancel of a claim the device never
// reached: one guarded move to lost, no manager item (no money moved). The
// lease is not waited out: a claim still claimed is a claim whose dispatch
// never began, so closing it immediately is safe.
func (l *TerminalLedger) CloseClaimedIntent(ctx context.Context, id uuid.UUID) error {
	tag, err := l.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE payment_terminal_intents
		SET state = 'lost', updated_at = NOW()
		WHERE id = $1 AND state = 'claimed'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrTerminalIntentConflict
	}
	return nil
}

// ApplyDeviceAnswer lands one asynchronous verdict, idempotent by provider
// reference: the same verdict twice is a no-op; a conflicting verdict is
// audited and queued for a manager; an answer on an intent the sweep already
// turned lost is audited and queued beside the sweep's own item. The intent
// itself never moves twice.
func (l *TerminalLedger) ApplyDeviceAnswer(ctx context.Context, a TerminalAnswer) (AnswerOutcome, error) {
	in, err := l.Get(ctx, a.IntentID)
	if err != nil {
		return "", err
	}
	verdictState := TerminalDeclined
	if a.Verdict == "authorized" {
		verdictState = TerminalAuthorized
	} else if a.Verdict != "declined" {
		return "", fmt.Errorf("the answer's verdict %q is neither authorized nor declined", a.Verdict)
	}
	switch in.State {
	case TerminalAuthorized, TerminalDeclined:
		if in.AnswerVerdict == a.Verdict {
			return AnswerDuplicate, nil
		}
		if err := l.queueConflictingAnswer(ctx, in, a, "conflicting"); err != nil {
			return "", err
		}
		return AnswerConflictQueued, nil
	}
	switch in.State {
	case TerminalPendingAtDevice:
		tag, err := l.db.GetExecutor(ctx).Exec(ctx, `
			UPDATE payment_terminal_intents
			SET state = $2, answered_at = NOW(), answer_verdict = $3, answer_brand = $4,
			    answer_last4 = $5, answer_auth_code = $6, answer_provider_ref = $7, updated_at = NOW()
			WHERE id = $1 AND state = 'pending_at_device'`,
			a.IntentID, string(verdictState), a.Verdict, a.Brand, a.Last4, a.AuthCode, a.ProviderRef)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() == 1 {
			return AnswerApplied, nil
		}
		// a contender landed an answer between the read and the move: reread
		// and treat the standing answer as this answer's, or queue the conflict
		fresh, err := l.Get(ctx, a.IntentID)
		if err != nil {
			return "", err
		}
		if fresh.AnswerVerdict == a.Verdict {
			return AnswerDuplicate, nil
		}
		if err := l.queueConflictingAnswer(ctx, fresh, a, "conflicting"); err != nil {
			return "", err
		}
		return AnswerConflictQueued, nil
	case TerminalLost, TerminalClaimed:
		if err := l.queueConflictingAnswer(ctx, in, a, "late"); err != nil {
			return "", err
		}
		return AnswerLateQueued, nil
	}
	return "", fmt.Errorf("the intent is in no state an answer can land on: %s", in.State)
}

// queueConflictingAnswer writes the audit row and the manager item of an
// answer that cannot land: a verdict contradicting the recorded one, or an
// answer after the sweep.
func (l *TerminalLedger) queueConflictingAnswer(ctx context.Context, in *TerminalIntent, a TerminalAnswer, why string) error {
	if _, err := l.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO payment_terminal_lost_items
			(intent_id, branch_id, register_id, transaction_id, kind, amount_cents, device_id, provider_ref)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (intent_id) DO NOTHING`,
		in.ID, in.BranchID, in.RegisterID, in.TransactionID, in.Kind, in.AmountCents, in.DeviceID, a.ProviderRef); err != nil {
		return err
	}
	if l.auditLog != nil {
		if err := l.auditLog.Log(ctx, audit.Entry{
			Action: "payment.terminal_answer_" + why, EntityType: "pos_transaction", EntityID: in.TransactionID,
			Changes: map[string]any{
				"intent_id": in.ID, "kind": in.Kind, "amount_cents": in.AmountCents,
				"recorded_verdict": in.AnswerVerdict, "answer_verdict": a.Verdict, "answer_provider_ref": a.ProviderRef,
			}}); err != nil {
			l.logger.Error("the terminal answer audit row was not written", "intent_id", in.ID, "error", err)
		}
	}
	return nil
}

// SweepResult says what one sweep pass closed and queued.
type SweepResult struct {
	ClaimedClosed int64
	DispatchedLost int64
}

// Sweep turns intents past their lease into lost: a claimed one never reached
// the device and is simply closed; a pending_at_device one may have moved
// money, so it becomes a manager item carrying who must resolve it, the
// amount, the register and the device reference.
func (l *TerminalLedger) Sweep(ctx context.Context, now time.Time) (*SweepResult, error) {
	claimed, err := l.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE payment_terminal_intents
		SET state = 'lost', updated_at = NOW()
		WHERE state = 'claimed' AND lease_until < $1`, now)
	if err != nil {
		return nil, err
	}
	dispatched, err := l.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE payment_terminal_intents
		SET state = 'lost', updated_at = NOW()
		WHERE state = 'pending_at_device' AND lease_until < $1`, now)
	if err != nil {
		return nil, err
	}
	// The queue rows are filed from the intents the pass just turned lost, so
	// a manager item exists exactly when a dispatched leg lost its answer
	// (never for a claim that never reached the device).
	if _, err := l.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO payment_terminal_lost_items
			(intent_id, branch_id, register_id, transaction_id, kind, amount_cents, device_id, provider_ref)
		SELECT i.id, i.branch_id, i.register_id, i.transaction_id, i.kind, i.amount_cents, i.device_id, i.provider_ref
		FROM payment_terminal_intents i
		WHERE i.state = 'lost' AND i.dispatched_at IS NOT NULL AND i.answered_at IS NULL AND i.lease_until < $1
		ON CONFLICT (intent_id) DO NOTHING`, now); err != nil {
		return nil, err
	}
	return &SweepResult{ClaimedClosed: claimed.RowsAffected(), DispatchedLost: dispatched.RowsAffected()}, nil
}

// OpenLostItems lists the queue's open items, oldest first, held to the
// caller's branch wall by the caller's own read.
func (l *TerminalLedger) OpenLostItems(ctx context.Context, branchID *uuid.UUID) ([]TerminalLostItem, error) {
	rows, err := l.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, intent_id, branch_id, register_id, transaction_id, kind, amount_cents, device_id,
		       provider_ref, state, resolved_by, resolution_reference, resolved_at
		FROM payment_terminal_lost_items
		WHERE state = 'open' AND ($1::uuid IS NULL OR branch_id = $1)
		ORDER BY created_at, id`, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TerminalLostItem
	for rows.Next() {
		var it TerminalLostItem
		if err := rows.Scan(&it.ID, &it.IntentID, &it.BranchID, &it.RegisterID, &it.TransactionID, &it.Kind,
			&it.AmountCents, &it.DeviceID, &it.ProviderRef, &it.State, &it.ResolvedBy, &it.ResolutionNote, &it.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// LostItem reads one queue item.
func (l *TerminalLedger) LostItem(ctx context.Context, id uuid.UUID) (*TerminalLostItem, error) {
	var it TerminalLostItem
	err := l.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, intent_id, branch_id, register_id, transaction_id, kind, amount_cents, device_id,
		       provider_ref, state, resolved_by, resolution_reference, resolved_at
		FROM payment_terminal_lost_items WHERE id = $1`, id).
		Scan(&it.ID, &it.IntentID, &it.BranchID, &it.RegisterID, &it.TransactionID, &it.Kind,
			&it.AmountCents, &it.DeviceID, &it.ProviderRef, &it.State, &it.ResolvedBy, &it.ResolutionNote, &it.ResolvedAt)
	if err != nil {
		return nil, ErrTerminalIntentNotFound
	}
	return &it, nil
}

// ResolveLostItemDeclined closes a queue item as declined: the terminal's own
// record said no, so nothing moved and nothing is booked. One guarded
// statement: only an open item resolves.
func (l *TerminalLedger) ResolveLostItemDeclined(ctx context.Context, id uuid.UUID, by, reference string) (bool, error) {
	tag, err := l.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE payment_terminal_lost_items
		SET state = 'resolved_declined', resolved_by = $2, resolution_reference = $3, resolved_at = NOW()
		WHERE id = $1 AND state = 'open'`, id, by, reference)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ResolveLostItemAuthorized closes a queue item as authorized after the
// caller booked what the terminal's record said. One guarded statement.
func (l *TerminalLedger) ResolveLostItemAuthorized(ctx context.Context, id uuid.UUID, by, reference string) (bool, error) {
	tag, err := l.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE payment_terminal_lost_items
		SET state = 'resolved_authorized', resolved_by = $2, resolution_reference = $3, resolved_at = NOW()
		WHERE id = $1 AND state = 'open'`, id, by, reference)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
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
