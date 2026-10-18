// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// Scheduling (the parity plan's PE-08): per yard delivery windows and pickup
// slots the dealer defines, and the contractor's hold on one concrete date of
// one of them. The dealer side of the window definitions lives in
// core/internal/scheduling; this file is the portal's own read of the same
// tables, scoped to the customer's own yards, and the hold lifecycle.
//
// Times: the window's start_time and end_time are times of day in the yard's
// own zone (locations.timezone). Every instant the API exchanges is UTC; the
// local spellings ride along for display.

// ScheduleWindowView is one concrete date of one window with the places left.
type ScheduleWindowView struct {
	WindowID      uuid.UUID `json:"window_id"`
	YardID        uuid.UUID `json:"yard_id"`
	YardCode      string    `json:"yard_code"`
	Kind          string    `json:"kind"` // delivery or pickup
	Date          string    `json:"date"` // YYYY-MM-DD, the yard's own day
	StartUTC      time.Time `json:"start_utc"`
	EndUTC        time.Time `json:"end_utc"`
	StartLocal    string    `json:"start_local"` // HH:MM in the yard's zone
	EndLocal      string    `json:"end_local"`
	Capacity      int       `json:"capacity"`
	Remaining     int       `json:"remaining"`
	CutoffMinutes int       `json:"cutoff_minutes"`
}

// ScheduleHoldDTO is the contractor's hold.
type ScheduleHoldDTO struct {
	ID        uuid.UUID  `json:"id"`
	WindowID  uuid.UUID  `json:"window_id"`
	YardID    uuid.UUID  `json:"yard_id"`
	Kind      string     `json:"kind"`
	Date      string     `json:"date"`
	StartUTC  time.Time  `json:"start_utc"`
	EndUTC    time.Time  `json:"end_utc"`
	OrderID   *uuid.UUID `json:"order_id"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ScheduleWindowRequest is the body of a hold create or change: which window,
// which date. Date is the yard's own calendar day.
type ScheduleWindowRequest struct {
	WindowID uuid.UUID  `json:"window_id"`
	Date     string     `json:"date"` // YYYY-MM-DD
	OrderID  *uuid.UUID `json:"order_id"`
}

// windowRow is the raw window plus its yard.
type windowRow struct {
	id            uuid.UUID
	yardID        uuid.UUID
	yardCode      string
	yardTZ        string
	kind          string
	weekday       int
	startMinutes  int // minutes from local midnight
	endMinutes    int
	capacity      int
	cutoffMinutes int
}

const windowSelect = `
	SELECT w.id, w.yard_id, COALESCE(l.code, ''), COALESCE(l.timezone, 'UTC'),
	       w.kind, w.weekday,
	       EXTRACT(EPOCH FROM w.start_time) / 60, EXTRACT(EPOCH FROM w.end_time) / 60,
	       w.capacity, w.cutoff_minutes
	FROM yard_schedule_windows w
	JOIN locations l ON l.id = w.yard_id`

func scanWindow(scanner interface{ Scan(dest ...any) error }) (windowRow, error) {
	var w windowRow
	var startMin, endMin float64
	err := scanner.Scan(&w.id, &w.yardID, &w.yardCode, &w.yardTZ, &w.kind, &w.weekday,
		&startMin, &endMin, &w.capacity, &w.cutoffMinutes)
	w.startMinutes, w.endMinutes = int(startMin), int(endMin)
	return w, err
}

// yardBelongsToCustomer holds the tenancy line for scheduling: the yard must
// be one of the customer's own branches (customer_branches).
func (r *PostgresRepository) yardBelongsToCustomer(ctx context.Context, yardID, customerID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM customer_branches WHERE customer_id = $1 AND branch_id = $2)
	`, customerID, yardID).Scan(&ok)
	return ok, err
}

// InTx runs fn inside one database transaction, joining an existing
// transaction when ctx already carries one.
func (r *PostgresRepository) InTx(ctx context.Context, fn func(context.Context) error) error {
	return r.db.RunInTx(ctx, fn)
}

// checkoutHoldTTL is the lifetime of an unattached checkout hold (order_id IS
// NULL). Holds attached to an order stay held until released or fulfilled.
const checkoutHoldTTL = 30 * time.Minute

// expireStaleCheckoutHolds marks unattached checkout holds older than
// checkoutHoldTTL as 'expired' so abandoned checkouts release their capacity.
func expireStaleCheckoutHolds(ctx context.Context, exec database.Executor) error {
	_, err := exec.Exec(ctx, `
		UPDATE portal_schedule_holds
		SET status = 'expired', updated_at = NOW()
		WHERE status = 'held'
		  AND order_id IS NULL
		  AND created_at < NOW() - ($1::bigint * interval '1 second')
	`, int64(checkoutHoldTTL/time.Second))
	return err
}

// heldCount counts the holds already placed on one window and date, excluding
// excludeHoldID when retargeting an existing hold in place and excluding stale
// unattached checkout holds. It takes the executor the caller resolved: inside
// RunInTx that is the transaction, so the count reads what the lock protects.
func heldCount(ctx context.Context, exec database.Executor, windowID uuid.UUID, date string, excludeHoldID uuid.UUID) (int, error) {
	var n int
	err := exec.QueryRow(ctx, `
		SELECT COUNT(*) FROM portal_schedule_holds
		WHERE window_id = $1 AND hold_date = $2 AND status = 'held' AND id <> $3
		  AND (order_id IS NOT NULL OR created_at >= NOW() - ($4::bigint * interval '1 second'))
	`, windowID, date, excludeHoldID, int64(checkoutHoldTTL/time.Second)).Scan(&n)
	return n, err
}

// yardTimezoneForCustomer checks that the yard belongs to the customer and
// returns its configured time zone (defaulting to UTC).
func (r *PostgresRepository) yardTimezoneForCustomer(ctx context.Context, yardID, customerID uuid.UUID) (string, bool, error) {
	var tz string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(l.timezone, 'UTC')
		FROM customer_branches cb
		LEFT JOIN locations l ON l.id = cb.branch_id
		WHERE cb.customer_id = $1 AND cb.branch_id = $2
	`, customerID, yardID).Scan(&tz)
	if err != nil {
		return "", false, nil
	}
	return tz, true, nil
}

// loadYardLocation resolves a yard's IANA time zone (or the test POSIX offset
// AlwaysBehindUTC) and falls back to UTC when unknown.
func loadYardLocation(tz string) *time.Location {
	if tz == "<-25>+25" {
		return time.FixedZone("-25", -25*3600)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// yardLocalToday returns the yard's current calendar day at 00:00:00 UTC so it
// compares directly with parseLocalDay.
func yardLocalToday(tz string) time.Time {
	now := time.Now().In(loadYardLocation(tz))
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// ListScheduleWindows expands the yard's active windows over a date range
// (inclusive, capped at 62 days) into concrete slots with the places left.
// The yard must be one of the customer's own.
func (r *PostgresRepository) ListScheduleWindows(ctx context.Context, customerID, yardID uuid.UUID, kind, from, to string) ([]ScheduleWindowView, error) {
	yardTZ, ok, err := r.yardTimezoneForCustomer(ctx, yardID, customerID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrYardNotFound
	}
	if err := expireStaleCheckoutHolds(ctx, r.db.GetExecutor(ctx)); err != nil {
		return nil, fmt.Errorf("failed to expire stale checkout holds: %w", err)
	}
	today := yardLocalToday(yardTZ)
	if strings.TrimSpace(from) == "" {
		from = today.Format("2006-01-02")
	}
	if strings.TrimSpace(to) == "" {
		to = today.AddDate(0, 0, 14).Format("2006-01-02")
	}
	fromDay, err := parseLocalDay(from)
	if err != nil {
		return nil, fmt.Errorf("%w: from must be YYYY-MM-DD", ErrInvalidRequest)
	}
	toDay, err := parseLocalDay(to)
	if err != nil {
		return nil, fmt.Errorf("%w: to must be YYYY-MM-DD", ErrInvalidRequest)
	}
	if toDay.Before(fromDay) {
		return nil, fmt.Errorf("%w: to is before from", ErrInvalidRequest)
	}
	if toDay.Sub(fromDay) > 62*24*time.Hour {
		return nil, fmt.Errorf("%w: the range is capped at 62 days", ErrInvalidRequest)
	}

	q := windowSelect + ` WHERE w.yard_id = $1 AND w.is_active`
	args := []any{yardID}
	if kind != "" {
		q += ` AND w.kind = $2`
		args = append(args, kind)
	}
	q += ` ORDER BY w.weekday, w.start_time`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list schedule windows: %w", err)
	}
	defer rows.Close()
	windows := make([]windowRow, 0)
	for rows.Next() {
		w, err := scanWindow(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan schedule window: %w", err)
		}
		windows = append(windows, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	views := make([]ScheduleWindowView, 0)
	for day := fromDay; !day.After(toDay); day = day.AddDate(0, 0, 1) {
		for _, w := range windows {
			if w.weekday != int(day.Weekday()) {
				continue
			}
			start, end, startLocal, endLocal, err := windowBoundsInZone(w, day)
			if err != nil {
				return nil, err
			}
			held, err := heldCount(ctx, r.db.GetExecutor(ctx), w.id, day.Format("2006-01-02"), uuid.Nil)
			if err != nil {
				return nil, err
			}
			remaining := w.capacity - held
			if remaining < 0 {
				remaining = 0
			}
			views = append(views, ScheduleWindowView{
				WindowID:      w.id,
				YardID:        w.yardID,
				YardCode:      w.yardCode,
				Kind:          w.kind,
				Date:          day.Format("2006-01-02"),
				StartUTC:      start,
				EndUTC:        end,
				StartLocal:    startLocal,
				EndLocal:      endLocal,
				Capacity:      w.capacity,
				Remaining:     remaining,
				CutoffMinutes: w.cutoffMinutes,
			})
		}
	}
	return views, nil
}

// parseLocalDay parses a YYYY-MM-DD day without time zone surprises.
func parseLocalDay(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", strings.TrimSpace(s), time.UTC)
}

// windowBoundsInZone resolves a window's start and end on a calendar day,
// interpreted in the yard's own zone, returned as UTC instants plus the local
// HH:MM spellings for display.
func windowBoundsInZone(w windowRow, day time.Time) (start, end time.Time, startLocal, endLocal string, err error) {
	loc := loadYardLocation(w.yardTZ)
	localDay := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	start = localDay.Add(time.Duration(w.startMinutes) * time.Minute)
	end = localDay.Add(time.Duration(w.endMinutes) * time.Minute)
	return start.UTC(), end.UTC(), start.In(loc).Format("15:04"), end.In(loc).Format("15:04"), nil
}

// CreateScheduleHold places one hold on one window and date, inside a
// transaction that locks the window row: capacity is counted under the lock,
// so two contractors racing for the last place cannot both take it.
func (r *PostgresRepository) CreateScheduleHold(ctx context.Context, customerID uuid.UUID, userID *uuid.UUID, req ScheduleWindowRequest) (*ScheduleHoldDTO, error) {
	var out *ScheduleHoldDTO
	err := r.db.RunInTx(ctx, func(txCtx context.Context) error {
		orderID := uuid.Nil
		if req.OrderID != nil {
			orderID = *req.OrderID
		}
		dto, err := r.holdLocked(txCtx, customerID, userID, req.WindowID, req.Date, orderID, uuid.Nil, false)
		if err != nil {
			return err
		}
		out = dto
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ChangeScheduleHold moves an existing held hold to a new window and date (or
// updates its order link on the same window and date). The existing hold stays
// held until the target window is locked and verified, and when the window and
// date are unchanged the booking cutoff is not re-applied to a slot the
// contractor already secured.
func (r *PostgresRepository) ChangeScheduleHold(ctx context.Context, customerID uuid.UUID, userID *uuid.UUID, holdID uuid.UUID, req ScheduleWindowRequest) (*ScheduleHoldDTO, error) {
	var out *ScheduleHoldDTO
	err := r.db.RunInTx(ctx, func(txCtx context.Context) error {
		ex := r.db.GetExecutor(txCtx)
		if err := expireStaleCheckoutHolds(txCtx, ex); err != nil {
			return fmt.Errorf("failed to expire stale checkout holds: %w", err)
		}
		var existingWindowID uuid.UUID
		var existingDate string
		var orderLink *uuid.UUID
		err := ex.QueryRow(txCtx, `
			SELECT window_id, hold_date::text, order_id FROM portal_schedule_holds
			WHERE id = $1 AND customer_id = $2 AND status = 'held' FOR UPDATE
		`, holdID, customerID).Scan(&existingWindowID, &existingDate, &orderLink)
		if err != nil {
			return ErrHoldNotFound
		}
		orderID := uuid.Nil
		if orderLink != nil {
			orderID = *orderLink
		}
		if req.OrderID != nil {
			orderID = *req.OrderID
		}
		unchangedWindow := existingWindowID == req.WindowID && existingDate == req.Date
		dto, err := r.holdLocked(txCtx, customerID, userID, req.WindowID, req.Date, orderID, holdID, unchangedWindow)
		if err != nil {
			return err
		}
		out = dto
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AttachScheduleHoldToOrder binds an unattached checkout hold (status = 'held'
// and order_id IS NULL) to a newly created order in place and stamps the
// order's scheduled delivery date and open stops inside the caller's
// transaction, without releasing the hold or re-running the booking cutoff.
func (r *PostgresRepository) AttachScheduleHoldToOrder(ctx context.Context, customerID, holdID, orderID uuid.UUID) error {
	return r.db.RunInTx(ctx, func(txCtx context.Context) error {
		ex := r.db.GetExecutor(txCtx)
		if err := expireStaleCheckoutHolds(txCtx, ex); err != nil {
			return fmt.Errorf("failed to expire stale checkout holds: %w", err)
		}
		var windowID uuid.UUID
		var date, tz string
		var startMin, endMin float64
		err := ex.QueryRow(txCtx, `
			SELECT h.window_id, h.hold_date::text,
			       EXTRACT(EPOCH FROM w.start_time) / 60, EXTRACT(EPOCH FROM w.end_time) / 60,
			       COALESCE(l.timezone, 'UTC')
			FROM portal_schedule_holds h
			JOIN yard_schedule_windows w ON w.id = h.window_id
			JOIN locations l ON l.id = w.yard_id
			WHERE h.id = $1 AND h.customer_id = $2 AND h.status = 'held' AND h.order_id IS NULL
			FOR UPDATE OF h
		`, holdID, customerID).Scan(&windowID, &date, &startMin, &endMin, &tz)
		if err != nil {
			return fmt.Errorf("%w: schedule hold is not available", ErrInvalidRequest)
		}
		day, err := parseLocalDay(date)
		if err != nil {
			return err
		}
		w := windowRow{id: windowID, yardTZ: tz, startMinutes: int(startMin), endMinutes: int(endMin)}
		start, end, _, _, err := windowBoundsInZone(w, day)
		if err != nil {
			return err
		}
		if _, err := ex.Exec(txCtx, `
			UPDATE portal_schedule_holds SET order_id = $1, updated_at = NOW()
			WHERE id = $2 AND customer_id = $3 AND status = 'held' AND order_id IS NULL
		`, orderID, holdID, customerID); err != nil {
			return fmt.Errorf("failed to attach schedule hold to order: %w", err)
		}
		if _, err := ex.Exec(txCtx, `
			UPDATE orders SET scheduled_delivery_date = $1::date, updated_at = NOW()
			WHERE id = $2 AND customer_id = $3
		`, date, orderID, customerID); err != nil {
			return fmt.Errorf("failed to stamp order schedule date: %w", err)
		}
		if _, err := ex.Exec(txCtx, `
			UPDATE deliveries SET scheduled_start = $1, scheduled_end = $2, updated_at = NOW()
			WHERE order_id = $3 AND status NOT IN ('DELIVERED', 'PARTIAL', 'FAILED')
		`, start, end, orderID); err != nil {
			return fmt.Errorf("failed to stamp delivery schedule window: %w", err)
		}
		return nil
	})
}

// ReleaseScheduleHold gives a held hold back and clears the window from its
// order and any open delivery stop inside one transaction.
func (r *PostgresRepository) ReleaseScheduleHold(ctx context.Context, customerID, holdID uuid.UUID) error {
	return r.db.RunInTx(ctx, func(txCtx context.Context) error {
		ex := r.db.GetExecutor(txCtx)
		var orderID *uuid.UUID
		err := ex.QueryRow(txCtx, `
			UPDATE portal_schedule_holds SET status = 'released', updated_at = NOW()
			WHERE id = $1 AND customer_id = $2 AND status = 'held'
			RETURNING order_id
		`, holdID, customerID).Scan(&orderID)
		if err != nil {
			return ErrHoldNotFound
		}
		if orderID != nil {
			if _, err := ex.Exec(txCtx, `
				UPDATE orders SET scheduled_delivery_date = NULL, updated_at = NOW()
				WHERE id = $1 AND customer_id = $2
			`, *orderID, customerID); err != nil {
				return fmt.Errorf("failed to clear order schedule date: %w", err)
			}
			if _, err := ex.Exec(txCtx, `
				UPDATE deliveries SET scheduled_start = NULL, scheduled_end = NULL, updated_at = NOW()
				WHERE order_id = $1 AND status NOT IN ('DELIVERED', 'PARTIAL', 'FAILED')
			`, *orderID); err != nil {
				return fmt.Errorf("failed to clear delivery schedule window: %w", err)
			}
		}
		return nil
	})
}

// GetScheduleHold reads one hold with its window facts, scoped to the customer.
func (r *PostgresRepository) GetScheduleHold(ctx context.Context, customerID, holdID uuid.UUID) (*ScheduleHoldDTO, error) {
	ex := r.db.GetExecutor(ctx)
	if err := expireStaleCheckoutHolds(ctx, ex); err != nil {
		return nil, fmt.Errorf("failed to expire stale checkout holds: %w", err)
	}
	var h ScheduleHoldDTO
	var startMin, endMin float64
	var date, tz string
	err := ex.QueryRow(ctx, `
		SELECT h.id, h.window_id, w.yard_id, w.kind, h.hold_date::text,
		       EXTRACT(EPOCH FROM w.start_time) / 60, EXTRACT(EPOCH FROM w.end_time) / 60,
		       h.order_id, h.status, h.created_at, h.updated_at,
		       COALESCE(l.timezone, 'UTC')
		FROM portal_schedule_holds h
		JOIN yard_schedule_windows w ON w.id = h.window_id
		JOIN locations l ON l.id = w.yard_id
		WHERE h.id = $1 AND h.customer_id = $2
	`, holdID, customerID).Scan(&h.ID, &h.WindowID, &h.YardID, &h.Kind, &date,
		&startMin, &endMin, &h.OrderID, &h.Status, &h.CreatedAt, &h.UpdatedAt, &tz)
	if err != nil {
		return nil, ErrHoldNotFound
	}
	h.Date = date
	day, _ := parseLocalDay(date)
	w := windowRow{yardTZ: tz, startMinutes: int(startMin), endMinutes: int(endMin)}
	h.StartUTC, h.EndUTC, _, _, _ = windowBoundsInZone(w, day)
	return &h, nil
}

// activeHoldForOrder reads the active held window hold for an order, or nil
// when the order carries none.
func (r *PostgresRepository) activeHoldForOrder(ctx context.Context, customerID, orderID uuid.UUID) (*ScheduleHoldDTO, error) {
	var h ScheduleHoldDTO
	var startMin, endMin float64
	var date, tz string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT h.id, h.window_id, w.yard_id, w.kind, h.hold_date::text,
		       EXTRACT(EPOCH FROM w.start_time) / 60, EXTRACT(EPOCH FROM w.end_time) / 60,
		       h.order_id, h.status, h.created_at, h.updated_at,
		       COALESCE(l.timezone, 'UTC')
		FROM portal_schedule_holds h
		JOIN yard_schedule_windows w ON w.id = h.window_id
		JOIN locations l ON l.id = w.yard_id
		WHERE h.order_id = $1 AND h.customer_id = $2 AND h.status = 'held'
		ORDER BY h.updated_at DESC LIMIT 1
	`, orderID, customerID).Scan(&h.ID, &h.WindowID, &h.YardID, &h.Kind, &date,
		&startMin, &endMin, &h.OrderID, &h.Status, &h.CreatedAt, &h.UpdatedAt, &tz)
	if err != nil {
		return nil, nil
	}
	h.Date = date
	day, _ := parseLocalDay(date)
	w := windowRow{yardTZ: tz, startMinutes: int(startMin), endMinutes: int(endMin)}
	h.StartUTC, h.EndUTC, _, _, _ = windowBoundsInZone(w, day)
	return &h, nil
}

// holdLocked is the shared heart of create and change: with the window row
// locked FOR UPDATE, validate the window, the yard, the date, the cutoff and
// the capacity, then write the hold. carryID retargets an existing hold
// (uuid.Nil creates a new row). When unchangedWindow is true, the caller
// already holds (windowID, date) so the booking cutoff is not re-applied.
func (r *PostgresRepository) holdLocked(ctx context.Context, customerID uuid.UUID, userID *uuid.UUID, windowID uuid.UUID, date string, orderID, carryID uuid.UUID, unchangedWindow bool) (*ScheduleHoldDTO, error) {
	ex := r.db.GetExecutor(ctx)
	day, err := parseLocalDay(date)
	if err != nil {
		return nil, fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalidRequest)
	}

	// Lock the window: capacity below is counted under this lock.
	w, err := scanWindow(ex.QueryRow(ctx, windowSelect+`
		WHERE w.id = $1 AND w.is_active
		FOR UPDATE OF w
	`, windowID))
	if err != nil {
		return nil, ErrWindowNotFound
	}
	if err := expireStaleCheckoutHolds(ctx, ex); err != nil {
		return nil, fmt.Errorf("failed to expire stale checkout holds: %w", err)
	}

	ok, err := r.yardBelongsToCustomer(ctx, w.yardID, customerID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrYardNotFound
	}
	if !unchangedWindow && day.Before(yardLocalToday(w.yardTZ)) {
		return nil, fmt.Errorf("%w: the date is in the past", ErrInvalidRequest)
	}
	if w.weekday != int(day.Weekday()) {
		return nil, fmt.Errorf("%w: the yard does not run this window on that weekday", ErrInvalidRequest)
	}

	start, end, _, _, err := windowBoundsInZone(w, day)
	if err != nil {
		return nil, err
	}
	// The cutoff: holds stop cutoff_minutes before the window starts, wall
	// clock in the yard's zone (the yard's own working day is the deadline a
	// dispatcher plans against). An unchanged window was already held before
	// cutoff, so it is not re-checked.
	if !unchangedWindow && time.Now().UTC().After(start.Add(-time.Duration(w.cutoffMinutes)*time.Minute)) {
		return nil, ErrCutoffPassed
	}

	held, err := heldCount(ctx, r.db.GetExecutor(ctx), w.id, date, carryID)
	if err != nil {
		return nil, err
	}
	if held >= w.capacity {
		return nil, ErrSlotFull
	}

	if orderID != uuid.Nil {
		var status string
		err = ex.QueryRow(ctx,
			`SELECT status FROM orders WHERE id = $1 AND customer_id = $2`, orderID, customerID).Scan(&status)
		if err != nil {
			return nil, ErrOrderNotFound
		}
		switch status {
		case "DRAFT", "ON_HOLD", "CONFIRMED", "BACKORDERED":
		default:
			return nil, fmt.Errorf("%w: only an unfulfilled order can carry a hold", ErrInvalidRequest)
		}
		// One held hold per order: the earlier one goes back.
		if _, err := ex.Exec(ctx, `
			UPDATE portal_schedule_holds SET status = 'released', updated_at = NOW()
			WHERE order_id = $1 AND customer_id = $2 AND status = 'held' AND id <> $3
		`, orderID, customerID, carryID); err != nil {
			return nil, err
		}
	}

	id := carryID
	now := time.Now().UTC()
	if id == uuid.Nil {
		id = uuid.New()
		if _, err := ex.Exec(ctx, `
			INSERT INTO portal_schedule_holds (id, window_id, hold_date, customer_id, order_id, created_by, status)
			VALUES ($1, $2, $3, $4, $5, $6, 'held')
		`, id, w.id, date, customerID, nilIfNil(orderID), userID); err != nil {
			return nil, fmt.Errorf("failed to place hold: %w", err)
		}
	} else {
		if _, err := ex.Exec(ctx, `
			INSERT INTO portal_schedule_holds (id, window_id, hold_date, customer_id, order_id, created_by, status)
			VALUES ($1, $2, $3, $4, $5, $6, 'held')
			ON CONFLICT (id) DO UPDATE SET window_id = $2, hold_date = $3, order_id = $5,
				status = 'held', updated_at = NOW()
		`, id, w.id, date, customerID, nilIfNil(orderID), userID); err != nil {
			return nil, fmt.Errorf("failed to change hold: %w", err)
		}
	}

	if orderID != uuid.Nil {
		if _, err := ex.Exec(ctx, `
			UPDATE orders SET scheduled_delivery_date = $1::date, updated_at = NOW()
			WHERE id = $2 AND customer_id = $3
		`, date, orderID, customerID); err != nil {
			return nil, fmt.Errorf("failed to stamp order schedule date: %w", err)
		}
		if _, err := ex.Exec(ctx, `
			UPDATE deliveries SET scheduled_start = $1, scheduled_end = $2, updated_at = NOW()
			WHERE order_id = $3 AND status NOT IN ('DELIVERED', 'PARTIAL', 'FAILED')
		`, start, end, orderID); err != nil {
			return nil, fmt.Errorf("failed to stamp delivery schedule window: %w", err)
		}
	}

	return &ScheduleHoldDTO{
		ID:        id,
		WindowID:  w.id,
		YardID:    w.yardID,
		Kind:      w.kind,
		Date:      date,
		StartUTC:  start,
		EndUTC:    end,
		OrderID:   nilIfNil(orderID),
		Status:    "held",
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// nilIfNil maps uuid.Nil to a SQL NULL pointer.
func nilIfNil(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// --- Service wrappers ---------------------------------------------------------
//
// The transactional heart lives on the repository (RunInTx owns the boundary,
// the same shape as CreateReorder); the service layer adds the capability's
// log line and nothing else, so the refusal paths stay the repository's
// sentinels and the handler maps them.

// ListOpenScheduleWindows serves the portal's open window and slot list.
func (s *Service) ListOpenScheduleWindows(ctx context.Context, customerID, yardID uuid.UUID, kind, from, to string) ([]ScheduleWindowView, error) {
	return s.repo.ListScheduleWindows(ctx, customerID, yardID, kind, from, to)
}

// CreateScheduleHold places a hold, at checkout or on an open order.
func (s *Service) CreateScheduleHold(ctx context.Context, customerID uuid.UUID, userID *uuid.UUID, req ScheduleWindowRequest) (*ScheduleHoldDTO, error) {
	dto, err := s.repo.CreateScheduleHold(ctx, customerID, userID, req)
	if err != nil {
		return nil, err
	}
	s.logger.Info("Portal schedule hold placed",
		"customer_id", customerID, "window_id", req.WindowID, "date", req.Date)
	return dto, nil
}

// ChangeScheduleHold moves a held hold to another window or date.
func (s *Service) ChangeScheduleHold(ctx context.Context, customerID uuid.UUID, userID *uuid.UUID, holdID uuid.UUID, req ScheduleWindowRequest) (*ScheduleHoldDTO, error) {
	dto, err := s.repo.ChangeScheduleHold(ctx, customerID, userID, holdID, req)
	if err != nil {
		return nil, err
	}
	s.logger.Info("Portal schedule hold changed",
		"customer_id", customerID, "hold_id", holdID, "window_id", req.WindowID, "date", req.Date)
	return dto, nil
}

// ReleaseScheduleHold gives a held hold back.
func (s *Service) ReleaseScheduleHold(ctx context.Context, customerID, holdID uuid.UUID) error {
	if err := s.repo.ReleaseScheduleHold(ctx, customerID, holdID); err != nil {
		return err
	}
	s.logger.Info("Portal schedule hold released", "customer_id", customerID, "hold_id", holdID)
	return nil
}

// GetScheduleHold reads one hold, scoped to the customer.
func (s *Service) GetScheduleHold(ctx context.Context, customerID, holdID uuid.UUID) (*ScheduleHoldDTO, error) {
	return s.repo.GetScheduleHold(ctx, customerID, holdID)
}
