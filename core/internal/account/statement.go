// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StatementLine is one subledger row of a statement.
type StatementLine struct {
	ID                uuid.UUID       `json:"id"`
	Date              string          `json:"date"`
	Type              TransactionType `json:"type"`
	Description       string          `json:"description"`
	AmountCents       httpx.Cents     `json:"amount_cents"`
	BalanceAfterCents httpx.Cents     `json:"balance_after_cents"`
	SourceKind        *string         `json:"source_kind"`
	ReferenceID       *uuid.UUID      `json:"reference_id"`
	JobID             *uuid.UUID      `json:"job_id"`
}

// OpenDocument is an invoice with an open amount or a credit memo with open
// credit (negative).
type OpenDocument struct {
	Kind       string      `json:"kind"` // invoice | credit_memo
	ID         uuid.UUID   `json:"id"`
	Number     string      `json:"number"`
	Date       string      `json:"date"`
	DueDate    *string     `json:"due_date"`
	JobID      *uuid.UUID  `json:"job_id"`
	TotalCents httpx.Cents `json:"total_cents"`
	OpenCents  httpx.Cents `json:"open_cents"`
}

// StatementCurrency is the statement of one currency.
type StatementCurrency struct {
	Currency            string          `json:"currency"`
	OpeningBalanceCents httpx.Cents     `json:"opening_balance_cents"`
	Lines               []StatementLine `json:"lines"`
	ClosingBalanceCents httpx.Cents     `json:"closing_balance_cents"`
	OpenDocuments       []OpenDocument  `json:"open_documents"`
}

// Statement is GET /api/v1/ar/customers/{id}/statement.
type Statement struct {
	CustomerID   uuid.UUID           `json:"customer_id"`
	CustomerName string              `json:"customer_name"`
	From         string              `json:"from"`
	To           string              `json:"to"`
	JobID        *uuid.UUID          `json:"job_id"`
	Currencies   []StatementCurrency `json:"currencies"`
}

// StatementQuery is the statement parameters; From and To are business dates in
// the customer's primary branch time zone.
type StatementQuery struct {
	CustomerID uuid.UUID
	From, To   time.Time
	JobID      *uuid.UUID
}

const statementRowsSQL = `
SELECT t.id, (t.created_at AT TIME ZONE tz.name)::date, t.type, COALESCE(t.description, ''), t.amount, t.balance_after, t.currency,
       t.source_kind, t.reference_id,
       COALESCE(inv.project_id, cm.project_id, ai.project_id, rcm.project_id, legacy.project_id) AS job
FROM customer_transactions t
CROSS JOIN (SELECT COALESCE((SELECT l.timezone FROM locations l JOIN customers c ON c.primary_branch_id = l.id WHERE c.id = $1), 'UTC') AS name) tz
LEFT JOIN invoices inv ON t.source_kind = 'invoice' AND inv.id = t.reference_id
LEFT JOIN credit_memos cm ON t.source_kind = 'credit_memo' AND cm.id = t.reference_id
LEFT JOIN ar_applications ap ON t.source_kind = 'application' AND ap.id = t.reference_id
LEFT JOIN invoices ai ON ai.id = ap.invoice_id
LEFT JOIN payment_refunds rf ON t.source_kind = 'refund' AND rf.id = t.reference_id
LEFT JOIN credit_memos rcm ON rcm.id = rf.credit_memo_id
LEFT JOIN invoices legacy ON t.source_kind IS NULL AND legacy.id = t.reference_id
WHERE t.customer_id = $1
ORDER BY t.currency, t.created_at, t.id`

// Statement answers the customer's statement: per currency the opening balance
// (the rows before From), every row in range (filtered to the job through the
// source document when given), the closing balance, and the open documents.
func (s *Service) Statement(ctx context.Context, q StatementQuery) (*Statement, error) {
	out := &Statement{CustomerID: q.CustomerID, From: date(q.From), To: date(q.To), JobID: q.JobID, Currencies: []StatementCurrency{}}
	err := s.ex(ctx).QueryRow(ctx, `SELECT name FROM customers WHERE id = $1`, q.CustomerID).Scan(&out.CustomerName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("account not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the customer: %w", err)
	}
	rows, err := s.ex(ctx).Query(ctx, statementRowsSQL, q.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the statement: %w", err)
	}
	byCur := map[string]*StatementCurrency{}
	var order []string
	for rows.Next() {
		var l StatementLine
		var day time.Time
		var typ, cur string
		var amt, after int64
		if err := rows.Scan(&l.ID, &day, &typ, &l.Description, &amt, &after, &cur, &l.SourceKind, &l.ReferenceID, &l.JobID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan a statement row: %w", err)
		}
		if q.JobID != nil && (l.JobID == nil || *l.JobID != *q.JobID) {
			continue
		}
		sc := byCur[cur]
		if sc == nil {
			sc = &StatementCurrency{Currency: cur, Lines: []StatementLine{}, OpenDocuments: []OpenDocument{}}
			byCur[cur] = sc
			order = append(order, cur)
		}
		l.Date, l.Type, l.AmountCents = date(day), TransactionType(typ), httpx.Cents(amt)
		switch {
		case day.Before(q.From):
			sc.OpeningBalanceCents += httpx.Cents(amt)
		case !day.After(q.To):
			sc.Lines = append(sc.Lines, l)
		default:
			continue
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, c := range order {
		sc := byCur[c]
		running := sc.OpeningBalanceCents
		for i := range sc.Lines {
			running += sc.Lines[i].AmountCents
			sc.Lines[i].BalanceAfterCents = running
		}
		sc.ClosingBalanceCents = running
	}
	docs, err := s.ex(ctx).Query(ctx, `
		SELECT 'invoice', i.id, i.number, i.currency, to_char(i.invoice_date, 'YYYY-MM-DD'), to_char(i.due_date, 'YYYY-MM-DD'), i.project_id,
		       ROUND(i.total_amount * 100)::bigint, ROUND(i.amount_open * 100)::bigint
		FROM invoices i WHERE i.customer_id = $1 AND i.status IN ('UNPAID', 'PARTIAL') AND i.amount_open > 0
		  AND ($2::uuid IS NULL OR i.project_id = $2) AND `+wall("i", 3, 4)+`
		UNION ALL
		SELECT 'credit_memo', m.id, m.number, m.currency, to_char(m.memo_date, 'YYYY-MM-DD'), NULL, m.project_id,
		       ROUND(m.total_amount * 100)::bigint, ROUND(m.amount_open * 100)::bigint
		FROM credit_memos m WHERE m.customer_id = $1 AND m.status IN ('OPEN', 'PARTIAL') AND m.amount_open < 0
		  AND ($2::uuid IS NULL OR m.project_id = $2) AND `+wall("m", 3, 4)+`
		ORDER BY 5, 3`, q.CustomerID, q.JobID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to read the open documents: %w", err)
	}
	for docs.Next() {
		var d OpenDocument
		var cur string
		var total, open int64
		if err := docs.Scan(&d.Kind, &d.ID, &d.Number, &cur, &d.Date, &d.DueDate, &d.JobID, &total, &open); err != nil {
			docs.Close()
			return nil, fmt.Errorf("failed to scan an open document: %w", err)
		}
		d.TotalCents, d.OpenCents = httpx.Cents(total), httpx.Cents(open)
		sc := byCur[cur]
		if sc == nil {
			sc = &StatementCurrency{Currency: cur, Lines: []StatementLine{}, OpenDocuments: []OpenDocument{}}
			byCur[cur] = sc
			order = append(order, cur)
		}
		sc.OpenDocuments = append(sc.OpenDocuments, d)
	}
	docs.Close()
	if err := docs.Err(); err != nil {
		return nil, err
	}
	sort.Strings(order)
	for _, c := range order {
		out.Currencies = append(out.Currencies, *byCur[c])
	}
	return out, nil
}

// DriftRow is one customer whose subledger, balance and documents disagree.
type DriftRow struct {
	CustomerID     uuid.UUID   `json:"customer_id"`
	CustomerName   string      `json:"customer_name"`
	Currency       string      `json:"currency"`
	BalanceCents   httpx.Cents `json:"balance_cents"`
	SubledgerCents httpx.Cents `json:"subledger_cents"`
	DocumentsCents httpx.Cents `json:"documents_cents"`
}

// LedgerRow is the ledger side of one currency: the balance of 1020 against the
// sum of the customers' balances, and the balance of 2200 against the sum of
// unapplied cash.
type LedgerRow struct {
	Currency         string      `json:"currency"`
	ReceivableLedger httpx.Cents `json:"receivable_ledger_cents"`
	BalanceSum       httpx.Cents `json:"balance_sum_cents"`
	DepositsLedger   httpx.Cents `json:"deposits_ledger_cents"`
	UnappliedSum     httpx.Cents `json:"unapplied_sum_cents"`
	ReceivableInSync bool        `json:"receivable_in_sync"`
	DepositsInSync   bool        `json:"deposits_in_sync"`
}

// Reconciliation is GET /api/v1/ar/reconciliation.
type Reconciliation struct {
	Customers  []DriftRow  `json:"customers"`
	Currencies []LedgerRow `json:"currencies"`
}

// Reconcile reports every customer whose customers.balance_due, subledger sum
// and document open amounts disagree, and for each currency whether the sum of
// balances equals account 1020 and the unapplied cash equals account 2200.
// Nothing repairs a difference: rows written before cycle 2 show here.
func (s *Service) Reconcile(ctx context.Context) (*Reconciliation, error) {
	out := &Reconciliation{Customers: []DriftRow{}, Currencies: []LedgerRow{}}
	rows, err := s.ex(ctx).Query(ctx, `
		WITH sub AS (
			SELECT customer_id, currency, SUM(amount) AS cents FROM customer_transactions GROUP BY customer_id, currency
		), docs AS (
			SELECT customer_id, currency, SUM(cents) AS cents FROM (
				SELECT customer_id, currency, ROUND(amount_open * 100)::bigint AS cents FROM invoices WHERE status <> 'VOID'
				UNION ALL
				SELECT customer_id, currency, ROUND(amount_open * 100)::bigint FROM credit_memos WHERE status NOT IN ('DRAFT', 'VOID')
			) d GROUP BY customer_id, currency
		), keys AS (
			SELECT customer_id, currency FROM sub UNION SELECT customer_id, currency FROM docs
		)
		SELECT c.id, c.name, k.currency, ROUND(c.balance_due * 100)::bigint, COALESCE(sub.cents, 0), COALESCE(docs.cents, 0)
		FROM keys k JOIN customers c ON c.id = k.customer_id
		LEFT JOIN sub ON sub.customer_id = k.customer_id AND sub.currency = k.currency
		LEFT JOIN docs ON docs.customer_id = k.customer_id AND docs.currency = k.currency
		WHERE ROUND(c.balance_due * 100)::bigint <> COALESCE(sub.cents, 0) OR COALESCE(sub.cents, 0) <> COALESCE(docs.cents, 0)
		ORDER BY c.name, c.id, k.currency`)
	if err != nil {
		return nil, fmt.Errorf("failed to reconcile the customers: %w", err)
	}
	for rows.Next() {
		var d DriftRow
		var bal, sub, docs int64
		if err := rows.Scan(&d.CustomerID, &d.CustomerName, &d.Currency, &bal, &sub, &docs); err != nil {
			rows.Close()
			return nil, err
		}
		d.BalanceCents, d.SubledgerCents, d.DocumentsCents = httpx.Cents(bal), httpx.Cents(sub), httpx.Cents(docs)
		out.Customers = append(out.Customers, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	led, err := s.ex(ctx).Query(ctx, `
		WITH ar AS (
			SELECT e.currency, SUM(l.debit - l.credit) AS cents FROM gl_journal_lines l
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.status = 'POSTED'
			JOIN gl_accounts a ON a.id = l.account_id AND a.code = '1020' GROUP BY e.currency
		), dep AS (
			SELECT e.currency, SUM(l.credit - l.debit) AS cents FROM gl_journal_lines l
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.status = 'POSTED'
			JOIN gl_accounts a ON a.id = l.account_id AND a.code = '2200' GROUP BY e.currency
		), bal AS (
			SELECT COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD') AS currency,
			       SUM(ROUND(c.balance_due * 100)::bigint) AS cents FROM customers c GROUP BY 1
		), un AS (
			SELECT p.currency, SUM(ROUND(p.amount_unapplied * 100)::bigint) AS cents FROM payments p WHERE p.status = 'POSTED' GROUP BY p.currency
		), cur AS (
			SELECT currency FROM ar UNION SELECT currency FROM dep UNION SELECT currency FROM bal UNION SELECT currency FROM un
		)
		SELECT cur.currency, COALESCE(ar.cents, 0), COALESCE(bal.cents, 0), COALESCE(dep.cents, 0), COALESCE(un.cents, 0)
		FROM cur LEFT JOIN ar USING (currency) LEFT JOIN bal USING (currency) LEFT JOIN dep USING (currency) LEFT JOIN un USING (currency)
		ORDER BY cur.currency`)
	if err != nil {
		return nil, fmt.Errorf("failed to read the ledger: %w", err)
	}
	defer led.Close()
	for led.Next() {
		var r LedgerRow
		var ar, bal, dep, un int64
		if err := led.Scan(&r.Currency, &ar, &bal, &dep, &un); err != nil {
			return nil, err
		}
		r.ReceivableLedger, r.BalanceSum, r.DepositsLedger, r.UnappliedSum = httpx.Cents(ar), httpx.Cents(bal), httpx.Cents(dep), httpx.Cents(un)
		r.ReceivableInSync, r.DepositsInSync = ar == bal, dep == un
		out.Currencies = append(out.Currencies, r)
	}
	return out, led.Err()
}
