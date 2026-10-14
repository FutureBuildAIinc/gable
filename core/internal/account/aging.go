// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// AgingItem is one row of the receivable aging (ADR 0005 section 10): one group
// in one currency. The fields of the coarser groupings are null.
type AgingItem struct {
	CustomerID     uuid.UUID   `json:"customer_id"`
	CustomerName   string      `json:"customer_name"`
	JobID          *uuid.UUID  `json:"job_id"`
	JobName        *string     `json:"job_name"`
	ShipToID       *uuid.UUID  `json:"ship_to_id"`
	ShipToCode     *string     `json:"ship_to_code"`
	Currency       string      `json:"currency"`
	CurrentCents   httpx.Cents `json:"current_cents"`
	Days1To30      httpx.Cents `json:"days_1_30_cents"`
	Days31To60     httpx.Cents `json:"days_31_60_cents"`
	Days61To90     httpx.Cents `json:"days_61_90_cents"`
	Over90         httpx.Cents `json:"over_90_cents"`
	UnappliedCents httpx.Cents `json:"unapplied_cents"`
	TotalCents     httpx.Cents `json:"total_cents"`
}

// GroupID is the group's own id for ordering: the job or the ship-to, else empty.
func (a *AgingItem) GroupID() string {
	switch {
	case a.JobID != nil:
		return a.JobID.String()
	case a.ShipToID != nil:
		return a.ShipToID.String()
	}
	return ""
}

// AgingQuery is the aging parameters.
type AgingQuery struct {
	GroupBy    string // customer | job | ship_to
	AsOf       time.Time
	Basis      string // due_date | invoice_date
	CustomerID *uuid.UUID
}

// agingSQL takes: $1 as_of, $2 basis, $3 group_by, $4 customer, $5 branch, $6 grants.
// Every amount is taken as of $1: an application counts when applied on or
// before it and not reversed by it; an invoice or credit memo voided after it
// counted then.
var agingSQL = `
WITH docs AS (
	SELECT i.customer_id, i.currency, i.project_id, i.ship_to_id, 0 AS kind,
	       ROUND(i.total_amount * 100)::bigint - COALESCE((SELECT SUM(ROUND(a.amount * 100)::bigint) FROM ar_applications a
	            WHERE a.invoice_id = i.id AND a.applied_on <= $1::date AND (a.reversed_on IS NULL OR a.reversed_on > $1::date)), 0) AS amt,
	       ($1::date - CASE WHEN $2 = 'invoice_date' THEN i.invoice_date ELSE COALESCE(i.due_date, i.invoice_date) END) AS days
	FROM invoices i
	WHERE i.invoice_date <= $1::date AND (i.voided_on IS NULL OR i.voided_on > $1::date)
	  AND ($4::uuid IS NULL OR i.customer_id = $4) AND ` + wall("i", 5, 6) + `
	UNION ALL
	SELECT m.customer_id, m.currency, m.project_id, m.ship_to_id, 1,
	       ROUND(m.total_amount * 100)::bigint
	       + COALESCE((SELECT SUM(ROUND(a.amount * 100)::bigint) FROM ar_applications a
	            WHERE a.credit_memo_id = m.id AND a.applied_on <= $1::date AND (a.reversed_on IS NULL OR a.reversed_on > $1::date)), 0)
	       + COALESCE((SELECT SUM(ROUND(f.amount * 100)::bigint) FROM payment_refunds f WHERE f.credit_memo_id = m.id AND f.refunded_on <= $1::date), 0),
	       0
	FROM credit_memos m
	WHERE m.number IS NOT NULL AND m.memo_date <= $1::date AND (m.voided_on IS NULL OR m.voided_on > $1::date)
	  AND ($4::uuid IS NULL OR m.customer_id = $4) AND ` + wall("m", 5, 6) + `
	UNION ALL
	SELECT p.customer_id, p.currency, p.project_id, NULL::uuid, 2,
	       -GREATEST(0, ROUND(p.amount * 100)::bigint
	       - COALESCE((SELECT SUM(ROUND(a.amount * 100)::bigint) FROM ar_applications a
	            WHERE a.payment_id = p.id AND a.kind = 'PAYMENT' AND a.applied_on <= $1::date AND (a.reversed_on IS NULL OR a.reversed_on > $1::date)), 0)
	       - COALESCE((SELECT SUM(ROUND(f.amount * 100)::bigint) FROM payment_refunds f WHERE f.payment_id = p.id AND f.refunded_on <= $1::date), 0)
	       - ROUND(p.migrated_excess * 100)::bigint),
	       0
	FROM payments p
	WHERE p.received_on <= $1::date AND (p.voided_on IS NULL OR p.voided_on > $1::date)
	  AND ($4::uuid IS NULL OR p.customer_id = $4) AND ` + wall("p", 5, 6) + `
)
SELECT d.customer_id, c.name, d.currency,
       CASE WHEN $3 = 'job' THEN d.project_id END AS job_id,
       CASE WHEN $3 = 'ship_to' THEN d.ship_to_id END AS ship_to_id,
       SUM(CASE WHEN d.kind = 0 AND d.days <= 0 THEN d.amt ELSE 0 END),
       SUM(CASE WHEN d.kind = 0 AND d.days BETWEEN 1 AND 30 THEN d.amt ELSE 0 END),
       SUM(CASE WHEN d.kind = 0 AND d.days BETWEEN 31 AND 60 THEN d.amt ELSE 0 END),
       SUM(CASE WHEN d.kind = 0 AND d.days BETWEEN 61 AND 90 THEN d.amt ELSE 0 END),
       SUM(CASE WHEN d.kind = 0 AND d.days > 90 THEN d.amt ELSE 0 END),
       SUM(CASE WHEN d.kind > 0 THEN d.amt ELSE 0 END)
FROM docs d JOIN customers c ON c.id = d.customer_id
WHERE d.amt <> 0
GROUP BY d.customer_id, c.name, d.currency, 4, 5`

// Aging answers every group of the aging, ordered by (customer name, customer
// id, group id, currency): the caller pages through it with the keyset.
func (s *Service) Aging(ctx context.Context, q AgingQuery) ([]AgingItem, error) {
	rows, err := s.ex(ctx).Query(ctx, agingSQL, date(q.AsOf), q.Basis, q.GroupBy, q.CustomerID,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to age the receivable: %w", err)
	}
	var out []AgingItem
	for rows.Next() {
		var it AgingItem
		var cur, d1, d2, d3, d4, un int64
		if err := rows.Scan(&it.CustomerID, &it.CustomerName, &it.Currency, &it.JobID, &it.ShipToID, &cur, &d1, &d2, &d3, &d4, &un); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan an aging row: %w", err)
		}
		it.CurrentCents, it.Days1To30, it.Days31To60, it.Days61To90, it.Over90, it.UnappliedCents =
			httpx.Cents(cur), httpx.Cents(d1), httpx.Cents(d2), httpx.Cents(d3), httpx.Cents(d4), httpx.Cents(un)
		it.TotalCents = httpx.Cents(cur + d1 + d2 + d3 + d4 + un)
		out = append(out, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		it := &out[i]
		if it.JobID != nil {
			var name string
			if err := s.ex(ctx).QueryRow(ctx, `SELECT name FROM projects WHERE id = $1`, *it.JobID).Scan(&name); err == nil {
				it.JobName = &name
			}
		}
		if it.ShipToID != nil {
			var code string
			if err := s.ex(ctx).QueryRow(ctx, `SELECT code FROM customer_ship_tos WHERE id = $1`, *it.ShipToID).Scan(&code); err == nil {
				it.ShipToCode = &code
			}
		}
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := &out[a], &out[b]
		if x.CustomerName != y.CustomerName {
			return x.CustomerName < y.CustomerName
		}
		if x.CustomerID != y.CustomerID {
			return x.CustomerID.String() < y.CustomerID.String()
		}
		if gx, gy := x.GroupID(), y.GroupID(); gx != gy {
			return gx < gy
		}
		return x.Currency < y.Currency
	})
	return out, nil
}

// AgingTotal is the bucket totals of one currency.
type AgingTotal struct {
	Currency       string      `json:"currency"`
	CurrentCents   httpx.Cents `json:"current_cents"`
	Days1To30      httpx.Cents `json:"days_1_30_cents"`
	Days31To60     httpx.Cents `json:"days_31_60_cents"`
	Days61To90     httpx.Cents `json:"days_61_90_cents"`
	Over90         httpx.Cents `json:"over_90_cents"`
	UnappliedCents httpx.Cents `json:"unapplied_cents"`
	TotalCents     httpx.Cents `json:"total_cents"`
}

// AgingSummary is GET /api/v1/ar/aging/summary.
type AgingSummary struct {
	AsOf   string       `json:"as_of"`
	Basis  string       `json:"basis"`
	Totals []AgingTotal `json:"totals"`
}

// AgingSummary totals the aging per currency (never across two).
func (s *Service) AgingSummary(ctx context.Context, q AgingQuery) (*AgingSummary, error) {
	q.GroupBy = "customer"
	items, err := s.Aging(ctx, q)
	if err != nil {
		return nil, err
	}
	byCur := map[string]*AgingTotal{}
	var order []string
	for _, it := range items {
		t := byCur[it.Currency]
		if t == nil {
			t = &AgingTotal{Currency: it.Currency}
			byCur[it.Currency] = t
			order = append(order, it.Currency)
		}
		t.CurrentCents += it.CurrentCents
		t.Days1To30 += it.Days1To30
		t.Days31To60 += it.Days31To60
		t.Days61To90 += it.Days61To90
		t.Over90 += it.Over90
		t.UnappliedCents += it.UnappliedCents
		t.TotalCents += it.TotalCents
	}
	sort.Strings(order)
	out := &AgingSummary{AsOf: date(q.AsOf), Basis: q.Basis, Totals: []AgingTotal{}}
	for _, c := range order {
		out.Totals = append(out.Totals, *byCur[c])
	}
	return out, nil
}
