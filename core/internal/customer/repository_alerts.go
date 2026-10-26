// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrAlertNotFound is the sentinel the repository answers with when the
// alert is not there.
var ErrAlertNotFound = errors.New("customer alert not found")

// The states an alert list can hold.
const (
	// AlertStateOpen holds the alerts still on the customer (the default).
	AlertStateOpen = "open"
	// AlertStateEnded holds the alerts that have been ended.
	AlertStateEnded = "ended"
	// AlertStateAll holds both.
	AlertStateAll = "all"
)

// AlertFilter is the alerts list's state and keyset position. The list
// resumes strictly past (AfterTime, AfterID) in (created_at, id) descending
// order.
type AlertFilter struct {
	State     string
	AfterTime *time.Time
	AfterID   uuid.UUID
	Limit     int
}

const alertColumns = `ca.id, ca.customer_id, ca.kind, ca.text, ca.show_at_counter, ca.show_at_orders,
	ca.acknowledge_required, ca.created_by, ca.created_at, ca.ended_by, ca.ended_at, ca.revision`

const alertJoin = ` FROM customer_alerts ca JOIN customers c ON c.id = ca.customer_id`

func scanAlert(row pgx.Row, a *Alert) error {
	var (
		kind    string
		endedBy *string
		endedAt *time.Time
		created time.Time
	)
	if err := row.Scan(&a.ID, &a.CustomerID, &kind, &a.Text, &a.ShowAtCounter, &a.ShowAtOrders,
		&a.AcknowledgeRequired, &a.CreatedBy, &created, &endedBy, &endedAt, &a.Revision); err != nil {
		return err
	}
	a.Kind = AlertKind(kind)
	a.CreatedAt = httpx.TimestampOf(created)
	if endedBy != nil {
		a.EndedBy = endedBy
	}
	if endedAt != nil {
		t := httpx.TimestampOf(*endedAt)
		a.EndedAt = &t
	}
	return nil
}

// InsertAlert writes a new alert of a customer.
func (r *PostgresRepository) InsertAlert(ctx context.Context, a *Alert) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO customer_alerts (id, customer_id, kind, text, show_at_counter, show_at_orders,
			acknowledge_required, created_by, created_at, revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1)`,
		a.ID, a.CustomerID, string(a.Kind), a.Text, a.ShowAtCounter, a.ShowAtOrders,
		a.AcknowledgeRequired, a.CreatedBy, a.CreatedAt.Time)
	if err != nil {
		return fmt.Errorf("failed to insert alert: %w", err)
	}
	return nil
}

// GetAlert reads one alert.
func (r *PostgresRepository) GetAlert(ctx context.Context, id uuid.UUID) (*Alert, error) {
	a := &Alert{}
	err := scanAlert(r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+alertColumns+alertJoin+`
		WHERE ca.id = $1 AND `+wall("c", 2, 3), id, branchctx.CustomerReadBranchForQuery(ctx), branchctx.CustomerReadGrantsSubForQuery(ctx)), a)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAlertNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get alert: %w", err)
	}
	return a, nil
}

// LockAlert takes the alert's row lock for the rest of the transaction, so a
// revision check and the end after it are one act.
func (r *PostgresRepository) LockAlert(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT ca.id`+alertJoin+`
		WHERE ca.id = $1 AND `+wall("c", 2, 3)+` FOR UPDATE OF ca`,
		id, branchctx.CustomerReadBranchForQuery(ctx), branchctx.CustomerReadGrantsSubForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAlertNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock alert: %w", err)
	}
	return nil
}

// EndAlert ends the alert: the row stays for the audit trail, its revision
// moves and the end says who called it off.
func (r *PostgresRepository) EndAlert(ctx context.Context, a *Alert) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE customer_alerts SET ended_by = $2, ended_at = $3, revision = revision + 1
		WHERE id = $1`,
		a.ID, nullableText(a.EndedBy), a.EndedAt.Time)
	if err != nil {
		return fmt.Errorf("failed to end alert: %w", err)
	}
	return nil
}

// alertWhere holds the list's conditions: the customer, the branch wall and
// the state the list was asked for.
func alertWhere(ctx context.Context, customerID uuid.UUID, f AlertFilter, withCursor bool) (string, []any) {
	args := []any{customerID, branchctx.CustomerReadBranchForQuery(ctx), branchctx.CustomerReadGrantsSubForQuery(ctx)}
	conds := []string{"ca.customer_id = $1", wall("c", 2, 3)}
	switch f.State {
	case AlertStateEnded:
		conds = append(conds, "ca.ended_at IS NOT NULL")
	case AlertStateAll:
	default:
		conds = append(conds, "ca.ended_at IS NULL")
	}
	if withCursor && f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(ca.created_at, ca.id) < ($%d, $%d)`, len(args)-1, len(args)))
	}
	return strings.Join(conds, " AND "), args
}

// ListAlerts is one page of a customer's alerts, newest first.
func (r *PostgresRepository) ListAlerts(ctx context.Context, customerID uuid.UUID, f AlertFilter) ([]Alert, error) {
	where, args := alertWhere(ctx, customerID, f, true)
	args = append(args, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `SELECT `+alertColumns+alertJoin+` WHERE `+where+
		fmt.Sprintf(` ORDER BY ca.created_at DESC, ca.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list alerts: %w", err)
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		if err := scanAlert(rows, &a); err != nil {
			return nil, fmt.Errorf("failed to scan alert: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountAlerts counts the alerts of a customer in the state the list holds.
func (r *PostgresRepository) CountAlerts(ctx context.Context, customerID uuid.UUID, f AlertFilter) (int64, error) {
	where, args := alertWhere(ctx, customerID, f, false)
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT COUNT(*)`+alertJoin+` WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count alerts: %w", err)
	}
	return n, nil
}
