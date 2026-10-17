// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// The order's journey as a contractor reads it (the parity plan's PE-04):
// five steps derived from Gable's own order and delivery states, with the
// proof of delivery where it exists. The flow is the delivery type's: a
// delivery goes out on a truck, a pickup waits at the yard.
//
// Confirmed  -> orders.status has left DRAFT.
// Preparing  -> a delivery stop exists for the order (the dispatcher has the
//               job on a board), or the order is fulfilled.
// Loaded     -> the stop's route is past DRAFT/SCHEDULED, or the stop is out
//               for delivery or beyond.
// Out for delivery / Ready for pickup -> the stop is OUT_FOR_DELIVERY, or
//               (pickup) the order is confirmed and a hold or stop exists.
// Delivered / Picked up -> the stop is DELIVERED or PARTIAL, or the order is
//               FULFILLED.

// TimelineStep is one step of the journey.
type TimelineStep struct {
	Key   string     `json:"key"`   // confirmed, preparing, loaded, out_for_delivery|ready_for_pickup, delivered|picked_up
	Label string     `json:"label"` // the words a contractor reads
	State string     `json:"state"` // done, current, upcoming
	At    *time.Time `json:"at"`    // when it happened, where Gable records one
}

// TimelinePOD is the proof of delivery, where it exists.
type TimelinePOD struct {
	SignedBy  *string    `json:"signed_by"`
	PhotoURL  *string    `json:"photo_url"`
	Place     *string    `json:"place"` // the delivery address the stop records
	Timestamp *time.Time `json:"timestamp"`
}

// OrderTimelineDTO is the whole journey for one order.
type OrderTimelineDTO struct {
	OrderID      uuid.UUID      `json:"order_id"`
	Status       string         `json:"status"`
	DeliveryType string         `json:"delivery_type"`
	IsCancelled  bool           `json:"is_cancelled"`
	Steps        []TimelineStep `json:"steps"`
	POD          *TimelinePOD   `json:"pod"`
}

// orderDeliveryState is what the timeline derives from beyond the order row.
type orderDeliveryState struct {
	deliveryType    string
	stopStatus      *string
	routeStatus     *string
	scheduledAt     *time.Time
	podSignedBy     *string
	podPhotoURL     *string
	podPlace        *string
	podAt           *time.Time
	deliveryAddress *string
}

// readOrderDeliveryState reads the order's own delivery type and its newest
// stop with its route, scoped to the customer (the order row was already
// scoped by the caller).
func (r *PostgresRepository) ReadOrderDeliveryState(ctx context.Context, orderID, customerID uuid.UUID) (*orderDeliveryState, error) {
	var st orderDeliveryState
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT o.delivery_type, d.status, rt.status, rt.scheduled_date,
		       d.pod_signed_by, d.pod_proof_url, d.pod_timestamp, c.address
		FROM orders o
		JOIN customers c ON c.id = o.customer_id
		LEFT JOIN LATERAL (
			SELECT d.status, d.route_id, d.pod_signed_by, d.pod_proof_url, d.pod_timestamp
			FROM deliveries d WHERE d.order_id = o.id
			ORDER BY d.created_at DESC LIMIT 1
		) d ON TRUE
		LEFT JOIN delivery_routes rt ON rt.id = d.route_id
		WHERE o.id = $1 AND o.customer_id = $2
	`, orderID, customerID).Scan(
		&st.deliveryType, &st.stopStatus, &st.routeStatus, &st.scheduledAt,
		&st.podSignedBy, &st.podPhotoURL, &st.podAt, &st.deliveryAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to read order delivery state: %w", err)
	}
	st.podPlace = st.deliveryAddress
	return &st, nil
}

// GetOrderTimeline builds the five step journey.
func (s *Service) GetOrderTimeline(ctx context.Context, orderID, customerID uuid.UUID) (*OrderTimelineDTO, error) {
	order, err := s.repo.GetOrderByIDAndCustomer(ctx, orderID, customerID)
	if err != nil {
		return nil, err
	}
	del, err := s.repo.ReadOrderDeliveryState(ctx, orderID, customerID)
	if err != nil {
		return nil, err
	}

	isPickup := del.deliveryType == "PICKUP"
	flow := deliveryFlow(isPickup)

	// doneRanks: how many steps are fully behind the order.
	done := 0
	status := order.Status
	stop := ptrString(del.stopStatus)
	route := ptrString(del.routeStatus)

	confirmed := status != "DRAFT"
	if confirmed {
		done = 1
	}
	preparing := stop != "" || status == "FULFILLED"
	if preparing && confirmed {
		done = 2
	}
	loaded := route == "IN_TRANSIT" || route == "COMPLETED" ||
		stop == "OUT_FOR_DELIVERY" || stop == "DELIVERED" || stop == "PARTIAL"
	if loaded && preparing {
		done = 3
	}
	finished := status == "FULFILLED" || stop == "DELIVERED" || stop == "PARTIAL"
	if finished {
		done = len(flow)
	}

	dto := &OrderTimelineDTO{
		OrderID:      orderID,
		Status:       status,
		DeliveryType: map[bool]string{true: "PICKUP", false: "DELIVERY"}[isPickup],
		IsCancelled:  status == "CANCELLED",
		Steps:        make([]TimelineStep, 0, 5),
	}
	if finished && (del.podSignedBy != nil || del.podPhotoURL != nil || del.podAt != nil) {
		dto.POD = &TimelinePOD{
			SignedBy:  del.podSignedBy,
			PhotoURL:  del.podPhotoURL,
			Place:     del.podPlace,
			Timestamp: del.podAt,
		}
	}
	if dto.POD == nil && finished && del.deliveryAddress != nil {
		dto.POD = &TimelinePOD{Place: del.deliveryAddress}
	}

	// A cancelled order promises nothing: every step reads upcoming.
	current := -1
	if !dto.IsCancelled {
		if finished {
			current = -1 // all done, no live step
		} else if loaded {
			current = 3
		} else if preparing {
			current = 2
		} else if confirmed {
			current = 1
		}
	}

	for i, f := range flow {
		state := "upcoming"
		if i < done {
			state = "done"
		} else if i == current {
			state = "current"
		}
		step := TimelineStep{Key: f.key, Label: f.label, State: state}
		switch {
		case i == 0 && confirmed:
			at := order.UpdatedAt
			step.At = &at
		case i == 3 && del.scheduledAt != nil && (loaded || current == 3):
			step.At = del.scheduledAt
		case i == 4 && finished && del.podAt != nil:
			step.At = del.podAt
		}
		dto.Steps = append(dto.Steps, step)
	}
	return dto, nil
}

func ptrString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

type flowStep struct {
	key   string
	label string
}

func deliveryFlow(isPickup bool) []flowStep {
	if isPickup {
		return []flowStep{
			{"confirmed", "Confirmed"},
			{"preparing", "Preparing"},
			{"ready_for_pickup", "Ready for pickup"},
			{"picked_up", "Picked up"},
		}
	}
	return []flowStep{
		{"confirmed", "Confirmed"},
		{"preparing", "Preparing"},
		{"loaded", "Loaded"},
		{"out_for_delivery", "Out for delivery"},
		{"delivered", "Delivered"},
	}
}

// SetDeliveryInstructionsRequest is the payload for the site instructions.
type SetDeliveryInstructionsRequest struct {
	Instructions string `json:"instructions"`
}

// SetOrderSiteInstructions edits the contractor's site delivery instructions
// on an unfulfilled order. The write is scoped to the customer and bumps
// updated_at so the change feed carries it.
func (s *Service) SetOrderSiteInstructions(ctx context.Context, orderID, customerID uuid.UUID, instructions string) (*PortalOrderDTO, error) {
	if len(instructions) > 2000 {
		return nil, fmt.Errorf("%w: instructions are capped at 2000 characters", ErrInvalidRequest)
	}
	if err := s.repo.SetOrderSiteInstructions(ctx, orderID, customerID, instructions); err != nil {
		return nil, err
	}
	s.logger.Info("Portal order site instructions set",
		"customer_id", customerID, "order_id", orderID)
	return s.repo.GetOrderByIDAndCustomer(ctx, orderID, customerID)
}

// SetOrderSiteInstructions writes the instructions on an unfulfilled order.
func (r *PostgresRepository) SetOrderSiteInstructions(ctx context.Context, orderID, customerID uuid.UUID, instructions string) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE orders SET site_delivery_instructions = $1, updated_at = NOW()
		WHERE id = $2 AND customer_id = $3 AND status NOT IN ('FULFILLED', 'CANCELLED')
	`, instructions, orderID, customerID)
	if err != nil {
		return fmt.Errorf("failed to set site instructions: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either not the caller's order (a 404 either way) or one the gate
		// refuses; the order's own state decides which, read it back.
		var status string
		err := r.db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT status FROM orders WHERE id = $1 AND customer_id = $2`, orderID, customerID).Scan(&status)
		if err != nil {
			return ErrOrderNotFound
		}
		return fmt.Errorf("%w: a %s order's site instructions are read only", ErrInvalidRequest, status)
	}
	return nil
}
