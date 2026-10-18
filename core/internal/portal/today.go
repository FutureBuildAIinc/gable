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

	"github.com/google/uuid"
)

// The Today read (GET /api/portal/v1/today): everything a contractor opens
// the portal to act on, each section a count with its first rows. Money is
// integer cents on this surface (the portal's legacy floats stay on the
// legacy reads; new surfaces never add another float).

const todayFirstRows = 5

// TodayQuoteRow is one quote awaiting the pro's decision.
type TodayQuoteRow struct {
	ID         uuid.UUID  `json:"id"`
	TotalCents int64      `json:"total_cents"`
	SentAt     *time.Time `json:"sent_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TodayDeliveryRow is one stop on today's board or the next one out.
type TodayDeliveryRow struct {
	DeliveryID   uuid.UUID  `json:"delivery_id"`
	OrderID      uuid.UUID  `json:"order_id"`
	Status       string     `json:"status"`
	ScheduledFor *time.Time `json:"scheduled_for"`
	YardCode     string     `json:"yard_code"`
	IsPickup     bool       `json:"is_pickup"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// TodayInvoiceRow is one open invoice due now or already overdue.
type TodayInvoiceRow struct {
	ID         uuid.UUID  `json:"id"`
	Number     string     `json:"number"`
	TotalCents int64      `json:"total_cents"`
	DueDate    *time.Time `json:"due_date"`
	IsOverdue  bool       `json:"is_overdue"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// TodayOrderRow is one open order.
type TodayOrderRow struct {
	ID         uuid.UUID `json:"id"`
	Status     string    `json:"status"`
	TotalCents int64     `json:"total_cents"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TodaySection is a count plus its first rows, never null.
type TodaySection[T any] struct {
	Count int `json:"count"`
	First []T `json:"first"`
}

// TodayDTO is the whole board.
type TodayDTO struct {
	QuotesAwaitingPro TodaySection[TodayQuoteRow]    `json:"quotes_awaiting_pro"`
	DeliveriesToday   TodaySection[TodayDeliveryRow] `json:"deliveries_today"`
	NextDeliveries    TodaySection[TodayDeliveryRow] `json:"next_deliveries"`
	InvoicesDue       TodaySection[TodayInvoiceRow]  `json:"invoices_due"`
	OpenOrders        TodaySection[TodayOrderRow]    `json:"open_orders"`
}

// TodayResult is the board plus the cache primitives (the same pair the order
// feed carries): an ETag for a cheap conditional GET and the cursor for
// X-Portal-Latest-Change.
type TodayResult struct {
	Today           *TodayDTO
	ETag            string
	LatestUpdatedAt *time.Time
}

// GetTodayBoard builds the board, scoped to the customer. seeMoney gates every
// money figure on the board (the invoice section, quote totals and open order
// totals): a role without it gets zeroed money fields rather than a refusal,
// because the board itself is still that role's to read.
func (s *Service) GetTodayBoard(ctx context.Context, customerID uuid.UUID, caps Capabilities, since *time.Time) (*TodayResult, error) {
	dto := &TodayDTO{}
	var latest *time.Time
	keep := func(t time.Time) {
		if latest == nil || t.After(*latest) {
			v := t
			latest = &v
		}
	}

	quotes, err := s.repo.TodayQuotes(ctx, customerID, since, todayFirstRows)
	if err != nil {
		return nil, err
	}
	if !caps.SeeMoney {
		for i := range quotes {
			quotes[i].TotalCents = 0
		}
	}
	dto.QuotesAwaitingPro.First = quotes
	dto.QuotesAwaitingPro.Count = len(quotes)
	total, err := s.repo.TodayQuotesCount(ctx, customerID)
	if err != nil {
		return nil, err
	}
	dto.QuotesAwaitingPro.Count = total
	for _, q := range quotes {
		keep(q.UpdatedAt)
	}

	deliv, err := s.repo.TodayDeliveries(ctx, customerID, true, since, todayFirstRows)
	if err != nil {
		return nil, err
	}
	dto.DeliveriesToday.First = deliv
	dto.DeliveriesToday.Count, err = s.repo.TodayDeliveriesCount(ctx, customerID, true)
	if err != nil {
		return nil, err
	}
	for _, d := range deliv {
		keep(d.UpdatedAt)
	}

	next, err := s.repo.NextDeliveries(ctx, customerID, true, todayFirstRows)
	if err != nil {
		return nil, err
	}
	if next == nil {
		next = []TodayDeliveryRow{}
	}
	if since != nil {
		filtered := make([]TodayDeliveryRow, 0, len(next))
		for _, d := range next {
			if d.UpdatedAt.After(*since) {
				filtered = append(filtered, d)
			}
		}
		dto.NextDeliveries.First = filtered
	} else {
		dto.NextDeliveries.First = next
	}
	dto.NextDeliveries.Count = len(next)
	for _, d := range dto.NextDeliveries.First {
		keep(d.UpdatedAt)
	}

	if caps.SeeMoney {
		invoices, err := s.repo.TodayInvoices(ctx, customerID, since, todayFirstRows)
		if err != nil {
			return nil, err
		}
		dto.InvoicesDue.First = invoices
		dto.InvoicesDue.Count, err = s.repo.TodayInvoicesCount(ctx, customerID)
		if err != nil {
			return nil, err
		}
		for _, i := range invoices {
			keep(i.UpdatedAt)
		}
	} else {
		dto.InvoicesDue.First = []TodayInvoiceRow{}
	}

	orders, err := s.repo.TodayOpenOrders(ctx, customerID, since, todayFirstRows)
	if err != nil {
		return nil, err
	}
	if !caps.SeeMoney {
		for i := range orders {
			orders[i].TotalCents = 0
		}
	}
	dto.OpenOrders.First = orders
	dto.OpenOrders.Count, err = s.repo.TodayOpenOrdersCount(ctx, customerID)
	if err != nil {
		return nil, err
	}
	for _, o := range orders {
		keep(o.UpdatedAt)
	}

	return &TodayResult{
		Today:           dto,
		ETag:            todayETag(customerID, dto, caps, since, latest),
		LatestUpdatedAt: latest,
	}, nil
}

// todayETag hashes the customer, the money visibility capability, the since
// cursor, the section counts and first-slice lengths, and the newest change so
// a validator can never match across tenants, across roles with different
// money visibility, or across different boards.
func todayETag(customerID uuid.UUID, dto *TodayDTO, caps Capabilities, since *time.Time, latest *time.Time) string {
	h := sha256.New()
	fmt.Fprintf(h, "c=%s;m=%t;q=%d/%d;d=%d/%d;n=%d/%d;i=%d/%d;o=%d/%d;", customerID, caps.SeeMoney,
		dto.QuotesAwaitingPro.Count, len(dto.QuotesAwaitingPro.First),
		dto.DeliveriesToday.Count, len(dto.DeliveriesToday.First),
		dto.NextDeliveries.Count, len(dto.NextDeliveries.First),
		dto.InvoicesDue.Count, len(dto.InvoicesDue.First),
		dto.OpenOrders.Count, len(dto.OpenOrders.First))
	if since != nil {
		fmt.Fprintf(h, "s=%d;", since.UTC().UnixNano())
	}
	if latest != nil {
		fmt.Fprintf(h, "t=%d;", latest.UTC().UnixNano())
	}
	return `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// --- repository reads --------------------------------------------------------

const todayDeliverySelect = `
	SELECT d.id, d.order_id, COALESCE(d.status, ''), COALESCE(d.scheduled_start, rt.scheduled_date),
	       COALESCE(l.code, ''), (o.delivery_type = 'PICKUP'), d.updated_at
	FROM deliveries d
	JOIN orders o ON o.id = d.order_id
	LEFT JOIN delivery_routes rt ON rt.id = d.route_id
	LEFT JOIN locations l ON l.id = o.branch_id
	WHERE o.customer_id = $1`

func scanTodayDelivery(rows interface {
	Next() bool
	Scan(dest ...any) error
}) ([]TodayDeliveryRow, error) {
	out := make([]TodayDeliveryRow, 0)
	for rows.Next() {
		var d TodayDeliveryRow
		if err := rows.Scan(&d.DeliveryID, &d.OrderID, &d.Status, &d.ScheduledFor,
			&d.YardCode, &d.IsPickup, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan today delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, nil
}

// TodayDeliveries lists today's stops (including pickups when pickup is true),
// the soonest first, capped by limit.
func (r *PostgresRepository) TodayDeliveries(ctx context.Context, customerID uuid.UUID, pickup bool, since *time.Time, limit int) ([]TodayDeliveryRow, error) {
	q := todayDeliverySelect
	if !pickup {
		q += ` AND o.delivery_type <> 'PICKUP'`
	}
	q += `
		AND COALESCE(d.scheduled_start, rt.scheduled_date) >= CURRENT_DATE
		AND COALESCE(d.scheduled_start, rt.scheduled_date) < CURRENT_DATE + interval '1 day'`
	args := []any{customerID}
	if since != nil {
		q += ` AND d.updated_at > $2`
		args = append(args, *since)
	}
	q += ` ORDER BY COALESCE(d.scheduled_start, rt.scheduled_date) ASC, d.stop_sequence NULLS LAST LIMIT ` + strconv.Itoa(limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list today deliveries: %w", err)
	}
	defer rows.Close()
	return scanTodayDelivery(rows)
}

// NextDeliveries lists the upcoming stops after today, soonest first.
func (r *PostgresRepository) NextDeliveries(ctx context.Context, customerID uuid.UUID, pickup bool, limit int) ([]TodayDeliveryRow, error) {
	q := todayDeliverySelect
	if !pickup {
		q += ` AND o.delivery_type <> 'PICKUP'`
	}
	q += ` AND COALESCE(d.scheduled_start, rt.scheduled_date) >= CURRENT_DATE + interval '1 day'`
	args := []any{customerID}
	q += ` ORDER BY COALESCE(d.scheduled_start, rt.scheduled_date) ASC, d.stop_sequence NULLS LAST LIMIT ` + strconv.Itoa(limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list next deliveries: %w", err)
	}
	defer rows.Close()
	return scanTodayDelivery(rows)
}

// TodayDeliveriesCount counts today's stops for the section headline.
func (r *PostgresRepository) TodayDeliveriesCount(ctx context.Context, customerID uuid.UUID, pickup bool) (int, error) {
	q := `
		SELECT COUNT(*) FROM deliveries d
		JOIN orders o ON o.id = d.order_id
		LEFT JOIN delivery_routes rt ON rt.id = d.route_id
		WHERE o.customer_id = $1
		  AND COALESCE(d.scheduled_start, rt.scheduled_date) >= CURRENT_DATE
		  AND COALESCE(d.scheduled_start, rt.scheduled_date) < CURRENT_DATE + interval '1 day'`
	if !pickup {
		q += ` AND o.delivery_type <> 'PICKUP'`
	}
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, q, customerID).Scan(&n)
	return n, err
}

// TodayQuotes lists SENT quotes awaiting the pro's accept or decline.
func (r *PostgresRepository) TodayQuotes(ctx context.Context, customerID uuid.UUID, since *time.Time, limit int) ([]TodayQuoteRow, error) {
	q := `
		SELECT id, ROUND(total_amount * 100), sent_at, expires_at, updated_at
		FROM quotes
		WHERE customer_id = $1 AND state = 'SENT'`
	args := []any{customerID}
	if since != nil {
		q += ` AND updated_at > $2`
		args = append(args, *since)
	}
	q += ` ORDER BY sent_at DESC NULLS LAST LIMIT ` + strconv.Itoa(limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list today quotes: %w", err)
	}
	defer rows.Close()
	out := make([]TodayQuoteRow, 0)
	for rows.Next() {
		var t TodayQuoteRow
		if err := rows.Scan(&t.ID, &t.TotalCents, &t.SentAt, &t.ExpiresAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan today quote: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TodayQuotesCount counts every SENT quote.
func (r *PostgresRepository) TodayQuotesCount(ctx context.Context, customerID uuid.UUID) (int, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT COUNT(*) FROM quotes WHERE customer_id = $1 AND state = 'SENT'`, customerID).Scan(&n)
	return n, err
}

// TodayInvoices lists open invoices due today or already overdue.
func (r *PostgresRepository) TodayInvoices(ctx context.Context, customerID uuid.UUID, since *time.Time, limit int) ([]TodayInvoiceRow, error) {
	q := `
		SELECT id, COALESCE(number, ''), ROUND(total_amount * 100), due_date,
		       (status IN ('UNPAID', 'PARTIAL') AND due_date < CURRENT_DATE), updated_at
		FROM invoices
		WHERE customer_id = $1 AND status IN ('UNPAID', 'PARTIAL') AND due_date <= CURRENT_DATE`
	args := []any{customerID}
	if since != nil {
		q += ` AND updated_at > $2`
		args = append(args, *since)
	}
	q += ` ORDER BY due_date ASC LIMIT ` + strconv.Itoa(limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list today invoices: %w", err)
	}
	defer rows.Close()
	out := make([]TodayInvoiceRow, 0)
	for rows.Next() {
		var t TodayInvoiceRow
		if err := rows.Scan(&t.ID, &t.Number, &t.TotalCents, &t.DueDate, &t.IsOverdue, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan today invoice: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TodayInvoicesCount counts every open invoice due today or overdue.
func (r *PostgresRepository) TodayInvoicesCount(ctx context.Context, customerID uuid.UUID) (int, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COUNT(*) FROM invoices
		WHERE customer_id = $1 AND status IN ('UNPAID', 'PARTIAL') AND due_date <= CURRENT_DATE
	`, customerID).Scan(&n)
	return n, err
}

// TodayOpenOrders lists the customer's open orders.
func (r *PostgresRepository) TodayOpenOrders(ctx context.Context, customerID uuid.UUID, since *time.Time, limit int) ([]TodayOrderRow, error) {
	q := `
		SELECT id, status, ROUND(total_amount * 100), updated_at
		FROM orders
		WHERE customer_id = $1 AND status IN ('DRAFT', 'ON_HOLD', 'CONFIRMED', 'BACKORDERED')`
	args := []any{customerID}
	if since != nil {
		q += ` AND updated_at > $2`
		args = append(args, *since)
	}
	q += ` ORDER BY updated_at DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list today orders: %w", err)
	}
	defer rows.Close()
	out := make([]TodayOrderRow, 0)
	for rows.Next() {
		var t TodayOrderRow
		if err := rows.Scan(&t.ID, &t.Status, &t.TotalCents, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan today order: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TodayOpenOrdersCount counts every open order.
func (r *PostgresRepository) TodayOpenOrdersCount(ctx context.Context, customerID uuid.UUID) (int, error) {
	var n int
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COUNT(*) FROM orders
		WHERE customer_id = $1 AND status IN ('DRAFT', 'ON_HOLD', 'CONFIRMED', 'BACKORDERED')
	`, customerID).Scan(&n)
	return n, err
}
