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
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinels the repository answers with; the service maps each to the wire.
var (
	ErrNotFound         = errors.New("customer not found")
	ErrShipToNotFound   = errors.New("ship-to not found")
	ErrContactNotFound  = errors.New("contact not found")
	ErrTermsNotFound    = errors.New("payment terms not found")
	ErrPriceLevelAbsent = errors.New("price level not found")
)

// ListFilter is the customer list's filters and keyset position. After is the
// last row of the previous page; the list resumes strictly past it in
// (created_at, id) descending order.
type ListFilter struct {
	Query         string
	Tier          *CustomerTier
	IsActive      *bool
	SalespersonID *uuid.UUID
	AfterTime     *time.Time
	AfterID       uuid.UUID
	Limit         int
}

// ChildFilter is the keyset position and filters of a child list (ship-tos,
// contacts, payment terms, price levels): newest first by (created_at, id).
type ChildFilter struct {
	IsActive  *bool
	Kind      *TermsKind
	AfterTime *time.Time
	AfterID   uuid.UUID
	Limit     int
}

// Repository is the customer store. Every method reads and writes through the
// context's executor, so inside a transaction (Service wraps each write in
// one) it never reaches for a second pool connection.
type Repository interface {
	InsertCustomer(ctx context.Context, c *Customer) error
	GetCustomer(ctx context.Context, id uuid.UUID) (*Customer, error)
	GetCustomerByEmail(ctx context.Context, email string) (*Customer, error)
	// LockCustomer takes the customer's row lock for the rest of the
	// transaction, so a revision check and the write after it are one act.
	LockCustomer(ctx context.Context, id uuid.UUID) error
	UpdateCustomer(ctx context.Context, c *Customer) error
	SetSalesperson(ctx context.Context, id uuid.UUID, salespersonID *uuid.UUID) error
	ListCustomers(ctx context.Context, f ListFilter) ([]Customer, error)
	CountCustomers(ctx context.Context, f ListFilter) (int64, error)

	GetEscalationPolicy(ctx context.Context, customerID uuid.UUID) (*EscalationPolicy, error)
	SetEscalationPolicy(ctx context.Context, p *EscalationPolicy) error

	ListPriceLevels(ctx context.Context, f ChildFilter) ([]PriceLevel, error)
	CountPriceLevels(ctx context.Context) (int64, error)

	DefaultCurrency(ctx context.Context) (string, error)
	EnabledCurrencies(ctx context.Context) ([]string, error)
	// OpenDocuments names the kinds of document that keep a customer's
	// currency fixed: orders not fulfilled or cancelled, invoices and credit
	// memos with an open amount, deposits with an unapplied amount.
	OpenDocuments(ctx context.Context, customerID uuid.UUID) ([]string, error)

	// DefaultTermsID is the seeded NET30 row, or uuid.Nil when it is gone.
	DefaultTermsID(ctx context.Context) (uuid.UUID, error)
	InsertTerms(ctx context.Context, t *PaymentTerms) error
	GetTerms(ctx context.Context, id uuid.UUID) (*PaymentTerms, error)
	LockTerms(ctx context.Context, id uuid.UUID) error
	UpdateTerms(ctx context.Context, t *PaymentTerms) error
	ListTerms(ctx context.Context, f ChildFilter) ([]PaymentTerms, error)
	CountTerms(ctx context.Context, f ChildFilter) (int64, error)

	InsertShipTo(ctx context.Context, s *ShipTo) error
	GetShipTo(ctx context.Context, id uuid.UUID) (*ShipTo, error)
	LockShipTo(ctx context.Context, id uuid.UUID) error
	UpdateShipTo(ctx context.Context, s *ShipTo) error
	ClearDefaultShipTo(ctx context.Context, customerID, except uuid.UUID) error
	CountShipTos(ctx context.Context, customerID uuid.UUID) (int64, error)
	ListShipTos(ctx context.Context, customerID uuid.UUID, f ChildFilter) ([]ShipTo, error)
	CountShipTosFiltered(ctx context.Context, customerID uuid.UUID, f ChildFilter) (int64, error)

	InsertContact(ctx context.Context, c *Contact) error
	GetContact(ctx context.Context, id uuid.UUID) (*Contact, error)
	LockContact(ctx context.Context, id uuid.UUID) error
	UpdateContact(ctx context.Context, c *Contact) error
	DeleteContact(ctx context.Context, id uuid.UUID) error
	ListContacts(ctx context.Context, customerID uuid.UUID, f ChildFilter) ([]Contact, error)
	CountContacts(ctx context.Context, customerID uuid.UUID, f ChildFilter) (int64, error)
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// referenceFields maps the foreign keys a write can violate to the request
// field that named the missing record, so a bad reference is a 400 naming it
// and not a 500 from the database.
var referenceFields = map[string]string{
	"customers_price_level_id_fkey":    "price_level_id",
	"customers_salesperson_id_fkey":    "salesperson_id",
	"customers_primary_branch_id_fkey": "primary_branch_id",
	"customers_payment_terms_id_fkey":  "payment_terms_id",
}

func mapWriteError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23503":
			if field, ok := referenceFields[pgErr.ConstraintName]; ok {
				return &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed, Message: "a referenced record does not exist",
					Details: []httpx.FieldError{{Field: field, Message: "no such record"}}}
			}
		case pgErr.Code == "23505":
			switch pgErr.ConstraintName {
			case "customers_account_number_key":
				return httpx.Duplicate("a customer with this account number already exists",
					httpx.Blocker("account_number_taken", "account_number is already used by another customer"))
			case "customer_ship_tos_code_key":
				return httpx.Duplicate("this customer already has a ship-to with this code",
					httpx.Blocker("ship_to_code_taken", "code is already used by another ship-to of this customer"))
			case "payment_terms_code_key":
				return httpx.Duplicate("payment terms with this code already exist",
					httpx.Blocker("terms_code_taken", "code is already used by other payment terms"))
			}
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// wall is the branch wall predicate for a customer alias: a nil branch means
// no wall. arg is the positional parameter that carries the branch.
func wall(alias string, arg int) string {
	return fmt.Sprintf(`($%d::uuid IS NULL OR EXISTS (SELECT 1 FROM customer_branches cb WHERE cb.customer_id = %s.id AND cb.branch_id = $%d))`, arg, alias, arg)
}

// nullable returns the value for a parameterised nullable text column.
func nullableText(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func centsArg(c *httpx.Cents) any {
	if c == nil {
		return nil
	}
	return int64(*c)
}

// ---- customers ----

const customerColumns = `
	c.id, c.account_number, c.name, NULLIF(c.email, ''), NULLIF(c.phone, ''), NULLIF(c.address, ''),
	c.tier::text, c.is_active, c.primary_branch_id,
	c.price_level_id, pl.name, pl.multiplier::float8, pl.created_at, pl.updated_at,
	c.salesperson_id, NULLIF(st.name, ''),
	CASE WHEN c.credit_limit IS NULL THEN NULL ELSE ROUND(c.credit_limit * 100)::bigint END,
	ROUND(c.balance_due * 100)::bigint,
	c.currency,
	COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD'),
	c.payment_terms_id, pt.code, pt.name, c.po_required, c.revision, c.created_at, COALESCE(c.updated_at, c.created_at)`

const customerFrom = `
	FROM customers c
	JOIN payment_terms pt ON pt.id = c.payment_terms_id
	LEFT JOIN price_levels pl ON pl.id = c.price_level_id
	LEFT JOIN sales_team st ON st.id = c.salesperson_id`

func scanCustomer(row pgx.Row, c *Customer) error {
	var (
		tier                 string
		plName               *string
		plMult               *float64
		plCreated, plUpdated *time.Time
		credit               *int64
		balance              int64
		created, updated     time.Time
	)
	if err := row.Scan(&c.ID, &c.AccountNumber, &c.Name, &c.Email, &c.Phone, &c.Address,
		&tier, &c.IsActive, &c.PrimaryBranchID,
		&c.PriceLevelID, &plName, &plMult, &plCreated, &plUpdated,
		&c.SalespersonID, &c.SalespersonName,
		&credit, &balance, &c.Currency, &c.EffectiveCurrency,
		&c.PaymentTermsID, &c.PaymentTerms.Code, &c.PaymentTerms.Name, &c.POrequired,
		&c.Revision, &created, &updated); err != nil {
		return err
	}
	c.Tier = CustomerTier(tier)
	if c.PriceLevelID != nil {
		pl := &PriceLevel{ID: *c.PriceLevelID}
		if plName != nil {
			pl.Name = *plName
		}
		if plMult != nil {
			pl.Multiplier = *plMult
		}
		if plCreated != nil {
			pl.CreatedAt = httpx.TimestampOf(*plCreated)
		}
		if plUpdated != nil {
			pl.UpdatedAt = httpx.TimestampOf(*plUpdated)
		}
		c.PriceLevel = pl
	}
	if credit != nil {
		cc := httpx.Cents(*credit)
		c.CreditLimitCents = &cc
	}
	c.BalanceCents = httpx.Cents(balance)
	c.PaymentTerms.ID = c.PaymentTermsID
	c.CreatedAt, c.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
	return nil
}

func (r *PostgresRepository) InsertCustomer(ctx context.Context, c *Customer) error {
	exec := r.db.GetExecutor(ctx)
	var branchArg any
	if c.PrimaryBranchID != uuid.Nil {
		branchArg = c.PrimaryBranchID
	} else if bid := branchctx.IDForQuery(ctx); bid != nil {
		branchArg = *bid
	}
	var branch uuid.UUID
	err := exec.QueryRow(ctx, `
		INSERT INTO customers (
			id, account_number, name, email, phone, address, tier, is_active,
			price_level_id, salesperson_id, credit_limit, currency, payment_terms_id, po_required,
			created_at, updated_at, revision, primary_branch_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7::customer_tier, $8,
			$9, $10, $11::numeric / 100, $12, COALESCE($13::uuid, payment_terms_default_id()), $14,
			$15, $15, 1,
			COALESCE($16::uuid, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')))
		RETURNING primary_branch_id`,
		c.ID, c.AccountNumber, c.Name, nullableText(c.Email), nullableText(c.Phone), nullableText(c.Address),
		string(c.Tier), c.IsActive,
		c.PriceLevelID, c.SalespersonID, centsArg(c.CreditLimitCents), nullableText(c.Currency), nilIfZero(c.PaymentTermsID), c.POrequired,
		c.CreatedAt.Time, branchArg,
	).Scan(&branch)
	if err != nil {
		return mapWriteError(err, "failed to insert customer")
	}
	c.PrimaryBranchID = branch
	// Mirror into customer_branches so the branch wall works uniformly.
	if _, err := exec.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		c.ID, branch); err != nil {
		return fmt.Errorf("failed to associate customer branch: %w", err)
	}
	return nil
}

func nilIfZero(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func (r *PostgresRepository) GetCustomer(ctx context.Context, id uuid.UUID) (*Customer, error) {
	c := &Customer{}
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+customerColumns+customerFrom+`
		WHERE c.id = $1 AND `+wall("c", 2), id, branchctx.IDForQuery(ctx))
	if err := scanCustomer(row, c); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get customer: %w", err)
	}
	return c, nil
}

func (r *PostgresRepository) GetCustomerByEmail(ctx context.Context, email string) (*Customer, error) {
	c := &Customer{}
	row := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+customerColumns+customerFrom+`
		WHERE c.email = $1 AND `+wall("c", 2)+` ORDER BY c.created_at, c.id LIMIT 1`, email, branchctx.IDForQuery(ctx))
	if err := scanCustomer(row, c); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get customer by email: %w", err)
	}
	return c, nil
}

func (r *PostgresRepository) LockCustomer(ctx context.Context, id uuid.UUID) error {
	var locked uuid.UUID
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT c.id FROM customers c WHERE c.id = $1 AND `+wall("c", 2)+` FOR UPDATE OF c`,
		id, branchctx.IDForQuery(ctx)).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to lock customer: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateCustomer(ctx context.Context, c *Customer) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE customers SET
			account_number = $2, name = $3, email = $4, phone = $5, address = $6,
			tier = $7::customer_tier, is_active = $8, price_level_id = $9, salesperson_id = $10,
			credit_limit = $11::numeric / 100, currency = $12, payment_terms_id = $13, po_required = $14,
			revision = revision + 1, updated_at = $15
		WHERE id = $1`,
		c.ID, c.AccountNumber, c.Name, nullableText(c.Email), nullableText(c.Phone), nullableText(c.Address),
		string(c.Tier), c.IsActive, c.PriceLevelID, c.SalespersonID, centsArg(c.CreditLimitCents),
		nullableText(c.Currency), c.PaymentTermsID, c.POrequired, c.UpdatedAt.Time)
	if err != nil {
		return mapWriteError(err, "failed to update customer")
	}
	return nil
}

func (r *PostgresRepository) SetSalesperson(ctx context.Context, id uuid.UUID, salespersonID *uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE customers SET salesperson_id = $2, revision = revision + 1, updated_at = NOW() WHERE id = $1`,
		id, salespersonID)
	if err != nil {
		return mapWriteError(err, "failed to update salesperson")
	}
	return nil
}

// likePattern escapes the LIKE metacharacters of a search term.
func likePattern(q string) string {
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + q + "%"
}

// customerWhere builds the one predicate the list and its count share.
func customerWhere(ctx context.Context, f ListFilter, withCursor bool) (string, []any) {
	args := []any{branchctx.IDForQuery(ctx)}
	conds := []string{wall("c", 1)}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Query != "" {
		add(`(c.name ILIKE ? OR c.account_number ILIKE ? OR COALESCE(c.email, '') ILIKE ?)`, likePattern(f.Query))
	}
	if f.Tier != nil {
		add(`c.tier = ?::customer_tier`, string(*f.Tier))
	}
	if f.IsActive != nil {
		add(`c.is_active = ?`, *f.IsActive)
	}
	if f.SalespersonID != nil {
		add(`c.salesperson_id = ?`, *f.SalespersonID)
	}
	if withCursor && f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		conds = append(conds, fmt.Sprintf(`(c.created_at, c.id) < ($%d, $%d)`, len(args)-1, len(args)))
	}
	return strings.Join(conds, " AND "), args
}

func (r *PostgresRepository) ListCustomers(ctx context.Context, f ListFilter) ([]Customer, error) {
	where, args := customerWhere(ctx, f, true)
	args = append(args, f.Limit)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `SELECT `+customerColumns+customerFrom+`
		WHERE `+where+fmt.Sprintf(` ORDER BY c.created_at DESC, c.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list customers: %w", err)
	}
	defer rows.Close()
	out := []Customer{}
	for rows.Next() {
		var c Customer
		if err := scanCustomer(rows, &c); err != nil {
			return nil, fmt.Errorf("failed to scan customer: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountCustomers(ctx context.Context, f ListFilter) (int64, error) {
	where, args := customerWhere(ctx, f, false)
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM customers c WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count customers: %w", err)
	}
	return n, nil
}

// ---- escalation policy ----

func (r *PostgresRepository) GetEscalationPolicy(ctx context.Context, customerID uuid.UUID) (*EscalationPolicy, error) {
	p := &EscalationPolicy{CustomerID: customerID}
	var (
		policy    string
		threshold int64
		signed    *time.Time
	)
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT c.price_escalation_policy, ROUND(c.escalation_threshold_pct * 10000)::bigint,
		       c.escalation_agreement_signed_at, c.escalation_agreement_ref, c.revision
		FROM customers c
		WHERE c.id = $1 AND `+wall("c", 2), customerID, branchctx.IDForQuery(ctx)).
		Scan(&policy, &threshold, &signed, &p.AgreementRef, &p.Revision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get escalation policy: %w", err)
	}
	p.Policy, p.ThresholdPercent, p.AgreementSignedAt = PolicyMode(policy), httpx.Quantity(threshold), httpx.PtrTimestamp(signed)
	return p, nil
}

func (r *PostgresRepository) SetEscalationPolicy(ctx context.Context, p *EscalationPolicy) error {
	var signed any
	if p.AgreementSignedAt != nil {
		signed = p.AgreementSignedAt.Time
	}
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE customers
		SET price_escalation_policy = $2, escalation_threshold_pct = $3::numeric / 10000,
		    escalation_agreement_signed_at = $4, escalation_agreement_ref = $5,
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1`,
		p.CustomerID, string(p.Policy), int64(p.ThresholdPercent), signed, nullableText(p.AgreementRef))
	if err != nil {
		return fmt.Errorf("failed to set escalation policy: %w", err)
	}
	return nil
}

// ---- price levels ----

func (r *PostgresRepository) ListPriceLevels(ctx context.Context, f ChildFilter) ([]PriceLevel, error) {
	args := []any{f.Limit}
	cursor := ""
	if f.AfterTime != nil {
		args = append(args, *f.AfterTime, f.AfterID)
		cursor = `WHERE (created_at, id) < ($2, $3)`
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, name, multiplier::float8, created_at, updated_at FROM price_levels `+cursor+`
		ORDER BY created_at DESC, id DESC LIMIT $1`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list price levels: %w", err)
	}
	defer rows.Close()
	out := []PriceLevel{}
	for rows.Next() {
		var l PriceLevel
		var created, updated time.Time
		if err := rows.Scan(&l.ID, &l.Name, &l.Multiplier, &created, &updated); err != nil {
			return nil, fmt.Errorf("failed to scan price level: %w", err)
		}
		l.CreatedAt, l.UpdatedAt = httpx.TimestampOf(created), httpx.TimestampOf(updated)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CountPriceLevels(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT COUNT(*) FROM price_levels`).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count price levels: %w", err)
	}
	return n, nil
}

// ---- currency ----

func (r *PostgresRepository) setting(ctx context.Context, key string) (string, error) {
	var v string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read setting %s: %w", key, err)
	}
	return strings.TrimSpace(v), nil
}

func (r *PostgresRepository) DefaultCurrency(ctx context.Context) (string, error) {
	v, err := r.setting(ctx, "currency.default")
	if err != nil || v == "" {
		return "USD", err
	}
	return v, nil
}

func (r *PostgresRepository) EnabledCurrencies(ctx context.Context) ([]string, error) {
	v, err := r.setting(ctx, "currency.enabled")
	if err != nil {
		return nil, err
	}
	if v == "" {
		d, err := r.DefaultCurrency(ctx)
		return []string{d}, err
	}
	var out []string
	for _, c := range strings.Split(v, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *PostgresRepository) OpenDocuments(ctx context.Context, customerID uuid.UUID) ([]string, error) {
	var orders, invoices, memos, deposits bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM orders WHERE customer_id = $1 AND status IN ('DRAFT', 'CONFIRMED', 'ON_HOLD')),
			EXISTS (SELECT 1 FROM invoices WHERE customer_id = $1 AND status IN ('UNPAID', 'PARTIAL')),
			EXISTS (SELECT 1 FROM credit_memos WHERE customer_id = $1 AND status IN ('DRAFT', 'OPEN', 'PARTIAL')),
			EXISTS (SELECT 1 FROM payments WHERE customer_id = $1 AND status = 'POSTED' AND amount_unapplied > 0)`,
		customerID).Scan(&orders, &invoices, &memos, &deposits)
	if err != nil {
		return nil, fmt.Errorf("failed to read the customer's open documents: %w", err)
	}
	var kinds []string
	for _, k := range []struct {
		open bool
		name string
	}{{orders, "orders"}, {invoices, "invoices"}, {memos, "credit memos"}, {deposits, "unapplied payments"}} {
		if k.open {
			kinds = append(kinds, k.name)
		}
	}
	return kinds, nil
}
