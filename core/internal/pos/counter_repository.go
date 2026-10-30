// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/tint"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The store half of the counter's account sales, tenders and documents
// (migration 126): the standing's open credit memos, the buyer roster, the
// check limit, the manager override and the sale's buyer.

// settingCheckLimit is the database wide check limit in cents; a customer's
// own check_limit wins, and neither set means no limit.
const settingCheckLimit = "pos.check_limit_cents"

// OpenCreditMemoCents is the customer's open store credit: the open amounts
// of its open and partly used credit memos, as a positive figure.
func (r *PostgresRepository) OpenCreditMemoCents(ctx context.Context, customerID uuid.UUID) (int64, error) {
	var cents int64
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT COALESCE(-SUM(ROUND(m.amount_open * 100)::bigint), 0) FROM credit_memos m
		WHERE m.customer_id = $1 AND m.status IN ('OPEN', 'PARTIAL')`, customerID).Scan(&cents)
	if err != nil {
		return 0, fmt.Errorf("failed to read the open credit memos: %w", err)
	}
	return cents, nil
}

// DefaultCheckLimitCents reads the database wide check limit; nil when the
// setting is absent or empty (no limit).
func (r *PostgresRepository) DefaultCheckLimitCents(ctx context.Context) (*int64, error) {
	var raw *string
	err := r.ex(ctx).QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, settingCheckLimit).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the check limit: %w", err)
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(*raw), 10, 64)
	if perr != nil || n < 0 {
		return nil, fmt.Errorf("the %s setting is not a whole number of cents: %q", settingCheckLimit, *raw)
	}
	return &n, nil
}

// Buyers lists the customer's roster: its active contacts flagged as
// authorized buyers, by name.
func (r *PostgresRepository) Buyers(ctx context.Context, customerID uuid.UUID) ([]Buyer, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT c.id, btrim(c.first_name || ' ' || c.last_name),
			CASE WHEN c.buyer_sale_cap IS NULL THEN NULL ELSE ROUND(c.buyer_sale_cap * 100)::bigint END
		FROM customer_contacts c
		WHERE c.customer_id = $1 AND c.authorized_buyer AND COALESCE(c.is_active, TRUE)
		ORDER BY c.last_name, c.first_name, c.id`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the buyer roster: %w", err)
	}
	defer rows.Close()
	out := []Buyer{}
	for rows.Next() {
		var b Buyer
		if err := rows.Scan(&b.ID, &b.Name, &b.CapCents); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CounterAlerts lists the customer's alerts that speak at the counter: the
// ones still open that are marked for the counter, oldest first.
func (r *PostgresRepository) CounterAlerts(ctx context.Context, customerID uuid.UUID) ([]CounterAlert, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, kind, text, acknowledge_required
		FROM customer_alerts
		WHERE customer_id = $1 AND ended_at IS NULL AND show_at_counter
		ORDER BY created_at, id`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the counter alerts: %w", err)
	}
	defer rows.Close()
	out := []CounterAlert{}
	for rows.Next() {
		var a CounterAlert
		if err := rows.Scan(&a.ID, &a.Kind, &a.Text, &a.AcknowledgeRequired); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// BuyerOf reads one roster buyer of the customer: false when the contact is
// not the customer's, is inactive, or is not flagged as a buyer.
func (r *PostgresRepository) BuyerOf(ctx context.Context, customerID, contactID uuid.UUID) (*Buyer, bool, error) {
	var b Buyer
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT c.id, btrim(c.first_name || ' ' || c.last_name),
			CASE WHEN c.buyer_sale_cap IS NULL THEN NULL ELSE ROUND(c.buyer_sale_cap * 100)::bigint END
		FROM customer_contacts c
		WHERE c.id = $2 AND c.customer_id = $1 AND c.authorized_buyer AND COALESCE(c.is_active, TRUE)`,
		customerID, contactID).Scan(&b.ID, &b.Name, &b.CapCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read the buyer: %w", err)
	}
	return &b, true, nil
}

// SetSaleBuyer names (or clears) the roster buyer of an open sale. It does not
// move the revision: the attach that calls it has just moved it once.
func (r *PostgresRepository) SetSaleBuyer(ctx context.Context, saleID uuid.UUID, contactID *uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET buyer_contact_id = $2 WHERE id = $1`, saleID, contactID)
	if err != nil {
		return fmt.Errorf("failed to name the sale's buyer: %w", mapWriteError(err))
	}
	return nil
}

// InsertOverride writes one live override; a live one of the same sale and
// kind answers 409 override_exists.
func (r *PostgresRepository) InsertOverride(ctx context.Context, o *AccountOverride) error {
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	var created time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		INSERT INTO pos_account_overrides (id, sale_id, kind, approved_by, reason, cap)
		VALUES ($1, $2, $3, $4, $5, $6::numeric / 100)
		RETURNING created_at`, o.ID, o.SaleID, o.Kind, o.ApprovedBy, o.Reason, o.CapCents).Scan(&created)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return conflict("override_exists", "this sale already holds a live "+o.Kind+" override: complete the sale to spend it")
	}
	if err != nil {
		return fmt.Errorf("failed to record the override: %w", mapWriteError(err))
	}
	o.CreatedAt = httpx.TimestampOf(created)
	return nil
}

// LiveOverride reads the sale's unspent override of kind; nil when none.
func (r *PostgresRepository) LiveOverride(ctx context.Context, saleID uuid.UUID, kind string) (*AccountOverride, error) {
	var o AccountOverride
	var created time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT id, sale_id, kind, approved_by, reason, ROUND(cap * 100)::bigint, created_at
		FROM pos_account_overrides WHERE sale_id = $1 AND kind = $2 AND consumed_at IS NULL`,
		saleID, kind).Scan(&o.ID, &o.SaleID, &o.Kind, &o.ApprovedBy, &o.Reason, &o.CapCents, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the override: %w", err)
	}
	o.CreatedAt = httpx.TimestampOf(created)
	return &o, nil
}

// ConsumeOverride spends the sale's live override of kind when its cap covers
// need cents, in the caller's transaction. The one statement is the guard: of
// any number of racing spends exactly one returns the row.
func (r *PostgresRepository) ConsumeOverride(ctx context.Context, saleID uuid.UUID, kind string, need int64) (*AccountOverride, bool, error) {
	var o AccountOverride
	var created time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		UPDATE pos_account_overrides SET consumed_at = NOW()
		WHERE sale_id = $1 AND kind = $2 AND consumed_at IS NULL AND cap >= $3::numeric / 100
		RETURNING id, sale_id, kind, approved_by, reason, ROUND(cap * 100)::bigint, created_at`,
		saleID, kind, need).Scan(&o.ID, &o.SaleID, &o.Kind, &o.ApprovedBy, &o.Reason, &o.CapCents, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to spend the override: %w", err)
	}
	o.CreatedAt = httpx.TimestampOf(created)
	return &o, true, nil
}

// LockOpenCreditMemos locks the customer's open and partly used credit memos
// in id order (ADR 0005 section 11, step 3) and answers them with their open
// cents as positive figures.
func (r *PostgresRepository) LockOpenCreditMemos(ctx context.Context, customerID uuid.UUID, currency string) ([]openMemo, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT m.id, COALESCE(m.number, ''), m.memo_date, -ROUND(m.amount_open * 100)::bigint
		FROM credit_memos m
		WHERE m.customer_id = $1 AND m.currency = $2 AND m.status IN ('OPEN', 'PARTIAL') AND m.amount_open < 0
		ORDER BY m.id FOR UPDATE`, customerID, currency)
	if err != nil {
		return nil, fmt.Errorf("failed to lock the open credit memos: %w", err)
	}
	defer rows.Close()
	var out []openMemo
	for rows.Next() {
		var m openMemo
		if err := rows.Scan(&m.ID, &m.Number, &m.MemoDate, &m.OpenCents); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LockCreditMemo locks one open or partly used credit memo by id (used by an
// exchange for the memo its own return just posted in the same transaction,
// without locking other open memos out of section 11's order).
func (r *PostgresRepository) LockCreditMemo(ctx context.Context, memoID uuid.UUID) (openMemo, error) {
	var m openMemo
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT m.id, COALESCE(m.number, ''), m.memo_date, -ROUND(m.amount_open * 100)::bigint
		FROM credit_memos m
		WHERE m.id = $1 AND m.status IN ('OPEN', 'PARTIAL') AND m.amount_open < 0
		FOR UPDATE`, memoID).Scan(&m.ID, &m.Number, &m.MemoDate, &m.OpenCents)
	if err != nil {
		return openMemo{}, fmt.Errorf("failed to lock the credit memo: %w", err)
	}
	return m, nil
}

// LockCustomer locks the customer row FOR UPDATE (ADR 0005 section 11, step 7)
// before a gapless document counter (step 8) or a foreign-key child insert.
func (r *PostgresRepository) LockCustomer(ctx context.Context, customerID uuid.UUID) error {
	var id uuid.UUID
	if err := r.ex(ctx).QueryRow(ctx, `SELECT id FROM customers WHERE id = $1 FOR UPDATE`, customerID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("customer not found")
		}
		return fmt.Errorf("failed to lock the customer: %w", err)
	}
	return nil
}

// InsertSignature seals one signature to a sale; a sale already signed
// answers 409 signature_exists (the row never changes once written).
func (r *PostgresRepository) InsertSignature(ctx context.Context, sig *storedSignature) error {
	_, err := r.ex(ctx).Exec(ctx, `
		INSERT INTO pos_signatures (sale_id, signer_name, image, image_sha256, total_cents, signed_at, seal, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sig.SaleID, sig.SignerName, sig.Image, sig.ImageSHA256, int64(sig.TotalCents), sig.SignedAt.Time, sig.Seal, sig.CapturedBy)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return conflict("signature_exists", "this sale is already signed: a sealed signature never changes")
	}
	if err != nil {
		return fmt.Errorf("failed to seal the signature: %w", mapWriteError(err))
	}
	return nil
}

// GetSignature reads a sale's sealed signature with its image; false when the
// sale has none.
func (r *PostgresRepository) GetSignature(ctx context.Context, saleID uuid.UUID) (*storedSignature, bool, error) {
	var sig storedSignature
	var signed time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT sale_id, signer_name, image, image_sha256, total_cents, signed_at, seal, captured_by
		FROM pos_signatures WHERE sale_id = $1`, saleID).
		Scan(&sig.SaleID, &sig.SignerName, &sig.Image, &sig.ImageSHA256, &sig.TotalCents, &signed, &sig.Seal, &sig.CapturedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read the signature: %w", err)
	}
	sig.SignedAt = httpx.TimestampOf(signed)
	return &sig, true, nil
}

// BumpPrintCount counts one more print of the sale's receipt and answers the
// new count. The statement is the guard: racing prints are numbered apart, and
// a sale voided between the initial read and the update is refused without
// touching revision.
func (r *PostgresRepository) BumpPrintCount(ctx context.Context, saleID uuid.UUID) (int, error) {
	var n int
	err := r.ex(ctx).QueryRow(ctx, `UPDATE pos_transactions SET print_count = print_count + 1
		WHERE id = $1 AND status = 'COMPLETED'
		RETURNING print_count`, saleID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.InvalidStateTransition("cannot print receipt for a non-completed sale")
	}
	if err != nil {
		return 0, fmt.Errorf("failed to count the print: %w", err)
	}
	return n, nil
}

// UnbumpPrintCount gives one print back (a render that failed).
func (r *PostgresRepository) UnbumpPrintCount(ctx context.Context, saleID uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET print_count = GREATEST(print_count - 1, 0) WHERE id = $1`, saleID)
	return err
}

// receiptSaleFacts holds the completed sale and the identity facts the 80mm
// thermal receipt prints beside it.
type receiptSaleFacts struct {
	sale          Sale
	cashierName   string
	customerName  string
	accountNumber string
	jobName       string
}

// ReceiptSaleByInvoice reads the completed counter sale that owns invoiceID,
// with the cashier, customer and job names the thermal receipt prints.
func (r *PostgresRepository) ReceiptSaleByInvoice(ctx context.Context, invoiceID uuid.UUID) (*receiptSaleFacts, error) {
	var out receiptSaleFacts
	var completed *time.Time
	var created time.Time
	s := &out.sale
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT `+saleCols+`,
			COALESCE((SELECT NULLIF(btrim(st.full_name), '') FROM staff st WHERE st.id = t.cashier_id),
			         (SELECT NULLIF(btrim(st.email), '') FROM staff st WHERE st.id = t.cashier_id), ''),
			COALESCE((SELECT CASE WHEN c.account_number = 'WALK-IN' THEN '' ELSE c.name END FROM customers c WHERE c.id = t.customer_id), ''),
			COALESCE((SELECT CASE WHEN c.account_number = 'WALK-IN' THEN '' ELSE COALESCE(c.account_number, '') END FROM customers c WHERE c.id = t.customer_id), ''),
			COALESCE((SELECT p.name FROM projects p WHERE p.id = t.job_id), '')
		FROM pos_transactions t
		WHERE t.invoice_id = $1 AND `+middleware.Wall("t", 2, 3),
		invoiceID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).
		Scan(&s.ID, &s.Number, &s.Revision, &s.BranchID, &s.RegisterID, &s.CashierID, &s.CustomerID, &s.Currency,
			&s.SubtotalCents, &s.TaxCents, &s.TotalCents, &s.ChangeCents, &s.TillSessionID, &s.Status, &s.InvoiceID,
			&completed, &created, &s.CustomerPO, &s.JobID, &s.BuyerContactID, &s.PrintCount, &s.ExchangeReturnID,
			&s.DeliveryType, &s.ShipToID,
			&out.cashierName, &out.customerName, &out.accountNumber, &out.jobName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("sale not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the receipt's sale: %w", err)
	}
	s.CompletedAt = httpx.PtrTimestamp(completed)
	s.CreatedAt = httpx.TimestampOf(created)
	s.DeliveryType = strings.ToLower(s.DeliveryType)
	if s.DeliveryType == "" {
		s.DeliveryType = "pickup"
	}
	return &out, nil
}

// RestockingFeeBps reads the database wide restocking fee of each return
// reason, in basis points (pos.restocking_fee_bps.<reason>); a reason with no
// setting carries no fee. A setting that is not a whole number from 0 to
// 10000 is an error, never a silent zero.
func (r *PostgresRepository) RestockingFeeBps(ctx context.Context) (map[string]int, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT key, value FROM system_settings WHERE key LIKE $1 ORDER BY key`,
		settingRestockingFeePrefix+"%")
	if err != nil {
		return nil, fmt.Errorf("failed to read the restocking fees: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var value *string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		code := strings.TrimPrefix(key, settingRestockingFeePrefix)
		if !isReturnReason(code) || value == nil || strings.TrimSpace(*value) == "" {
			continue
		}
		n, perr := strconv.Atoi(strings.TrimSpace(*value))
		if perr != nil || n < 0 || n > 10000 {
			return nil, fmt.Errorf("the %s setting is not a whole number of basis points from 0 to 10000: %q", key, *value)
		}
		out[code] = n
	}
	return out, rows.Err()
}

// LockStock takes the inventory rows of the products at the branch FOR
// UPDATE in (product id, inventory id) order: the product ids come sorted by
// the caller, and the statement keeps the order (ADR 0005 section 11, step
// 6). An act that moves stock in two directions holds them all first.
func (r *PostgresRepository) LockStock(ctx context.Context, productIDs []uuid.UUID, branchID uuid.UUID) error {
	if len(productIDs) == 0 {
		return nil
	}
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT i.id FROM inventory i JOIN locations l ON l.id = i.location_id
		WHERE i.product_id = ANY($1) AND l.branch_id = $2
		ORDER BY i.product_id, i.id FOR UPDATE OF i`, productIDs, branchID)
	if err != nil {
		return fmt.Errorf("failed to lock the stock: %w", err)
	}
	rows.Close()
	return rows.Err()
}

// LinkExchange points a return and the sale that replaced its goods at each
// other, and records how the surplus went (cash, or left as credit).
func (r *PostgresRepository) LinkExchange(ctx context.Context, returnID, saleID uuid.UUID, method RefundMethod) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_returns SET exchange_sale_id = $2, refund_method = $3, updated_at = NOW()
		WHERE id = $1`, returnID, saleID, string(method)); err != nil {
		return fmt.Errorf("failed to link the exchange's return: %w", err)
	}
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET exchange_return_id = $2 WHERE id = $1`, saleID, returnID); err != nil {
		return fmt.Errorf("failed to link the exchange's sale: %w", err)
	}
	return nil
}

// TintChargeRule reads the dealer's tint charge rule (row CP-16): the flat
// figure per container and the tiers that may cover it. None set means no
// charge, the dominant retail model folding the tint into the shelf price.
func (r *PostgresRepository) TintChargeRule(ctx context.Context) (tint.ChargeRule, error) {
	return tint.ReadChargeRule(ctx, r.ex(ctx))
}

// TintBaseTier reads the tier of the base a tint mixes in: "deep" for the
// deepest base of a system, "flat" otherwise, and "" when the base cannot be
// resolved (a hand written line with no formula and no matching base). The
// server resolves it from the formula first and from the line's base next, so
// the charge never differs from what the preview quoted and never follows a
// tier word the screen sent.
func (r *PostgresRepository) TintBaseTier(ctx context.Context, formulaID, productID *uuid.UUID, baseCode string) (string, error) {
	if formulaID != nil {
		var deep bool
		err := r.ex(ctx).QueryRow(ctx, `
			SELECT b.is_deepest FROM paint_formulas f JOIN paint_bases b ON b.id = f.base_id
			WHERE f.id = $1`, *formulaID).Scan(&deep)
		if err == nil {
			if deep {
				return tint.BaseTierDeep, nil
			}
			return tint.BaseTierFlat, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("failed to read the tint base tier: %w", err)
		}
	}
	baseCode = strings.TrimSpace(baseCode)
	if baseCode == "" && productID == nil {
		return "", nil
	}
	var deep bool
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT b.is_deepest
		FROM paint_bases b
		WHERE ($1 <> '' AND b.code = $1 AND ($2::uuid IS NULL OR b.product_id = $2 OR b.product_id IS NULL))
		   OR ($1 = '' AND $2::uuid IS NOT NULL AND b.product_id = $2)
		ORDER BY CASE WHEN $2::uuid IS NOT NULL AND b.product_id = $2 THEN 0 ELSE 1 END,
		         b.created_at DESC
		LIMIT 1`, baseCode, productID).Scan(&deep)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read the tint base tier: %w", err)
	}
	if deep {
		return tint.BaseTierDeep, nil
	}
	return tint.BaseTierFlat, nil
}

// TintedNoReturn reads the tinted no return policy: off unless the setting
// holds true, because the rule is a dealer policy and the large retailers
// run it both ways.
func (r *PostgresRepository) TintedNoReturn(ctx context.Context) (bool, error) {
	var value *string
	err := r.ex(ctx).QueryRow(ctx,
		`SELECT value FROM system_settings WHERE key = $1`, settingTintedNoReturn).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to read the tinted no return setting: %w", err)
	}
	return value != nil && strings.TrimSpace(*value) == "true", nil
}
