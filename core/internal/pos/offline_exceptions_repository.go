// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The offline exception queue's reads and writes (ADR 0011 3.4, slice O4).

// offlineExceptionListCols are the columns the manager's list and the
// resolution read, aliased e.
const offlineExceptionListCols = `e.id, e.sale_id, e.register_id, e.branch_id, e.device_epoch, e.device_seq,
	e.gap_from_seq, e.gap_to_seq, e.offline_ref, e.till_session_id, e.business_date, e.customer_id,
	e.warning_alert_ids, e.signed_sale, e.signature_digest, e.reason_code, e.reason_message, e.cash_cents,
	e.check_cents, e.account_cents, e.total_cents, e.status, e.resolution_action, e.resolution_reason,
	e.resolved_by, e.resolved_at, e.posted_sale_id, e.revision, e.created_at`

func scanOfflineExceptionFull(row pgx.Row) (*OfflineException, error) {
	e := &OfflineException{}
	var warnings, signed []byte
	var date time.Time
	var resolvedAt *time.Time
	var created time.Time
	err := row.Scan(&e.ID, &e.SaleID, &e.RegisterID, &e.BranchID, &e.DeviceEpoch, &e.DeviceSeq, &e.GapFromSeq,
		&e.GapToSeq, &e.OfflineRef, &e.TillSessionID, &date, &e.CustomerID, &warnings, &signed, &e.signatureDigest,
		&e.ReasonCode, &e.ReasonMessage, &e.CashCents, &e.CheckCents, &e.AccountCents, &e.TotalCents, &e.Status,
		&e.ResolutionAction, &e.ResolutionReason, &e.ResolvedBy, &resolvedAt, &e.PostedSaleID, &e.Revision, &created)
	if err != nil {
		return nil, err
	}
	e.BusinessDate = date.Format("2006-01-02")
	e.WarningAlertIDs = []uuid.UUID{}
	if len(warnings) > 0 {
		if err := json.Unmarshal(warnings, &e.WarningAlertIDs); err != nil {
			return nil, fmt.Errorf("failed to read the exception's warnings: %w", err)
		}
	}
	e.SignedSale = json.RawMessage(signed)
	e.ResolvedAt = httpx.PtrTimestamp(resolvedAt)
	e.CreatedAt = httpx.TimestampOf(created)
	e.finish()
	return e, nil
}

// OfflineExceptionHead reads an exception's branch, register and epoch
// without a lock or a wall, so the caller can hold the branch by name and
// take lock 0a before the row itself (ADR 0011 3.2).
func (r *PostgresRepository) OfflineExceptionHead(ctx context.Context, id uuid.UUID) (*offlineExceptionHead, error) {
	h := &offlineExceptionHead{}
	err := r.ex(ctx).QueryRow(ctx, `SELECT branch_id, register_id, device_epoch FROM pos_offline_exceptions WHERE id = $1`, id).
		Scan(&h.BranchID, &h.RegisterID, &h.DeviceEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the offline exception: %w", err)
	}
	return h, nil
}

// LockOfflineException takes one exception row FOR UPDATE (lock 0b, ADR
// 0011 3.2), after the caller holds the register's device row.
func (r *PostgresRepository) LockOfflineException(ctx context.Context, id uuid.UUID) (*OfflineException, error) {
	e, err := scanOfflineExceptionFull(r.ex(ctx).QueryRow(ctx, `SELECT `+offlineExceptionListCols+`
		FROM pos_offline_exceptions e WHERE e.id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock the offline exception: %w", err)
	}
	return e, nil
}

// GetOfflineException reads one exception inside the caller's wall.
func (r *PostgresRepository) GetOfflineException(ctx context.Context, id uuid.UUID) (*OfflineException, error) {
	e, err := scanOfflineExceptionFull(r.ex(ctx).QueryRow(ctx, `SELECT `+offlineExceptionListCols+`
		FROM pos_offline_exceptions e WHERE e.id = $1 AND `+middleware.Wall("e", 2, 3), id,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the offline exception: %w", err)
	}
	return e, nil
}

// ListOfflineExceptions reads a branch's exceptions of the given statuses,
// oldest first, inside the caller's wall.
func (r *PostgresRepository) ListOfflineExceptions(ctx context.Context, branch uuid.UUID, statuses []string,
	after *time.Time, afterID *uuid.UUID, limit int) ([]OfflineException, bool, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+offlineExceptionListCols+` FROM pos_offline_exceptions e
		WHERE e.branch_id = $1 AND e.status = ANY($2::text[]) AND `+middleware.Wall("e", 3, 4)+`
		  AND ($5::timestamptz IS NULL OR (e.created_at, e.id) > ($5::timestamptz, $6::uuid))
		ORDER BY e.created_at, e.id LIMIT $7`,
		branch, statuses, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), after, afterID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list the offline exceptions: %w", err)
	}
	defer rows.Close()
	out := []OfflineException{}
	for rows.Next() {
		e, err := scanOfflineExceptionFull(rows)
		if err != nil {
			return nil, false, fmt.Errorf("failed to read an offline exception: %w", err)
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// ResolveOfflineExceptionRow records a manager's resolution on the locked
// row and moves its revision.
func (r *PostgresRepository) ResolveOfflineExceptionRow(ctx context.Context, id uuid.UUID, status, action, reason,
	actor string, posted *uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_offline_exceptions SET status = $2, resolution_action = $3,
		resolution_reason = $4, resolved_by = $5, resolved_at = NOW(), posted_sale_id = COALESCE($6, posted_sale_id),
		revision = revision + 1, updated_at = NOW() WHERE id = $1`, id, status, action, reason, actor, posted); err != nil {
		return fmt.Errorf("failed to resolve the offline exception: %w", mapWriteError(err))
	}
	return nil
}

// SetOfflineExceptionPosted names the sale a resolution posted.
func (r *PostgresRepository) SetOfflineExceptionPosted(ctx context.Context, id, sale uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_offline_exceptions SET posted_sale_id = $2, updated_at = NOW()
		WHERE id = $1`, id, sale); err != nil {
		return fmt.Errorf("failed to record the posted sale: %w", err)
	}
	return nil
}

// InsertOfflineGap writes the sequence_gap item a manager's accept or write
// off of a held item leaves (ADR 0011 3.4): it names the missing numbers,
// sits at the first of them, and is resolved as it is written, so it holds
// no close; a late item of that range closes it as found.
func (r *PostgresRepository) InsertOfflineGap(ctx context.Context, held *OfflineException, from, to int64,
	action, reason, actor string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.ex(ctx).QueryRow(ctx, `INSERT INTO pos_offline_exceptions (sale_id, register_id, branch_id, device_epoch,
			device_seq, gap_from_seq, gap_to_seq, till_session_id, business_date, reason_code, reason_message, status,
			resolution_action, resolution_reason, resolved_by, resolved_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $4, $5, $6, $7::date, 'sequence_gap', $8, 'resolved', $9, $10, $11, NOW())
		RETURNING id`, held.RegisterID, held.BranchID, held.DeviceEpoch, from, to, held.TillSessionID, held.BusinessDate,
		fmt.Sprintf("a manager accepted the missing sequence %d to %d of epoch %d", from, to, held.DeviceEpoch),
		action, reason, actor).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to write the sequence gap item: %w", mapWriteError(err))
	}
	return id, nil
}

// OfflineHeldBelow reports whether a held item of the register's epoch waits
// at a lower sequence than seq.
func (r *PostgresRepository) OfflineHeldBelow(ctx context.Context, register string, epoch, seq int64) (bool, error) {
	var ok bool
	err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pos_offline_exceptions WHERE register_id = $1
		AND device_epoch = $2 AND device_seq < $3 AND status = 'held')`, register, epoch, seq).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("failed to read the register's held items: %w", err)
	}
	return ok, nil
}

// MarkResolvedOfflineSale writes the offline provenance of a sale a
// manager's resolution posts (ADR 0011 3.4): as MarkOfflineSale, with the
// device sequence left null for an accepted sequence_reused item, and, for a
// late post, no till session and the closed one kept in offline_session_id.
func (r *PostgresRepository) MarkResolvedOfflineSale(ctx context.Context, id uuid.UUID, it *offlineItem, snapshot uuid.UUID,
	seq *int64, offlineSession *uuid.UUID) error {
	s := &it.Sale
	var soldAt *time.Time
	if t, err := time.Parse(time.RFC3339Nano, s.SoldAt); err == nil {
		soldAt = &t
	}
	_, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET device_epoch = $2, device_seq = $3, offline_ref = $4,
		snapshot_id = $5, signature_digest = $6, replaces_sale_id = $7, different_customer = $8, unknown_cart_id = $9,
		queue_count = $10, queue_total_cents = $11, confirmed_mark = $12, client_created_at = $13, customer_po = $14,
		job_id = $15, buyer_contact_id = $16,
		till_session_id = CASE WHEN $17::uuid IS NULL THEN till_session_id ELSE NULL END,
		offline_session_id = $17
		WHERE id = $1`, id, s.DeviceEpoch, seq, s.OfflineRef, snapshot, it.Digest, uuidArg(s.ReplacesSaleID),
		s.DifferentCustomer, uuidArg(s.UnknownCartID), s.QueueCount, s.QueueTotalCents, s.ConfirmedMark, soldAt,
		strArg(s.CustomerPO), uuidArg(s.JobID), uuidArg(s.BuyerContactID), offlineSession)
	if err != nil {
		return fmt.Errorf("failed to record the offline sale: %w", mapWriteError(err))
	}
	return nil
}

// EarliestOpenBusinessDate is the late post's date (ADR 0011 3.4): the first
// date from the given one up to the branch's today that is neither a closed
// branch day nor inside a closed fiscal period. False when there is none.
func (r *PostgresRepository) EarliestOpenBusinessDate(ctx context.Context, branch uuid.UUID, from time.Time) (time.Time, bool, error) {
	var d time.Time
	err := r.ex(ctx).QueryRow(ctx, `SELECT d::date FROM generate_series($2::date,
			GREATEST($2::date, (NOW() AT TIME ZONE `+branchZoneSQL+`)::date), interval '1 day') AS d
		WHERE NOT EXISTS (SELECT 1 FROM till_day_closes c WHERE c.branch_id = $1 AND c.status = 'CLOSED'
				AND c.business_date = d::date)
		  AND NOT EXISTS (SELECT 1 FROM gl_fiscal_periods p WHERE p.status = 'CLOSED'
				AND d::date BETWEEN p.start_date AND p.end_date)
		ORDER BY d LIMIT 1`, branch, from).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("failed to find the earliest open business date: %w", err)
	}
	return d, true, nil
}

// The close's reads of the exception rows (ADR 0011 3.2): plain reads that
// take no row lock, made after the close holds the session or the day lock,
// so a close never waits for a resolution's lock 0b. A posted
// sold_past_warning row holds no close (lead ruling, entry 623); every other
// open or held row does.
const offlineExceptionHolds = `e.status IN ('open', 'held') AND e.reason_code <> 'sold_past_warning'`

// OpenOfflineExceptionsOfSession lists the offline references (or ids) of the
// open exceptions naming a session.
func (r *PostgresRepository) OpenOfflineExceptionsOfSession(ctx context.Context, session uuid.UUID) ([]string, error) {
	return r.offlineHoldRefs(ctx, `SELECT COALESCE(NULLIF(e.offline_ref, ''), e.id::text) FROM pos_offline_exceptions e
		WHERE e.till_session_id = $1 AND `+offlineExceptionHolds+` ORDER BY e.device_seq, e.id`, session)
}

// OpenOfflineExceptionsOfDay lists the open exceptions of a branch that name
// a session of the business date or earlier, or, naming no session, carry
// such a date.
func (r *PostgresRepository) OpenOfflineExceptionsOfDay(ctx context.Context, branch uuid.UUID, date string) ([]string, error) {
	return r.offlineHoldRefs(ctx, `SELECT COALESCE(NULLIF(e.offline_ref, ''), e.id::text) FROM pos_offline_exceptions e
		LEFT JOIN till_sessions t ON t.id = e.till_session_id
		WHERE e.branch_id = $1 AND `+offlineExceptionHolds+`
		  AND COALESCE((t.opened_at AT TIME ZONE `+branchZoneSQL+`)::date, e.business_date) <= $2::date
		ORDER BY e.created_at, e.id`, branch, date)
}

func (r *PostgresRepository) offlineHoldRefs(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := r.ex(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to read the open offline exceptions: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// OfflineUndrained reports the register's enrolled device whose own reported
// mark is past the mark the server recorded (ADR 0011 section 2: a sale the
// device assigned has not reached the server), as a plain read.
func (r *PostgresRepository) OfflineUndrained(ctx context.Context, register string) (epoch, reported, high int64, found bool, err error) {
	err = r.ex(ctx).QueryRow(ctx, `SELECT epoch, reported_seq, high_seq FROM pos_register_devices
		WHERE register_id = $1 AND revoked_at IS NULL AND reported_seq > high_seq`, register).Scan(&epoch, &reported, &high)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, false, fmt.Errorf("failed to read the register's offline mark: %w", err)
	}
	return epoch, reported, high, true, nil
}
