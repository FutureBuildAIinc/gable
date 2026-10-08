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

// childCursor appends a (created_at, id) keyset predicate on alias to conds.
func childCursor(f ChildFilter, alias string, args *[]any, conds *[]string) {
	if f.AfterTime == nil {
		return
	}
	*args = append(*args, *f.AfterTime, f.AfterID)
	*conds = append(*conds, fmt.Sprintf(`(%s.created_at, %s.id) < ($%d, $%d)`, alias, alias, len(*args)-1, len(*args)))
}

// ---- payment terms ----

const termsColumns = `t.id, t.code, t.name, t.kind, t.net_days, t.day_of_month,
	CASE WHEN t.discount_percent IS NULL THEN NULL ELSE ROUND(t.discount_percent * 10000)::bigint END,
	t.discount_days, t.is_active, t.revision, t.created_at, t.updated_at`

func scanTerms(row pgx.Row, t *PaymentTerms) error {
	var (
		kind             string
		discount         *int64
		created, updated time.Time
	)
	if err := row.Scan(&t.ID, &t.Code, &t.Name, &kind, &t.NetDays, &t.DayOfMonth, &discount,
		&t.DiscountDays, &t.IsActive, &t.Revision, &created, &updated); err != nil {
		return err
	}
	t.Kind = TermsKind(kind)
	if discount != nil {
		q := httpx.Quantity(*discount)
		t.DiscountPercent = &q
	}
	t.CreatedAt, t.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

func quantityArg(q *httpx.Quantity) any {
	if q == nil {
		return nil
	}
	return int64(*q)
}

func (r *PostgresRepository) DefaultTermsID(ctx context.Context) (uuid.UUID, error) {
	var id *uuid.UUID
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT payment_terms_default_id()`).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("failed to read the default payment terms: %w", err)
	}
	if id == nil {
		return uuid.Nil, nil
	}
	return *id, nil
}

func (r *PostgresRepository) InsertTerms(ctx context.Context, t *PaymentTerms) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO payment_terms (id, code, name, kind, net_days, day_of_month, discount_percent, discount_days,
			is_active, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::numeric / 10000, $8, $9, 1, $10, $10)`,
		t.ID, t.Code, t.Name, string(t.Kind), t.NetDays, t.DayOfMonth, quantityArg(t.DiscountPercent), t.DiscountDays,
		t.IsActive, t.CreatedAt.Time)
	if err != nil {
		return mapWriteError(err, "failed to insert payment terms")
	}
	return nil
}

func (r *PostgresRepository) GetTerms(ctx context.Context, id uuid.UUID) (*PaymentTerms, error) {
	t := &PaymentTerms{}
	err := scanTerms(r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+termsColumns+` FROM payment_terms t WHERE t.id = $1`, id), t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTermsNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get payment terms: %w", err)
	}
	return t, nil
}

func (r *PostgresRepository) LockTerms(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT id FROM payment_terms WHERE id = $1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTermsNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock payment terms: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateTerms(ctx context.Context, t *PaymentTerms) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE payment_terms SET name = $2, kind = $3, net_days = $4, day_of_month = $5,
			discount_percent = $6::numeric / 10000, discount_days = $7, is_active = $8,
			revision = revision + 1, updated_at = $9
		WHERE id = $1`,
		t.ID, t.Name, string(t.Kind), t.NetDays, t.DayOfMonth, quantityArg(t.DiscountPercent), t.DiscountDays,
		t.IsActive, t.UpdatedAt.Time)
	if err != nil {
		return mapWriteError(err, "failed to update payment terms")
	}
	return nil
}

func termsWhere(f ChildFilter, withCursor bool) (string, []any) {
	var args []any
	conds := []string{"TRUE"}
	if f.IsActive != nil {
		args = append(args, *f.IsActive)
		conds = append(conds, fmt.Sprintf("t.is_active = $%d", len(args)))
	}
	if f.Kind != nil {
		args = append(args, string(*f.Kind))
		conds = append(conds, fmt.Sprintf("t.kind = $%d", len(args)))
	}
	if withCursor {
		childCursor(f, "t", &args, &conds)
	}
	return strings.Join(conds, " AND "), args
}

func (r *PostgresRepository) ListTerms(ctx context.Context, f ChildFilter) ([]PaymentTerms, error) {
	where, args := termsWhere(f, true)
	args = append(args, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `SELECT `+termsColumns+` FROM payment_terms t WHERE `+where+
		fmt.Sprintf(` ORDER BY t.created_at DESC, t.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list payment terms: %w", err)
	}
	defer rows.Close()
	out := []PaymentTerms{}
	for rows.Next() {
		var t PaymentTerms
		if err := scanTerms(rows, &t); err != nil {
			return nil, fmt.Errorf("failed to scan payment terms: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountTerms(ctx context.Context, f ChildFilter) (int64, error) {
	where, args := termsWhere(f, false)
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM payment_terms t WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count payment terms: %w", err)
	}
	return n, nil
}

// ---- ship-tos ----

const shipToColumns = `s.id, s.customer_id, s.code, s.name, s.line1, s.line2, s.city, s.region, s.postal_code,
	s.country, s.phone, s.delivery_instructions,
	CASE WHEN s.tax_rate IS NULL THEN NULL ELSE ROUND(s.tax_rate * 100 * 10000)::bigint END,
	s.is_default, s.is_active, s.revision, s.created_at, s.updated_at`

const shipToJoin = ` FROM customer_ship_tos s JOIN customers c ON c.id = s.customer_id`

func scanShipTo(row pgx.Row, s *ShipTo) error {
	var (
		rate             *int64
		created, updated time.Time
	)
	if err := row.Scan(&s.ID, &s.CustomerID, &s.Code, &s.Name, &s.Line1, &s.Line2, &s.City, &s.Region, &s.PostalCode,
		&s.Country, &s.Phone, &s.DeliveryInstructions, &rate, &s.IsDefault, &s.IsActive, &s.Revision, &created, &updated); err != nil {
		return err
	}
	if rate != nil {
		q := httpx.Quantity(*rate)
		s.TaxRatePercent = &q
	}
	s.CreatedAt, s.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

// taxRateArg is the stored rate for a percent at the wire scale: percent
// divided by 100, with the scale 4 of the wire taken out.
func taxRateArg(q *httpx.Quantity) any {
	if q == nil {
		return nil
	}
	return int64(*q)
}

func (r *PostgresRepository) InsertShipTo(ctx context.Context, s *ShipTo) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, line2, city, region, postal_code, country,
			phone, delivery_instructions, tax_rate, is_default, is_active, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::numeric / 1000000, $14, $15, 1, $16, $16)`,
		s.ID, s.CustomerID, s.Code, s.Name, s.Line1, nullableText(s.Line2), nullableText(s.City), nullableText(s.Region),
		nullableText(s.PostalCode), nullableText(s.Country), nullableText(s.Phone), nullableText(s.DeliveryInstructions),
		taxRateArg(s.TaxRatePercent), s.IsDefault, s.IsActive, s.CreatedAt.Time)
	if err != nil {
		return mapWriteError(err, "failed to insert ship-to")
	}
	return nil
}

func (r *PostgresRepository) GetShipTo(ctx context.Context, id uuid.UUID) (*ShipTo, error) {
	s := &ShipTo{}
	err := scanShipTo(r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+shipToColumns+shipToJoin+`
		WHERE s.id = $1 AND `+wall("c", 2), id, branchctx.IDForQuery(ctx)), s)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrShipToNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get ship-to: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) LockShipTo(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT s.id`+shipToJoin+`
		WHERE s.id = $1 AND `+wall("c", 2)+` FOR UPDATE OF s`, id, branchctx.IDForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrShipToNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock ship-to: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateShipTo(ctx context.Context, s *ShipTo) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE customer_ship_tos SET code = $2, name = $3, line1 = $4, line2 = $5, city = $6, region = $7,
			postal_code = $8, country = $9, phone = $10, delivery_instructions = $11,
			tax_rate = $12::numeric / 1000000, is_default = $13, is_active = $14,
			revision = revision + 1, updated_at = $15
		WHERE id = $1`,
		s.ID, s.Code, s.Name, s.Line1, nullableText(s.Line2), nullableText(s.City), nullableText(s.Region),
		nullableText(s.PostalCode), nullableText(s.Country), nullableText(s.Phone), nullableText(s.DeliveryInstructions),
		taxRateArg(s.TaxRatePercent), s.IsDefault, s.IsActive, s.UpdatedAt.Time)
	if err != nil {
		return mapWriteError(err, "failed to update ship-to")
	}
	return nil
}

// ClearDefaultShipTo unsets the default of every ship-to of the customer but
// the one named, so a new default never meets the one-default index.
func (r *PostgresRepository) ClearDefaultShipTo(ctx context.Context, customerID, except uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE customer_ship_tos SET is_default = FALSE, revision = revision + 1, updated_at = NOW()
		WHERE customer_id = $1 AND is_default AND id <> $2`, customerID, except)
	if err != nil {
		return fmt.Errorf("failed to clear the default ship-to: %w", err)
	}
	return nil
}

func (r *PostgresRepository) CountShipTos(ctx context.Context, customerID uuid.UUID) (int64, error) {
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT COUNT(*) FROM customer_ship_tos WHERE customer_id = $1`, customerID).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count ship-tos: %w", err)
	}
	return n, nil
}

func shipToWhere(ctx context.Context, customerID uuid.UUID, f ChildFilter, withCursor bool) (string, []any) {
	args := []any{customerID, branchctx.IDForQuery(ctx)}
	conds := []string{"s.customer_id = $1", wall("c", 2)}
	if f.IsActive != nil {
		args = append(args, *f.IsActive)
		conds = append(conds, fmt.Sprintf("s.is_active = $%d", len(args)))
	}
	if withCursor {
		childCursor(f, "s", &args, &conds)
	}
	return strings.Join(conds, " AND "), args
}

func (r *PostgresRepository) ListShipTos(ctx context.Context, customerID uuid.UUID, f ChildFilter) ([]ShipTo, error) {
	where, args := shipToWhere(ctx, customerID, f, true)
	args = append(args, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `SELECT `+shipToColumns+shipToJoin+` WHERE `+where+
		fmt.Sprintf(` ORDER BY s.created_at DESC, s.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list ship-tos: %w", err)
	}
	defer rows.Close()
	out := []ShipTo{}
	for rows.Next() {
		var s ShipTo
		if err := scanShipTo(rows, &s); err != nil {
			return nil, fmt.Errorf("failed to scan ship-to: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountShipTosFiltered(ctx context.Context, customerID uuid.UUID, f ChildFilter) (int64, error) {
	where, args := shipToWhere(ctx, customerID, f, false)
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT COUNT(*)`+shipToJoin+` WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count ship-tos: %w", err)
	}
	return n, nil
}

// ---- contacts ----

const contactColumns = `cc.id, cc.customer_id, cc.first_name, cc.last_name, NULLIF(cc.title, ''), NULLIF(cc.email, ''),
	NULLIF(cc.phone, ''), NULLIF(cc.role, ''), COALESCE(cc.is_primary, FALSE), COALESCE(cc.is_active, TRUE),
	cc.can_place_orders,
	CASE WHEN cc.order_limit IS NULL THEN NULL ELSE ROUND(cc.order_limit * 100)::bigint END,
	cc.revision, cc.created_at, COALESCE(cc.updated_at, cc.created_at)`

const contactJoin = ` FROM customer_contacts cc JOIN customers c ON c.id = cc.customer_id`

func scanContact(row pgx.Row, c *Contact) error {
	var (
		role             *string
		limit            *int64
		created, updated time.Time
	)
	if err := row.Scan(&c.ID, &c.CustomerID, &c.FirstName, &c.LastName, &c.Title, &c.Email, &c.Phone, &role,
		&c.IsPrimary, &c.IsActive, &c.CanPlaceOrders, &limit, &c.Revision, &created, &updated); err != nil {
		return err
	}
	if role != nil {
		r := ContactRole(*role)
		c.Role = &r
	}
	if limit != nil {
		l := httpx.Cents(*limit)
		c.OrderLimitCents = &l
	}
	c.CreatedAt, c.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

func roleArg(r *ContactRole) any {
	if r == nil {
		return nil
	}
	return string(*r)
}

func (r *PostgresRepository) InsertContact(ctx context.Context, c *Contact) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		INSERT INTO customer_contacts (id, customer_id, first_name, last_name, title, email, phone, role,
			is_primary, is_active, can_place_orders, order_limit, revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric / 100, 1, $13, $13)`,
		c.ID, c.CustomerID, c.FirstName, c.LastName, nullableText(c.Title), nullableText(c.Email), nullableText(c.Phone),
		roleArg(c.Role), c.IsPrimary, c.IsActive, c.CanPlaceOrders, centsArg(c.OrderLimitCents), c.CreatedAt.Time)
	if err != nil {
		return fmt.Errorf("failed to insert contact: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetContact(ctx context.Context, id uuid.UUID) (*Contact, error) {
	c := &Contact{}
	err := scanContact(r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+contactColumns+contactJoin+`
		WHERE cc.id = $1 AND `+wall("c", 2), id, branchctx.IDForQuery(ctx)), c)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrContactNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get contact: %w", err)
	}
	return c, nil
}

func (r *PostgresRepository) LockContact(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT cc.id`+contactJoin+`
		WHERE cc.id = $1 AND `+wall("c", 2)+` FOR UPDATE OF cc`, id, branchctx.IDForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrContactNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock contact: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateContact(ctx context.Context, c *Contact) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE customer_contacts SET first_name = $2, last_name = $3, title = $4, email = $5, phone = $6, role = $7,
			is_primary = $8, is_active = $9, can_place_orders = $10, order_limit = $11::numeric / 100,
			revision = revision + 1, updated_at = $12
		WHERE id = $1`,
		c.ID, c.FirstName, c.LastName, nullableText(c.Title), nullableText(c.Email), nullableText(c.Phone),
		roleArg(c.Role), c.IsPrimary, c.IsActive, c.CanPlaceOrders, centsArg(c.OrderLimitCents), c.UpdatedAt.Time)
	if err != nil {
		return fmt.Errorf("failed to update contact: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeleteContact(ctx context.Context, id uuid.UUID) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM customer_contacts WHERE id = $1`, id); err != nil {
		return fmt.Errorf("failed to delete contact: %w", err)
	}
	return nil
}

func contactWhere(ctx context.Context, customerID uuid.UUID, f ChildFilter, withCursor bool) (string, []any) {
	args := []any{customerID, branchctx.IDForQuery(ctx)}
	conds := []string{"cc.customer_id = $1", wall("c", 2)}
	if f.IsActive != nil {
		args = append(args, *f.IsActive)
		conds = append(conds, fmt.Sprintf("COALESCE(cc.is_active, TRUE) = $%d", len(args)))
	}
	if withCursor {
		childCursor(f, "cc", &args, &conds)
	}
	return strings.Join(conds, " AND "), args
}

func (r *PostgresRepository) ListContacts(ctx context.Context, customerID uuid.UUID, f ChildFilter) ([]Contact, error) {
	where, args := contactWhere(ctx, customerID, f, true)
	args = append(args, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `SELECT `+contactColumns+contactJoin+` WHERE `+where+
		fmt.Sprintf(` ORDER BY cc.created_at DESC, cc.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list contacts: %w", err)
	}
	defer rows.Close()
	out := []Contact{}
	for rows.Next() {
		var c Contact
		if err := scanContact(rows, &c); err != nil {
			return nil, fmt.Errorf("failed to scan contact: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountContacts(ctx context.Context, customerID uuid.UUID, f ChildFilter) (int64, error) {
	where, args := contactWhere(ctx, customerID, f, false)
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT COUNT(*)`+contactJoin+` WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count contacts: %w", err)
	}
	return n, nil
}
