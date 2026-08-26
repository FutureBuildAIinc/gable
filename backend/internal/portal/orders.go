// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// --- Project association (capability 2) ---------------------------------

// ProjectBelongsToCustomer is the tenancy check every caller-supplied
// project_id goes through before it is written or filtered on.
//
// It is a separate query rather than a join condition because the caller needs
// to tell "no such project" apart from "no orders on it" — and because the
// answer must be "not found" for another customer's project, not "forbidden".
func (r *PostgresRepository) ProjectBelongsToCustomer(ctx context.Context, projectID, customerID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1 AND customer_id = $2)`,
		projectID, customerID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to verify project ownership: %w", err)
	}
	return exists, nil
}

// SetOrderProject attaches an order to a project (or detaches it when
// projectID is nil).
//
// Both the order and the project are scoped to the customer: the UPDATE's
// WHERE carries `customer_id = $3`, and the project was verified by the caller.
// A cross-customer id therefore affects zero rows and is reported as not found.
func (s *Service) SetOrderProject(ctx context.Context, orderID, customerID uuid.UUID, projectID *uuid.UUID) (*PortalOrderDTO, error) {
	if projectID != nil {
		ok, err := s.repo.ProjectBelongsToCustomer(ctx, *projectID, customerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrProjectNotFound
		}
	}

	if err := s.repo.SetOrderProject(ctx, orderID, customerID, projectID); err != nil {
		return nil, err
	}

	s.logger.Info("Portal order project set",
		"customer_id", customerID, "order_id", orderID, "project_id", projectID)

	return s.repo.GetOrderByIDAndCustomer(ctx, orderID, customerID)
}

// SetOrderProject writes orders.project_id, scoped to the customer.
func (r *PostgresRepository) SetOrderProject(ctx context.Context, orderID, customerID uuid.UUID, projectID *uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE orders SET project_id = $1, updated_at = NOW() WHERE id = $2 AND customer_id = $3`,
		projectID, orderID, customerID)
	if err != nil {
		return fmt.Errorf("failed to set order project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrOrderNotFound
	}
	return nil
}

// --- Order cancellation (capability 3) ----------------------------------

// CancelOrder cancels an order on behalf of the calling customer.
//
// Three gates, in order, and each one exists because skipping it breaks
// something specific:
//
//  1. OWNERSHIP. The status read is scoped by customer_id, so a portal user
//     cannot cancel another contractor's order — and gets 404, not 403, so
//     the endpoint is not an oracle for which order ids exist.
//
//  2. GOODS IN MOTION. A CONFIRMED order whose stop is already on a dispatched
//     route, or already delivered, is refused here even though
//     order.CancelOrder would allow it. The ERP is right that a CONFIRMED
//     order is cancellable in general; it is the portal that must not let a
//     contractor cancel a load that is on a truck. A dealer can still cancel
//     it from the ERP side, with a human deciding what happens to the pallet.
//
//  3. THE ERP STATE MACHINE. order.CancelOrder owns the rest: FULFILLED is
//     refused, a second cancel is refused, and the CONFIRMED path releases
//     the allocated stock in the same transaction as the status write.
func (s *Service) CancelOrder(ctx context.Context, orderID, customerID uuid.UUID, reason string) (*CancelOrderResponse, error) {
	previous, err := s.repo.GetOrderStatusForCustomer(ctx, orderID, customerID)
	if err != nil {
		return nil, err
	}

	inMotion, detail, err := s.repo.OrderDeliveryInMotion(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if inMotion {
		return nil, fmt.Errorf("%w: %s — call the dealer", ErrCancelRefused, detail)
	}

	if s.orderSvc == nil {
		return nil, fmt.Errorf("order service is not configured")
	}
	if err := s.orderSvc.CancelOrder(ctx, orderID, reason); err != nil {
		return nil, err
	}

	s.logger.Info("Portal order cancelled",
		"customer_id", customerID, "order_id", orderID, "previous_status", previous)

	return &CancelOrderResponse{
		OrderID:        orderID,
		Status:         string(order.StatusCancelled),
		PreviousStatus: previous,
		Message:        "Order cancelled",
	}, nil
}

// GetOrderStatusForCustomer reads an order's status scoped to a customer.
func (r *PostgresRepository) GetOrderStatusForCustomer(ctx context.Context, orderID, customerID uuid.UUID) (string, error) {
	var status string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT status FROM orders WHERE id = $1 AND customer_id = $2`,
		orderID, customerID).Scan(&status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrOrderNotFound
		}
		return "", fmt.Errorf("failed to read order status: %w", err)
	}
	return status, nil
}

// OrderDeliveryInMotion reports whether any stop for this order has left the
// planning stage — on a route that is IN_TRANSIT or COMPLETED, or already
// DELIVERED / FAILED / PARTIAL.
//
// The route-status line is the same one AI_LM's ReplaceDeliveryRoute draws:
// past DRAFT/SCHEDULED, the load is real and nobody should be able to make it
// disappear from a browser.
func (r *PostgresRepository) OrderDeliveryInMotion(ctx context.Context, orderID uuid.UUID) (bool, string, error) {
	var deliveryStatus, routeStatus *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT d.status, rt.status
		FROM deliveries d
		LEFT JOIN delivery_routes rt ON rt.id = d.route_id
		WHERE d.order_id = $1
		  AND (
		        UPPER(COALESCE(d.status, '')) IN ('DELIVERED', 'FAILED', 'PARTIAL')
		     OR UPPER(COALESCE(rt.status, '')) IN ('IN_TRANSIT', 'COMPLETED')
		      )
		LIMIT 1
	`, orderID).Scan(&deliveryStatus, &routeStatus)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to check delivery state: %w", err)
	}

	switch {
	case routeStatus != nil && isCommittedRouteStatus(*routeStatus):
		return true, "this order is on a route that is " + *routeStatus, nil
	case deliveryStatus != nil:
		return true, "this order has a delivery that is " + *deliveryStatus, nil
	default:
		return true, "this order has a delivery in progress", nil
	}
}

// --- Change feed (capability 8) -----------------------------------------

// orderPageSize caps one page of the order list. It is the same cap for the
// display list and for the change feed, but the two truncate differently — see
// ListOrdersByCustomerFiltered.
const orderPageSize = 50

// OrderListResult is a page of orders plus the two cache primitives that let a
// consumer stop re-transferring an unchanged list every 30 seconds.
type OrderListResult struct {
	Orders []PortalOrderDTO

	// ETag is a strong validator over (count, newest updated_at). Both parts
	// matter: updated_at alone cannot see a DELETE or a newly visible row
	// whose timestamp is older than the current maximum.
	ETag string

	// LatestUpdatedAt is the cursor to send back as ?since= next time. It is
	// the newest updated_at IN THIS RESULT, so a caller that polls with it
	// gets strictly newer rows.
	//
	// For a `since` page it is a true watermark: the query orders by
	// updated_at and completes the trailing tie group, so every row with
	// updated_at <= this value (and > the previous cursor) is in this result.
	// Advancing to it therefore cannot skip anything.
	//
	// For a page fetched WITHOUT `since` — the bootstrap / display list, which
	// is ordered and truncated by created_at — it is only the newest change in
	// the page. A customer with more than orderPageSize orders has rows outside
	// it, so a consumer that wants the no-loss guarantee bootstraps with an
	// explicit `?since=` (an epoch timestamp is fine) rather than promoting the
	// display list's cursor.
	LatestUpdatedAt *time.Time
}

// ListOrdersFiltered returns the customer's orders, optionally narrowed to a
// project and/or to what has changed since a timestamp.
//
// The `since` filter compares updated_at, not created_at, and that is the
// whole point of capability 8: order.UpdateStatus writes `updated_at = NOW()`
// on every ERP status change, so a CONFIRMED -> ON_HOLD move that a consumer
// rounds to the same display state still shows up in the feed. A created_at
// cursor would have missed it — which is exactly the de-duplication defect
// the portal consumer had to fix on its own side.
//
// The comparison is strictly greater-than, so polling with the ETag's
// LatestUpdatedAt does not re-deliver the row that produced it. That is only
// safe because the `since` page is ordered and tie-completed by updated_at
// (ListOrdersByCustomerFiltered): the cursor below is the newest change in the
// page, and nothing at or below it was left behind.
func (s *Service) ListOrdersFiltered(ctx context.Context, customerID uuid.UUID, filter OrderListFilter) (*OrderListResult, error) {
	if filter.ProjectID != nil {
		ok, err := s.repo.ProjectBelongsToCustomer(ctx, *filter.ProjectID, customerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrProjectNotFound
		}
	}

	orders, err := s.repo.ListOrdersByCustomerFiltered(ctx, customerID, filter)
	if err != nil {
		return nil, err
	}

	result := &OrderListResult{Orders: orders}
	var latest *time.Time
	for i := range orders {
		if latest == nil || orders[i].UpdatedAt.After(*latest) {
			t := orders[i].UpdatedAt
			latest = &t
		}
	}
	result.LatestUpdatedAt = latest
	result.ETag = orderListETag(customerID, filter, len(orders), latest)
	return result, nil
}

// orderListETag builds the validator.
//
// The customer id and the filter are hashed in alongside the data so an ETag
// can never match across tenants or across different queries. Without the
// customer id, two customers whose lists happen to have the same length and
// the same newest timestamp would share a validator, and a shared cache in
// front of the API could serve one contractor the other's 304.
func orderListETag(customerID uuid.UUID, filter OrderListFilter, count int, latest *time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "c=%s;", customerID)
	if filter.ProjectID != nil {
		fmt.Fprintf(h, "p=%s;", *filter.ProjectID)
	}
	if filter.Since != nil {
		fmt.Fprintf(h, "s=%d;", filter.Since.UTC().UnixNano())
	}
	fmt.Fprintf(h, "n=%d;", count)
	if latest != nil {
		fmt.Fprintf(h, "t=%d;", latest.UTC().UnixNano())
	}
	return `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// ListOrdersByCustomerFiltered is the one order-list query. The unfiltered
// ListOrdersByCustomer delegates to it so the two can never disagree about
// which columns an order carries or how it is scoped.
func (r *PostgresRepository) ListOrdersByCustomerFiltered(ctx context.Context, customerID uuid.UUID, filter OrderListFilter) ([]PortalOrderDTO, error) {
	query := `
		SELECT o.id, o.status, o.total_amount::float8, o.created_at, o.updated_at,
		       o.project_id, p.name
		FROM orders o
		LEFT JOIN projects p ON p.id = o.project_id
		WHERE o.customer_id = $1
	`
	args := []interface{}{customerID}
	argIdx := 2

	if filter.ProjectID != nil {
		query += ` AND o.project_id = $` + strconv.Itoa(argIdx)
		args = append(args, *filter.ProjectID)
		argIdx++
	}
	if filter.Since != nil {
		query += ` AND o.updated_at > $` + strconv.Itoa(argIdx)
		args = append(args, *filter.Since)
		argIdx++
	}

	// Ordering, and what the page cap is allowed to throw away.
	//
	// Without a cursor this is the display list — the "My Orders" page and the
	// dashboard's five most recent (service.go:159-168) — so it is newest job
	// first and the cap drops the oldest.
	//
	// With a cursor it is the change feed, and created_at ordering is wrong
	// there: the cursor compares updated_at, so a created_at cap cuts the page
	// out of the MIDDLE of the range the cursor walks. An order created two
	// years ago whose status finally moved ranks below 50 newer ones, drops out
	// of the page, and the cursor — computed over the page it was in
	// (ListOrdersFiltered) — advances past its updated_at. `updated_at > since`
	// then excludes it on every subsequent poll, permanently.
	//
	// Ordering by updated_at ASC makes the cap truncate the TAIL of that range
	// instead: everything cut is newer than the page's newest row, so it
	// arrives on the next poll.
	//
	// WITH TIES closes the last hole. Two orders can share an updated_at to
	// microsecond precision — a bulk UPDATE stamps every row it touches with
	// the same transaction NOW() — and a plain LIMIT could cut between them.
	// The cursor would be their shared timestamp and the strictly-greater-than
	// comparison would drop the ones that did not fit. FETCH FIRST ... WITH
	// TIES pulls in every row tied with the last, which is exactly what makes
	// max(updated_at) over the page a watermark rather than a guess, whether
	// the page came back full or partial. The cost is that a page can exceed
	// the cap when a single bulk update is bigger than it; that is bounded by
	// the size of one tie group and is the only shape that cannot lose a row.
	if filter.Since != nil {
		query += ` ORDER BY o.updated_at ASC FETCH FIRST ` + strconv.Itoa(orderPageSize) + ` ROWS WITH TIES`
	} else {
		query += ` ORDER BY o.created_at DESC LIMIT ` + strconv.Itoa(orderPageSize)
	}

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list orders: %w", err)
	}
	defer rows.Close()

	orders := make([]PortalOrderDTO, 0)
	for rows.Next() {
		var o PortalOrderDTO
		if err := rows.Scan(&o.ID, &o.Status, &o.TotalAmount, &o.CreatedAt,
			&o.UpdatedAt, &o.ProjectID, &o.ProjectName); err != nil {
			return nil, fmt.Errorf("failed to scan order: %w", err)
		}
		o.Lines = make([]PortalLineDTO, 0) // Initialize empty for JSON []
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	for i := range orders {
		lines, err := r.getOrderLines(ctx, orders[i].ID)
		if err != nil {
			return nil, err
		}
		orders[i].Lines = lines
	}

	return orders, nil
}
