// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PortalBalanceTilesDTO holds the contractor's account-level balance figures
// in integer cents.
type PortalBalanceTilesDTO struct {
	Currency             string `json:"currency"`
	CurrentBalanceCents  int64  `json:"current_balance_cents"`
	CreditLimitCents     *int64 `json:"credit_limit_cents"`
	AvailableCreditCents *int64 `json:"available_credit_cents"`
	PastDueCents         int64  `json:"past_due_cents"`
	UnappliedCashCents   int64  `json:"unapplied_cash_cents"`
	NetBalanceCents      int64  `json:"net_balance_cents"`
	OpenInvoiceCount     int    `json:"open_invoice_count"`
	OverdueInvoiceCount  int    `json:"overdue_invoice_count"`
}

// PortalAgingBucketsDTO holds the customer's receivable aging buckets in integer cents.
type PortalAgingBucketsDTO struct {
	AsOf            string `json:"as_of"`
	Currency        string `json:"currency"`
	CurrentCents    int64  `json:"current_cents"`
	Days1To30Cents  int64  `json:"days_1_30_cents"`
	Days31To60Cents int64  `json:"days_31_60_cents"`
	Days61To90Cents int64  `json:"days_61_90_cents"`
	DaysOver90Cents int64  `json:"days_over_90_cents"`
	UnappliedCents  int64  `json:"unapplied_cents"`
	TotalOpenCents  int64  `json:"total_open_cents"`
	NetCents        int64  `json:"net_cents"`
}

// PortalJobSummaryDTO rolls up spend, quoted-vs-ordered variance, invoiced/open
// amounts (all in integer cents) and delivery progress for one contractor project.
type PortalJobSummaryDTO struct {
	ProjectID          uuid.UUID `json:"project_id"`
	ProjectName        string    `json:"project_name"`
	Status             string    `json:"status"`
	BudgetCents        int64     `json:"budget_cents"`
	QuotedCents        int64     `json:"quoted_cents"`
	OrderedCents       int64     `json:"ordered_cents"`
	InvoicedCents      int64     `json:"invoiced_cents"`
	PaidCents          int64     `json:"paid_cents"`
	OpenBalanceCents   int64     `json:"open_balance_cents"`
	QuoteVarianceCents int64     `json:"quote_variance_cents"`
	OrderCount         int       `json:"order_count"`
	InvoiceCount       int       `json:"invoice_count"`
	DeliveryCount      int       `json:"delivery_count"`
	DeliveredCount     int       `json:"delivered_count"`
}

// PortalBillingSummaryDTO combines balance tiles, aging buckets, and job summaries.
type PortalBillingSummaryDTO struct {
	BalanceTiles PortalBalanceTilesDTO `json:"balance_tiles"`
	Aging        PortalAgingBucketsDTO `json:"aging"`
	JobSummaries []PortalJobSummaryDTO `json:"job_summaries"`
}

// PortalStatementLineDTO is one subledger movement on a customer statement.
type PortalStatementLineDTO struct {
	ID                uuid.UUID  `json:"id"`
	Date              string     `json:"date"`
	Type              string     `json:"type"`
	Description       string     `json:"description"`
	AmountCents       int64      `json:"amount_cents"`
	BalanceAfterCents int64      `json:"balance_after_cents"`
	ReferenceID       *uuid.UUID `json:"reference_id,omitempty"`
	JobID             *uuid.UUID `json:"job_id,omitempty"`
}

// PortalStatementOpenDocDTO is an open invoice or credit memo on a statement.
type PortalStatementOpenDocDTO struct {
	Kind       string     `json:"kind"`
	ID         uuid.UUID  `json:"id"`
	Number     string     `json:"number"`
	Date       string     `json:"date"`
	DueDate    *string    `json:"due_date,omitempty"`
	JobID      *uuid.UUID `json:"job_id,omitempty"`
	TotalCents int64      `json:"total_cents"`
	OpenCents  int64      `json:"open_cents"`
}

// PortalStatementDTO is the contractor's live statement and aging view.
type PortalStatementDTO struct {
	CustomerID          uuid.UUID                   `json:"customer_id"`
	CustomerName        string                      `json:"customer_name"`
	From                string                      `json:"from"`
	To                  string                      `json:"to"`
	Currency            string                      `json:"currency"`
	OpeningBalanceCents int64                       `json:"opening_balance_cents"`
	ClosingBalanceCents int64                       `json:"closing_balance_cents"`
	UnappliedCents      int64                       `json:"unapplied_cents"`
	NetBalanceCents     int64                       `json:"net_balance_cents"`
	Aging               PortalAgingBucketsDTO       `json:"aging"`
	Lines               []PortalStatementLineDTO    `json:"lines"`
	OpenInvoices        []PortalStatementOpenDocDTO `json:"open_invoices"`
}

// PortalStatementRunItemDTO is one historical statement run snapshot for the customer.
type PortalStatementRunItemDTO struct {
	ID                  uuid.UUID `json:"id"`
	RunID               uuid.UUID `json:"run_id"`
	PeriodStart         string    `json:"period_start"`
	PeriodEnd           string    `json:"period_end"`
	Currency            string    `json:"currency"`
	OpeningBalanceCents int64     `json:"opening_balance_cents"`
	InvoicesCents       int64     `json:"invoices_cents"`
	PaymentsCents       int64     `json:"payments_cents"`
	FinanceChargesCents int64     `json:"finance_charges_cents"`
	ClosingBalanceCents int64     `json:"closing_balance_cents"`
	CurrentCents        int64     `json:"current_cents"`
	Days1To30Cents      int64     `json:"days_1_30_cents"`
	Days31To60Cents     int64     `json:"days_31_60_cents"`
	Days61To90Cents     int64     `json:"days_61_90_cents"`
	DaysOver90Cents     int64     `json:"days_over_90_cents"`
	UnappliedCents      int64     `json:"unapplied_cents"`
	NetBalanceCents     int64     `json:"net_balance_cents"`
	CreatedAt           time.Time `json:"created_at"`
}

func (s *Service) computeCustomerAging(ctx context.Context, customerID uuid.UUID, asOf time.Time) (PortalAgingBucketsDTO, error) {
	out := PortalAgingBucketsDTO{
		AsOf:     asOf.Format("2006-01-02"),
		Currency: "USD",
	}
	if s.accountSvc == nil {
		return out, nil
	}
	sum, err := s.accountSvc.AgingSummary(branchctx.WithSystem(ctx), account.AgingQuery{
		GroupBy:      "customer",
		AsOf:         asOf,
		Basis:        "due_date",
		CustomerID:   &customerID,
		WholeAccount: true,
	})
	if err != nil {
		return out, err
	}
	if len(sum.Totals) > 0 {
		t := sum.Totals[0]
		out.Currency = t.Currency
		out.CurrentCents = int64(t.CurrentCents)
		out.Days1To30Cents = int64(t.Days1To30)
		out.Days31To60Cents = int64(t.Days31To60)
		out.Days61To90Cents = int64(t.Days61To90)
		out.DaysOver90Cents = int64(t.Over90)
		unapplied := int64(t.UnappliedCents)
		if unapplied < 0 {
			unapplied = -unapplied
		}
		out.UnappliedCents = unapplied
		out.TotalOpenCents = out.CurrentCents + out.Days1To30Cents + out.Days31To60Cents + out.Days61To90Cents + out.DaysOver90Cents
		out.NetCents = int64(t.TotalCents)
	}
	return out, nil
}

// customerBranchDay is the calendar day of the branch the customer belongs
// to, which is the day an invoice falls due on: the house rule for money.
// The aging is read on that day, so the billing summary's tiles and the
// desk's aging answer alike, and a branch west of UTC gets the same answer
// the desk gives it instead of the session's day.
//
// The branch is the customer's own primary branch, and the day is read
// through the AR core's LocalDate, the way a portal payment dates itself
// (payments.go). A customer with no branch to read falls back to the
// session's day, as the aging always did.
func (s *Service) customerBranchDay(ctx context.Context, customerID uuid.UUID) (time.Time, error) {
	now := s.now().UTC()
	if s.accountSvc == nil {
		return now, nil
	}
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return now, nil
	}
	var branchID uuid.UUID
	err := pgRepo.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT primary_branch_id FROM customers WHERE id = $1`, customerID).Scan(&branchID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return now, nil
		}
		return time.Time{}, fmt.Errorf("failed to read the customer's branch: %w", err)
	}
	day, err := s.accountSvc.LocalDate(ctx, branchID, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to read the branch's day: %w", err)
	}
	return day, nil
}

// GetBillingSummary returns balance tiles, receivable aging buckets, and
// per-project financial/delivery summaries in integer cents.
func (s *Service) GetBillingSummary(ctx context.Context, customerID uuid.UUID) (*PortalBillingSummaryDTO, error) {
	asOf, err := s.customerBranchDay(ctx, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the customer's branch day: %w", err)
	}
	aging, err := s.computeCustomerAging(ctx, customerID, asOf)
	if err != nil {
		return nil, fmt.Errorf("failed to compute aging: %w", err)
	}

	tiles := PortalBalanceTilesDTO{
		Currency:           aging.Currency,
		PastDueCents:       aging.Days1To30Cents + aging.Days31To60Cents + aging.Days61To90Cents + aging.DaysOver90Cents,
		UnappliedCashCents: aging.UnappliedCents,
		NetBalanceCents:    aging.NetCents,
	}

	if s.accountSvc != nil {
		sum, err := s.accountSvc.GetSummary(branchctx.WithSystem(ctx), customerID)
		if err != nil {
			return nil, fmt.Errorf("failed to get account summary: %w", err)
		}
		if sum != nil {
			tiles.Currency = sum.Currency
			tiles.CurrentBalanceCents = int64(sum.BalanceCents)
			if sum.CreditLimitCents != nil {
				v := int64(*sum.CreditLimitCents)
				tiles.CreditLimitCents = &v
			}
			if sum.AvailableCredit != nil {
				v := int64(*sum.AvailableCredit)
				tiles.AvailableCreditCents = &v
			}
			if sum.UnappliedCents != 0 {
				u := int64(sum.UnappliedCents)
				if u < 0 {
					u = -u
				}
				tiles.UnappliedCashCents = u
			}
		}
	}

	jobs := make([]PortalJobSummaryDTO, 0)
	if pgRepo, ok := s.pgRepo(); ok && pgRepo != nil {
		exec := pgRepo.db.GetExecutor(ctx)
		if err := exec.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE i.status IN ('UNPAID', 'PARTIAL')),
				COUNT(*) FILTER (WHERE `+invoice.OverdueExpr("i")+`)
			FROM invoices i
			WHERE i.customer_id = $1
		`, customerID).Scan(&tiles.OpenInvoiceCount, &tiles.OverdueInvoiceCount); err != nil {
			return nil, fmt.Errorf("failed to count open invoices: %w", err)
		}

		rows, qErr := exec.Query(ctx, `
			SELECT
				p.id,
				p.name,
				COALESCE(p.status, 'ACTIVE'),
				COALESCE((SELECT SUM(ROUND(q.total_amount * 100)::bigint) FROM quotes q WHERE q.project_id = p.id AND q.customer_id = $1 AND q.state IN ('SENT', 'ACCEPTED')), 0) AS quoted_cents,
				COALESCE((SELECT SUM(ROUND(o.total_amount * 100)::bigint) FROM orders o WHERE o.project_id = p.id AND o.customer_id = $1 AND o.status <> 'CANCELLED'), 0) AS ordered_cents,
				COALESCE((SELECT SUM(ROUND(i.total_amount * 100)::bigint) FROM invoices i LEFT JOIN orders o ON o.id = i.order_id WHERE COALESCE(i.project_id, o.project_id) = p.id AND i.customer_id = $1 AND i.status <> 'VOID'), 0) AS invoiced_cents,
				COALESCE((SELECT SUM(ROUND(i.amount_open * 100)::bigint) FROM invoices i LEFT JOIN orders o ON o.id = i.order_id WHERE COALESCE(i.project_id, o.project_id) = p.id AND i.customer_id = $1 AND i.status IN ('UNPAID', 'PARTIAL')), 0) AS open_cents,
				COALESCE((SELECT COUNT(*) FROM orders o WHERE o.project_id = p.id AND o.customer_id = $1 AND o.status <> 'CANCELLED'), 0)::int AS order_count,
				COALESCE((SELECT COUNT(*) FROM invoices i LEFT JOIN orders o ON o.id = i.order_id WHERE COALESCE(i.project_id, o.project_id) = p.id AND i.customer_id = $1 AND i.status <> 'VOID'), 0)::int AS invoice_count,
				COALESCE((SELECT COUNT(*) FROM deliveries d JOIN orders o ON o.id = d.order_id WHERE o.project_id = p.id AND o.customer_id = $1), 0)::int AS delivery_count,
				COALESCE((SELECT COUNT(*) FROM deliveries d JOIN orders o ON o.id = d.order_id WHERE o.project_id = p.id AND o.customer_id = $1 AND d.status = 'DELIVERED'), 0)::int AS delivered_count
			FROM projects p
			WHERE p.customer_id = $1
			ORDER BY p.created_at DESC
		`, customerID)
		if qErr != nil {
			return nil, fmt.Errorf("failed to query job summaries: %w", qErr)
		}
		defer rows.Close()
		for rows.Next() {
			var j PortalJobSummaryDTO
			if err := rows.Scan(
				&j.ProjectID, &j.ProjectName, &j.Status,
				&j.QuotedCents, &j.OrderedCents,
				&j.InvoicedCents, &j.OpenBalanceCents,
				&j.OrderCount, &j.InvoiceCount, &j.DeliveryCount, &j.DeliveredCount,
			); err != nil {
				return nil, fmt.Errorf("failed to scan job summary: %w", err)
			}
			j.BudgetCents = j.QuotedCents
			j.PaidCents = j.InvoicedCents - j.OpenBalanceCents
			if j.PaidCents < 0 {
				j.PaidCents = 0
			}
			j.QuoteVarianceCents = j.OrderedCents - j.QuotedCents
			jobs = append(jobs, j)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("failed to iterate job summaries: %w", err)
		}
	}

	return &PortalBillingSummaryDTO{
		BalanceTiles: tiles,
		Aging:        aging,
		JobSummaries: jobs,
	}, nil
}

// GetLiveStatement returns the customer's live statement and aging buckets for [from, to].
func (s *Service) GetLiveStatement(ctx context.Context, customerID uuid.UUID, from, to time.Time) (*PortalStatementDTO, error) {
	if s.accountSvc == nil {
		return nil, fmt.Errorf("account service is not configured")
	}
	sysCtx := branchctx.WithSystem(ctx)
	st, err := s.accountSvc.Statement(sysCtx, account.StatementQuery{
		CustomerID: customerID,
		From:       from,
		To:         to,
	})
	if err != nil {
		return nil, err
	}
	aging, err := s.computeCustomerAging(ctx, customerID, to)
	if err != nil {
		return nil, err
	}

	out := &PortalStatementDTO{
		CustomerID:   st.CustomerID,
		CustomerName: st.CustomerName,
		From:         st.From,
		To:           st.To,
		Currency:     aging.Currency,
		Aging:        aging,
		Lines:        []PortalStatementLineDTO{},
		OpenInvoices: []PortalStatementOpenDocDTO{},
	}
	if len(st.Currencies) > 0 {
		c := st.Currencies[0]
		out.Currency = c.Currency
		out.OpeningBalanceCents = int64(c.OpeningBalanceCents)
		out.ClosingBalanceCents = int64(c.ClosingBalanceCents)
		out.UnappliedCents = int64(c.UnappliedCents)
		out.NetBalanceCents = int64(c.NetBalanceCents)
		for _, l := range c.Lines {
			out.Lines = append(out.Lines, PortalStatementLineDTO{
				ID:                l.ID,
				Date:              l.Date,
				Type:              string(l.Type),
				Description:       l.Description,
				AmountCents:       int64(l.AmountCents),
				BalanceAfterCents: int64(l.BalanceAfterCents),
				ReferenceID:       l.ReferenceID,
				JobID:             l.JobID,
			})
		}
		for _, od := range c.OpenDocuments {
			out.OpenInvoices = append(out.OpenInvoices, PortalStatementOpenDocDTO{
				Kind:       od.Kind,
				ID:         od.ID,
				Number:     od.Number,
				Date:       od.Date,
				DueDate:    od.DueDate,
				JobID:      od.JobID,
				TotalCents: int64(od.TotalCents),
				OpenCents:  int64(od.OpenCents),
			})
		}
	}
	return out, nil
}

// ListStatementRuns lists statement run snapshots belonging to customerID.
func (s *Service) ListStatementRuns(ctx context.Context, customerID uuid.UUID) ([]PortalStatementRunItemDTO, error) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return []PortalStatementRunItemDTO{}, nil
	}
	rows, err := pgRepo.db.GetExecutor(ctx).Query(ctx, `
		SELECT
			i.id,
			i.run_id,
			r.period_start::text,
			r.period_end::text,
			i.currency,
			i.opening_balance_cents,
			i.invoices_cents,
			i.payments_cents,
			i.finance_charges_cents,
			i.closing_balance_cents,
			COALESCE((i.aging_buckets->>'current_cents')::bigint, 0),
			COALESCE((i.aging_buckets->>'days_1_30_cents')::bigint, 0),
			COALESCE((i.aging_buckets->>'days_31_60_cents')::bigint, 0),
			COALESCE((i.aging_buckets->>'days_61_90_cents')::bigint, 0),
			COALESCE((i.aging_buckets->>'over_90_cents')::bigint, 0),
			ABS(COALESCE(
				(i.aging_buckets->>'unapplied_cents')::bigint,
				(
					SELECT COALESCE(SUM(ROUND(p.amount_unapplied * 100)::bigint), 0)
					FROM payments p
					WHERE p.customer_id = i.customer_id
					  AND p.currency = i.currency
					  AND p.status IN ('POSTED', 'PARTIALLY_APPLIED')
					  AND p.received_on <= COALESCE(r.as_of_date, r.period_end)
				)
			))::bigint,
			r.created_at
		FROM ar_statement_run_items i
		JOIN ar_statement_runs r ON r.id = i.run_id
		WHERE i.customer_id = $1
		ORDER BY r.period_end DESC, r.created_at DESC
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list statement runs: %w", err)
	}
	defer rows.Close()

	out := make([]PortalStatementRunItemDTO, 0)
	for rows.Next() {
		var it PortalStatementRunItemDTO
		if err := rows.Scan(
			&it.ID, &it.RunID, &it.PeriodStart, &it.PeriodEnd, &it.Currency,
			&it.OpeningBalanceCents, &it.InvoicesCents, &it.PaymentsCents, &it.FinanceChargesCents,
			&it.ClosingBalanceCents,
			&it.CurrentCents, &it.Days1To30Cents, &it.Days31To60Cents,
			&it.Days61To90Cents, &it.DaysOver90Cents,
			&it.UnappliedCents, &it.CreatedAt,
		); err != nil {
			return nil, err
		}
		it.NetBalanceCents = it.ClosingBalanceCents - it.UnappliedCents
		out = append(out, it)
	}
	return out, rows.Err()
}

// RenderInvoicePDF generates a PDF document for an invoice owned by customerID.
func (s *Service) RenderInvoicePDF(ctx context.Context, customerID, invoiceID uuid.UUID) ([]byte, string, error) {
	if s.invoiceSvc == nil || s.docSvc == nil || s.customerSvc == nil {
		return nil, "", fmt.Errorf("document generation services are not configured")
	}
	// Verify ownership first so another customer's invoice ID answers 404.
	if _, err := s.repo.GetInvoiceByIDAndCustomer(ctx, invoiceID, customerID); err != nil {
		return nil, "", ErrInvoiceNotFound
	}
	sysCtx := branchctx.WithSystem(ctx)
	inv, err := s.invoiceSvc.GetInvoice(sysCtx, invoiceID)
	if err != nil || inv == nil || inv.CustomerID != customerID {
		return nil, "", ErrInvoiceNotFound
	}
	cust, err := s.customerSvc.GetCustomer(sysCtx, customerID)
	if err != nil {
		return nil, "", err
	}
	pdf, err := s.docSvc.GenerateInvoicePDF(sysCtx, inv, cust)
	if err != nil {
		return nil, "", err
	}
	num := inv.Number
	if num == "" {
		num = inv.ID.String()[:8]
	}
	return pdf, fmt.Sprintf("invoice-%s.pdf", num), nil
}

// RenderStatementPDF generates a PDF document for a customer's statement over [from, to].
func (s *Service) RenderStatementPDF(ctx context.Context, customerID uuid.UUID, from, to time.Time) ([]byte, string, error) {
	if s.accountSvc == nil || s.docSvc == nil || s.customerSvc == nil {
		return nil, "", fmt.Errorf("document generation services are not configured")
	}
	sysCtx := branchctx.WithSystem(ctx)
	st, err := s.accountSvc.Statement(sysCtx, account.StatementQuery{
		CustomerID: customerID,
		From:       from,
		To:         to,
	})
	if err != nil {
		return nil, "", err
	}
	cust, err := s.customerSvc.GetCustomer(sysCtx, customerID)
	if err != nil {
		return nil, "", err
	}
	pdf, err := s.docSvc.GenerateStatementPDF(sysCtx, st, cust)
	if err != nil {
		return nil, "", err
	}
	return pdf, fmt.Sprintf("statement-%s.pdf", to.Format("2006-01-02")), nil
}

// RenderStatementRunItemPDF generates a PDF for a specific statement run item owned by customerID.
func (s *Service) RenderStatementRunItemPDF(ctx context.Context, customerID, itemID uuid.UUID) ([]byte, string, error) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return nil, "", ErrStatementNotFound
	}
	var from, to time.Time
	err := pgRepo.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT r.period_start, r.period_end
		FROM ar_statement_run_items i
		JOIN ar_statement_runs r ON r.id = i.run_id
		WHERE i.id = $1 AND i.customer_id = $2
	`, itemID, customerID).Scan(&from, &to)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrStatementNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return s.RenderStatementPDF(ctx, customerID, from, to)
}
