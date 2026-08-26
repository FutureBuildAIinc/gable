// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WHY THIS IS A REQUEST QUEUE AND NOT A SCHEDULE WRITE
//
// A portal reschedule looks like it should be `UPDATE delivery_routes SET
// scheduled_date = ...`. It must not be, for three independent reasons, any
// one of which is disqualifying:
//
//  1. THE DATE IS SHARED. `delivery_routes.scheduled_date DATE NOT NULL`
//     (migration 009) is a property of the ROUTE, and a route carries every
//     stop assigned to that truck that day. Moving it to satisfy one
//     contractor silently moves every other contractor on the same truck.
//     There is no per-delivery date to write instead: `deliveries.
//     scheduled_start`/`scheduled_end` exist (migration 040) but are dead
//     columns — nothing in the codebase reads or writes them — and
//     `deliveries.estimated_arrival` is a computed ETA from the optimiser,
//     not a promise.
//
//  2. AI_LM DELETES WHAT IT DID NOT WRITE. integrations.ReplaceDeliveryRoute
//     handles POST /api/integration/delivery-routes by running
//     `DELETE FROM deliveries WHERE route_id IN (SELECT id FROM
//     delivery_routes WHERE vehicle_id = $1 AND scheduled_date = $2 AND
//     status IN ('DRAFT','SCHEDULED'))` followed by the same DELETE on the
//     route, then re-inserting. Any portal edit to a route still in
//     DRAFT/SCHEDULED is destroyed by the next plan push, without an error
//     and without a trace. A contractor would be shown a date that quietly
//     reverted.
//
//  3. THERE IS NO SEAM TO WRITE THROUGH. internal/delivery exposes no route
//     update, no unassign, no move-stop-to-another-route and no cancel; its
//     Repository interface is CreateRoute / GetRoute / ListRoutes /
//     UpdateRouteStatus. Building one from the portal would mean inventing
//     dispatch semantics from the outside of the dispatch module.
//
// So the portal records the ASK, scoped to the customer, and the dispatcher
// resolves it against the board they actually control. The endpoint returns
// 202 with `applied: false` and says so, which is the honest answer, and a
// consumer can render "requested — awaiting the dealer" instead of a date
// nobody has agreed to.
//
// SCOPED OUT, deliberately: applying an approved request to the schedule.
// That is a dispatch-side feature (route mutation + AI_LM re-plan
// interaction) and shipping half of it — a portal write that a plan push
// erases — would be worse than not shipping it.

// Reschedule request statuses. PENDING is the only one the portal writes;
// APPLIED / DECLINED / SUPERSEDED are the dispatcher's answers, plus the
// automatic supersede when a contractor changes their mind.
const (
	RescheduleStatusPending    = "PENDING"
	RescheduleStatusApplied    = "APPLIED"
	RescheduleStatusDeclined   = "DECLINED"
	RescheduleStatusSuperseded = "SUPERSEDED"
)

// rescheduleDateLayout is the wire format for the requested date: a calendar
// day, not an instant. A delivery is scheduled to a day; accepting a timestamp
// would imply the dealer can commit to an hour, which no part of this system
// can honour.
const rescheduleDateLayout = "2006-01-02"

// maxRescheduleHorizonDays bounds how far out a request may be pushed. A year
// is far past any real job and stops a typo like 2206-03-01 from sitting in
// the dispatcher's queue forever.
const maxRescheduleHorizonDays = 365

// RescheduleDeliveryRequest is the portal user asking for a different day.
type RescheduleDeliveryRequest struct {
	RequestedDate string `json:"requested_date"` // YYYY-MM-DD
	Reason        string `json:"reason"`
}

// DeliveryRescheduleDTO is a recorded reschedule request.
type DeliveryRescheduleDTO struct {
	ID         uuid.UUID `json:"id"`
	DeliveryID uuid.UUID `json:"delivery_id"`
	OrderID    uuid.UUID `json:"order_id"`

	RequestedDate string `json:"requested_date"` // YYYY-MM-DD
	Reason        string `json:"reason"`
	Status        string `json:"status"`

	// Applied is false for a PENDING request and is the field a consumer
	// should branch on. It exists so nothing has to infer "did this actually
	// change the schedule?" from the status string.
	Applied bool `json:"applied"`

	// CurrentScheduledDate is the date the dealer's board says today, or null
	// when the stop is not on a route yet. It is included so a consumer can
	// show the ask next to the current answer without a second call.
	CurrentScheduledDate *string `json:"current_scheduled_date"`

	ResolutionNote *string   `json:"resolution_note"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// --- Service ------------------------------------------------------------

// RequestDeliveryReschedule records a customer's ask for a different delivery
// day. It never writes delivery_routes — see the note at the top of this file.
//
// Refusals (409, ErrRescheduleRefused) are the interesting part:
//
//   - the stop is already DELIVERED / FAILED / PARTIAL — it is history
//   - its route is IN_TRANSIT or COMPLETED — the truck is loaded and moving,
//     and a portal "reschedule" would be a promise the driver cannot keep
//
// A stop on a DRAFT/SCHEDULED route, or on no route at all, is requestable.
func (s *Service) RequestDeliveryReschedule(ctx context.Context, deliveryID, customerID uuid.UUID, userID *uuid.UUID, req RescheduleDeliveryRequest) (*DeliveryRescheduleDTO, error) {
	requested, err := time.Parse(rescheduleDateLayout, strings.TrimSpace(req.RequestedDate))
	if err != nil {
		return nil, fmt.Errorf("%w: requested_date must be YYYY-MM-DD", ErrInvalidRequest)
	}

	// Compare calendar days in UTC. today is midnight UTC, so a request for
	// today itself is allowed (a dispatcher can still re-sequence a morning
	// route) while yesterday is not.
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if requested.Before(today) {
		return nil, fmt.Errorf("%w: requested_date is in the past", ErrInvalidRequest)
	}
	if requested.After(today.AddDate(0, 0, maxRescheduleHorizonDays)) {
		return nil, fmt.Errorf("%w: requested_date is more than %d days out",
			ErrInvalidRequest, maxRescheduleHorizonDays)
	}

	state, err := s.repo.GetDeliveryRescheduleState(ctx, deliveryID, customerID)
	if err != nil {
		return nil, err
	}

	if isTerminalDeliveryStatus(state.DeliveryStatus) {
		return nil, fmt.Errorf("%w: this delivery is already %s",
			ErrRescheduleRefused, state.DeliveryStatus)
	}
	if state.RouteStatus != nil && isCommittedRouteStatus(*state.RouteStatus) {
		return nil, fmt.Errorf("%w: its route is %s — the load is already dispatched, call the dealer",
			ErrRescheduleRefused, *state.RouteStatus)
	}

	id, err := s.repo.CreateRescheduleRequest(ctx, deliveryID, customerID, userID, requested, strings.TrimSpace(req.Reason))
	if err != nil {
		return nil, fmt.Errorf("failed to record reschedule request: %w", err)
	}

	s.logger.Info("Portal delivery reschedule requested",
		"customer_id", customerID, "delivery_id", deliveryID,
		"requested_date", requested.Format(rescheduleDateLayout),
		"route_status", state.RouteStatus)

	return s.repo.GetRescheduleRequest(ctx, id, customerID)
}

// GetDeliveryReschedule returns the newest reschedule request for a delivery,
// or ErrDeliveryNotFound when the delivery is not the caller's. A delivery the
// caller owns that has never been rescheduled returns (nil, nil) so the
// handler can answer 204 rather than inventing an empty request.
func (s *Service) GetDeliveryReschedule(ctx context.Context, deliveryID, customerID uuid.UUID) (*DeliveryRescheduleDTO, error) {
	if _, err := s.repo.GetDeliveryRescheduleState(ctx, deliveryID, customerID); err != nil {
		return nil, err
	}
	return s.repo.GetLatestRescheduleRequest(ctx, deliveryID, customerID)
}

// isTerminalDeliveryStatus reports whether the stop is history. These are the
// three statuses delivery.CompleteDelivery accepts as terminal.
func isTerminalDeliveryStatus(status string) bool {
	switch strings.ToUpper(status) {
	case "DELIVERED", "FAILED", "PARTIAL":
		return true
	}
	return false
}

// isCommittedRouteStatus reports whether the route has left the planning stage.
// These are exactly the statuses AI_LM's ReplaceDeliveryRoute will NOT delete,
// which is the same line: past it, the load is real.
func isCommittedRouteStatus(status string) bool {
	switch strings.ToUpper(status) {
	case "IN_TRANSIT", "COMPLETED":
		return true
	}
	return false
}

// --- Repository ---------------------------------------------------------

// deliveryRescheduleState is the minimum needed to decide whether a reschedule
// may be requested: the stop's own status, and its route's status and date if
// it is on one.
type deliveryRescheduleState struct {
	DeliveryID     uuid.UUID
	OrderID        uuid.UUID
	DeliveryStatus string
	RouteStatus    *string
	ScheduledDate  *time.Time
}

// GetDeliveryRescheduleState loads a delivery scoped to the calling customer.
// The customer link runs deliveries -> orders -> customer_id, the same join
// ListDeliveriesByCustomer already uses.
func (r *PostgresRepository) GetDeliveryRescheduleState(ctx context.Context, deliveryID, customerID uuid.UUID) (*deliveryRescheduleState, error) {
	var st deliveryRescheduleState
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT d.id, d.order_id, COALESCE(d.status, 'PENDING'), rt.status, rt.scheduled_date
		FROM deliveries d
		JOIN orders o ON o.id = d.order_id
		LEFT JOIN delivery_routes rt ON rt.id = d.route_id
		WHERE d.id = $1 AND o.customer_id = $2
	`, deliveryID, customerID).Scan(&st.DeliveryID, &st.OrderID, &st.DeliveryStatus, &st.RouteStatus, &st.ScheduledDate)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrDeliveryNotFound
		}
		return nil, fmt.Errorf("failed to load delivery: %w", err)
	}
	return &st, nil
}

// CreateRescheduleRequest records the ask.
//
// A contractor who asks twice supersedes their own earlier request rather than
// leaving two contradictory dates in the dispatcher's queue. The supersede and
// the insert share a transaction, and the partial unique index
// idx_pdrr_one_open_per_delivery is what makes the invariant hold even if two
// requests race.
func (r *PostgresRepository) CreateRescheduleRequest(ctx context.Context, deliveryID, customerID uuid.UUID, userID *uuid.UUID, requestedDate time.Time, reason string) (uuid.UUID, error) {
	id := uuid.New()

	err := r.db.RunInTx(ctx, func(txCtx context.Context) error {
		exec := r.db.GetExecutor(txCtx)

		_, err := exec.Exec(txCtx, `
			UPDATE portal_delivery_reschedule_requests
			SET status = 'SUPERSEDED', updated_at = NOW()
			WHERE delivery_id = $1 AND customer_id = $2 AND status = 'PENDING'
		`, deliveryID, customerID)
		if err != nil {
			return fmt.Errorf("failed to supersede prior request: %w", err)
		}

		_, err = exec.Exec(txCtx, `
			INSERT INTO portal_delivery_reschedule_requests
				(id, delivery_id, customer_id, requested_date, reason, status, requested_by)
			VALUES ($1, $2, $3, $4::date, $5, 'PENDING', $6)
		`, id, deliveryID, customerID, requestedDate, reason, userID)
		if err != nil {
			return fmt.Errorf("failed to insert reschedule request: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

const rescheduleSelect = `
	SELECT rr.id, rr.delivery_id, d.order_id, rr.requested_date, rr.reason,
	       rr.status, rr.resolution_note, rt.scheduled_date,
	       rr.created_at, rr.updated_at
	FROM portal_delivery_reschedule_requests rr
	JOIN deliveries d ON d.id = rr.delivery_id
	LEFT JOIN delivery_routes rt ON rt.id = d.route_id
`

func scanReschedule(row pgx.Row) (*DeliveryRescheduleDTO, error) {
	var dto DeliveryRescheduleDTO
	var requested time.Time
	var scheduled *time.Time
	if err := row.Scan(
		&dto.ID, &dto.DeliveryID, &dto.OrderID, &requested, &dto.Reason,
		&dto.Status, &dto.ResolutionNote, &scheduled,
		&dto.CreatedAt, &dto.UpdatedAt,
	); err != nil {
		return nil, err
	}
	dto.RequestedDate = requested.Format(rescheduleDateLayout)
	dto.Applied = dto.Status == RescheduleStatusApplied
	if scheduled != nil {
		s := scheduled.Format(rescheduleDateLayout)
		dto.CurrentScheduledDate = &s
	}
	return &dto, nil
}

// GetRescheduleRequest reads one request, scoped to the customer that filed it.
func (r *PostgresRepository) GetRescheduleRequest(ctx context.Context, id, customerID uuid.UUID) (*DeliveryRescheduleDTO, error) {
	row := r.db.GetExecutor(ctx).QueryRow(ctx,
		rescheduleSelect+` WHERE rr.id = $1 AND rr.customer_id = $2`, id, customerID)
	dto, err := scanReschedule(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrDeliveryNotFound
		}
		return nil, fmt.Errorf("failed to read reschedule request: %w", err)
	}
	return dto, nil
}

// GetLatestRescheduleRequest returns the newest request for a delivery, or
// (nil, nil) when the customer has never filed one.
func (r *PostgresRepository) GetLatestRescheduleRequest(ctx context.Context, deliveryID, customerID uuid.UUID) (*DeliveryRescheduleDTO, error) {
	row := r.db.GetExecutor(ctx).QueryRow(ctx,
		rescheduleSelect+` WHERE rr.delivery_id = $1 AND rr.customer_id = $2
		 ORDER BY rr.created_at DESC LIMIT 1`, deliveryID, customerID)
	dto, err := scanReschedule(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read reschedule request: %w", err)
	}
	return dto, nil
}
