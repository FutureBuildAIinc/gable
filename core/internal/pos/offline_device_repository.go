// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The device rows and the recorded mark (ADR 0011 section 6.1, slice O2).

const registerDeviceCols = `register_id, epoch, public_key, enrolled_by, enrolled_at, revoked_by, revoked_at,
	revoke_reason, high_seq, chain_head, confirmed_seq, reported_seq`

func scanRegisterDevice(row pgx.Row) (*RegisterDevice, error) {
	d := &RegisterDevice{}
	var enrolledAt time.Time
	var revokedAt *time.Time
	if err := row.Scan(&d.RegisterID, &d.DeviceEpoch, &d.PublicKey, &d.EnrolledBy, &enrolledAt, &d.RevokedBy, &revokedAt,
		&d.RevokeReason, &d.HighSeq, &d.ChainHead, &d.ConfirmedSeq, &d.ReportedSeq); err != nil {
		return nil, err
	}
	d.EnrolledAt = httpx.TimestampOf(enrolledAt)
	if revokedAt != nil {
		ts := httpx.TimestampOf(*revokedAt)
		d.RevokedAt = &ts
	}
	return d, nil
}

// LockRegisterForEnrolment takes the register's row FOR NO KEY UPDATE, so two
// enrolments or revocations of one register serialize while a sale's foreign
// key share on the row never waits for them. It answers false when no such
// register exists.
func (r *PostgresRepository) LockRegisterForEnrolment(ctx context.Context, register string) (bool, error) {
	var id string
	err := r.ex(ctx).QueryRow(ctx, `SELECT id FROM pos_registers WHERE id = $1 FOR NO KEY UPDATE`, register).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to lock the register: %w", err)
	}
	return true, nil
}

// RegisterDevices reads the register's active enrolment, if any, and its
// latest revoked one, and the highest epoch it has ever held.
func (r *PostgresRepository) RegisterDevices(ctx context.Context, register string) (active, previous *RegisterDevice, maxEpoch int64, err error) {
	if err = r.ex(ctx).QueryRow(ctx, `SELECT COALESCE(MAX(epoch), 0) FROM pos_register_devices WHERE register_id = $1`,
		register).Scan(&maxEpoch); err != nil {
		return nil, nil, 0, fmt.Errorf("failed to read the register's epochs: %w", err)
	}
	active, err = scanRegisterDevice(r.ex(ctx).QueryRow(ctx, `SELECT `+registerDeviceCols+` FROM pos_register_devices
		WHERE register_id = $1 AND revoked_at IS NULL`, register))
	if errors.Is(err, pgx.ErrNoRows) {
		active, err = nil, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to read the register's enrolment: %w", err)
	}
	previous, err = scanRegisterDevice(r.ex(ctx).QueryRow(ctx, `SELECT `+registerDeviceCols+` FROM pos_register_devices
		WHERE register_id = $1 AND revoked_at IS NOT NULL ORDER BY epoch DESC LIMIT 1`, register))
	if errors.Is(err, pgx.ErrNoRows) {
		previous, err = nil, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to read the register's last enrolment: %w", err)
	}
	return active, previous, maxEpoch, nil
}

// PublicKeyEnrolled answers whether any enrolment, of any register or
// epoch, ever held this key.
func (r *PostgresRepository) PublicKeyEnrolled(ctx context.Context, key string) (bool, error) {
	var used bool
	if err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pos_register_devices WHERE lower(public_key) = lower($1))`,
		key).Scan(&used); err != nil {
		return false, fmt.Errorf("failed to read the enrolled keys: %w", err)
	}
	return used, nil
}

// RevokeRegisterDevice marks the register's enrolment of an epoch revoked.
// The caller holds lock 0a on it (LockRegisterDevice), so a sync of that
// epoch in flight finishes first and every later one reads the revocation.
func (r *PostgresRepository) RevokeRegisterDevice(ctx context.Context, register string, epoch int64, by, reason string) (*RegisterDevice, error) {
	d, err := scanRegisterDevice(r.ex(ctx).QueryRow(ctx, `UPDATE pos_register_devices
		SET revoked_at = NOW(), revoked_by = $3, revoke_reason = $4, updated_at = NOW()
		WHERE register_id = $1 AND epoch = $2 AND revoked_at IS NULL
		RETURNING `+registerDeviceCols, register, epoch, by, reason))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to revoke the register's device: %w", err)
	}
	return d, nil
}

// InsertRegisterDevice records a new enrolment, its chain starting at
// GENESIS (the head the sync verifies a new epoch's first sale against).
func (r *PostgresRepository) InsertRegisterDevice(ctx context.Context, register string, branch uuid.UUID, epoch int64, key, by string) (*RegisterDevice, error) {
	d, err := scanRegisterDevice(r.ex(ctx).QueryRow(ctx, `INSERT INTO pos_register_devices
		(register_id, branch_id, epoch, public_key, enrolled_by, chain_head)
		VALUES ($1, $2, $3, $4, $5, 'GENESIS') RETURNING `+registerDeviceCols, register, branch, epoch, key, by))
	if err != nil {
		return nil, fmt.Errorf("failed to enrol the register's device: %w", mapWriteError(err))
	}
	return d, nil
}

// RecordedMark is the highest sequence the server has recorded for a
// register's epoch (ADR 0011 section 6.2): a posted sale, or an exception
// row that holds its number (a held row counted), or the device row's own
// mark. A replay_mismatch row advances nothing and counts for nothing; a
// bad_signature row's number is not proven; a gap or unknown tail item names
// a range, not a sale.
func (r *PostgresRepository) RecordedMark(ctx context.Context, register string, epoch int64) (int64, error) {
	var mark int64
	err := r.ex(ctx).QueryRow(ctx, `SELECT GREATEST(
		COALESCE((SELECT MAX(device_seq) FROM pos_transactions WHERE register_id = $1 AND device_epoch = $2), 0),
		COALESCE((SELECT MAX(device_seq) FROM pos_offline_exceptions WHERE register_id = $1 AND device_epoch = $2
			AND reason_code NOT IN ('replay_mismatch', 'bad_signature', 'sequence_gap', 'unknown_tail')), 0),
		COALESCE((SELECT high_seq FROM pos_register_devices WHERE register_id = $1 AND epoch = $2), 0))`,
		register, epoch).Scan(&mark)
	if err != nil {
		return 0, fmt.Errorf("failed to read the register's recorded mark: %w", err)
	}
	return mark, nil
}

// OpenBadCopy answers whether the register holds an open bad_signature row of
// the epoch: a copy whose true sale has not arrived yet.
func (r *PostgresRepository) OpenBadCopy(ctx context.Context, register string, epoch int64) (*int64, error) {
	var seq int64
	err := r.ex(ctx).QueryRow(ctx, `SELECT device_seq FROM pos_offline_exceptions
		WHERE register_id = $1 AND device_epoch = $2 AND reason_code = 'bad_signature' AND status = 'open'
		ORDER BY device_seq LIMIT 1`, register, epoch).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the register's open bad copies: %w", err)
	}
	return &seq, nil
}

// RegisterOpenSession is the register's open till session and its business
// date (the day it opened in its branch's zone), nil when none is open.
func (r *PostgresRepository) RegisterOpenSession(ctx context.Context, register string) (*offlineOpenSession, error) {
	s := &offlineOpenSession{}
	err := r.ex(ctx).QueryRow(ctx, `SELECT t.id, t.cashier_id, t.opened_at, ROUND(COALESCE(t.opening_float, 0) * 100)::bigint,
		`+offlineSessionDate+` FROM till_sessions t WHERE t.register_id = $1 AND t.status = 'OPEN'
		ORDER BY t.opened_at DESC LIMIT 1`, register).
		Scan(&s.ID, &s.CashierID, &s.OpenedAt, &s.OpeningFloatCents, &s.BusinessDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the register's open session: %w", err)
	}
	return s, nil
}

// UnknownTailRecorded answers whether the register already holds an unknown
// tail item of an epoch, so a later enrolment never writes a second one.
func (r *PostgresRepository) UnknownTailRecorded(ctx context.Context, register string, epoch int64) (bool, error) {
	var found bool
	if err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pos_offline_exceptions
		WHERE register_id = $1 AND device_epoch = $2 AND reason_code = 'unknown_tail')`, register, epoch).Scan(&found); err != nil {
		return false, fmt.Errorf("failed to read the register's unknown tail items: %w", err)
	}
	return found, nil
}

// InsertUnknownTail writes the unknown tail item a re-enrolment over a lost
// or rolled back store leaves (ADR 0011 section 5.1): the old epoch, the
// missing numbers as a range, the register's open session when one is open
// (so it holds the till close until a manager accepts it).
func (r *PostgresRepository) InsertUnknownTail(ctx context.Context, register string, branch uuid.UUID, epoch, from, to int64,
	session *uuid.UUID, businessDate time.Time, message string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.ex(ctx).QueryRow(ctx, `INSERT INTO pos_offline_exceptions (sale_id, register_id, branch_id, device_epoch,
			device_seq, gap_from_seq, gap_to_seq, offline_ref, till_session_id, business_date, reason_code, reason_message, status)
		VALUES ($1, $2, $3, $4, $5, $5, $6, '', $7, $8, 'unknown_tail', $9, 'open') RETURNING id`,
		uuid.New(), register, branch, epoch, from, to, session, businessDate, message).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to write the unknown tail item: %w", mapWriteError(err))
	}
	return id, nil
}
