// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is the repository's answer for an order that does not exist or
// is outside the caller's branch wall. The service maps it to 404.
var ErrNotFound = errors.New("order not found")

// ListFilter is the list route's filters and keyset position.
type ListFilter struct {
	Statuses     []OrderStatus
	CustomerID   *uuid.UUID
	JobID        *uuid.UUID
	ShipToID     *uuid.UUID
	DeliveryType *DeliveryType
	QuoteID      *uuid.UUID
	AfterTime    *time.Time
	AfterID      uuid.UUID
	Limit        int
}

// CustomerFacts are the customer attributes an order create and confirm
// read: the currency the order copies (ADR 0005 4.2), the salesperson the
// order defaults to, and the credit and PO rules of section 5.3.
type CustomerFacts struct {
	Exists           bool
	Name             string
	Currency         string
	SalespersonID    *uuid.UUID
	CreditLimitCents *httpx.Cents
	PORequired       bool
}

// ContactAuthority is the contact authority of ADR 0005 5.3.
type ContactAuthority struct {
	Exists          bool
	CanPlaceOrders  bool
	OrderLimitCents *httpx.Cents
}

// Repository is the order store. Every statement goes through the context's
// executor, so inside a transaction it never reaches for a second pool
// connection.
type Repository interface {
	NextNumber(ctx context.Context) (string, error)
	// DefaultBranchID answers the deployment's default branch, the same
	// fallback the order insert's COALESCE always used for raw writers.
	DefaultBranchID(ctx context.Context) (uuid.UUID, error)
	GetOrder(ctx context.Context, id uuid.UUID) (*Order, error)
	LockOrder(ctx context.Context, id uuid.UUID) error
	InsertOrder(ctx context.Context, o *Order) error
	ReplaceDraft(ctx context.Context, o *Order) error
	SaveTransition(ctx context.Context, o *Order) error
	ListOrders(ctx context.Context, f ListFilter) ([]OrderSummary, error)
	CountOrders(ctx context.Context, f ListFilter) (int64, error)

	LookupProducts(ctx context.Context, ids []uuid.UUID) (map[string]salesdoc.ProductRef, error)
	LookupKitComponents(ctx context.Context, kitIDs []uuid.UUID) (map[string][]salesdoc.KitComponent, error)
	LookupChargeCodes(ctx context.Context, codes []string) (map[string]salesdoc.ChargeCode, error)
	CustomerFacts(ctx context.Context, customerID uuid.UUID) (CustomerFacts, error)
	ContactAuthority(ctx context.Context, contactID uuid.UUID) (ContactAuthority, error)
	DefaultShipToID(ctx context.Context, customerID uuid.UUID) (*uuid.UUID, error)
	ShipTo(ctx context.Context, shipToID uuid.UUID) (snapshot *ShipToSnapshot, rate *string, err error)
	BranchTaxRate(ctx context.Context, branchID uuid.UUID) (*string, error)
	CustomerExempt(ctx context.Context, customerID uuid.UUID) (bool, error)

	// The credit check reads documents, never customers.balance_due
	// (ADR 0005 5.3).
	OpenReceivableCents(ctx context.Context, customerID uuid.UUID, excludingOrder *uuid.UUID) (int64, error)
	HasInvoices(ctx context.Context, orderID uuid.UUID) (bool, error)
	// SaveLineQuantities writes the allocated, back ordered and fulfilled
	// quantities of the lines (ADR 0005 5.4); the caller holds the order lock.
	SaveLineQuantities(ctx context.Context, lines []OrderLine) error
	// The allocation request queue (ADR 0005 5.4).
	QueueAllocationRequests(ctx context.Context, branchID uuid.UUID, productIDs []uuid.UUID) (int, error)
	ClaimAllocationRequest(ctx context.Context) (uuid.UUID, bool, error)
	DeleteAllocationRequest(ctx context.Context, orderID uuid.UUID) error

	// The fulfilment request queue (ADR 0005 5.5).
	InsertFulfillmentRequest(ctx context.Context, deliveryID, orderID uuid.UUID) error
	NextFulfillmentRequest(ctx context.Context) (*FulfillmentRequest, error)
	ClaimFulfillmentRequest(ctx context.Context, deliveryID uuid.UUID) (bool, error)
	DeleteFulfillmentRequest(ctx context.Context, deliveryID uuid.UUID) error
	RecordFulfillmentFailure(ctx context.Context, deliveryID uuid.UUID, lastError string, maxAttempts int) (parked bool, err error)
	ListFulfillmentRequests(ctx context.Context, f RequestFilter) ([]FulfillmentRequest, error)
	RetryFulfillmentRequest(ctx context.Context, deliveryID uuid.UUID) (orderID uuid.UUID, found bool, err error)
	DeliveryRequestOrder(ctx context.Context, deliveryID uuid.UUID) (uuid.UUID, bool, error)

	// Fulfilment (ADR 0005 5.6).
	LockCustomerCredit(ctx context.Context, customerID uuid.UUID) error
	UnbilledRemainderCents(ctx context.Context, orderID uuid.UUID) (int64, error)
	BranchLocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error)
	DeliveryOrderID(ctx context.Context, deliveryID uuid.UUID) (uuid.UUID, bool, error)
	NonStockReceiptsFor(ctx context.Context, orderLineID uuid.UUID) (NonStockReceipts, bool, error)
	OrderExistsForQuote(ctx context.Context, quoteID uuid.UUID) (bool, error)
}

type PostgresRepository struct{ db *database.DB }

func NewRepository(db *database.DB) *PostgresRepository { return &PostgresRepository{db: db} }

// NextNumber mints the next SO- number through the caller's executor, so a
// create that rolls back abandons it (ADR 0001 section 8).
func (r *PostgresRepository) NextNumber(ctx context.Context) (string, error) {
	return httpx.NextDocumentNumber(ctx, r.db.GetExecutor(ctx), "order_number_seq", "SO", httpx.DefaultDocNumberWidth)
}

// referenceFields maps the foreign keys a write can violate to the request
// field that named the missing record, so a bad reference is a 400 naming it
// and not a 500 from the database.
var referenceFields = map[string]string{
	"orders_customer_id_fkey":           "customer_id",
	"orders_quote_id_fkey":              "quote_id",
	"orders_project_id_fkey":            "job_id",
	"orders_ship_to_id_fkey":            "ship_to_id",
	"orders_ordered_by_contact_id_fkey": "ordered_by_contact_id",
	"orders_salesperson_id_fkey":        "salesperson_id",
	"orders_branch_id_fkey":             "branch_id",
	"order_lines_product_id_fkey":       "lines.product_id",
	"order_lines_charge_code_id_fkey":   "lines.charge_code",
	"order_lines_quote_line_id_fkey":    "lines.quote_line_id",
}

func validationFailed(msg string, details ...httpx.FieldError) *httpx.Error {
	return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed, Message: msg, Details: details}
}

func mapWriteError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23503":
			if field, ok := referenceFields[pgErr.ConstraintName]; ok {
				return validationFailed("a referenced record does not exist",
					httpx.FieldError{Field: field, Message: "no such record"})
			}
		case pgErr.Code == "23505" && pgErr.ConstraintName == "order_lines_pkey":
			return validationFailed("a line id belongs to another document",
				httpx.FieldError{Field: "lines.id", Message: "is already used by a line of another order"})
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// summaryColumns is the one header projection: a list item and the head of
// the full document both read it, with the margin rollup beside.
const summaryColumns = `
	o.id, o.number, o.branch_id, o.customer_id, COALESCE(c.name, ''), o.quote_id, o.project_id,
	o.status, o.revision, o.currency, o.delivery_type, o.ship_to_id, o.customer_po, o.ordered_by_contact_id,
	o.salesperson_id, COALESCE(st.name, ''), o.scheduled_delivery_date::text,
	ROUND(o.subtotal * 100)::bigint, ROUND(o.tax_amount * 100)::bigint, o.tax_rate::text, o.tax_exempt, o.tax_source,
	ROUND(o.total_amount * 100)::bigint,
	ROUND(COALESCE((
		SELECT SUM(cost_sum) FROM (
			SELECT ROUND(l.quantity * COALESCE(p.average_unit_cost, 0), 2) AS cost_sum
			FROM order_lines l LEFT JOIN products p ON p.id = l.product_id
			WHERE l.order_id = o.id
			  AND l.line_type IN ('PRODUCT', 'COMPONENT') AND l.product_id IS NOT NULL
		) s
	), 0) * 100)::bigint,
	o.hold_reason, o.hold_note, o.confirmed_at, o.created_at, o.updated_at,
	COALESCE((
		SELECT ARRAY_AGG(i.id ORDER BY i.created_at, i.id) FROM invoices i WHERE i.order_id = o.id AND i.status <> 'VOID'
	), '{}')`

const summaryFrom = `
	FROM orders o
	LEFT JOIN customers c ON c.id = o.customer_id
	LEFT JOIN sales_team st ON st.id = o.salesperson_id`

func (r *PostgresRepository) scanSummary(row pgx.Row, s *OrderSummary, extra ...any) error {
	var (
		status, delivery, taxSource string
		subtotal, tax, total, cost  int64
		taxRate                     *string
		holdReason                  *string
		confirmed                   *time.Time
		created, updated            time.Time
	)
	dest := append([]any{
		&s.ID, &s.Number, &s.BranchID, &s.CustomerID, &s.CustomerName, &s.QuoteID, &s.JobID,
		&status, &s.Revision, &s.Currency, &delivery, &s.ShipToID, &s.CustomerPO, &s.OrderedByContactID,
		&s.SalespersonID, &s.SalespersonName, &s.ScheduledDeliveryDate,
		&subtotal, &tax, &taxRate, &s.TaxExempt, &taxSource, &total, &cost,
		&holdReason, &s.HoldNote, &confirmed, &created, &updated,
		&s.InvoiceIDs,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	s.Status = OrderStatus(status)
	s.DeliveryType = DeliveryType(delivery)
	s.TaxSource = salesdoc.TaxSource(taxSource)
	s.SubtotalCents, s.TaxCents, s.TotalCents = httpx.Cents(subtotal), httpx.Cents(tax), httpx.Cents(total)
	s.TotalCostCents = httpx.Cents(cost)
	s.TotalMarginCents = s.SubtotalCents - s.TotalCostCents
	if taxRate != nil {
		if pct, err := RateToPercent(*taxRate); err == nil {
			s.TaxRatePercent = &pct
		}
	}
	if s.SubtotalCents > 0 {
		pct := percentString(int64(s.TotalMarginCents), int64(s.SubtotalCents))
		s.MarginPercent = &pct
	}
	if holdReason != nil {
		hr := HoldReason(*holdReason)
		s.HoldReason = &hr
	}
	s.ConfirmedAt = httpx.PtrTimestamp(confirmed)
	s.CreatedAt, s.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

// RateToPercent widens a stored rate ("0.088750") to the wire's percent
// string ("8.875"): the rate is scale 6, the percent at most 4 fraction
// digits, the shift exact.
func RateToPercent(rate string) (string, error) { return salesdoc.RateToPercent(rate) }

// PercentToRate narrows a wire percent ("8.875") to the stored rate string
// ("8.875000" percent = 0.08875). The percent carries at most 4 fraction
// digits, so the rate lands within NUMERIC(9,6) exactly.
func PercentToRate(percent string) (string, error) {
	// The percent travels at scale 4; the rate's scale 6 integer is the same
	// digits (8.8750 percent is 0.088750), so the percent is parsed at the
	// percent's own scale, never the rate's.
	scaled, err := parseFixedScaleLocal(percent, 4)
	if err != nil {
		return "", err
	}
	return formatRate(scaled), nil
}

// parseFixedScaleLocal parses a plain decimal at a fixed scale, exactly:
// the fraction's digits walk in at their own places, the rest pads.
func parseFixedScaleLocal(s string, scale int) (int64, error) {
	neg := strings.HasPrefix(s, "-")
	body := strings.TrimPrefix(s, "-")
	intPart, fracPart := body, ""
	if i := strings.IndexByte(body, '.'); i >= 0 {
		intPart, fracPart = body[:i], body[i+1:]
	}
	if intPart == "" {
		return 0, fmt.Errorf("not a plain decimal")
	}
	var value int64
	for _, d := range intPart {
		if d < '0' || d > '9' {
			return 0, fmt.Errorf("not a plain decimal")
		}
		value = value*10 + int64(d-'0')
	}
	used := len(fracPart)
	if used > scale {
		used = scale
	}
	for i, d := range fracPart {
		if d < '0' || d > '9' {
			return 0, fmt.Errorf("not a plain decimal")
		}
		if i < used {
			value = value*10 + int64(d-'0')
		} else if d != '0' {
			return 0, fmt.Errorf("precision beyond the scale")
		}
	}
	for i := 0; i < scale-used; i++ {
		value *= 10
	}
	if neg {
		value = -value
	}
	return value, nil
}

func formatRate(scaled int64) string {
	out := fmt.Sprintf("%d.%06d", scaled/1000000, abs64(scaled)%1000000)
	if scaled < 0 {
		out = "-" + out
	}
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// trimFixed drops trailing zero padding and a bare dot, the wire's shortest
// exact decimal form.
func trimFixed(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// percentString renders a margin percentage as the wire's decimal string,
// two fraction digits, half away from zero.
func percentString(part, whole int64) string {
	neg := false
	if part < 0 {
		neg = true
		part = -part
	}
	// percent = part * 10000 / whole, two fraction digits.
	scaled := part * 1000000 / max64(whole, 1)
	out := fmt.Sprintf("%d.%02d", scaled/10000, scaled%10000)
	if neg {
		out = "-" + out
	}
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (r *PostgresRepository) DefaultBranchID(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("no default branch configured: %w", err)
	}
	return id, nil
}

func (r *PostgresRepository) GetOrder(ctx context.Context, id uuid.UUID) (*Order, error) {
	o := &Order{}
	row := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+summaryColumns+`, o.ship_to_snapshot`+summaryFrom+`
		WHERE o.id = $1 AND ($2::uuid IS NULL OR o.branch_id = $2)`,
		id, middleware.BranchIDForQuery(ctx))
	var snapshot []byte
	if err := r.scanSummary(row, &o.OrderSummary, &snapshot); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get order header: %w", err)
	}
	if len(snapshot) > 0 {
		var s ShipToSnapshot
		if err := jsonUnmarshal(snapshot, &s); err == nil {
			o.ShipTo = &s
		}
	}
	lines, err := r.orderLines(ctx, id)
	if err != nil {
		return nil, err
	}
	o.Lines = lines
	return o, nil
}

const lineColumns = `
	l.id, l.position, l.line_type, l.parent_line_id, l.product_id, l.charge_code_id,
	cc.code, l.sku, l.description,
	ROUND(l.quantity * 10000)::bigint, l.uom, l.price_uom,
	ROUND(l.uom_qty * 10000)::bigint, ROUND(l.price_uom_qty * 10000)::bigint,
	ROUND(l.unit_price * 10000)::bigint, ROUND(l.priced_unit_price * 10000)::bigint, l.price_source,
	l.override_reason, ROUND(l.discount_percent * 10000)::bigint, ROUND(l.discount_amount * 100)::bigint,
	l.discount_reason, l.price_adjusted_by, ROUND(l.line_total * 100)::bigint, l.taxable,
	l.revenue_account_code, l.is_special_order, l.vendor_id, ROUND(l.special_order_cost * 10000)::bigint,
	l.quote_line_id, ROUND(l.quantity_allocated * 10000)::bigint,
	ROUND(l.quantity_backordered * 10000)::bigint, ROUND(l.quantity_fulfilled * 10000)::bigint, l.created_at`

func (r *PostgresRepository) orderLines(ctx context.Context, orderID uuid.UUID) ([]OrderLine, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+lineColumns+`
		FROM order_lines l
		LEFT JOIN charge_codes cc ON cc.id = l.charge_code_id
		WHERE l.order_id = $1
		ORDER BY l.position, l.created_at, l.id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to get order lines: %w", err)
	}
	defer rows.Close()
	out := []OrderLine{}
	for rows.Next() {
		var (
			l                   OrderLine
			quantity            *int64
			uom, priceUOM       *string
			uomQty, priceUomQty *int64
			unitPrice, priced   *int64
			discountPct         *int64
			discountAmt         *int64
			lineTotal           *int64
			specialCost         *int64
			source              string
		)
		if err := rows.Scan(&l.ID, &l.Position, &l.LineType, &l.ParentLineID, &l.ProductID, &l.ChargeCodeID,
			&l.ChargeCode, &l.SKU, &l.Description,
			&quantity, &uom, &priceUOM, &uomQty, &priceUomQty,
			&unitPrice, &priced, &source,
			&l.OverrideReason, &discountPct, &discountAmt,
			&l.DiscountReason, &l.PriceAdjustedBy, &lineTotal, &l.Taxable,
			&l.RevenueAccountCode, &l.IsSpecialOrder, &l.VendorID, &specialCost,
			&l.QuoteLineID, qtyScanPtr(&l.QuantityAllocated), qtyScanPtr(&l.QuantityBackordered), qtyScanPtr(&l.QuantityFulfilled), timeScanPtr(&l.CreatedAt)); err != nil {
			return nil, fmt.Errorf("failed to scan order line: %w", err)
		}
		l.PriceSource = salesdoc.PriceSource(source)
		l.Quantity = ptrQuantity(quantity)
		l.UOM = uom
		l.PriceUOM = priceUOM
		l.UOMQty = ptrQuantity(uomQty)
		l.PriceUOMQty = ptrQuantity(priceUomQty)
		l.UnitPrice = ptrPrice(unitPrice)
		l.PricedUnitPrice = ptrPrice(priced)
		l.DiscountPercent = ptrQuantity(discountPct)
		l.DiscountAmount = ptrCents(discountAmt)
		l.LineTotal = ptrCents(lineTotal)
		l.SpecialOrderCost = ptrPrice(specialCost)
		out = append(out, l)
	}
	return out, rows.Err()
}

// qtyScanPtr and timeScanPtr adapt the order line's own quantity and
// timestamp fields to row scan targets.
func qtyScanPtr(q *httpx.Quantity) *int64        { return (*int64)(q) }
func timeScanPtr(ts *httpx.Timestamp) *time.Time { return &ts.Time }

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func ptrQuantity(v *int64) *httpx.Quantity {
	if v == nil {
		return nil
	}
	q := httpx.Quantity(*v)
	return &q
}

func ptrPrice(v *int64) *httpx.Price {
	if v == nil {
		return nil
	}
	p := httpx.Price(*v)
	return &p
}

func ptrCents(v *int64) *httpx.Cents {
	if v == nil {
		return nil
	}
	c := httpx.Cents(*v)
	return &c
}

// LockOrder takes the order row FOR NO KEY UPDATE: it serializes every act on
// the order (a second lock waits) while staying compatible with the FOR KEY
// SHARE a foreign key insert takes on the row, so the allocation subscriber's
// request insert never waits behind a confirm (ADR 0003 section 2, ADR 0005 5.4).
func (r *PostgresRepository) LockOrder(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM orders WHERE id = $1 AND ($2::uuid IS NULL OR branch_id = $2) FOR NO KEY UPDATE`,
		id, middleware.BranchIDForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock order: %w", err)
	}
	return nil
}

func nullIfEmpty(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func tsArg(t *httpx.Timestamp) any {
	if t == nil {
		return nil
	}
	return t.Time
}

func dateArg(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

// InsertOrder writes the header and its lines. The caller has priced the
// document, resolved its tax and minted its number; the revision starts at 1.
func (r *PostgresRepository) InsertOrder(ctx context.Context, o *Order) error {
	exec := r.db.GetExecutor(ctx)
	var branchArg any
	if o.BranchID != uuid.Nil {
		branchArg = o.BranchID
	}
	var shipSnapshot any
	if o.ShipTo != nil {
		raw, err := jsonMarshal(o.ShipTo)
		if err != nil {
			return err
		}
		shipSnapshot = raw
	}
	_, err := exec.Exec(ctx, `
		INSERT INTO orders (
			id, number, customer_id, quote_id, project_id, status, revision, currency,
			delivery_type, ship_to_id, ship_to_snapshot, customer_po, ordered_by_contact_id,
			salesperson_id, scheduled_delivery_date,
			subtotal, tax_amount, tax_rate, tax_exempt, tax_source, total_amount,
			created_at, updated_at, branch_id
		) VALUES ($1, $2, $3, $4, $5, $6, 1, $7,
			$8, $9, $10, $11, $12, $13, $14::date,
			$15::numeric / 100, $16::numeric / 100, $17, $18, $19, $20::numeric / 100,
			$21, $21, COALESCE($22::uuid, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')))`,
		o.ID, o.Number, o.CustomerID, o.QuoteID, o.JobID, string(o.Status), o.Currency,
		string(o.DeliveryType), o.ShipToID, shipSnapshot, nullIfEmpty(o.CustomerPO), o.OrderedByContactID,
		o.SalespersonID, dateArg(o.ScheduledDeliveryDate),
		int64(o.SubtotalCents), int64(o.TaxCents), rateArg(o.TaxRatePercent), o.TaxExempt, string(o.TaxSource),
		int64(o.TotalCents), o.CreatedAt.Time, branchArg)
	if err != nil {
		return mapWriteError(err, "failed to insert order")
	}
	return r.insertLines(ctx, o)
}

// rateArg renders the wire percent for storage, or NULL when the provider
// answered.
func rateArg(percent *string) any {
	if percent == nil {
		return nil
	}
	rate, err := PercentToRate(*percent)
	if err != nil {
		return nil
	}
	return rate
}

func (r *PostgresRepository) insertLines(ctx context.Context, o *Order) error {
	exec := r.db.GetExecutor(ctx)
	for i := range o.Lines {
		l := &o.Lines[i]
		_, err := exec.Exec(ctx, `
			INSERT INTO order_lines (
				id, order_id, position, line_type, parent_line_id, product_id, charge_code_id,
				sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty,
				unit_price, priced_unit_price, price_source, override_reason,
				discount_percent, discount_amount, discount_reason, price_adjusted_by,
				line_total, taxable, revenue_account_code,
				is_special_order, vendor_id, special_order_cost, quote_line_id, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10::numeric / 10000, $11, $12, $13::numeric / 10000, $14::numeric / 10000,
				$15::numeric / 10000, $16::numeric / 10000, $17, $18,
				$19::numeric / 10000, $20::numeric / 100, $21, $22,
				$23::numeric / 100, $24, $25,
				$26, $27, $28::numeric / 10000, $29, $30)`,
			l.ID, o.ID, l.Position, string(l.LineType), l.ParentLineID, productArg(l.ProductID), l.ChargeCodeID,
			nullIfEmpty(l.SKU), l.Description, qtyArg(l.Quantity), strArg(l.UOM), strArg(l.PriceUOM),
			qtyArg(l.UOMQty), qtyArg(l.PriceUOMQty),
			priceArg(l.UnitPrice), priceArg(l.PricedUnitPrice), string(l.PriceSource), nullIfEmpty(l.OverrideReason),
			qtyArg(l.DiscountPercent), centsArg(l.DiscountAmount), nullIfEmpty(l.DiscountReason), nullIfEmpty(l.PriceAdjustedBy),
			centsArg(l.LineTotal), l.Taxable, nullIfEmpty(l.RevenueAccountCode),
			l.IsSpecialOrder, l.VendorID, priceArg(l.SpecialOrderCost), l.QuoteLineID, l.CreatedAt.Time)
		if err != nil {
			return mapWriteError(err, "failed to insert order line")
		}
	}
	return nil
}

func productArg(id *uuid.UUID) any {
	if id == nil || *id == uuid.Nil {
		return nil
	}
	return *id
}

func strArg(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func qtyArg(q *httpx.Quantity) any {
	if q == nil {
		return nil
	}
	return int64(*q)
}

func priceArg(p *httpx.Price) any {
	if p == nil {
		return nil
	}
	return int64(*p)
}

func centsArg(c *httpx.Cents) any {
	if c == nil {
		return nil
	}
	return int64(*c)
}

// ReplaceDraft replaces the header fields an edit owns and all lines, moving
// the revision (the recipe's edit rule).
func (r *PostgresRepository) ReplaceDraft(ctx context.Context, o *Order) error {
	exec := r.db.GetExecutor(ctx)
	_, err := exec.Exec(ctx, `
		UPDATE orders
		SET customer_id = $2, project_id = $3, delivery_type = $4, ship_to_id = $5, customer_po = $6,
			ordered_by_contact_id = $7, salesperson_id = $8, scheduled_delivery_date = $9::date,
			subtotal = $10::numeric / 100, tax_amount = $11::numeric / 100, tax_rate = $12,
			tax_exempt = $13, tax_source = $14, total_amount = $15::numeric / 100,
			revision = revision + 1, updated_at = NOW()
		WHERE id = $1`,
		o.ID, o.CustomerID, o.JobID, string(o.DeliveryType), o.ShipToID, nullIfEmpty(o.CustomerPO),
		o.OrderedByContactID, o.SalespersonID, dateArg(o.ScheduledDeliveryDate),
		int64(o.SubtotalCents), int64(o.TaxCents), rateArg(o.TaxRatePercent), o.TaxExempt, string(o.TaxSource),
		int64(o.TotalCents))
	if err != nil {
		return mapWriteError(err, "failed to update order header")
	}
	if _, err := exec.Exec(ctx, `DELETE FROM order_lines WHERE order_id = $1`, o.ID); err != nil {
		return fmt.Errorf("failed to delete old lines: %w", err)
	}
	return r.insertLines(ctx, o)
}

// SaveTransition writes the fields a transition owns and moves the revision.
// The caller holds the row lock.
func (r *PostgresRepository) SaveTransition(ctx context.Context, o *Order) error {
	var shipSnapshot any
	if o.ShipTo != nil {
		raw, err := jsonMarshal(o.ShipTo)
		if err != nil {
			return err
		}
		shipSnapshot = raw
	}
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE orders
		SET status = $2, hold_reason = $3, hold_note = $4, confirmed_at = $5,
			ship_to_id = COALESCE($6, ship_to_id), ship_to_snapshot = COALESCE($7, ship_to_snapshot),
			subtotal = $8::numeric / 100, tax_amount = $9::numeric / 100, tax_rate = $10,
			tax_exempt = $11, tax_source = $12, total_amount = $13::numeric / 100,
			revision = revision + 1, updated_at = NOW()
		WHERE id = $1`,
		o.ID, string(o.Status), holdReasonArg(o.HoldReason), nullIfEmpty(o.HoldNote), tsArg(o.ConfirmedAt),
		o.ShipToID, shipSnapshot,
		int64(o.SubtotalCents), int64(o.TaxCents), rateArg(o.TaxRatePercent), o.TaxExempt, string(o.TaxSource),
		int64(o.TotalCents))
	if err != nil {
		return mapWriteError(err, "failed to write order transition")
	}
	return nil
}

func holdReasonArg(h *HoldReason) any {
	if h == nil {
		return nil
	}
	return string(*h)
}

const listFilters = `
	WHERE ($1::uuid IS NULL OR o.branch_id = $1)
	  AND (cardinality($2::text[]) = 0 OR o.status = ANY($2))
	  AND ($3::uuid IS NULL OR o.customer_id = $3)
	  AND ($4::uuid IS NULL OR o.project_id = $4)
	  AND ($5::uuid IS NULL OR o.ship_to_id = $5)
	  AND ($6::text IS NULL OR o.delivery_type = $6)
	  AND ($7::uuid IS NULL OR o.quote_id = $7)`

func statusStrings(states []OrderStatus) []string {
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = string(s)
	}
	return out
}

func (r *PostgresRepository) ListOrders(ctx context.Context, f ListFilter) ([]OrderSummary, error) {
	var delivery any
	if f.DeliveryType != nil {
		delivery = string(*f.DeliveryType)
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT `+summaryColumns+summaryFrom+listFilters+`
		  AND ($8::timestamptz IS NULL OR (o.created_at, o.id) < ($8, $9::uuid))
		ORDER BY o.created_at DESC, o.id DESC
		LIMIT $10`,
		middleware.BranchIDForQuery(ctx), statusStrings(f.Statuses), f.CustomerID, f.JobID, f.ShipToID,
		delivery, f.QuoteID, f.AfterTime, f.AfterID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list orders: %w", err)
	}
	defer rows.Close()
	out := []OrderSummary{}
	for rows.Next() {
		var s OrderSummary
		if err := r.scanSummary(rows, &s); err != nil {
			return nil, fmt.Errorf("failed to scan order: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountOrders(ctx context.Context, f ListFilter) (int64, error) {
	var delivery any
	if f.DeliveryType != nil {
		delivery = string(*f.DeliveryType)
	}
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT count(*) FROM orders o`+listFilters,
		middleware.BranchIDForQuery(ctx), statusStrings(f.Statuses), f.CustomerID, f.JobID, f.ShipToID,
		delivery, f.QuoteID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count orders: %w", err)
	}
	return n, nil
}

func (r *PostgresRepository) LookupProducts(ctx context.Context, ids []uuid.UUID) (map[string]salesdoc.ProductRef, error) {
	out := map[string]salesdoc.ProductRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, COALESCE(sku, ''), COALESCE(description, ''), uom_primary::text,
		       ROUND(base_price * 10000)::bigint, ROUND(COALESCE(average_unit_cost, 0) * 10000)::bigint,
		       COALESCE(commission_rate, 0), is_kit, taxable
		FROM products WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to look up products: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			p          salesdoc.ProductRef
			base, cost int64
		)
		if err := rows.Scan(&p.ID, &p.SKU, &p.Description, &p.UOMPrimary, &base, &cost, &p.CommissionRate, &p.IsKit, &p.Taxable); err != nil {
			return nil, fmt.Errorf("failed to scan product: %w", err)
		}
		p.BasePrice, p.AverageCost = httpx.Price(base), httpx.Price(cost)
		out[p.ID.String()] = p
	}
	return out, rows.Err()
}

func (r *PostgresRepository) LookupKitComponents(ctx context.Context, kitIDs []uuid.UUID) (map[string][]salesdoc.KitComponent, error) {
	out := map[string][]salesdoc.KitComponent{}
	if len(kitIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT kit_product_id, component_product_id, ROUND(quantity * 10000)::bigint, position
		FROM product_kit_components WHERE kit_product_id = ANY($1)
		ORDER BY kit_product_id, position`, kitIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to look up kit components: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k salesdoc.KitComponent
		var qty int64
		if err := rows.Scan(&k.KitProductID, &k.ComponentProductID, &qty, &k.Position); err != nil {
			return nil, fmt.Errorf("failed to scan kit component: %w", err)
		}
		k.Quantity = httpx.Quantity(qty)
		out[k.KitProductID.String()] = append(out[k.KitProductID.String()], k)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) LookupChargeCodes(ctx context.Context, codes []string) (map[string]salesdoc.ChargeCode, error) {
	out := map[string]salesdoc.ChargeCode{}
	if len(codes) == 0 {
		return out, nil
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, code, name, revenue_account_code, taxable,
		       ROUND(default_unit_price * 10000)::bigint, is_active, revision
		FROM charge_codes WHERE code = ANY($1)`, codes)
	if err != nil {
		return nil, fmt.Errorf("failed to look up charge codes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			c     salesdoc.ChargeCode
			price *int64
		)
		if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.RevenueAccountCode, &c.Taxable, &price, &c.IsActive, &c.Revision); err != nil {
			return nil, fmt.Errorf("failed to scan charge code: %w", err)
		}
		if price != nil {
			p := httpx.Price(*price)
			c.DefaultUnitPrice = &p
		}
		out[c.Code] = c
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CustomerFacts(ctx context.Context, customerID uuid.UUID) (CustomerFacts, error) {
	var f CustomerFacts
	var credit *int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT TRUE, COALESCE(name, ''), COALESCE(currency, s.value), salesperson_id,
		      ROUND(credit_limit * 100)::bigint, po_required
		FROM customers
		CROSS JOIN (SELECT value FROM system_settings WHERE key = 'currency.default') s
		WHERE id = $1`, customerID).
		Scan(&f.Exists, &f.Name, &f.Currency, &f.SalespersonID, &credit, &f.PORequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("failed to read customer facts: %w", err)
	}
	if credit != nil {
		c := httpx.Cents(*credit)
		f.CreditLimitCents = &c
	}
	return f, nil
}

func (r *PostgresRepository) ContactAuthority(ctx context.Context, contactID uuid.UUID) (ContactAuthority, error) {
	var a ContactAuthority
	var limit *int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT TRUE, can_place_orders, ROUND(order_limit * 100)::bigint
		FROM customer_contacts WHERE id = $1`, contactID).
		Scan(&a.Exists, &a.CanPlaceOrders, &limit)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return a, fmt.Errorf("failed to read contact authority: %w", err)
	}
	if limit != nil {
		c := httpx.Cents(*limit)
		a.OrderLimitCents = &c
	}
	return a, nil
}

func (r *PostgresRepository) DefaultShipToID(ctx context.Context, customerID uuid.UUID) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM customer_ship_tos WHERE customer_id = $1 AND is_default LIMIT 1`, customerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read default ship-to: %w", err)
	}
	return id, nil
}

func (r *PostgresRepository) ShipTo(ctx context.Context, shipToID uuid.UUID) (*ShipToSnapshot, *string, error) {
	var (
		s    ShipToSnapshot
		rate *string
	)
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT id, code, COALESCE(name, ''), COALESCE(line1, ''), line2, COALESCE(city, ''),
		       COALESCE(region, ''), COALESCE(postal_code, ''), country, phone, delivery_instructions,
		       tax_rate::text
		FROM customer_ship_tos WHERE id = $1`, shipToID).
		Scan(&s.ID, &s.Code, &s.Name, &s.Line1, &s.Line2, &s.City, &s.Region, &s.PostalCode, &s.Country,
			&s.Phone, &s.DeliveryInstructions, &rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, validationFailed("a referenced record does not exist",
			httpx.FieldError{Field: "ship_to_id", Message: "no such ship-to"})
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read ship-to: %w", err)
	}
	return &s, rate, nil
}

func (r *PostgresRepository) BranchTaxRate(ctx context.Context, branchID uuid.UUID) (*string, error) {
	var rate *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT default_tax_rate::text FROM locations WHERE id = $1`, branchID).Scan(&rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read branch tax rate: %w", err)
	}
	return rate, nil
}

func (r *PostgresRepository) CustomerExempt(ctx context.Context, customerID uuid.UUID) (bool, error) {
	var exempt bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tax_exemptions WHERE customer_id = $1 AND is_active)`, customerID).Scan(&exempt)
	if err != nil {
		return false, fmt.Errorf("failed to read tax exemptions: %w", err)
	}
	return exempt, nil
}

// OpenReceivableCents is the customer's open receivable plus the unbilled
// remainder of the customer's other orders in confirmed, backordered or
// on_hold (ADR 0005 5.3), minus the order being confirmed (its own total is
// added by the caller): the credit check reads documents, never
// customers.balance_due, which history left stale.
func (r *PostgresRepository) OpenReceivableCents(ctx context.Context, customerID uuid.UUID, excludingOrder *uuid.UUID) (int64, error) {
	var open int64
	// The open receivable: each open invoice's total less the payments
	// recorded against it (in C2-2 and C2-3 the sum over the customer's
	// invoices in UNPAID or PARTIAL; OVERDUE is no longer a status).
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(ROUND(i.total_amount * 100)::bigint
		                   - COALESCE((SELECT SUM(ROUND(p.amount * 100)::bigint)
		                               FROM payments p WHERE p.invoice_id = i.id), 0)), 0)
		FROM invoices i
		WHERE i.customer_id = $1 AND i.status IN ('UNPAID', 'PARTIAL')`, customerID).Scan(&open)
	if err != nil {
		return 0, fmt.Errorf("failed to sum the open receivable: %w", err)
	}

	// The unbilled remainder of the customer's other live orders: total less
	// the totals of its invoices not in void, clamped at zero.
	var remainder int64
	err = r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(GREATEST(
			ROUND(o.total_amount * 100)::bigint
			- COALESCE((SELECT SUM(ROUND(i.total_amount * 100)::bigint)
			            FROM invoices i WHERE i.order_id = o.id AND i.status <> 'VOID'), 0), 0)), 0)
		FROM orders o
		WHERE o.customer_id = $1
		  AND o.status IN ('CONFIRMED', 'BACKORDERED', 'ON_HOLD')
		  AND ($2::uuid IS NULL OR o.id <> $2)`, customerID, excludingOrder).Scan(&remainder)
	if err != nil {
		return 0, fmt.Errorf("failed to sum unbilled remainders: %w", err)
	}
	return open + remainder, nil
}

func (r *PostgresRepository) HasInvoices(ctx context.Context, orderID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM invoices WHERE order_id = $1 AND status <> 'VOID')`, orderID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to read invoices: %w", err)
	}
	return exists, nil
}

func (r *PostgresRepository) OrderExistsForQuote(ctx context.Context, quoteID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM orders WHERE quote_id = $1 AND status <> 'CANCELLED')`, quoteID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to read orders for quote: %w", err)
	}
	return exists, nil
}

// SaveLineQuantities writes each line's allocated, back ordered and fulfilled
// quantities.
func (r *PostgresRepository) SaveLineQuantities(ctx context.Context, lines []OrderLine) error {
	exec := r.db.GetExecutor(ctx)
	for i := range lines {
		l := &lines[i]
		if _, err := exec.Exec(ctx, `
			UPDATE order_lines
			SET quantity_allocated = $2::numeric / 10000, quantity_backordered = $3::numeric / 10000,
				quantity_fulfilled = $4::numeric / 10000
			WHERE id = $1`,
			l.ID, int64(l.QuantityAllocated), int64(l.QuantityBackordered), int64(l.QuantityFulfilled)); err != nil {
			return fmt.Errorf("failed to write the line quantities: %w", err)
		}
	}
	return nil
}

// QueueAllocationRequests inserts one request for each back ordered order of
// the branch with a back ordered line on one of the products, in the orders'
// (confirmed_at, id) order so position carries that order, ON CONFLICT DO
// NOTHING. It takes no lock but the request rows' own and reads through the
// caller's transaction.
func (r *PostgresRepository) QueueAllocationRequests(ctx context.Context, branchID uuid.UUID, productIDs []uuid.UUID) (int, error) {
	if len(productIDs) == 0 {
		return 0, nil
	}
	ct, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO order_allocation_requests (order_id)
		SELECT o.id FROM orders o
		WHERE o.status = 'BACKORDERED' AND o.branch_id = $1
		  AND EXISTS (SELECT 1 FROM order_lines l
		              WHERE l.order_id = o.id AND l.product_id = ANY($2) AND l.quantity_backordered > 0)
		ORDER BY o.confirmed_at, o.id
		ON CONFLICT (order_id) DO NOTHING`, branchID, productIDs)
	if err != nil {
		return 0, fmt.Errorf("failed to queue allocation requests: %w", err)
	}
	return int(ct.RowsAffected()), nil
}

// ClaimAllocationRequest takes the lowest position request nobody holds, row
// locked FOR UPDATE SKIP LOCKED (lock order step 0).
func (r *PostgresRepository) ClaimAllocationRequest(ctx context.Context) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT order_id FROM order_allocation_requests ORDER BY position LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to claim an allocation request: %w", err)
	}
	return id, true, nil
}

func (r *PostgresRepository) DeleteAllocationRequest(ctx context.Context, orderID uuid.UUID) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM order_allocation_requests WHERE order_id = $1`, orderID); err != nil {
		return fmt.Errorf("failed to delete the allocation request: %w", err)
	}
	return nil
}

// LockCustomerCredit serializes the acts that read one customer's credit
// exposure (a confirm, a release, a fulfilment) with a transaction scoped
// advisory lock keyed on the customer, taken right after the order row. It is
// not a row lock, so it adds no edge to the lock order of ADR 0005 section 11:
// the only acts that take it are those three, and each takes inventory rows in
// the one order after it.
func (r *PostgresRepository) LockCustomerCredit(ctx context.Context, customerID uuid.UUID) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('order-credit:' || $1::text, 0))`, customerID.String()); err != nil {
		return fmt.Errorf("failed to serialize the customer's credit acts: %w", err)
	}
	return nil
}

// UnbilledRemainderCents is the order's total less the totals of its invoices
// not in void, clamped at zero (ADR 0005 5.3).
func (r *PostgresRepository) UnbilledRemainderCents(ctx context.Context, orderID uuid.UUID) (int64, error) {
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(GREATEST(
			ROUND(o.total_amount * 100)::bigint
			- COALESCE((SELECT SUM(ROUND(i.total_amount * 100)::bigint) FROM invoices i
			            WHERE i.order_id = o.id AND i.status <> 'VOID'), 0), 0), 0)
		FROM orders o WHERE o.id = $1`, orderID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to read the unbilled remainder: %w", err)
	}
	return n, nil
}

// BranchLocalDate is the branch's local calendar date at the instant (the
// business date of ADR 0005 section 8.1).
func (r *PostgresRepository) BranchLocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error) {
	var d time.Time
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT ($1::timestamptz AT TIME ZONE COALESCE((SELECT timezone FROM locations WHERE id = $2), 'UTC'))::date`,
		at, branchID).Scan(&d)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to read the branch's date: %w", err)
	}
	return d, nil
}

// DeliveryOrderID answers the order a delivery belongs to.
func (r *PostgresRepository) DeliveryOrderID(ctx context.Context, deliveryID uuid.UUID) (uuid.UUID, bool, error) {
	var orderID *uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT order_id FROM deliveries WHERE id = $1`, deliveryID).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && orderID == nil) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to read the delivery: %w", err)
	}
	return *orderID, true, nil
}

// NonStockReceipts is what the received purchase order lines linked to a non
// stock (or direct ship) order line posted to 1030, and what the order line's
// earlier bills have already relieved (ADR 0005 8.4 as PR 35 amends it).
type NonStockReceipts struct {
	PostedCents   httpx.Cents    // sum of round(qty_received x cost) per linked line, the Extend a receipt posts
	Received      httpx.Quantity // sum of qty_received over the linked lines, scale 4
	RelievedCents httpx.Cents    // the cost the order line's invoice lines already carry
}

// NonStockReceiptsFor reads the receipts linked to an order line; false when
// none is received yet.
func (r *PostgresRepository) NonStockReceiptsFor(ctx context.Context, orderLineID uuid.UUID) (NonStockReceipts, bool, error) {
	var out NonStockReceipts
	var posted, received, relieved int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(ROUND(qty_received * cost, 2) * 100), 0)::bigint,
		       COALESCE(SUM(ROUND(qty_received * 10000)), 0)::bigint,
		       (SELECT COALESCE(SUM(ROUND(il.cost * 100)), 0)::bigint FROM invoice_lines il
		        JOIN invoices iv ON iv.id = il.invoice_id AND iv.status <> 'VOID' WHERE il.order_line_id = $1)
		FROM purchase_order_lines
		WHERE linked_so_line_id = $1 AND COALESCE(qty_received, 0) > 0`, orderLineID).Scan(&posted, &received, &relieved)
	if err != nil {
		return out, false, fmt.Errorf("failed to read the special order receipts: %w", err)
	}
	if received <= 0 {
		return out, false, nil
	}
	return NonStockReceipts{PostedCents: httpx.Cents(posted), Received: httpx.Quantity(received), RelievedCents: httpx.Cents(relieved)}, true, nil
}

// FulfillmentRequest is a row of order_fulfillment_requests (ADR 0005 5.5):
// a completed delivery waiting to be billed.
type FulfillmentRequest struct {
	DeliveryID uuid.UUID        `json:"delivery_id"`
	OrderID    uuid.UUID        `json:"order_id"`
	Position   int64            `json:"position"`
	Attempts   int              `json:"attempts"`
	LastError  *string          `json:"last_error"`
	ParkedAt   *httpx.Timestamp `json:"parked_at"`
	CreatedAt  httpx.Timestamp  `json:"created_at"`
}

// RequestFilter is the request list's filters and keyset position.
type RequestFilter struct {
	Parked        *bool
	OrderID       *uuid.UUID
	AfterPosition *int64
	Limit         int
}

// maxFulfilmentAttempts is how many failed attempts park a request.
const maxFulfilmentAttempts = 10

func (r *PostgresRepository) InsertFulfillmentRequest(ctx context.Context, deliveryID, orderID uuid.UUID) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO order_fulfillment_requests (delivery_id, order_id) VALUES ($1, $2) ON CONFLICT (delivery_id) DO NOTHING`,
		deliveryID, orderID); err != nil {
		return fmt.Errorf("failed to queue the fulfilment request: %w", err)
	}
	return nil
}

const requestColumns = `delivery_id, order_id, position, attempts, last_error, parked_at, created_at`

func scanRequest(row pgx.Row) (*FulfillmentRequest, error) {
	var q FulfillmentRequest
	var parked *time.Time
	if err := row.Scan(&q.DeliveryID, &q.OrderID, &q.Position, &q.Attempts, &q.LastError, &parked, &q.CreatedAt.Time); err != nil {
		return nil, err
	}
	if parked != nil {
		ts := httpx.TimestampOf(*parked)
		q.ParkedAt = &ts
	}
	return &q, nil
}

// NextFulfillmentRequest peeks the lowest position request that is not parked,
// without locking it: the worker prices the tax with the provider between this
// read and the claim, and a provider call never holds a lock.
func (r *PostgresRepository) NextFulfillmentRequest(ctx context.Context) (*FulfillmentRequest, error) {
	q, err := scanRequest(r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+requestColumns+` FROM order_fulfillment_requests WHERE parked_at IS NULL ORDER BY position LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the next fulfilment request: %w", err)
	}
	return q, nil
}

// ClaimFulfillmentRequest takes the named request FOR UPDATE SKIP LOCKED
// (lock order step 0); false when another worker holds it or it is gone or
// parked.
func (r *PostgresRepository) ClaimFulfillmentRequest(ctx context.Context, deliveryID uuid.UUID) (bool, error) {
	var id uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT delivery_id FROM order_fulfillment_requests WHERE delivery_id = $1 AND parked_at IS NULL FOR UPDATE SKIP LOCKED`,
		deliveryID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to claim the fulfilment request: %w", err)
	}
	return true, nil
}

func (r *PostgresRepository) DeleteFulfillmentRequest(ctx context.Context, deliveryID uuid.UUID) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM order_fulfillment_requests WHERE delivery_id = $1`, deliveryID); err != nil {
		return fmt.Errorf("failed to delete the fulfilment request: %w", err)
	}
	return nil
}

// RecordFulfillmentFailure counts a failed attempt and keeps its error; the
// attempt that reaches maxAttempts parks the request. It answers whether this
// call parked it.
func (r *PostgresRepository) RecordFulfillmentFailure(ctx context.Context, deliveryID uuid.UUID, lastError string, maxAttempts int) (bool, error) {
	var parked bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE order_fulfillment_requests
		SET attempts = attempts + 1, last_error = $2,
		    parked_at = CASE WHEN attempts + 1 >= $3 THEN clock_timestamp() ELSE parked_at END
		WHERE delivery_id = $1 AND parked_at IS NULL
		RETURNING parked_at IS NOT NULL`, deliveryID, lastError, maxAttempts).Scan(&parked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to record the fulfilment failure: %w", err)
	}
	return parked, nil
}

// ListFulfillmentRequests lists requests by position, inside the caller's
// branch wall (the order's branch).
func (r *PostgresRepository) ListFulfillmentRequests(ctx context.Context, f RequestFilter) ([]FulfillmentRequest, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT q.delivery_id, q.order_id, q.position, q.attempts, q.last_error, q.parked_at, q.created_at
		FROM order_fulfillment_requests q JOIN orders o ON o.id = q.order_id
		WHERE ($1::uuid IS NULL OR o.branch_id = $1)
		  AND ($2::bool IS NULL OR (q.parked_at IS NOT NULL) = $2)
		  AND ($3::uuid IS NULL OR q.order_id = $3)
		  AND ($4::bigint IS NULL OR q.position > $4)
		ORDER BY q.position LIMIT $5`,
		middleware.BranchIDForQuery(ctx), f.Parked, f.OrderID, f.AfterPosition, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list the fulfilment requests: %w", err)
	}
	defer rows.Close()
	out := []FulfillmentRequest{}
	for rows.Next() {
		q, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *q)
	}
	return out, rows.Err()
}

// RetryFulfillmentRequest clears parked_at and attempts; found is false when
// there is no such request in the caller's branch wall.
func (r *PostgresRepository) RetryFulfillmentRequest(ctx context.Context, deliveryID uuid.UUID) (uuid.UUID, bool, error) {
	var orderID uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE order_fulfillment_requests q SET parked_at = NULL, attempts = 0
		FROM orders o
		WHERE q.delivery_id = $1 AND o.id = q.order_id AND ($2::uuid IS NULL OR o.branch_id = $2)
		RETURNING q.order_id`, deliveryID, middleware.BranchIDForQuery(ctx)).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to retry the fulfilment request: %w", err)
	}
	return orderID, true, nil
}

// DeliveryRequestOrder answers the order of a queued request.
func (r *PostgresRepository) DeliveryRequestOrder(ctx context.Context, deliveryID uuid.UUID) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT order_id FROM order_fulfillment_requests WHERE delivery_id = $1`, deliveryID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to read the fulfilment request: %w", err)
	}
	return id, true, nil
}
