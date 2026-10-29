// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The scale ticket and device persistence (CP-19). The ticket number comes
// from the column default over scale_ticket_number_seq, so a rolled back
// ticket never hands its number to another, and a void is an update, never
// a delete.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// scaleTicketCols is the ticket projection, newest first when listed.
const scaleTicketCols = `id, ticket_number, revision, branch_id, scale_device_id, vehicle_ref, direction, status,
	ROUND(gross_lb * 10000)::bigint, ROUND(tare_lb * 10000)::bigint, ROUND(net_lb * 10000)::bigint,
	reading_unit, source, manual_reason, manual_note, net_basis, weigh_in_ticket_id, sale_line_id,
	captured_by, captured_at, void_reason, created_at`

func scanScaleTicket(row pgx.Row) (*ScaleTicket, error) {
	var t ScaleTicket
	var gross, tare, net *int64
	var direction, status, source string
	var basis *string
	var capturedAt, created time.Time
	if err := row.Scan(&t.ID, &t.TicketNumber, &t.Revision, &t.BranchID, &t.ScaleDeviceID, &t.VehicleRef,
		&direction, &status, &gross, &tare, &net,
		&t.ReadingUnit, &source, &t.ManualReason, &t.ManualNote, &basis, &t.WeighInTicketID, &t.SaleLineID,
		&t.CapturedBy, &capturedAt, &t.VoidReason, &created); err != nil {
		return nil, err
	}
	t.Direction = TicketDirection(direction)
	t.Status = TicketStatus(status)
	t.Source = WeightSourceName(source)
	if basis != nil {
		lower := strings.ToLower(*basis)
		t.NetBasis = &lower
	}
	t.GrossLb = ptrQuantityOf(gross)
	t.TareLb = ptrQuantityOf(tare)
	t.NetLb = ptrQuantityOf(net)
	t.CapturedAt = httpx.TimestampOf(capturedAt)
	t.CreatedAt = httpx.TimestampOf(created)
	return &t, nil
}

func ptrQuantityOf(v *int64) *httpx.Quantity {
	if v == nil {
		return nil
	}
	q := httpx.Quantity(*v)
	return &q
}

func (r *PostgresRepository) InsertScaleTicket(ctx context.Context, t *ScaleTicket) error {
	row := r.ex(ctx).QueryRow(ctx, `INSERT INTO scale_tickets
		(branch_id, scale_device_id, vehicle_ref, direction, status, gross_lb, tare_lb, net_lb,
			reading_unit, source, manual_reason, manual_note, net_basis, weigh_in_ticket_id, sale_line_id, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6::numeric / 10000, $7::numeric / 10000, $8::numeric / 10000,
			$9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id, ticket_number, revision, created_at`,
		t.BranchID, t.ScaleDeviceID, t.VehicleRef, string(t.Direction), string(t.Status),
		qtyArg(t.GrossLb), qtyArg(t.TareLb), qtyArg(t.NetLb),
		t.ReadingUnit, string(t.Source), t.ManualReason, t.ManualNote, upperOrNil(t.NetBasis),
		t.WeighInTicketID, t.SaleLineID, t.CapturedBy)
	var created time.Time
	if err := row.Scan(&t.ID, &t.TicketNumber, &t.Revision, &created); err != nil {
		return fmt.Errorf("failed to write the scale ticket: %w", mapWriteError(err))
	}
	t.CreatedAt = httpx.TimestampOf(created)
	return nil
}

func upperOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return strings.ToUpper(*s)
}

func (r *PostgresRepository) GetScaleTicket(ctx context.Context, id uuid.UUID) (*ScaleTicket, error) {
	row := r.ex(ctx).QueryRow(ctx, `SELECT `+scaleTicketCols+` FROM scale_tickets t
		WHERE t.id = $1 AND `+middleware.Wall("t", 2, 3),
		id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	t, err := scanScaleTicket(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the scale ticket: %w", err)
	}
	return t, nil
}

// LockScaleTicket reads one ticket FOR UPDATE inside the caller's
// transaction: the void and the weigh out serialize on it. The branch wall
// applies, the way the pos package walls its other reads: a ticket outside
// the caller's reach is a missing row, so the void of another branch's
// ticket is refused before anything moves.
func (r *PostgresRepository) LockScaleTicket(ctx context.Context, id uuid.UUID) (*ScaleTicket, error) {
	row := r.ex(ctx).QueryRow(ctx, `SELECT `+scaleTicketCols+` FROM scale_tickets t
		WHERE t.id = $1 AND `+middleware.Wall("t", 2, 3)+` FOR UPDATE`,
		id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	t, err := scanScaleTicket(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to lock the scale ticket: %w", err)
	}
	return t, nil
}

// FindOpenWeighIn is the vehicle's latest open weigh in on the branch.
func (r *PostgresRepository) FindOpenWeighIn(ctx context.Context, branchID uuid.UUID, vehicleRef string) (*ScaleTicket, error) {
	if vehicleRef == "" {
		return nil, nil
	}
	row := r.ex(ctx).QueryRow(ctx, `SELECT `+scaleTicketCols+` FROM scale_tickets
		WHERE branch_id = $1 AND vehicle_ref = $2 AND direction = 'WEIGH_IN' AND status = 'OPEN'
		ORDER BY created_at DESC, ticket_number DESC LIMIT 1`, branchID, vehicleRef)
	t, err := scanScaleTicket(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find the open weigh in: %w", err)
	}
	return t, nil
}

func (r *PostgresRepository) ListScaleTickets(ctx context.Context, branchID uuid.UUID, status, vehicleRef string) ([]ScaleTicket, error) {
	query := `SELECT ` + scaleTicketCols + ` FROM scale_tickets WHERE branch_id = $1`
	args := []any{branchID}
	if status != "" {
		args = append(args, strings.ToUpper(status))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if vehicleRef != "" {
		args = append(args, vehicleRef)
		query += fmt.Sprintf(" AND vehicle_ref = $%d", len(args))
	}
	query += " ORDER BY ticket_number"
	rows, err := r.ex(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list the scale tickets: %w", err)
	}
	defer rows.Close()
	out := []ScaleTicket{}
	for rows.Next() {
		t, err := scanScaleTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// LinkScaleTicketLine names the sale line the ticket prices, written after
// the line's re-add so the foreign key never chases a deleted row.
func (r *PostgresRepository) LinkScaleTicketLine(ctx context.Context, ticketID, lineID uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx, `UPDATE scale_tickets SET sale_line_id = $2 WHERE id = $1`, ticketID, lineID)
	if err != nil {
		return fmt.Errorf("failed to link the ticket to the line: %w", err)
	}
	return nil
}

// CompleteScaleTicket marks the weigh in weighed out, guarded by the
// revision the caller locked and read.
func (r *PostgresRepository) CompleteScaleTicket(ctx context.Context, id uuid.UUID, revision int64) error {
	tag, err := r.ex(ctx).Exec(ctx, `UPDATE scale_tickets SET status = 'COMPLETED', revision = revision + 1
		WHERE id = $1 AND revision = $2 AND status = 'OPEN'`, id, revision)
	if err != nil {
		return fmt.Errorf("failed to complete the weigh in: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.StaleRevision("the weigh in moved while the truck was on the scale")
	}
	return nil
}

// VoidScaleTicket marks the ticket void with its reason, guarded by the
// revision: the number stays, the row stays.
func (r *PostgresRepository) VoidScaleTicket(ctx context.Context, id uuid.UUID, reason string, revision int64) error {
	tag, err := r.ex(ctx).Exec(ctx, `UPDATE scale_tickets SET status = 'VOID', void_reason = $2, revision = revision + 1
		WHERE id = $1 AND revision = $3 AND status <> 'VOID'`, id, reason, revision)
	if err != nil {
		return fmt.Errorf("failed to void the scale ticket: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.StaleRevision("the ticket moved while the void was written")
	}
	return nil
}

// SetLineWeight writes the weight block onto the counter line.
func (r *PostgresRepository) SetLineWeight(ctx context.Context, lineID uuid.UUID, w LineWeight) error {
	tag, err := r.ex(ctx).Exec(ctx, `UPDATE pos_line_items SET
		weight_source = $2, weight_gross_lb = $3::numeric / 10000, weight_tare_lb = $4::numeric / 10000,
		weight_net_lb = $5::numeric / 10000, weight_net_basis = $6, scale_device_id = $7,
		scale_ticket_id = $8, scale_ticket_number = $9, manual_weight_reason = $10,
		weight_captured_by = $11, weight_captured_at = NOW()
		WHERE id = $1`,
		lineID, strings.ToUpper(string(w.Source)), qtyArg(w.GrossLb), qtyArg(w.TareLb), qtyArg(w.NetLb),
		upperOrNil(w.NetBasis), w.ScaleDeviceID, w.ScaleTicketID, w.ScaleTicketNumber, w.ManualReason, w.CapturedBy)
	if err != nil {
		return fmt.Errorf("failed to write the line's weight: %w", mapWriteError(err))
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("line not found on this sale")
	}
	return nil
}

// ActiveScaleDevice is the branch's one active scale, when it has one.
func (r *PostgresRepository) ActiveScaleDevice(ctx context.Context, branchID uuid.UUID) (*ScaleDevice, error) {
	row := r.ex(ctx).QueryRow(ctx, `SELECT id, branch_id, name, protocol, endpoint, certificate_no, location_note
		FROM scale_devices WHERE branch_id = $1 AND is_active ORDER BY created_at, id LIMIT 1`, branchID)
	var d ScaleDevice
	if err := row.Scan(&d.ID, &d.BranchID, &d.Name, &d.Protocol, &d.Endpoint, &d.CertificateNo, &d.LocationNote); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the branch's scale: %w", err)
	}
	return &d, nil
}
