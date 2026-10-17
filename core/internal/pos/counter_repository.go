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
