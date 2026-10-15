// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/collections"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Repository defines the data access interface for dashboard metrics.
// branchID and grantsSub are the caller's reach (SEC-NOHEADER-1, the
// principal's ruling on the reporting surface): pass the branch a header
// names, or the user sub whose user_locations grants scope a no header
// caller, or both nil for the every branch view an administrator or owner
// keeps.
type Repository interface {
	GetDashboardSummary(ctx context.Context, branchID *uuid.UUID, grantsSub *string) (*DashboardSummary, error)
	GetInventoryAlerts(ctx context.Context, branchID *uuid.UUID, grantsSub *string, limit int) ([]InventoryAlert, error)
	GetTopCustomers(ctx context.Context, branchID *uuid.UUID, grantsSub *string, limit int, days int) ([]TopCustomer, error)
	GetOrderActivity(ctx context.Context, branchID *uuid.UUID, grantsSub *string, limit int) (*OrderActivity, error)
	GetRevenueTrend(ctx context.Context, branchID *uuid.UUID, grantsSub *string, days int) ([]RevenueTrendPoint, error)
}

// PostgresRepository implements Repository for Postgres.
type PostgresRepository struct {
	db *database.DB
}

// BusinessDay is the default branch's business date now, the day the
// dashboard's day figures are for; the service keys its caches by it.
func (r *PostgresRepository) BusinessDay(ctx context.Context) (string, error) {
	var day string
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+collections.TodaySQL+`::text`).Scan(&day); err != nil {
		return "", fmt.Errorf("failed to read the business day: %w", err)
	}
	return day, nil
}

// NewRepository creates a new dashboard repository.
func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// GetDashboardSummary returns all aggregate KPIs in a single query using a CTE.
// The caller's reach walls every arm (the payments by their own yard, the
// rest through their own branch).
// Outstanding AR is the yard's net open receivable as the aging reads it
// today (account.agingSQL): each invoice's open amount, each numbered credit
// memo's open amount and each payment's unapplied cash as a negative. It is
// inlined, not read through the account module, so the one query, the per
// reach cache and its reach keys stay as they are.
func (r *PostgresRepository) GetDashboardSummary(ctx context.Context, branchID *uuid.UUID, grantsSub *string) (*DashboardSummary, error) {
	summary := &DashboardSummary{}

	// Today's and yesterday's figures are the day's NET collections at the
	// caller's yard (pkg/collections): payments received, walled by the
	// payment's own branch_id (the AR core never writes an invoice_id), less
	// refunds paid out, never a voided payment.
	query := `
		WITH asof AS (
			-- The open receivable's day: the default branch's business date,
			-- the aging's default (account.todayInDefaultBranch), never the
			-- server process's local day.
			SELECT ` + collections.TodaySQL + ` AS d
		),
		today_rev AS (
			SELECT ` + collections.NetCentsAt("(SELECT d FROM asof)", 1, 2) + ` AS val
		),
		yesterday_rev AS (
			SELECT ` + collections.NetCentsAt("(SELECT d FROM asof) - 1", 1, 2) + ` AS val
		),
		active_ord AS (
			SELECT COUNT(*) AS val
			FROM orders
			WHERE status IN ('PENDING', 'CONFIRMED', 'PROCESSING', 'READY', 'ALLOCATED')
			  AND ` + middleware.Wall("", 1, 2) + `
		),
		pending_disp AS (
			SELECT COUNT(*) AS val
			FROM deliveries d
			LEFT JOIN orders o ON o.id = d.order_id
			WHERE d.status IN ('PENDING', 'ASSIGNED')
			  AND ` + middleware.Wall("o", 1, 2) + `
		),
		open_docs AS (
			SELECT ROUND(i.total_amount * 100)::bigint - COALESCE((SELECT SUM(ROUND(a.amount * 100)::bigint) FROM ar_applications a
			            WHERE a.invoice_id = i.id AND a.applied_on <= (SELECT d FROM asof) AND (a.reversed_on IS NULL OR a.reversed_on > (SELECT d FROM asof))), 0) AS amt
			FROM invoices i
			WHERE i.invoice_date <= (SELECT d FROM asof) AND (i.voided_on IS NULL OR i.voided_on > (SELECT d FROM asof))
			  AND ` + middleware.Wall("i", 1, 2) + `
			UNION ALL
			SELECT ROUND(m.total_amount * 100)::bigint
			       + COALESCE((SELECT SUM(ROUND(a.amount * 100)::bigint) FROM ar_applications a
			            WHERE a.credit_memo_id = m.id AND a.applied_on <= (SELECT d FROM asof) AND (a.reversed_on IS NULL OR a.reversed_on > (SELECT d FROM asof))), 0)
			       + COALESCE((SELECT SUM(ROUND(f.amount * 100)::bigint) FROM payment_refunds f WHERE f.credit_memo_id = m.id AND f.refunded_on <= (SELECT d FROM asof)), 0)
			FROM credit_memos m
			WHERE m.number IS NOT NULL AND m.memo_date <= (SELECT d FROM asof) AND (m.voided_on IS NULL OR m.voided_on > (SELECT d FROM asof))
			  AND ` + middleware.Wall("m", 1, 2) + `
			UNION ALL
			SELECT -GREATEST(0, ROUND(p.amount * 100)::bigint
			       - COALESCE((SELECT SUM(ROUND(a.amount * 100)::bigint) FROM ar_applications a
			            WHERE a.payment_id = p.id AND a.kind = 'PAYMENT' AND a.applied_on <= (SELECT d FROM asof) AND (a.reversed_on IS NULL OR a.reversed_on > (SELECT d FROM asof))), 0)
			       - COALESCE((SELECT SUM(ROUND(f.amount * 100)::bigint) FROM payment_refunds f WHERE f.payment_id = p.id AND f.refunded_on <= (SELECT d FROM asof)), 0)
			       - ROUND(p.migrated_excess * 100)::bigint)
			FROM payments p
			WHERE p.received_on <= (SELECT d FROM asof) AND (p.voided_on IS NULL OR p.voided_on > (SELECT d FROM asof))
			  AND ` + middleware.Wall("p", 1, 2) + `
		),
		outstanding AS (
			SELECT (SELECT COALESCE(SUM(amt), 0)::bigint FROM open_docs) AS amount,
			       (SELECT COUNT(*) FROM invoices
			         WHERE status IN ('UNPAID', 'PARTIAL')
			           AND ` + middleware.Wall("", 1, 2) + `) AS cnt
		)
		SELECT
			(SELECT val FROM today_rev),
			(SELECT val FROM yesterday_rev),
			(SELECT val FROM active_ord),
			(SELECT val FROM pending_disp),
			(SELECT amount FROM outstanding),
			(SELECT cnt FROM outstanding)
	`

	var todayRevenue, yesterdayRevenue int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, branchID, grantsSub).Scan(
		&todayRevenue,
		&yesterdayRevenue,
		&summary.ActiveOrders,
		&summary.PendingDispatch,
		&summary.OutstandingAR,
		&summary.OutstandingARCount,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query dashboard summary: %w", err)
	}

	summary.TodayRevenue = todayRevenue
	if yesterdayRevenue > 0 {
		summary.TodayRevenueChange = float64(todayRevenue-yesterdayRevenue) / float64(yesterdayRevenue) * 100
	}

	return summary, nil
}

// GetInventoryAlerts returns products with low or zero stock, one row per
// product and yard, walled to the caller's reach through the stocking
// location's branch. A yard's stock is its own row plus every zone and bin
// whose branch_id is that yard, so the quantity compared with the reorder point
// (and reported) is that sum, the rule the counter's search already follows.
// The row's location_id is the yard and yard_name its name. A row with no
// yard (a legacy row with no location) stays its own alert.
func (r *PostgresRepository) GetInventoryAlerts(ctx context.Context, branchID *uuid.UUID, grantsSub *string, limit int) ([]InventoryAlert, error) {
	query := `
		SELECT
			p.id, p.sku, p.description,
			COALESCE(SUM(i.quantity), 0) as current_qty,
			COALESCE(p.reorder_point, 10) as reorder_qty,
			CASE
				WHEN COALESCE(SUM(i.quantity), 0) = 0 THEN 'OUT_OF_STOCK'
				ELSE 'LOW_STOCK'
			END as alert_type,
			COALESCE(l.branch_id::text, '') as location_id,
			COALESCE(y.name, y.code, '') as yard_name
		FROM products p
		LEFT JOIN inventory i ON p.id = i.product_id
		LEFT JOIN locations l ON l.id = i.location_id
		LEFT JOIN locations y ON y.id = l.branch_id
		WHERE ` + middleware.Wall("l", 2, 3) + `
		GROUP BY p.id, p.sku, p.description, p.reorder_point, l.branch_id, y.name, y.code
		HAVING COALESCE(SUM(i.quantity), 0) <= COALESCE(p.reorder_point, 10)
		ORDER BY COALESCE(SUM(i.quantity), 0) ASC, p.sku ASC, l.branch_id ASC
		LIMIT $1
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, limit, branchID, grantsSub)
	if err != nil {
		return nil, fmt.Errorf("failed to query inventory alerts: %w", err)
	}
	defer rows.Close()

	alerts := make([]InventoryAlert, 0)
	for rows.Next() {
		var a InventoryAlert
		if err := rows.Scan(&a.ProductID, &a.SKU, &a.Name, &a.CurrentQty, &a.ReorderQty, &a.AlertType, &a.LocationID, &a.YardName); err != nil {
			return nil, fmt.Errorf("failed to scan inventory alert: %w", err)
		}
		alerts = append(alerts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating inventory alerts: %w", err)
	}

	return alerts, nil
}

// GetTopCustomers returns top customers by revenue in the given period,
// walled to the caller's reach through each order's branch.
func (r *PostgresRepository) GetTopCustomers(ctx context.Context, branchID *uuid.UUID, grantsSub *string, limit int, days int) ([]TopCustomer, error) {
	cutoff := time.Now().AddDate(0, 0, -days)
	query := `
		SELECT
			c.id, c.name,
			(COALESCE(SUM(inv.total_amount), 0) * 100)::bigint as total_revenue,
			COUNT(DISTINCT o.id) as order_count
		FROM customers c
		LEFT JOIN orders o ON c.id = o.customer_id AND o.created_at >= $1
			AND ` + middleware.Wall("o", 3, 4) + `
		LEFT JOIN invoices inv ON o.id = inv.order_id
		GROUP BY c.id, c.name
		HAVING COALESCE(SUM(inv.total_amount), 0) > 0
		ORDER BY total_revenue DESC, c.name ASC, c.id ASC
		LIMIT $2
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, cutoff, limit, branchID, grantsSub)
	if err != nil {
		return nil, fmt.Errorf("failed to query top customers: %w", err)
	}
	defer rows.Close()

	customers := make([]TopCustomer, 0)
	for rows.Next() {
		var c TopCustomer
		if err := rows.Scan(&c.CustomerID, &c.CustomerName, &c.TotalRevenue, &c.OrderCount); err != nil {
			return nil, fmt.Errorf("failed to scan top customer: %w", err)
		}
		customers = append(customers, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating top customers: %w", err)
	}

	return customers, nil
}

// GetOrderActivity returns recent orders and status breakdown, walled to the
// caller's reach.
func (r *PostgresRepository) GetOrderActivity(ctx context.Context, branchID *uuid.UUID, grantsSub *string, limit int) (*OrderActivity, error) {
	activity := &OrderActivity{
		RecentOrders:    make([]RecentOrder, 0),
		StatusBreakdown: make(map[string]int),
	}

	// Recent orders
	queryRecent := `
		SELECT o.id, c.name, (o.total_amount * 100)::bigint, o.status, o.created_at
		FROM orders o
		LEFT JOIN customers c ON o.customer_id = c.id
		WHERE ` + middleware.Wall("o", 2, 3) + `
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $1
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, queryRecent, limit, branchID, grantsSub)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent orders: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var o RecentOrder
		var customerName *string
		if err := rows.Scan(&o.OrderID, &customerName, &o.TotalAmount, &o.Status, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan recent order: %w", err)
		}
		if customerName != nil {
			o.CustomerName = *customerName
		} else {
			o.CustomerName = "Walk-In"
		}
		activity.RecentOrders = append(activity.RecentOrders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating recent orders: %w", err)
	}

	// Status breakdown
	queryStatus := `
		SELECT status, COUNT(*)
		FROM orders
		WHERE created_at >= NOW() - INTERVAL '30 days'
		  AND ` + middleware.Wall("", 1, 2) + `
		GROUP BY status
	`
	statusRows, err := r.db.GetExecutor(ctx).Query(ctx, queryStatus, branchID, grantsSub)
	if err != nil {
		return nil, fmt.Errorf("failed to query order status breakdown: %w", err)
	}
	defer statusRows.Close()

	for statusRows.Next() {
		var status string
		var count int
		if err := statusRows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("failed to scan status breakdown: %w", err)
		}
		activity.StatusBreakdown[status] = count
	}
	if err := statusRows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating status breakdown: %w", err)
	}

	return activity, nil
}

// GetRevenueTrend returns the daily net collections for the last N days at
// the caller's yard (the payment's own branch, walled by the caller's reach).
func (r *PostgresRepository) GetRevenueTrend(ctx context.Context, branchID *uuid.UUID, grantsSub *string, days int) ([]RevenueTrendPoint, error) {
	// The days are the default branch's business days: the last N are today
	// and the N-1 before it, each the day's net collections at the caller's
	// yard (pkg/collections: payments by received_on and their own branch,
	// less refunds paid out, never a voided payment), the card's definition.
	query := `SELECT date, cents AS revenue FROM (` +
		collections.NetByDay("("+collections.TodaySQL+") - ($1::int - 1)", collections.TodaySQL, 2, 3) + `) d`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, days, branchID, grantsSub)
	if err != nil {
		return nil, fmt.Errorf("failed to query revenue trend: %w", err)
	}
	defer rows.Close()

	trend := make([]RevenueTrendPoint, 0)
	for rows.Next() {
		var point RevenueTrendPoint
		var dateVal time.Time
		if err := rows.Scan(&dateVal, &point.Revenue); err != nil {
			return nil, fmt.Errorf("failed to scan revenue trend point: %w", err)
		}
		point.Date = dateVal.Format("2006-01-02")
		trend = append(trend, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating revenue trend: %w", err)
	}

	return trend, nil
}
