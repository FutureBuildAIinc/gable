// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Store is the Postgres store. Every statement goes through the context's
// executor (database.DB.GetExecutor), so a call made inside the service's
// transaction joins it and a call made outside runs on the pool.
type Store struct {
	db *database.DB
}

// NewStore builds the store.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

func (s *Store) exec(ctx context.Context) database.Executor { return s.db.GetExecutor(ctx) }

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func qtyArg(q httpx.Quantity) int64 { return int64(q) }

// --- settings and logo ---

const settingsCols = `COALESCE(display_name, ''), logo IS NOT NULL, payment_terms, deposit_bps, tax_mode, tax_rate_bps, markup_kind, markup_bps, require_code`

// GetSettings reads the contractor's defaults; an account that never saved
// any has the zero defaults.
func (s *Store) GetSettings(ctx context.Context, customerID uuid.UUID) (*Settings, error) {
	out := &Settings{CustomerID: customerID, Tax: Tax{Mode: TaxAtCheckout}, Markup: Markup{Kind: KindMarkup}}
	var kind, taxMode string
	var dep, rate, bps int64
	err := s.exec(ctx).QueryRow(ctx, `SELECT `+settingsCols+` FROM pro_contractor_settings WHERE customer_id = $1`, customerID).
		Scan(&out.DisplayName, &out.HasLogo, &out.PaymentTerms, &dep, &taxMode, &rate, &kind, &bps, &out.RequireCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read contractor settings: %w", err)
	}
	out.DepositBPS = dep
	out.Tax = Tax{Mode: taxMode, RateBPS: rate}
	out.Markup = Markup{Kind: MarkupKind(kind), BPS: bps}
	return out, nil
}

// PutSettings upserts the contractor's defaults (the logo is separate).
func (s *Store) PutSettings(ctx context.Context, st *Settings) error {
	_, err := s.exec(ctx).Exec(ctx, `
		INSERT INTO pro_contractor_settings (customer_id, display_name, payment_terms, deposit_bps, tax_mode, tax_rate_bps, markup_kind, markup_bps, require_code, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (customer_id) DO UPDATE SET display_name = EXCLUDED.display_name, payment_terms = EXCLUDED.payment_terms,
			deposit_bps = EXCLUDED.deposit_bps, tax_mode = EXCLUDED.tax_mode, tax_rate_bps = EXCLUDED.tax_rate_bps,
			markup_kind = EXCLUDED.markup_kind, markup_bps = EXCLUDED.markup_bps, require_code = EXCLUDED.require_code, updated_at = NOW()`,
		st.CustomerID, st.DisplayName, st.PaymentTerms, st.DepositBPS, st.Tax.Mode, st.Tax.RateBPS, string(st.Markup.Kind), st.Markup.BPS, st.RequireCode)
	if err != nil {
		return fmt.Errorf("save contractor settings: %w", err)
	}
	return nil
}

// SetLogo stores the contractor's mark (nil clears it).
func (s *Store) SetLogo(ctx context.Context, customerID uuid.UUID, l *Logo) error {
	var b []byte
	var ct *string
	if l != nil {
		b, ct = l.Bytes, &l.ContentType
	}
	_, err := s.exec(ctx).Exec(ctx, `
		INSERT INTO pro_contractor_settings (customer_id, logo, logo_content_type) VALUES ($1, $2, $3)
		ON CONFLICT (customer_id) DO UPDATE SET logo = EXCLUDED.logo, logo_content_type = EXCLUDED.logo_content_type, updated_at = NOW()`,
		customerID, b, ct)
	if err != nil {
		return fmt.Errorf("save contractor logo: %w", err)
	}
	return nil
}

// GetLogo reads the contractor's mark, nil when there is none.
func (s *Store) GetLogo(ctx context.Context, customerID uuid.UUID) (*Logo, error) {
	var l Logo
	err := s.exec(ctx).QueryRow(ctx, `SELECT logo, logo_content_type FROM pro_contractor_settings WHERE customer_id = $1 AND logo IS NOT NULL`, customerID).
		Scan(&l.Bytes, &l.ContentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read contractor logo: %w", err)
	}
	return &l, nil
}

// ContractorName is the name the customer sees for the contractor: the
// display name they set, or the account's own name.
func (s *Store) ContractorName(ctx context.Context, customerID uuid.UUID) (string, bool, error) {
	var name string
	var hasLogo bool
	err := s.exec(ctx).QueryRow(ctx, `
		SELECT COALESCE(NULLIF(trim(cs.display_name), ''), c.name), COALESCE(cs.logo IS NOT NULL, FALSE)
		FROM customers c LEFT JOIN pro_contractor_settings cs ON cs.customer_id = c.id WHERE c.id = $1`, customerID).Scan(&name, &hasLogo)
	if err != nil {
		return "", false, fmt.Errorf("read contractor name: %w", err)
	}
	return name, hasLogo, nil
}

// --- quotes ---

// NextReference draws the next gapless-enough reference, CQ-000001.
func (s *Store) NextReference(ctx context.Context) (string, error) {
	var n int64
	if err := s.exec(ctx).QueryRow(ctx, `SELECT nextval('pro_customer_quote_seq')`).Scan(&n); err != nil {
		return "", fmt.Errorf("next quote reference: %w", err)
	}
	return fmt.Sprintf("CQ-%06d", n), nil
}

// ProjectOwned reports whether the project belongs to the customer.
func (s *Store) ProjectOwned(ctx context.Context, projectID, customerID uuid.UUID) (bool, error) {
	var ok bool
	err := s.exec(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND customer_id = $2)`, projectID, customerID).Scan(&ok)
	return ok, err
}

// InsertQuote writes a new draft with all its children.
func (s *Store) InsertQuote(ctx context.Context, q *Quote) error {
	_, err := s.exec(ctx).Exec(ctx, `
		INSERT INTO pro_customer_quotes (id, customer_id, reference, title, project_id, source_quote_id, job_site_address,
			end_customer_name, end_customer_email, end_customer_phone, site_contact_name, site_contact_phone, status,
			markup_kind, markup_bps, hide_line_prices, tax_mode, tax_rate_bps, payment_terms, deposit_bps, customer_note,
			require_code, valid_until, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'draft',$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		q.ID, q.CustomerID, q.Reference, q.Title, q.ProjectID, q.SourceQuoteID, q.JobSiteAddress,
		q.EndCustomer.Name, q.EndCustomer.Email, q.EndCustomer.Phone, q.SiteContact.Name, q.SiteContact.Phone,
		string(q.Markup.Kind), q.Markup.BPS, q.HideLinePrices, q.Tax.Mode, q.Tax.RateBPS, q.PaymentTerms, q.DepositBPS,
		q.CustomerNote, q.RequireCode, q.ValidUntil, nil)
	if err != nil {
		return fmt.Errorf("insert customer quote: %w", err)
	}
	return s.insertChildren(ctx, q)
}

func (s *Store) insertChildren(ctx context.Context, q *Quote) error {
	ex := s.exec(ctx)
	for _, g := range q.Groups {
		if _, err := ex.Exec(ctx, `INSERT INTO pro_customer_quote_option_groups (id, quote_id, label, position) VALUES ($1,$2,$3,$4)`,
			g.ID, q.ID, g.Label, g.Position); err != nil {
			return fmt.Errorf("insert option group: %w", err)
		}
	}
	for _, l := range q.Lines {
		var ok *string
		var obps *int64
		if l.Override != nil {
			k := string(l.Override.Kind)
			b := l.Override.BPS
			ok, obps = &k, &b
		}
		if _, err := ex.Exec(ctx, `
			INSERT INTO pro_customer_quote_lines (id, quote_id, position, product_id, category_id, sku, description, uom, quantity,
				unit_cost_ten_thousandths, cost_cents, price_basis, priced_at, option_group_id, override_kind, override_bps,
				applied_tier, applied_kind, applied_bps, sell_cents, note)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric / 10000,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
			l.ID, q.ID, l.Position, l.ProductID, l.CategoryID, l.SKU, l.Description, l.UOM, qtyArg(l.Quantity),
			l.UnitCostTenThousandths, l.CostCents, l.PriceBasis, l.PricedAt, l.GroupID, ok, obps,
			string(l.Applied.Tier), string(l.Applied.Kind), l.Applied.BPS, l.SellCents, l.Note); err != nil {
			return fmt.Errorf("insert quote line: %w", err)
		}
		state := string(l.Selection)
		if state == "" {
			state = string(SelectionPending)
		}
		if _, err := ex.Exec(ctx, `INSERT INTO pro_customer_quote_selections (quote_id, line_id, state) VALUES ($1,$2,$3)`, q.ID, l.ID, state); err != nil {
			return fmt.Errorf("insert selection: %w", err)
		}
	}
	for _, r := range q.CategoryRules {
		if _, err := ex.Exec(ctx, `INSERT INTO pro_customer_quote_category_rules (quote_id, category_id, kind, bps) VALUES ($1,$2,$3,$4)`,
			q.ID, r.CategoryID, string(r.Markup.Kind), r.Markup.BPS); err != nil {
			return fmt.Errorf("insert category rule: %w", err)
		}
	}
	for _, l := range q.Labour {
		var hours *int64
		if l.RateType == RateHourly {
			h := qtyArg(l.Hours)
			hours = &h
		}
		if _, err := ex.Exec(ctx, `
			INSERT INTO pro_customer_quote_labour_lines (id, quote_id, position, description, rate_type, rate_cents, hours, contractor_only)
			VALUES ($1,$2,$3,$4,$5,$6,$7::numeric / 10000,$8)`,
			l.ID, q.ID, l.Position, l.Description, l.RateType, l.RateCents, hours, l.ContractorOnly); err != nil {
			return fmt.Errorf("insert labour line: %w", err)
		}
	}
	for _, o := range q.Overhead {
		if _, err := ex.Exec(ctx, `
			INSERT INTO pro_customer_quote_overhead_lines (id, quote_id, position, label, amount_type, value, contractor_only)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			o.ID, q.ID, o.Position, o.Label, o.AmountType, o.Value, o.ContractorOnly); err != nil {
			return fmt.Errorf("insert overhead line: %w", err)
		}
	}
	return nil
}

// ReplaceDraft rewrites a draft: the header and every child. The caller holds
// the row (GetQuote ... forUpdate) and has checked the revision.
func (s *Store) ReplaceDraft(ctx context.Context, q *Quote) error {
	ex := s.exec(ctx)
	tag, err := ex.Exec(ctx, `
		UPDATE pro_customer_quotes SET title = $2, project_id = $3, source_quote_id = $4, job_site_address = $5,
			end_customer_name = $6, end_customer_email = $7, end_customer_phone = $8, site_contact_name = $9, site_contact_phone = $10,
			markup_kind = $11, markup_bps = $12, hide_line_prices = $13, tax_mode = $14, tax_rate_bps = $15,
			payment_terms = $16, deposit_bps = $17, customer_note = $18, require_code = $19, valid_until = $20,
			revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND status = 'draft'`,
		q.ID, q.Title, q.ProjectID, q.SourceQuoteID, q.JobSiteAddress,
		q.EndCustomer.Name, q.EndCustomer.Email, q.EndCustomer.Phone, q.SiteContact.Name, q.SiteContact.Phone,
		string(q.Markup.Kind), q.Markup.BPS, q.HideLinePrices, q.Tax.Mode, q.Tax.RateBPS,
		q.PaymentTerms, q.DepositBPS, q.CustomerNote, q.RequireCode, q.ValidUntil)
	if err != nil {
		return fmt.Errorf("update customer quote: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInvalidState
	}
	for _, t := range []string{"pro_customer_quote_selections", "pro_customer_quote_lines", "pro_customer_quote_option_groups",
		"pro_customer_quote_category_rules", "pro_customer_quote_labour_lines", "pro_customer_quote_overhead_lines"} {
		if _, err := ex.Exec(ctx, `DELETE FROM `+t+` WHERE quote_id = $1`, q.ID); err != nil {
			return fmt.Errorf("clear %s: %w", t, err)
		}
	}
	return s.insertChildren(ctx, q)
}

const quoteCols = `id, customer_id, reference, title, project_id, source_quote_id, job_site_address,
	end_customer_name, end_customer_email, end_customer_phone, site_contact_name, site_contact_phone, status,
	markup_kind, markup_bps, hide_line_prices, tax_mode, tax_rate_bps, payment_terms, deposit_bps, customer_note,
	require_code, valid_until, revision, sent_at, approved_at, declined_at, confirmed_at, dealer_accepted_at, order_id,
	created_at, updated_at`

func scanQuote(row pgx.Row) (*Quote, error) {
	var q Quote
	var status, mk, tm string
	err := row.Scan(&q.ID, &q.CustomerID, &q.Reference, &q.Title, &q.ProjectID, &q.SourceQuoteID, &q.JobSiteAddress,
		&q.EndCustomer.Name, &q.EndCustomer.Email, &q.EndCustomer.Phone, &q.SiteContact.Name, &q.SiteContact.Phone, &status,
		&mk, &q.Markup.BPS, &q.HideLinePrices, &tm, &q.Tax.RateBPS, &q.PaymentTerms, &q.DepositBPS, &q.CustomerNote,
		&q.RequireCode, &q.ValidUntil, &q.Revision, &q.SentAt, &q.ApprovedAt, &q.DeclinedAt, &q.ConfirmedAt, &q.DealerAcceptedAt, &q.OrderID,
		&q.CreatedAt, &q.UpdatedAt)
	if err != nil {
		return nil, err
	}
	q.Status, q.Markup.Kind, q.Tax.Mode = Status(status), MarkupKind(mk), tm
	return &q, nil
}

// Reach is the caller's branch reach as the branch middleware settled it: the
// context branch, or for a user with no header the sub whose grants apply,
// or neither for an administrator without a header (every branch).
type Reach struct {
	Branch *uuid.UUID
	Grants *string
}

// ReachOf reads the reach the request's context carries.
func ReachOf(ctx context.Context) Reach {
	return Reach{Branch: middleware.BranchIDForQuery(ctx), Grants: middleware.GrantsSubForQuery(ctx)}
}

// GetQuote reads one quote with its children. customerID, when not nil,
// scopes the read to the contractor account, so another account's quote is
// not there at all. forUpdate takes the row lock.
func (s *Store) GetQuote(ctx context.Context, id uuid.UUID, customerID *uuid.UUID, forUpdate bool) (*Quote, error) {
	return s.getQuote(ctx, id, customerID, nil, forUpdate)
}

// GetQuoteInReach reads a quote the way the dealer's desk may: only when one
// of its contractor's branches is inside the caller's reach, the predicate the
// customer module holds its reads to. A quote outside it is not there.
func (s *Store) GetQuoteInReach(ctx context.Context, id uuid.UUID, forUpdate bool) (*Quote, error) {
	r := ReachOf(ctx)
	return s.getQuote(ctx, id, nil, &r, forUpdate)
}

func (s *Store) getQuote(ctx context.Context, id uuid.UUID, customerID *uuid.UUID, reach *Reach, forUpdate bool) (*Quote, error) {
	sql := `SELECT ` + quoteCols + ` FROM pro_customer_quotes WHERE id = $1 AND ($2::uuid IS NULL OR customer_id = $2)`
	args := []any{id, customerID}
	if reach != nil {
		sql += ` AND EXISTS (SELECT 1 FROM customers c WHERE c.id = pro_customer_quotes.customer_id AND ` + customer.Wall("c", 3, 4) + `)`
		args = append(args, reach.Branch, reach.Grants)
	}
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	q, err := scanQuote(s.exec(ctx).QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read customer quote: %w", err)
	}
	if err := s.loadChildren(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

func (s *Store) loadChildren(ctx context.Context, q *Quote) error {
	ex := s.exec(ctx)
	rows, err := ex.Query(ctx, `SELECT id, label, position FROM pro_customer_quote_option_groups WHERE quote_id = $1 ORDER BY position, id`, q.ID)
	if err != nil {
		return fmt.Errorf("read option groups: %w", err)
	}
	for rows.Next() {
		var g OptionGroup
		if err := rows.Scan(&g.ID, &g.Label, &g.Position); err != nil {
			rows.Close()
			return err
		}
		q.Groups = append(q.Groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = ex.Query(ctx, `
		SELECT l.id, l.position, l.product_id, l.category_id, l.sku, l.description, l.uom, ROUND(l.quantity * 10000)::bigint,
			l.unit_cost_ten_thousandths, l.cost_cents, l.price_basis, l.priced_at, l.option_group_id, l.override_kind, l.override_bps,
			l.applied_tier, l.applied_kind, l.applied_bps, l.sell_cents, l.note, COALESCE(s.state, 'pending')
		FROM pro_customer_quote_lines l LEFT JOIN pro_customer_quote_selections s ON s.line_id = l.id
		WHERE l.quote_id = $1 ORDER BY l.position, l.id`, q.ID)
	if err != nil {
		return fmt.Errorf("read quote lines: %w", err)
	}
	for rows.Next() {
		var l Line
		var qty int64
		var okind *string
		var obps *int64
		var tier, kind, state string
		if err := rows.Scan(&l.ID, &l.Position, &l.ProductID, &l.CategoryID, &l.SKU, &l.Description, &l.UOM, &qty,
			&l.UnitCostTenThousandths, &l.CostCents, &l.PriceBasis, &l.PricedAt, &l.GroupID, &okind, &obps,
			&tier, &kind, &l.Applied.BPS, &l.SellCents, &l.Note, &state); err != nil {
			rows.Close()
			return err
		}
		l.Quantity = httpx.Quantity(qty)
		if okind != nil && obps != nil {
			l.Override = &Markup{Kind: MarkupKind(*okind), BPS: *obps}
		}
		l.Applied.Tier, l.Applied.Kind, l.Selection = Tier(tier), MarkupKind(kind), SelectionState(state)
		q.Lines = append(q.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = ex.Query(ctx, `SELECT category_id, kind, bps FROM pro_customer_quote_category_rules WHERE quote_id = $1 ORDER BY category_id`, q.ID)
	if err != nil {
		return fmt.Errorf("read category rules: %w", err)
	}
	for rows.Next() {
		var r CategoryRule
		var kind string
		if err := rows.Scan(&r.CategoryID, &kind, &r.Markup.BPS); err != nil {
			rows.Close()
			return err
		}
		r.Markup.Kind = MarkupKind(kind)
		q.CategoryRules = append(q.CategoryRules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = ex.Query(ctx, `SELECT id, position, description, rate_type, rate_cents, COALESCE(ROUND(hours * 10000)::bigint, 0), contractor_only
		FROM pro_customer_quote_labour_lines WHERE quote_id = $1 ORDER BY position, id`, q.ID)
	if err != nil {
		return fmt.Errorf("read labour lines: %w", err)
	}
	for rows.Next() {
		var l Labour
		var hours int64
		if err := rows.Scan(&l.ID, &l.Position, &l.Description, &l.RateType, &l.RateCents, &hours, &l.ContractorOnly); err != nil {
			rows.Close()
			return err
		}
		l.Hours = httpx.Quantity(hours)
		q.Labour = append(q.Labour, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = ex.Query(ctx, `SELECT id, position, label, amount_type, value, contractor_only
		FROM pro_customer_quote_overhead_lines WHERE quote_id = $1 ORDER BY position, id`, q.ID)
	if err != nil {
		return fmt.Errorf("read overhead lines: %w", err)
	}
	for rows.Next() {
		var o Overhead
		if err := rows.Scan(&o.ID, &o.Position, &o.Label, &o.AmountType, &o.Value, &o.ContractorOnly); err != nil {
			rows.Close()
			return err
		}
		q.Overhead = append(q.Overhead, o)
	}
	rows.Close()
	return rows.Err()
}

// ListFilter narrows the contractor's list.
type ListFilter struct {
	Status    *Status
	AfterTime *time.Time
	AfterID   uuid.UUID
	Limit     int
}

// ListQuotes lists a contractor account's quotes newest first (with children),
// one more than the limit so the caller can tell whether a page follows.
func (s *Store) ListQuotes(ctx context.Context, customerID uuid.UUID, f ListFilter) ([]*Quote, error) {
	var status *string
	if f.Status != nil {
		st := string(*f.Status)
		status = &st
	}
	var afterID *uuid.UUID
	if f.AfterTime != nil {
		afterID = &f.AfterID
	}
	rows, err := s.exec(ctx).Query(ctx, `SELECT `+quoteCols+` FROM pro_customer_quotes
		WHERE customer_id = $1 AND ($2::text IS NULL OR status = $2)
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
		ORDER BY created_at DESC, id DESC LIMIT $5`, customerID, status, f.AfterTime, afterID, f.Limit+1)
	if err != nil {
		return nil, fmt.Errorf("list customer quotes: %w", err)
	}
	var out []*Quote
	for rows.Next() {
		q, err := scanQuote(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, q := range out {
		if err := s.loadChildren(ctx, q); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListSent lists the quotes a dealer may see (sent or later) across the
// contractor accounts inside the caller's branch reach, newest first.
func (s *Store) ListSent(ctx context.Context, limit int, afterTime *time.Time, afterID uuid.UUID) ([]*Quote, error) {
	var aid *uuid.UUID
	if afterTime != nil {
		aid = &afterID
	}
	reach := ReachOf(ctx)
	rows, err := s.exec(ctx).Query(ctx, `SELECT `+quoteCols+` FROM pro_customer_quotes
		WHERE status <> 'draft' AND ($1::timestamptz IS NULL OR (updated_at, id) < ($1, $2))
		  AND EXISTS (SELECT 1 FROM customers c WHERE c.id = pro_customer_quotes.customer_id AND `+customer.Wall("c", 4, 5)+`)
		ORDER BY updated_at DESC, id DESC LIMIT $3`, afterTime, aid, limit+1, reach.Branch, reach.Grants)
	if err != nil {
		return nil, fmt.Errorf("list sent customer quotes: %w", err)
	}
	var out []*Quote
	for rows.Next() {
		q, err := scanQuote(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, q := range out {
		if err := s.loadChildren(ctx, q); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Transition moves a quote from one status to another, only if it is in
// `from` (a lost race moves nothing and returns false). set names extra
// columns to write, e.g. "sent_at = $3".
func (s *Store) Transition(ctx context.Context, id uuid.UUID, from, to Status, set string, args ...any) (bool, error) {
	sql := `UPDATE pro_customer_quotes SET status = $2, revision = revision + 1, updated_at = NOW()`
	if set != "" {
		sql += `, ` + set
	}
	sql += ` WHERE id = $1 AND status = $3`
	all := append([]any{id, string(to), string(from)}, args...)
	// positional placeholders in `set` start at $4
	tag, err := s.exec(ctx).Exec(ctx, sql, all...)
	if err != nil {
		return false, fmt.Errorf("move customer quote to %s: %w", to, err)
	}
	return tag.RowsAffected() == 1, nil
}

// BumpRevision moves the revision of a quote (a re-send or a revise).
func (s *Store) BumpRevision(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx).Exec(ctx, `UPDATE pro_customer_quotes SET revision = revision + 1, updated_at = NOW() WHERE id = $1`, id)
	return err
}

// SetDealerAccepted stamps the dealer's acceptance once.
func (s *Store) SetDealerAccepted(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.exec(ctx).Exec(ctx, `UPDATE pro_customer_quotes SET dealer_accepted_at = $2, updated_at = NOW()
		WHERE id = $1 AND status = 'confirmed' AND dealer_accepted_at IS NULL`, id, at)
	if err != nil {
		return false, fmt.Errorf("record dealer acceptance: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// SetSelections writes the customer's decisions, after the caller has
// checked them against the quote.
func (s *Store) SetSelections(ctx context.Context, quoteID uuid.UUID, states map[uuid.UUID]SelectionState, at time.Time) error {
	for line, st := range states {
		var decided *time.Time
		if st != SelectionPending {
			decided = &at
		}
		tag, err := s.exec(ctx).Exec(ctx, `UPDATE pro_customer_quote_selections SET state = $3, decided_at = $4 WHERE quote_id = $1 AND line_id = $2`,
			quoteID, line, string(st), decided)
		if err != nil {
			return fmt.Errorf("write selection: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
	}
	return nil
}

// --- links ---

// InsertLink stores a link by hash.
func (s *Store) InsertLink(ctx context.Context, quoteID uuid.UUID, hash string, expires time.Time) (*Link, error) {
	var l Link
	var id uuid.UUID
	err := s.exec(ctx).QueryRow(ctx, `INSERT INTO pro_portal_share_tokens (quote_id, token_hash, expires_at) VALUES ($1,$2,$3)
		RETURNING id, quote_id::text, token_hash, created_at, expires_at`, quoteID, hash, expires).
		Scan(&id, &l.QuoteID, &l.Hash, &l.CreatedAt, &l.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("store share token: %w", err)
	}
	l.ID = id.String()
	return &l, nil
}

// InsertDealerLink stores a link to one of the dealer's own quotes by hash.
func (s *Store) InsertDealerLink(ctx context.Context, dealerQuoteID uuid.UUID, hash string, expires time.Time) (*Link, error) {
	var l Link
	var id uuid.UUID
	err := s.exec(ctx).QueryRow(ctx, `INSERT INTO pro_portal_share_tokens (dealer_quote_id, token_hash, expires_at) VALUES ($1,$2,$3)
		RETURNING id, dealer_quote_id::text, token_hash, created_at, expires_at`, dealerQuoteID, hash, expires).
		Scan(&id, &l.DealerQuoteID, &l.Hash, &l.CreatedAt, &l.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("store dealer quote share token: %w", err)
	}
	l.ID = id.String()
	return &l, nil
}

// RevokeLiveDealer revokes the dealer quote's live link, if any.
func (s *Store) RevokeLiveDealer(ctx context.Context, dealerQuoteID uuid.UUID, reason string, at time.Time) (int64, error) {
	tag, err := s.exec(ctx).Exec(ctx, `UPDATE pro_portal_share_tokens SET revoked_at = $2, revoked_reason = $3
		WHERE dealer_quote_id = $1 AND revoked_at IS NULL`, dealerQuoteID, at, reason)
	if err != nil {
		return 0, fmt.Errorf("revoke dealer quote share token: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DealerName is the dealer's own name for a page the dealer's quote is shown on.
func (s *Store) DealerName(ctx context.Context) (string, error) {
	var n string
	err := s.exec(ctx).QueryRow(ctx, `SELECT COALESCE(NULLIF(trim(trading_name), ''), NULLIF(trim(legal_name), ''), '') FROM dealer_profile WHERE id = 1`).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return n, err
}

// RevokeLive revokes the quote's live link, if any, and says how many.
func (s *Store) RevokeLive(ctx context.Context, quoteID uuid.UUID, reason string, at time.Time) (int64, error) {
	tag, err := s.exec(ctx).Exec(ctx, `UPDATE pro_portal_share_tokens SET revoked_at = $2, revoked_reason = $3
		WHERE quote_id = $1 AND revoked_at IS NULL`, quoteID, at, reason)
	if err != nil {
		return 0, fmt.Errorf("revoke share token: %w", err)
	}
	return tag.RowsAffected(), nil
}

// LiveLink returns the quote's live link, if any.
func (s *Store) LiveLink(ctx context.Context, quoteID uuid.UUID) (*Link, error) {
	return s.scanLink(s.exec(ctx).QueryRow(ctx, `SELECT id::text, COALESCE(quote_id::text, ''), COALESCE(dealer_quote_id::text, ''), token_hash, created_at, expires_at, revoked_at
		FROM pro_portal_share_tokens WHERE quote_id = $1 AND revoked_at IS NULL`, quoteID))
}

// LinkByHash finds a link by the SHA-256 hash of the presented token.
func (s *Store) LinkByHash(ctx context.Context, hash string) (*Link, error) {
	return s.scanLink(s.exec(ctx).QueryRow(ctx, `SELECT id::text, COALESCE(quote_id::text, ''), COALESCE(dealer_quote_id::text, ''), token_hash, created_at, expires_at, revoked_at
		FROM pro_portal_share_tokens WHERE token_hash = $1`, hash))
}

func (s *Store) scanLink(row pgx.Row) (*Link, error) {
	var l Link
	if err := row.Scan(&l.ID, &l.QuoteID, &l.DealerQuoteID, &l.Hash, &l.CreatedAt, &l.ExpiresAt, &l.RevokedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read share token: %w", err)
	}
	return &l, nil
}

// TouchLink records that the customer opened the link.
func (s *Store) TouchLink(ctx context.Context, linkID string, at time.Time) error {
	_, err := s.exec(ctx).Exec(ctx, `UPDATE pro_portal_share_tokens SET last_viewed_at = $2, first_viewed_at = COALESCE(first_viewed_at, $2) WHERE id = $1::uuid`, linkID, at)
	return err
}

// --- verification codes ---

// CodeRow is a stored code with its identity.
type CodeRow struct {
	ID          uuid.UUID
	Channel     string
	Destination string
	CodeRecord
}

// InsertCode stores a freshly minted code's hash.
func (s *Store) InsertCode(ctx context.Context, quoteID uuid.UUID, linkID, channel, destination, salt, hash string, expires time.Time) error {
	_, err := s.exec(ctx).Exec(ctx, `INSERT INTO pro_portal_verification_codes (quote_id, token_id, channel, destination, salt, code_hash, max_attempts, expires_at)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, $7, $8)`, quoteID, linkID, channel, destination, salt, hash, CodeMaxAttempts, expires)
	if err != nil {
		return fmt.Errorf("store verification code: %w", err)
	}
	return nil
}

// LatestCodeForUpdate returns the link's most recent code, locked.
func (s *Store) LatestCodeForUpdate(ctx context.Context, linkID string) (*CodeRow, error) {
	var c CodeRow
	var qid uuid.UUID
	err := s.exec(ctx).QueryRow(ctx, `SELECT id, quote_id, channel, destination, salt, code_hash, attempts, max_attempts, expires_at, consumed_at
		FROM pro_portal_verification_codes WHERE token_id = $1::uuid ORDER BY created_at DESC, id DESC LIMIT 1 FOR UPDATE`, linkID).
		Scan(&c.ID, &qid, &c.Channel, &c.Destination, &c.Salt, &c.Hash, &c.Attempts, &c.MaxAttempts, &c.ExpiresAt, &c.ConsumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read verification code: %w", err)
	}
	c.QuoteID = qid.String()
	return &c, nil
}

// CountCodesSince counts the codes issued for a link since a time.
func (s *Store) CountCodesSince(ctx context.Context, linkID string, since time.Time) (int, error) {
	var n int
	err := s.exec(ctx).QueryRow(ctx, `SELECT count(*) FROM pro_portal_verification_codes WHERE token_id = $1::uuid AND created_at >= $2`, linkID, since).Scan(&n)
	return n, err
}

// BumpCodeAttempts counts one try against a code.
func (s *Store) BumpCodeAttempts(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx).Exec(ctx, `UPDATE pro_portal_verification_codes SET attempts = attempts + 1 WHERE id = $1`, id)
	return err
}

// ConsumeCode marks a code used, once.
func (s *Store) ConsumeCode(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	tag, err := s.exec(ctx).Exec(ctx, `UPDATE pro_portal_verification_codes SET consumed_at = $2 WHERE id = $1 AND consumed_at IS NULL`, id, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// --- the approval log ---

// AppendLog adds one step to the approval record.
func (s *Store) AppendLog(ctx context.Context, e *ApprovalEntry) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	var sk, sd, th, snap, sum *string
	nz := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	sk, sd, th, snap, sum = nz(e.SignatureKind), nz(e.SignatureData), nz(e.TokenHash), nz(e.Snapshot), nz(e.SnapshotSHA256)
	_, err := s.exec(ctx).Exec(ctx, `
		INSERT INTO pro_customer_quote_approval_log (id, quote_id, quote_revision, step, actor_kind, actor_name, actor_contact, actor_ref,
			occurred_at, token_hash, signature_kind, signature_data, source_address, user_agent, code_verified, snapshot, snapshot_sha256, total_cents, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		e.ID, e.QuoteID, e.QuoteRevision, e.Step, e.ActorKind, e.ActorName, e.ActorContact, e.ActorRef,
		e.OccurredAt, th, sk, sd, e.SourceAddress, e.UserAgent, e.CodeVerified, snap, sum, e.TotalCents, e.Note)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrInvalidState
		}
		return fmt.Errorf("append approval log: %w", err)
	}
	return nil
}

// ListLog reads a quote's approval record in order.
func (s *Store) ListLog(ctx context.Context, quoteID uuid.UUID) ([]ApprovalEntry, error) {
	rows, err := s.exec(ctx).Query(ctx, `
		SELECT id, quote_id, quote_revision, step, actor_kind, actor_name, actor_contact, actor_ref, occurred_at,
			COALESCE(token_hash, ''), COALESCE(signature_kind, ''), COALESCE(signature_data, ''), source_address, user_agent,
			code_verified, COALESCE(snapshot, ''), COALESCE(snapshot_sha256, ''), total_cents, note
		FROM pro_customer_quote_approval_log WHERE quote_id = $1 ORDER BY seq`, quoteID)
	if err != nil {
		return nil, fmt.Errorf("read approval log: %w", err)
	}
	defer rows.Close()
	var out []ApprovalEntry
	for rows.Next() {
		var e ApprovalEntry
		if err := rows.Scan(&e.ID, &e.QuoteID, &e.QuoteRevision, &e.Step, &e.ActorKind, &e.ActorName, &e.ActorContact, &e.ActorRef, &e.OccurredAt,
			&e.TokenHash, &e.SignatureKind, &e.SignatureData, &e.SourceAddress, &e.UserAgent, &e.CodeVerified, &e.Snapshot, &e.SnapshotSHA256, &e.TotalCents, &e.Note); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- the linked order and project ---

// OrderBrief is the order a quote converted into.
type OrderBrief struct {
	ID         uuid.UUID
	Number     string
	Status     string
	TotalCents int64
}

// OrderBriefOf reads the order, nil when the quote has none.
func (s *Store) OrderBriefOf(ctx context.Context, orderID *uuid.UUID) (*OrderBrief, error) {
	if orderID == nil {
		return nil, nil
	}
	var o OrderBrief
	err := s.exec(ctx).QueryRow(ctx, `SELECT id, number, status, ROUND(total_amount * 100)::bigint FROM orders WHERE id = $1`, *orderID).
		Scan(&o.ID, &o.Number, &o.Status, &o.TotalCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read linked order: %w", err)
	}
	return &o, nil
}

// ProjectName reads a project's name.
func (s *Store) ProjectName(ctx context.Context, projectID *uuid.UUID) (string, error) {
	if projectID == nil {
		return "", nil
	}
	var n string
	err := s.exec(ctx).QueryRow(ctx, `SELECT name FROM projects WHERE id = $1`, *projectID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return n, err
}

// CustomerName reads an account's name.
func (s *Store) CustomerName(ctx context.Context, customerID uuid.UUID) (string, error) {
	var n string
	err := s.exec(ctx).QueryRow(ctx, `SELECT name FROM customers WHERE id = $1`, customerID).Scan(&n)
	return n, err
}

// Setting reads one system setting, "" when unset.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.exec(ctx).QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// ExportProject is one of the contractor's projects, for the export.
type ExportProject struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	CreatedAt httpx.Timestamp `json:"created_at"`
}

// ListProjects lists the contractor account's projects.
func (s *Store) ListProjects(ctx context.Context, customerID uuid.UUID) ([]ExportProject, error) {
	rows, err := s.exec(ctx).Query(ctx, `SELECT id, name, lower(status), created_at FROM projects WHERE customer_id = $1 ORDER BY created_at, id`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	out := []ExportProject{}
	for rows.Next() {
		var p ExportProject
		var at time.Time
		if err := rows.Scan(&p.ID, &p.Name, &p.Status, &at); err != nil {
			return nil, err
		}
		p.CreatedAt = httpx.TimestampOf(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// StaffName is the display name the dealer's staff roster holds for an email
// address, "" when it names no one.
func (s *Store) StaffName(ctx context.Context, email string) (string, error) {
	if strings.TrimSpace(email) == "" {
		return "", nil
	}
	var n string
	err := s.exec(ctx).QueryRow(ctx, `SELECT full_name FROM staff WHERE lower(email) = lower($1) LIMIT 1`, email).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return n, err
}
