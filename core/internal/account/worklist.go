// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The controller's AR worklist (AR-08): overdue and over-limit accounts,
// worst first, with the aging, the credit utilisation, the hold state and the
// action the figure asks for. The severity score is the thing that ranks the
// phone calls; it weighs the deep buckets harder than the shallow ones,
// because an account whose money is ninety days gone is worth more of the
// controller's morning than one thirty days late, whatever the sums.

// WorklistRow is one account on the worklist.
type WorklistRow struct {
	CustomerID     uuid.UUID   `json:"customer_id"`
	CustomerName   string      `json:"customer_name"`
	Currency       string      `json:"currency"`
	BalanceCents   httpx.Cents `json:"balance_cents"`
	CurrentCents   httpx.Cents `json:"current_cents"`
	Days1To30      httpx.Cents `json:"days_1_30_cents"`
	Days31To60     httpx.Cents `json:"days_31_60_cents"`
	Days61To90     httpx.Cents `json:"days_61_90_cents"`
	Over90         httpx.Cents `json:"over_90_cents"`
	PastDueCents   httpx.Cents `json:"past_due_cents"`
	OverLimitCents httpx.Cents `json:"over_limit_cents"`
	// CreditLimitCents is null for an account with no limit: such an account
	// has no utilisation and cannot go over.
	CreditLimitCents *httpx.Cents `json:"credit_limit_cents"`
	// UtilizationPct is the balance over the limit, rounded, null for no
	// limit; past 100 the account is over.
	UtilizationPct    *int64           `json:"credit_utilization_pct"`
	HoldStatus        string           `json:"hold_status"` // none | active | overridden
	HoldID            string           `json:"hold_id,omitempty"`
	OverrideUntil     *httpx.Timestamp `json:"override_until,omitempty"`
	SeverityScore     int64            `json:"severity_score"`
	RecommendedAction string           `json:"recommended_action"` // hold | call | send_statement | finance_charge
}

// Worklist is GET /api/v1/ar/worklist.
type Worklist struct {
	AsOf string        `json:"as_of"`
	Rows []WorklistRow `json:"rows"`
}

// holdScoreBump is what a standing hold adds to the severity score: enough
// that a held account outranks any plain overdue one, whatever its buckets.
const holdScoreBump int64 = 10_000_00

// severityScore is the whole ranking, a pure function so the order the
// controller sees can be argued with without a database: four times the over
// 90 bucket, twice the 61 to 90 bucket, once the 31 to 60 bucket, once the
// over-limit amount, and the hold bump on top.
func severityScore(r WorklistRow) int64 {
	score := 4*int64(r.Over90) + 2*int64(r.Days61To90) + int64(r.Days31To60) + int64(r.OverLimitCents)
	if r.HoldStatus != "none" {
		score += holdScoreBump
	}
	return score
}

// recommendedAction is the ladder the buckets climb: a held, over limit or
// 90 day account is held or held again; 61 day money gets the phone; 31 day
// money is what the finance charge run is for; anything else with a balance
// gets a statement.
func recommendedAction(r WorklistRow) string {
	switch {
	case r.HoldStatus != "none" || r.OverLimitCents > 0 || r.Over90 > 0:
		return "hold"
	case r.Days61To90 > 0:
		return "finance_charge"
	case r.Days31To60 > 0:
		return "call"
	default:
		return "send_statement"
	}
}

// Worklist ranks the accounts the aging and the balances name. The aging
// carries the caller's wall; the balance and the limit are the account's own
// whole figures, as the account summary reads them.
func (s *Service) Worklist(ctx context.Context, asOf time.Time, limit int) (*Worklist, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	items, err := s.Aging(ctx, AgingQuery{GroupBy: "customer", Basis: "due_date", AsOf: asOf})
	if err != nil {
		return nil, err
	}
	byCustomer := map[uuid.UUID]*WorklistRow{}
	var order []uuid.UUID
	for _, it := range items {
		row := byCustomer[it.CustomerID]
		if row == nil {
			row = &WorklistRow{CustomerID: it.CustomerID, CustomerName: it.CustomerName}
			byCustomer[it.CustomerID] = row
			order = append(order, it.CustomerID)
		}
		row.CurrentCents += it.CurrentCents
		row.Days1To30 += it.Days1To30
		row.Days31To60 += it.Days31To60
		row.Days61To90 += it.Days61To90
		row.Over90 += it.Over90
		row.PastDueCents += it.Days1To30 + it.Days31To60 + it.Days61To90 + it.Over90
	}
	// The accounts' whole figures and their holds, in two queries.
	if len(order) > 0 {
		rows, err := s.ex(ctx).Query(ctx, `
			SELECT c.id, COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD'),
			       ROUND(c.balance_due * 100)::bigint, COALESCE(ROUND(c.credit_limit * 100)::bigint, 0)
			FROM customers c WHERE c.id = ANY($1)`, order)
		if err != nil {
			return nil, fmt.Errorf("failed to read the accounts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var cur string
			var balance, limitCents int64
			if err := rows.Scan(&id, &cur, &balance, &limitCents); err != nil {
				return nil, fmt.Errorf("failed to scan an account: %w", err)
			}
			if row := byCustomer[id]; row != nil {
				row.Currency, row.BalanceCents = cur, httpx.Cents(balance)
				if limitCents > 0 {
					l := httpx.Cents(limitCents)
					row.CreditLimitCents = &l
					over := balance - limitCents
					if over < 0 {
						over = 0
					}
					row.OverLimitCents = httpx.Cents(over)
					row.UtilizationPct = new(int64)
					*row.UtilizationPct = balance * 100 / limitCents
				}
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		holds, err := s.ex(ctx).Query(ctx, `
			SELECT DISTINCT ON (h.customer_id) h.customer_id, h.id, h.status, h.override_until
			FROM credit_holds h
			WHERE h.status IN `+standingHoldStates+` AND h.customer_id = ANY($1)
			ORDER BY h.customer_id, h.placed_at DESC`, order)
		if err != nil {
			return nil, fmt.Errorf("failed to read the holds: %w", err)
		}
		defer holds.Close()
		for holds.Next() {
			var id uuid.UUID
			var holdID uuid.UUID
			var status string
			var overrideUntil *time.Time
			if err := holds.Scan(&id, &holdID, &status, &overrideUntil); err != nil {
				return nil, fmt.Errorf("failed to scan a hold: %w", err)
			}
			if row := byCustomer[id]; row != nil {
				row.HoldStatus, row.HoldID = lower(status), holdID.String()
				row.OverrideUntil = httpx.PtrTimestamp(overrideUntil)
			}
		}
		if err := holds.Err(); err != nil {
			return nil, err
		}
	}
	out := &Worklist{AsOf: date(asOf), Rows: []WorklistRow{}}
	for _, id := range order {
		row := byCustomer[id]
		// A past-due bucket of exactly zero and a zero balance are nobody's
		// phone call: the worklist names work, not accounts.
		if row.PastDueCents == 0 && row.BalanceCents == 0 {
			continue
		}
		row.SeverityScore = severityScore(*row)
		row.RecommendedAction = recommendedAction(*row)
		out.Rows = append(out.Rows, *row)
	}
	sort.SliceStable(out.Rows, func(a, b int) bool {
		x, y := &out.Rows[a], &out.Rows[b]
		if x.SeverityScore != y.SeverityScore {
			return x.SeverityScore > y.SeverityScore
		}
		if x.CustomerName != y.CustomerName {
			return x.CustomerName < y.CustomerName
		}
		return x.CustomerID.String() < y.CustomerID.String()
	})
	if len(out.Rows) > limit {
		out.Rows = out.Rows[:limit]
	}
	return out, nil
}
