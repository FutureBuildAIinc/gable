// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Credit holds (AR-02, AR-08): the rows the counter and the order paths read
// through customers.credit_hold, and the sweep that keeps them true to the
// books. A hold stands while its status is active or overridden: an override
// is the same hold carrying a time-bounded manager's window, never a
// release, so the record that the account breached its terms survives the
// window.
//
// The sweep is idempotent by construction: it converges the hold set toward
// what the books say it should be, and the one-standing-hold index makes a
// second place a no-op rather than a race. It releases only the holds it
// placed itself (placed_by empty); a hold a person placed is a person's to
// lift, and one carrying a manager's override stands until its window ends.

// HoldType is why a hold stands: the account is past its credit limit, past
// due, or a person placed it by hand.
type HoldType string

const (
	HoldTypeOverLimit HoldType = "OVER_LIMIT"
	HoldTypePastDue   HoldType = "PAST_DUE"
	HoldTypeManual    HoldType = "MANUAL"
)

// CreditHoldStatus is the life of a hold.
type CreditHoldStatus string

const (
	HoldStatusActive     CreditHoldStatus = "ACTIVE"
	HoldStatusReleased   CreditHoldStatus = "RELEASED"
	HoldStatusOverridden CreditHoldStatus = "OVERRIDDEN"
)

// standingHoldStates is the SQL status list of a hold that still stands.
const standingHoldStates = `('ACTIVE', 'OVERRIDDEN')`

// CreditHold is a credit_holds row on the wire.
type CreditHold struct {
	ID             uuid.UUID        `json:"id"`
	CustomerID     uuid.UUID        `json:"customer_id"`
	CustomerName   string           `json:"customer_name"`
	Reason         string           `json:"reason"`
	HoldType       string           `json:"hold_type"`
	Status         string           `json:"status"`
	PlacedBy       *string          `json:"placed_by"`
	PlacedAt       httpx.Timestamp  `json:"placed_at"`
	ReleasedBy     *string          `json:"released_by"`
	ReleasedAt     *httpx.Timestamp `json:"released_at"`
	ReleaseReason  *string          `json:"release_reason"`
	OverrideUntil  *httpx.Timestamp `json:"override_until"`
	OverrideBy     *string          `json:"override_by"`
	OverrideReason *string          `json:"override_reason"`
	MaxOrderCents  *httpx.Cents     `json:"max_order_cents"`
	// Credit is the account's standing beside the hold, on the list read only.
	Credit *CreditStanding `json:"credit,omitempty"`
}

// CreditStanding is the account summary a hold list carries: the limit (null
// for none), the balance, and the open invoice cents past their due date.
type CreditStanding struct {
	CreditLimitCents    *httpx.Cents `json:"credit_limit_cents"`
	CurrentBalanceCents httpx.Cents  `json:"current_balance_cents"`
	PastDueCents        httpx.Cents  `json:"past_due_cents"`
}

// pastDueCentsSQL is the open invoice cents of one customer, aliased c, past
// their due date as of the date parameter numbered day: the aging's
// due-date arithmetic (an application counts when applied by the day and not
// reversed by it; an invoice voided after the day counted then).
func pastDueCentsSQL(day int) string {
	return fmt.Sprintf(`COALESCE((
		SELECT SUM(ROUND(i.total_amount * 100)::bigint - COALESCE((SELECT SUM(ROUND(a.amount * 100)::bigint) FROM ar_applications a
		            WHERE a.invoice_id = i.id AND a.applied_on <= $%[1]d::date AND (a.reversed_on IS NULL OR a.reversed_on > $%[1]d::date)), 0))
		FROM invoices i
		WHERE i.customer_id = c.id AND i.status IN ('UNPAID', 'PARTIAL') AND i.invoice_date <= $%[1]d::date
		  AND COALESCE(i.due_date, i.invoice_date) < $%[1]d::date), 0)`, day)
}

const creditHoldColumns = `h.id, h.customer_id, c.name, h.reason, h.hold_type, h.status, h.placed_by, h.placed_at,
	h.released_by, h.released_at, h.release_reason, h.override_until, h.override_by, h.override_reason,
	h.max_order_cents`

func scanCreditHold(row pgx.Row, h *CreditHold) error {
	var holdType, status string
	var placed time.Time
	var released, overrideUntil *time.Time
	var maxOrder *int64
	if err := row.Scan(&h.ID, &h.CustomerID, &h.CustomerName, &h.Reason, &holdType, &status, &h.PlacedBy, &placed,
		&h.ReleasedBy, &released, &h.ReleaseReason, &overrideUntil, &h.OverrideBy, &h.OverrideReason,
		&maxOrder); err != nil {
		return err
	}
	h.HoldType, h.Status = lower(holdType), lower(status)
	h.PlacedAt = httpx.TimestampOf(placed)
	h.ReleasedAt, h.OverrideUntil = httpx.PtrTimestamp(released), httpx.PtrTimestamp(overrideUntil)
	if maxOrder != nil {
		c := httpx.Cents(*maxOrder)
		h.MaxOrderCents = &c
	}
	return nil
}

// CreditHoldFilter selects the holds a list answers.
type CreditHoldFilter struct {
	Status     string // standing | released | all (default standing)
	CustomerID *uuid.UUID
	AfterTime  *time.Time
	AfterID    uuid.UUID
	Limit      int
}

// ListCreditHolds answers one page of holds, newest first by (placed_at, id),
// each with the account's standing beside it. A customer named outside the
// caller's reach reads as missing, the subledger's wall.
func (s *Service) ListCreditHolds(ctx context.Context, f CreditHoldFilter) ([]CreditHold, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	statusSQL := `h.status IN ` + standingHoldStates
	switch f.Status {
	case "", "standing":
	case "released":
		statusSQL = `h.status = 'RELEASED'`
	case "all":
		statusSQL = `TRUE`
	default:
		return nil, invalidField("status", "must be one of: standing, released, all")
	}
	if f.CustomerID != nil {
		if err := s.CustomerInWall(ctx, *f.CustomerID); err != nil {
			return nil, err
		}
	}
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT `+creditHoldColumns+`
		FROM credit_holds h
		JOIN customers c ON c.id = h.customer_id AND `+customerWall(5, 6)+`
		WHERE `+statusSQL+` AND ($4::uuid IS NULL OR h.customer_id = $4)
		  AND ($2::timestamptz IS NULL OR (h.placed_at, h.id) < ($2, $3::uuid))
		ORDER BY h.placed_at DESC, h.id DESC
		LIMIT $1`, f.Limit+1, f.AfterTime, f.AfterID, f.CustomerID,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list the credit holds: %w", err)
	}
	defer rows.Close()
	out := []CreditHold{}
	for rows.Next() {
		var h CreditHold
		if err := scanCreditHold(rows, &h); err != nil {
			return nil, fmt.Errorf("failed to scan a credit hold: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachCreditStanding(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// CountCreditHolds is the size of the list the filter answers.
func (s *Service) CountCreditHolds(ctx context.Context, f CreditHoldFilter) (int64, error) {
	statusSQL := `h.status IN ` + standingHoldStates
	switch f.Status {
	case "", "standing":
	case "released":
		statusSQL = `h.status = 'RELEASED'`
	case "all":
		statusSQL = `TRUE`
	default:
		return 0, invalidField("status", "must be one of: standing, released, all")
	}
	var n int64
	if err := s.ex(ctx).QueryRow(ctx, `
		SELECT count(*) FROM credit_holds h
		JOIN customers c ON c.id = h.customer_id AND `+customerWall(3, 4)+`
		WHERE `+statusSQL+` AND ($2::uuid IS NULL OR h.customer_id = $2)`,
		f.CustomerID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count the credit holds: %w", err)
	}
	return n, nil
}

// attachCreditStanding fills the account summary of every listed hold in one
// query.
func (s *Service) attachCreditStanding(ctx context.Context, holds []CreditHold) error {
	if len(holds) == 0 {
		return nil
	}
	day, err := s.todayInDefaultBranch(ctx)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(holds))
	seen := map[uuid.UUID]bool{}
	for _, h := range holds {
		if !seen[h.CustomerID] {
			seen[h.CustomerID] = true
			ids = append(ids, h.CustomerID)
		}
	}
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT c.id, COALESCE(ROUND(c.credit_limit * 100)::bigint, 0), ROUND(c.balance_due * 100)::bigint, `+pastDueCentsSQL(2)+`
		FROM customers c WHERE c.id = ANY($1)`, ids, day)
	if err != nil {
		return fmt.Errorf("failed to read the accounts' standing: %w", err)
	}
	defer rows.Close()
	byCustomer := map[uuid.UUID]*CreditStanding{}
	for rows.Next() {
		var id uuid.UUID
		var limit, balance, pastDue int64
		if err := rows.Scan(&id, &limit, &balance, &pastDue); err != nil {
			return fmt.Errorf("failed to scan an account's standing: %w", err)
		}
		st := &CreditStanding{CurrentBalanceCents: httpx.Cents(balance), PastDueCents: httpx.Cents(pastDue)}
		if limit > 0 {
			l := httpx.Cents(limit)
			st.CreditLimitCents = &l
		}
		byCustomer[id] = st
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range holds {
		if st, ok := byCustomer[holds[i].CustomerID]; ok {
			holds[i].Credit = st
		}
	}
	return nil
}

// PlaceHoldIn is the body of POST /api/v1/ar/credit-holds.
type PlaceHoldIn struct {
	CustomerID uuid.UUID
	Reason     string
	HoldType   string // manual (the default) | over_limit | past_due
	Actor      string
	Role       string
}

// PlaceHold places a manual credit hold. Idempotent: an account that already
// carries a standing hold answers it unchanged (placed false), never a
// second hold, and the act writes customers.credit_hold in the same
// transaction.
func (s *Service) PlaceHold(ctx context.Context, in PlaceHoldIn) (*CreditHold, bool, error) {
	if in.Role != "" && !FinanceRole(in.Role) {
		return nil, false, httpx.Forbidden("placing a credit hold needs the admin, owner or finance role")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, false, invalidField("reason", "is required")
	}
	if len(reason) > 500 {
		return nil, false, invalidField("reason", "must be at most 500 characters")
	}
	holdType := HoldTypeManual
	if in.HoldType != "" {
		switch lower(in.HoldType) {
		case "manual":
		case "over_limit":
			holdType = HoldTypeOverLimit
		case "past_due":
			holdType = HoldTypePastDue
		default:
			return nil, false, invalidField("hold_type", "must be one of: manual, over_limit, past_due")
		}
	}
	if err := s.CustomerInWall(ctx, in.CustomerID); err != nil {
		return nil, false, err
	}
	var out *CreditHold
	placed := false
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.lockCreditOrder(ctx, in.CustomerID); err != nil {
			return err
		}
		if _, err := s.lockCustomer(ctx, in.CustomerID); err != nil {
			return err
		}
		id := uuid.New()
		err := s.ex(ctx).QueryRow(ctx, `
			INSERT INTO credit_holds (id, customer_id, reason, hold_type, status, placed_by)
			SELECT $1, $2, $3, $4, 'ACTIVE', NULLIF($5, '')
			WHERE NOT EXISTS (SELECT 1 FROM credit_holds WHERE customer_id = $2 AND status IN `+standingHoldStates+`)
			RETURNING id`, id, in.CustomerID, reason, string(holdType), in.Actor).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			// already held: the standing hold is the answer, and that is a success
		} else if err != nil {
			return fmt.Errorf("failed to place the credit hold: %w", err)
		} else {
			placed = true
		}
		if err := s.syncCreditHoldFlag(ctx, in.CustomerID); err != nil {
			return err
		}
		if placed && s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_hold.placed", EntityType: "credit_hold", EntityID: id,
				UserID: in.Actor, Changes: map[string]interface{}{"customer_id": in.CustomerID, "hold_type": string(holdType),
					"reason": reason}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		hold, err := s.standingHold(ctx, in.CustomerID)
		if err != nil {
			return err
		}
		out = hold
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, placed, nil
}

// standingHold reads the customer's standing hold, if any.
func (s *Service) standingHold(ctx context.Context, customerID uuid.UUID) (*CreditHold, error) {
	var h CreditHold
	err := scanCreditHold(s.ex(ctx).QueryRow(ctx, `
		SELECT `+creditHoldColumns+`
		FROM credit_holds h JOIN customers c ON c.id = h.customer_id
		WHERE h.customer_id = $1 AND h.status IN `+standingHoldStates, customerID), &h)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the credit hold: %w", err)
	}
	return &h, nil
}

// syncCreditHoldFlag writes customers.credit_hold from the standing holds: a
// hold blocks the account unless a manager's override window is live, so the
// override is what it buys, a way through until it elapses. It runs inside
// the caller's transaction, beside the hold write it follows.
func (s *Service) syncCreditHoldFlag(ctx context.Context, customerID uuid.UUID) error {
	if _, err := s.ex(ctx).Exec(ctx, `
		UPDATE customers SET credit_hold = EXISTS (SELECT 1 FROM credit_holds WHERE customer_id = $1
				AND (status = 'ACTIVE' OR (status = 'OVERRIDDEN' AND override_until <= $2))),
			updated_at = NOW()
		WHERE id = $1`, customerID, s.now()); err != nil {
		return fmt.Errorf("failed to sync the credit hold flag: %w", err)
	}
	return nil
}

// HoldActIn is the body of the release and override routes.
type HoldActIn struct {
	Reason        string     `json:"reason"`
	OverrideUntil *time.Time `json:"override_until"`
	MaxOrderCents *int64     `json:"max_order_cents"`
	Actor         string
	Role          string
}

// ReleaseHold lifts a standing hold with a reason, audit and the flag sync.
func (s *Service) ReleaseHold(ctx context.Context, id uuid.UUID, in HoldActIn) (*CreditHold, error) {
	if in.Role != "" && !FinanceRole(in.Role) {
		return nil, httpx.Forbidden("releasing a credit hold needs the admin, owner or finance role")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, invalidField("reason", "is required")
	}
	if len(reason) > 500 {
		return nil, invalidField("reason", "must be at most 500 characters")
	}
	var out *CreditHold
	err := s.inTx(ctx, func(ctx context.Context) error {
		hold, err := s.lockHold(ctx, id)
		if err != nil {
			return err
		}
		if err := s.CustomerInWall(ctx, hold.CustomerID); err != nil {
			return err
		}
		if err := s.lockCreditOrder(ctx, hold.CustomerID); err != nil {
			return err
		}
		if hold.Status == "released" {
			return httpx.InvalidStateTransition("the credit hold is already released")
		}
		if _, err := s.ex(ctx).Exec(ctx, `
			UPDATE credit_holds SET status = 'RELEASED', released_by = COALESCE(NULLIF($2, ''), 'system'), released_at = NOW(),
				release_reason = $3, override_until = NULL, override_by = NULL, override_reason = NULL,
				max_order_cents = NULL, updated_at = NOW()
			WHERE id = $1`, id, in.Actor, reason); err != nil {
			return fmt.Errorf("failed to release the credit hold: %w", err)
		}
		if err := s.syncCreditHoldFlag(ctx, hold.CustomerID); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_hold.released", EntityType: "credit_hold", EntityID: id,
				UserID: in.Actor, Changes: map[string]interface{}{"customer_id": hold.CustomerID, "reason": reason,
					"hold_type": hold.HoldType}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out, err = s.getHold(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OverrideHold puts a time-bounded manager's window on a standing hold: the
// hold stays on the account, the audit names who let it through and until
// when, and the window must open into the future.
func (s *Service) OverrideHold(ctx context.Context, id uuid.UUID, in HoldActIn) (*CreditHold, error) {
	if in.Role != "" && !FinanceRole(in.Role) {
		return nil, httpx.Forbidden("overriding a credit hold needs the admin, owner or finance role")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, invalidField("reason", "is required")
	}
	if len(reason) > 500 {
		return nil, invalidField("reason", "must be at most 500 characters")
	}
	if in.OverrideUntil == nil {
		return nil, invalidField("override_until", "is required")
	}
	if !in.OverrideUntil.After(s.now()) {
		return nil, invalidField("override_until", "must be in the future: an override buys time, it does not close the account")
	}
	if in.MaxOrderCents != nil && *in.MaxOrderCents < 0 {
		return nil, invalidField("max_order_cents", "must not be negative")
	}
	var out *CreditHold
	err := s.inTx(ctx, func(ctx context.Context) error {
		hold, err := s.lockHold(ctx, id)
		if err != nil {
			return err
		}
		if err := s.CustomerInWall(ctx, hold.CustomerID); err != nil {
			return err
		}
		if err := s.lockCreditOrder(ctx, hold.CustomerID); err != nil {
			return err
		}
		if hold.Status == "released" {
			return httpx.InvalidStateTransition("the credit hold is released: nothing to override")
		}
		if _, err := s.ex(ctx).Exec(ctx, `
			UPDATE credit_holds SET status = 'OVERRIDDEN', override_until = $2,
				override_by = COALESCE(NULLIF($3, ''), 'system'),
				override_reason = $4, max_order_cents = $5, updated_at = NOW()
			WHERE id = $1`, id, *in.OverrideUntil, in.Actor, reason, in.MaxOrderCents); err != nil {
			return fmt.Errorf("failed to override the credit hold: %w", err)
		}
		if err := s.syncCreditHoldFlag(ctx, hold.CustomerID); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_hold.overridden", EntityType: "credit_hold", EntityID: id,
				UserID: in.Actor, Changes: map[string]interface{}{"customer_id": hold.CustomerID, "override_until": *in.OverrideUntil,
					"reason": reason, "max_order_cents": in.MaxOrderCents}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out, err = s.getHold(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// lockHold takes the hold row FOR UPDATE.
func (s *Service) lockHold(ctx context.Context, id uuid.UUID) (*CreditHold, error) {
	var h CreditHold
	err := scanCreditHold(s.ex(ctx).QueryRow(ctx, `
		SELECT `+creditHoldColumns+`
		FROM credit_holds h JOIN customers c ON c.id = h.customer_id
		WHERE h.id = $1 FOR UPDATE OF h`, id), &h)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("credit hold not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock the credit hold: %w", err)
	}
	return &h, nil
}

func (s *Service) getHold(ctx context.Context, id uuid.UUID) (*CreditHold, error) {
	var h CreditHold
	err := scanCreditHold(s.ex(ctx).QueryRow(ctx, `
		SELECT `+creditHoldColumns+`
		FROM credit_holds h JOIN customers c ON c.id = h.customer_id
		WHERE h.id = $1`, id), &h)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("credit hold not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the credit hold: %w", err)
	}
	return &h, nil
}

// HoldEvaluation is what one sweep did or would do.
type HoldEvaluation struct {
	DryRun       bool            `json:"dry_run"`
	Evaluated    int             `json:"evaluated"`
	WouldPlace   []HoldPlacement `json:"would_place"`
	WouldRelease []HoldRelease   `json:"would_release"`
	Placed       int             `json:"placed"`
	Released     int             `json:"released"`
}

// HoldPlacement is one account the sweep would hold, or held.
type HoldPlacement struct {
	CustomerID uuid.UUID `json:"customer_id"`
	HoldType   string    `json:"hold_type"`
	Reason     string    `json:"reason"`
	HoldID     string    `json:"hold_id,omitempty"`
}

// HoldRelease is one standing hold the sweep would lift, or lifted.
type HoldRelease struct {
	HoldID     uuid.UUID `json:"hold_id"`
	CustomerID uuid.UUID `json:"customer_id"`
	Reason     string    `json:"reason"`
}

// holdAccountFacts is one account's input to the hold rule.
type holdAccountFacts struct {
	CustomerID   uuid.UUID
	CreditLimit  int64 // 0 for none
	Balance      int64
	PastDueCents int64
}

// holdDecision is the whole rule, a pure function over one account's facts so
// the sweep's policy can be argued with without a database. An account that
// breaches both triggers is held as over limit: "you are past your limit"
// beats "you have old invoices" when both are true.
func holdDecision(f holdAccountFacts) (HoldType, string) {
	if f.CreditLimit > 0 && f.Balance > f.CreditLimit {
		return HoldTypeOverLimit, fmt.Sprintf("open balance %s is past the %s credit limit",
			moneyString(f.Balance), moneyString(f.CreditLimit))
	}
	if f.PastDueCents > 0 {
		return HoldTypePastDue, fmt.Sprintf("%s is past due", moneyString(f.PastDueCents))
	}
	return "", ""
}

// clearedDecision names the standing automatic hold a sweep lifts: only the
// trigger the hold was placed for counts, and only when it is gone.
func clearedDecision(f holdAccountFacts, holdType string) string {
	switch holdType {
	case "over_limit":
		if f.CreditLimit <= 0 || f.Balance <= f.CreditLimit {
			return fmt.Sprintf("balance %s is back inside the credit limit", moneyString(f.Balance))
		}
	case "past_due":
		if f.PastDueCents <= 0 {
			return "no invoice is past due any more"
		}
	}
	return ""
}

// EvaluateHolds is the sweep (POST /api/v1/ar/credit-holds/evaluate). Dry run
// answers the blast radius and touches nobody; executed, it places and lifts
// idempotently and keeps customers.credit_hold true to the books. It never
// lifts a hold a person placed, nor one carrying a manager's override. The
// named customers, when given, narrow the sweep to them; absent, every active
// credit account is evaluated.
func (s *Service) EvaluateHolds(ctx context.Context, dryRun bool, only []uuid.UUID, actor, role string) (*HoldEvaluation, error) {
	if role != "" && !FinanceRole(role) {
		return nil, httpx.Forbidden("running the credit hold sweep needs the admin, owner or finance role")
	}
	day, err := s.todayInDefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	factsSQL := `
		SELECT c.id, COALESCE(ROUND(c.credit_limit * 100)::bigint, 0), ROUND(c.balance_due * 100)::bigint, ` + pastDueCentsSQL(1) + `
		FROM customers c
		WHERE ROUND(c.balance_due * 100)::bigint <> 0 OR COALESCE(c.credit_limit, 0) > 0`
	var rows pgx.Rows
	if len(only) > 0 {
		rows, err = s.ex(ctx).Query(ctx, factsSQL+` AND c.id = ANY($2)`, day, only)
	} else {
		rows, err = s.ex(ctx).Query(ctx, factsSQL, day)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the credit accounts: %w", err)
	}
	defer rows.Close()
	facts := []holdAccountFacts{}
	for rows.Next() {
		var f holdAccountFacts
		if err := rows.Scan(&f.CustomerID, &f.CreditLimit, &f.Balance, &f.PastDueCents); err != nil {
			return nil, fmt.Errorf("failed to scan a credit account: %w", err)
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The standing holds beside their accounts: a standing hold keeps its
	// account off the place list, and only a hold the sweep placed itself,
	// carrying no override whose window is still open, is a lift candidate.
	held, err := s.ex(ctx).Query(ctx, `
		SELECT h.id, h.customer_id, h.hold_type, h.placed_by IS NULL, COALESCE(h.override_until > $1, FALSE)
		FROM credit_holds h
		WHERE h.status IN `+standingHoldStates, s.now())
	if err != nil {
		return nil, fmt.Errorf("failed to read the standing holds: %w", err)
	}
	defer held.Close()
	type standing struct {
		id       uuid.UUID
		cust     uuid.UUID
		kind     string
		auto     bool
		override bool // the override window is still open
	}
	holdRows := []standing{}
	for held.Next() {
		var r standing
		if err := held.Scan(&r.id, &r.cust, &r.kind, &r.auto, &r.override); err != nil {
			return nil, fmt.Errorf("failed to scan a standing hold: %w", err)
		}
		holdRows = append(holdRows, r)
	}
	if err := held.Err(); err != nil {
		return nil, err
	}
	byCustomer := map[uuid.UUID]*standing{}
	for i := range holdRows {
		byCustomer[holdRows[i].cust] = &holdRows[i]
	}
	var wanted map[uuid.UUID]bool
	if len(only) > 0 {
		wanted = map[uuid.UUID]bool{}
		for _, id := range only {
			wanted[id] = true
		}
	}

	out := &HoldEvaluation{DryRun: dryRun, Evaluated: len(facts), WouldPlace: []HoldPlacement{}, WouldRelease: []HoldRelease{}}
	factOf := map[uuid.UUID]holdAccountFacts{}
	for _, f := range facts {
		factOf[f.CustomerID] = f
	}
	// The place half: an account with no standing hold that breaches the rule.
	for _, f := range facts {
		if byCustomer[f.CustomerID] != nil {
			continue
		}
		if kind, reason := holdDecision(f); kind != "" {
			out.WouldPlace = append(out.WouldPlace, HoldPlacement{CustomerID: f.CustomerID, HoldType: lower(string(kind)), Reason: reason})
		}
	}
	// The lift half: a hold the sweep placed itself, with no override
	// standing, whose trigger is gone. The account's facts are its own (an
	// account with nothing left owed may have no fact row at all).
	for i := range holdRows {
		st := &holdRows[i]
		if wanted != nil && !wanted[st.cust] {
			continue
		}
		if !st.auto || st.override {
			continue // a person's hold is a person's to lift; a live override stands its window
		}
		f := factOf[st.cust]
		if reason := clearedDecision(f, lower(st.kind)); reason != "" {
			out.WouldRelease = append(out.WouldRelease, HoldRelease{HoldID: st.id, CustomerID: st.cust, Reason: reason})
		}
	}
	if dryRun {
		return out, nil
	}
	return out, s.applyHoldEvaluation(ctx, out, only, actor)
}

// applyHoldEvaluation writes the sweep's plan: each placement and release its
// own guarded statement, so a run that meets a hold another act just placed
// skips rather than doubles it.
func (s *Service) applyHoldEvaluation(ctx context.Context, ev *HoldEvaluation, only []uuid.UUID, actor string) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		// The elapsed overrides go back to active first: the window is over,
		// the hold stands on its own again, and a lift planned against it is
		// no longer behind a window.
		expired, err := s.ex(ctx).Query(ctx, `
			UPDATE credit_holds SET status = 'ACTIVE', override_until = NULL, override_by = NULL,
				override_reason = NULL, max_order_cents = NULL, updated_at = NOW()
			WHERE status = 'OVERRIDDEN' AND override_until <= $1
			  AND ($2::uuid[] IS NULL OR customer_id = ANY($2))
			RETURNING id, customer_id`, s.now(), uuidsOrNil(only))
		if err != nil {
			return fmt.Errorf("failed to expire the credit hold overrides: %w", err)
		}
		type expiredHold struct {
			id   uuid.UUID
			cust uuid.UUID
		}
		var expiredHolds []expiredHold
		for expired.Next() {
			var e expiredHold
			if err := expired.Scan(&e.id, &e.cust); err != nil {
				expired.Close()
				return fmt.Errorf("failed to scan an expired override: %w", err)
			}
			expiredHolds = append(expiredHolds, e)
		}
		expired.Close()
		if err := expired.Err(); err != nil {
			return err
		}
		for _, e := range expiredHolds {
			if err := s.syncCreditHoldFlag(ctx, e.cust); err != nil {
				return err
			}
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_hold.override_expired", EntityType: "credit_hold",
					EntityID: e.id, UserID: actor, Changes: map[string]interface{}{"customer_id": e.cust}}); err != nil {
					return fmt.Errorf("failed to write the audit row: %w", err)
				}
			}
		}
		for i := range ev.WouldPlace {
			p := &ev.WouldPlace[i]
			id := uuid.New()
			err := s.ex(ctx).QueryRow(ctx, `
				INSERT INTO credit_holds (id, customer_id, reason, hold_type, status)
				SELECT $1, $2, $3, $4, 'ACTIVE'
				WHERE NOT EXISTS (SELECT 1 FROM credit_holds WHERE customer_id = $2 AND status IN `+standingHoldStates+`)
				RETURNING id`, id, p.CustomerID, p.Reason, upper(p.HoldType)).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // another act placed the hold first: nothing to add
			}
			if err != nil {
				return fmt.Errorf("failed to place the credit hold: %w", err)
			}
			if err := s.syncCreditHoldFlag(ctx, p.CustomerID); err != nil {
				return err
			}
			p.HoldID = id.String()
			ev.Placed++
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_hold.placed", EntityType: "credit_hold", EntityID: id,
					UserID: actor, Changes: map[string]interface{}{"customer_id": p.CustomerID, "hold_type": p.HoldType,
						"reason": p.Reason, "by": "sweep"}}); err != nil {
					return fmt.Errorf("failed to write the audit row: %w", err)
				}
			}
		}
		for i := range ev.WouldRelease {
			r := &ev.WouldRelease[i]
			tag, err := s.ex(ctx).Exec(ctx, `
				UPDATE credit_holds SET status = 'RELEASED', released_by = COALESCE(NULLIF($2, ''), 'sweep'), released_at = NOW(),
					release_reason = $3, updated_at = NOW()
				WHERE id = $1 AND status IN `+standingHoldStates+` AND override_until IS NULL`, r.HoldID, actor, r.Reason)
			if err != nil {
				return fmt.Errorf("failed to release the credit hold: %w", err)
			}
			if tag.RowsAffected() == 0 {
				continue // the hold was lifted or overridden while the sweep ran
			}
			if err := s.syncCreditHoldFlag(ctx, r.CustomerID); err != nil {
				return err
			}
			ev.Released++
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_hold.released", EntityType: "credit_hold", EntityID: r.HoldID,
					UserID: actor, Changes: map[string]interface{}{"customer_id": r.CustomerID, "reason": r.Reason,
						"by": "sweep"}}); err != nil {
					return fmt.Errorf("failed to write the audit row: %w", err)
				}
			}
		}
		return nil
	})
}

// moneyString renders cents for a hold reason a person reads.
func moneyString(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	out := fmt.Sprintf("$%d.%02d", cents/100, cents%100)
	if neg {
		return "-" + out
	}
	return out
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
