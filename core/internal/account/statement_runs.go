// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Batch statement runs (AR-04): a run is an artefact, not a query. The
// figures it records stand as they stood at the period's last day, and a
// second run of the same period is another artefact, never a mutation of the
// first: a run writes no balance, no document and no entry, so two runs of
// one period answer the same numbers. Queuing an item only writes the run's
// own delivery record and its outbox event; nothing sends here.

// StatementRun is an ar_statement_runs row on the wire.
type StatementRun struct {
	ID                uuid.UUID          `json:"id"`
	PeriodStart       string             `json:"period_start"`
	PeriodEnd         string             `json:"period_end"`
	AsOfDate          string             `json:"as_of_date"`
	Status            string             `json:"status"`
	TotalCustomers    int                `json:"total_customers"`
	QueuedCount       int                `json:"queued_count"`
	SentCount         int                `json:"sent_count"`
	TotalBalanceCents httpx.Cents        `json:"total_balance_cents"`
	TriggeredBy       *string            `json:"triggered_by"`
	CreatedAt         httpx.Timestamp    `json:"created_at"`
	CompletedAt       *httpx.Timestamp   `json:"completed_at"`
	Items             []StatementRunItem `json:"items,omitempty"`
}

// StatementRunItem is one customer's recorded statement figures.
type StatementRunItem struct {
	ID                  uuid.UUID        `json:"id"`
	RunID               uuid.UUID        `json:"run_id"`
	CustomerID          uuid.UUID        `json:"customer_id"`
	CustomerName        string           `json:"customer_name"`
	Currency            string           `json:"currency"`
	OpeningBalanceCents httpx.Cents      `json:"opening_balance_cents"`
	InvoicesCents       httpx.Cents      `json:"invoices_cents"`
	PaymentsCents       httpx.Cents      `json:"payments_cents"`
	FinanceChargesCents httpx.Cents      `json:"finance_charges_cents"`
	ClosingBalanceCents httpx.Cents      `json:"closing_balance_cents"`
	DeliveryStatus      string           `json:"delivery_status"`
	OutboxID            *uuid.UUID       `json:"outbox_id"`
	AgingBuckets        map[string]int64 `json:"aging_buckets"`
}

const statementRunColumns = `r.id, r.period_start, r.period_end, r.as_of_date, r.status, r.total_customers,
	r.queued_count, r.sent_count, r.total_balance_cents, r.triggered_by, r.created_at, r.completed_at`

func scanStatementRun(row pgx.Row, r *StatementRun) error {
	var start, end, asOf time.Time
	var created time.Time
	var completed *time.Time
	if err := row.Scan(&r.ID, &start, &end, &asOf, &r.Status, &r.TotalCustomers, &r.QueuedCount, &r.SentCount,
		&r.TotalBalanceCents, &r.TriggeredBy, &created, &completed); err != nil {
		return err
	}
	r.PeriodStart, r.PeriodEnd, r.AsOfDate = date(start), date(end), date(asOf)
	r.Status = lower(r.Status)
	r.CreatedAt, r.CompletedAt = httpx.TimestampOf(created), httpx.PtrTimestamp(completed)
	return nil
}

const statementRunItemColumns = `i.id, i.run_id, i.customer_id, c.name, i.currency, i.opening_balance_cents, i.invoices_cents,
	i.payments_cents, i.finance_charges_cents, i.closing_balance_cents, i.delivery_status, i.outbox_id, i.aging_buckets`

func scanStatementRunItem(row pgx.Row, it *StatementRunItem) error {
	var buckets []byte
	if err := row.Scan(&it.ID, &it.RunID, &it.CustomerID, &it.CustomerName, &it.Currency, &it.OpeningBalanceCents,
		&it.InvoicesCents, &it.PaymentsCents, &it.FinanceChargesCents, &it.ClosingBalanceCents,
		&it.DeliveryStatus, &it.OutboxID, &buckets); err != nil {
		return err
	}
	it.DeliveryStatus = lower(it.DeliveryStatus)
	if len(buckets) > 0 {
		if err := json.Unmarshal(buckets, &it.AgingBuckets); err != nil {
			return fmt.Errorf("failed to read the aging buckets: %w", err)
		}
	}
	if it.AgingBuckets == nil {
		it.AgingBuckets = map[string]int64{}
	}
	return nil
}

// StatementRunIn is the body of POST /api/v1/ar/statements/runs.
type StatementRunIn struct {
	PeriodStart time.Time
	PeriodEnd   time.Time
	CustomerIDs []uuid.UUID
	QueueEmail  bool
	Actor       string
	Role        string
}

// statementFigures is one customer's period figures, read from the subledger
// with each row's own day (the statement's day, so a run and the statement
// read agree on where a row belongs).
type statementFigures struct {
	Opening, Invoices, Payments, FinanceCharges, Closing int64
}

func (s *Service) statementFigures(ctx context.Context, customerID uuid.UUID, from, to time.Time) (statementFigures, error) {
	var f statementFigures
	err := s.ex(ctx).QueryRow(ctx, `
SELECT COALESCE(SUM(x.amount) FILTER (WHERE x.day < $2::date), 0),
       COALESCE(SUM(x.amount) FILTER (WHERE x.day >= $2::date AND x.day <= $3::date AND x.type = 'INVOICE' AND NOT x.fc), 0),
       COALESCE(SUM(-x.amount) FILTER (WHERE x.day >= $2::date AND x.day <= $3::date AND x.type = 'PAYMENT'), 0),
       COALESCE(SUM(x.amount) FILTER (WHERE x.day >= $2::date AND x.day <= $3::date AND x.fc), 0),
       COALESCE(SUM(x.amount) FILTER (WHERE x.day <= $3::date), 0)
FROM (`+statementDayRowsSQL+`
) x`, customerID, date(from), date(to)).Scan(&f.Opening, &f.Invoices, &f.Payments, &f.FinanceCharges, &f.Closing)
	if err != nil {
		return f, fmt.Errorf("failed to read the statement figures: %w", err)
	}
	return f, nil
}

// agingBucketsOf is one customer's aging as of the run's last day, the whole
// account (a run is a whole book act), across the currencies the account
// carries.
func (s *Service) agingBucketsOf(ctx context.Context, customerID uuid.UUID, asOf time.Time) (map[string]int64, error) {
	items, err := s.Aging(ctx, AgingQuery{GroupBy: "customer", Basis: "due_date", AsOf: asOf,
		CustomerID: &customerID, WholeAccount: true})
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, it := range items {
		out["current_cents"] += int64(it.CurrentCents)
		out["days_1_30_cents"] += int64(it.Days1To30)
		out["days_31_60_cents"] += int64(it.Days31To60)
		out["days_61_90_cents"] += int64(it.Days61To90)
		out["over_90_cents"] += int64(it.Over90)
	}
	return out, nil
}

// CreateStatementRun generates one batch run: every customer with a non-zero
// balance (or exactly the customers named), each with opening balance,
// invoices, payments, finance charges, closing balance and the aging buckets
// of the period's last day, recorded as the run's items. Nothing sends here:
// queue_email marks the positive balances QUEUED and writes each one's
// statement.queued event, the record a later sender works from.
func (s *Service) CreateStatementRun(ctx context.Context, in StatementRunIn) (*StatementRun, error) {
	if in.Role != "" && !FinanceRole(in.Role) {
		return nil, httpx.Forbidden("generating a statement run needs the admin, owner or finance role")
	}
	today, err := s.todayInDefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	if in.PeriodEnd.IsZero() {
		in.PeriodEnd = today
	}
	if in.PeriodStart.IsZero() {
		in.PeriodStart = in.PeriodEnd.AddDate(0, -1, 0)
	}
	if date(in.PeriodStart) > date(in.PeriodEnd) {
		return nil, invalidField("period_start", "must not be after period_end")
	}
	if date(in.PeriodEnd) > date(today) {
		return nil, invalidField("period_end", fmt.Sprintf("has not come yet: a statement period ends no later than today, %s", date(today)))
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range in.CustomerIDs {
		if seen[id] {
			return nil, invalidField("customer_ids", "a customer appears once in a request")
		}
		seen[id] = true
	}

	run := &StatementRun{}
	err = s.inTx(ctx, func(ctx context.Context) error {
		id := uuid.New()
		if _, err := s.ex(ctx).Exec(ctx, `
			INSERT INTO ar_statement_runs (id, period_start, period_end, as_of_date, status, triggered_by)
			VALUES ($1, $2::date, $3::date, $3::date, 'RUNNING', NULLIF($4, ''))`,
			id, date(in.PeriodStart), date(in.PeriodEnd), in.Actor); err != nil {
			return fmt.Errorf("failed to open the statement run: %w", err)
		}
		// The customers the run covers: the named ones, else every account
		// carrying a balance. Both arms are whole book: a run is a finance
		// act, not a branch report.
		type target struct {
			id       uuid.UUID
			name     string
			currency string
		}
		readTargets := func(sql string, args ...any) ([]target, error) {
			rows, err := s.ex(ctx).Query(ctx, sql, args...)
			if err != nil {
				return nil, fmt.Errorf("failed to read the run's customers: %w", err)
			}
			defer rows.Close()
			var out []target
			for rows.Next() {
				var t target
				if err := rows.Scan(&t.id, &t.name, &t.currency); err != nil {
					return nil, fmt.Errorf("failed to scan a customer: %w", err)
				}
				out = append(out, t)
			}
			return out, rows.Err()
		}
		var targets []target
		if len(in.CustomerIDs) > 0 {
			var err error
			targets, err = readTargets(`
				SELECT c.id, c.name, COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')
				FROM customers c WHERE c.id = ANY($1)`, in.CustomerIDs)
			if err != nil {
				return err
			}
			if len(targets) != len(in.CustomerIDs) {
				return invalidField("customer_ids", "names a customer that does not exist")
			}
		} else {
			var err error
			targets, err = readTargets(`
				SELECT c.id, c.name, COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')
				FROM customers c WHERE ROUND(c.balance_due * 100)::bigint <> 0`)
			if err != nil {
				return err
			}
		}
		sort.Slice(targets, func(a, b int) bool {
			if targets[a].name != targets[b].name {
				return targets[a].name < targets[b].name
			}
			return targets[a].id.String() < targets[b].id.String()
		})

		events := []outbox.Event{}
		var totalBalance int64
		queued := 0
		items := []StatementRunItem{}
		for _, t := range targets {
			f, err := s.statementFigures(ctx, t.id, in.PeriodStart, in.PeriodEnd)
			if err != nil {
				return err
			}
			buckets, err := s.agingBucketsOf(ctx, t.id, in.PeriodEnd)
			if err != nil {
				return err
			}
			bucketsJSON, err := json.Marshal(buckets)
			if err != nil {
				return fmt.Errorf("failed to record the aging buckets: %w", err)
			}
			delivery := "NOT_QUEUED"
			var eventID *uuid.UUID
			if in.QueueEmail {
				if f.Closing > 0 {
					delivery = "QUEUED"
					evID := uuid.New()
					eventID = &evID
					queued++
					events = append(events, outbox.Event{ID: evID, Type: "statement.queued", EntityType: "statement_run",
						EntityID: id, Data: json.RawMessage(fmt.Sprintf(
							`{"customer_id":%q,"closing_balance_cents":%d,"period_start":%q,"period_end":%q}`,
							t.id, f.Closing, date(in.PeriodStart), date(in.PeriodEnd)))})
				} else {
					delivery = "SKIPPED" // nothing is owed: nobody wants a statement saying so
				}
			}
			itemID := uuid.New()
			if _, err := s.ex(ctx).Exec(ctx, `
				INSERT INTO ar_statement_run_items (id, run_id, customer_id, currency, opening_balance_cents, invoices_cents,
					payments_cents, finance_charges_cents, closing_balance_cents, delivery_status, outbox_id, aging_buckets)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb)`,
				itemID, id, t.id, t.currency, f.Opening, f.Invoices, f.Payments, f.FinanceCharges, f.Closing,
				delivery, eventID, string(bucketsJSON)); err != nil {
				return fmt.Errorf("failed to record the statement item: %w", err)
			}
			totalBalance += f.Closing
			items = append(items, StatementRunItem{ID: itemID, RunID: id, CustomerID: t.id, CustomerName: t.name,
				Currency: t.currency, OpeningBalanceCents: httpx.Cents(f.Opening), InvoicesCents: httpx.Cents(f.Invoices),
				PaymentsCents: httpx.Cents(f.Payments), FinanceChargesCents: httpx.Cents(f.FinanceCharges),
				ClosingBalanceCents: httpx.Cents(f.Closing), DeliveryStatus: lower(delivery), OutboxID: eventID,
				AgingBuckets: buckets})
		}
		if _, err := s.ex(ctx).Exec(ctx, `
			UPDATE ar_statement_runs SET status = 'COMPLETED', total_customers = $2, queued_count = $3,
				total_balance_cents = $4, completed_at = NOW()
			WHERE id = $1`, id, len(targets), queued, totalBalance); err != nil {
			return fmt.Errorf("failed to close the statement run: %w", err)
		}
		if err := s.RecordEvents(ctx, events); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "statement_run.completed", EntityType: "ar_statement_run",
				EntityID: id, UserID: in.Actor, Changes: map[string]interface{}{"period_start": date(in.PeriodStart),
					"period_end": date(in.PeriodEnd), "total_customers": len(targets), "queued_count": queued,
					"total_balance_cents": totalBalance}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		run.ID = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Read the finished run back through the same columns the list answers.
	out, err := s.GetStatementRun(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// StatementRunFilter is the run list's keyset position.
type StatementRunFilter struct {
	AfterTime *time.Time
	AfterID   uuid.UUID
	Limit     int
}

// ListStatementRuns answers one page of runs, newest first by (created_at, id).
func (s *Service) ListStatementRuns(ctx context.Context, f StatementRunFilter) ([]StatementRun, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT `+statementRunColumns+`
		FROM ar_statement_runs r
		WHERE ($2::timestamptz IS NULL OR (r.created_at, r.id) < ($2, $3::uuid))
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $1`, f.Limit+1, f.AfterTime, f.AfterID)
	if err != nil {
		return nil, fmt.Errorf("failed to list the statement runs: %w", err)
	}
	defer rows.Close()
	out := []StatementRun{}
	for rows.Next() {
		var r StatementRun
		if err := scanStatementRun(rows, &r); err != nil {
			return nil, fmt.Errorf("failed to scan a statement run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountStatementRuns is the size of the run list.
func (s *Service) CountStatementRuns(ctx context.Context) (int64, error) {
	var n int64
	if err := s.ex(ctx).QueryRow(ctx, `SELECT count(*) FROM ar_statement_runs`).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count the statement runs: %w", err)
	}
	return n, nil
}

// GetStatementRun reads one run with its items, customers by name. A run of
// another id is missing, as ever.
func (s *Service) GetStatementRun(ctx context.Context, id uuid.UUID) (*StatementRun, error) {
	run := &StatementRun{}
	err := scanStatementRun(s.ex(ctx).QueryRow(ctx,
		`SELECT `+statementRunColumns+` FROM ar_statement_runs r WHERE r.id = $1`, id), run)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("statement run not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the statement run: %w", err)
	}
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT `+statementRunItemColumns+`
		FROM ar_statement_run_items i JOIN customers c ON c.id = i.customer_id
		WHERE i.run_id = $1
		ORDER BY c.name, i.customer_id, i.id`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to read the statement items: %w", err)
	}
	defer rows.Close()
	run.Items = []StatementRunItem{}
	for rows.Next() {
		var it StatementRunItem
		if err := scanStatementRunItem(rows, &it); err != nil {
			return nil, fmt.Errorf("failed to scan a statement item: %w", err)
		}
		run.Items = append(run.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return run, nil
}
