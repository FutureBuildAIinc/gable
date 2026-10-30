// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

// The United States running total resets at each crossing, and which tenders
// a crossing consumed is decided by the order the payer's lock admits the
// completions, never by the order their transactions began. These tests hold
// one completion's transaction open from before another crosses, so its start
// time is earlier than the crossing while it takes the lock later.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// testVaultKey seals the payer's sensitive values in these tests: an obvious
// pattern, never a real key shape.
const testVaultKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// keyedService is the large cash service with the test vault key, as a
// configured stack runs it.
func keyedService(t *testing.T, db *database.DB) *largecash.Service {
	t.Helper()
	v, err := payment.NewVault(testVaultKey)
	if err != nil {
		t.Fatal(err)
	}
	return largecash.NewService(db, v, testVaultKey)
}

// usBranch is a United States branch with a yard, a register and n completed
// sale rows a counted tender can hang from. The cleanup takes every row the
// test wrote under the branch.
func usBranch(t *testing.T, db *database.DB, n int) (uuid.UUID, []uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	branch, yard, reg := uuid.New(), uuid.New(), "REG-"+uuid.NewString()[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO locations (id, type, code, name, country_code, default_tax_rate) VALUES ($1, 'BRANCH', $2, 'capture race', 'US', 0)`, branch, "CR-"+branch.String()[:8])
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`, yard, "CRY-"+branch.String()[:8], branch)
	exec(`INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'capture race', $3)`, reg, yard, branch)
	sales := make([]uuid.UUID, n)
	for i := range sales {
		sales[i] = uuid.New()
		exec(`INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, status, currency) VALUES ($1, $2, $3, $4, 'COMPLETED', 'USD')`,
			sales[i], reg, uuid.New(), branch)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_events WHERE branch_id = $1`, branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE register_id = $1`, reg)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id = $1`, reg)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, yard, branch)
	})
	return branch, sales
}

// windowPayer is one walk-in payer whose taxpayer identification number keys
// every sale together.
func windowPayer() *largecash.PayerIn {
	return &largecash.PayerIn{Name: "Window Payer", Address: "2 Test Road", Occupation: "builder",
		DateOfBirth: "1981-03-04", IDType: "passport", IDNumber: "WIN-ID-0001", TaxpayerID: "99-0000002"}
}

func cashFacts(branch, sale uuid.UUID, cents int64) *largecash.CompletionFacts {
	return &largecash.CompletionFacts{BranchID: branch, TransactionID: sale, Currency: "USD", Payer: windowPayer(),
		Tenders: []largecash.TenderFacts{{Method: "cash", AmountCents: cents}}}
}

// captureAfter opens a completion's transaction, pins its start time with a
// first statement, runs between while it is open, and only then captures:
// the completion began before between ran and takes the payer's lock after.
func captureAfter(t *testing.T, db *database.DB, svc *largecash.Service, f *largecash.CompletionFacts, between func()) {
	t.Helper()
	ctx := context.Background()
	err := db.RunInTx(ctx, func(ctx context.Context) error {
		var one int
		if err := db.GetExecutor(ctx).QueryRow(ctx, `SELECT 1 WHERE now() IS NOT NULL`).Scan(&one); err != nil {
			return err
		}
		between()
		return svc.Capture(ctx, f)
	})
	if err != nil {
		t.Fatalf("the held completion: %v", err)
	}
}

func capture(t *testing.T, db *database.DB, svc *largecash.Service, f *largecash.CompletionFacts) {
	t.Helper()
	if err := db.RunInTx(context.Background(), func(ctx context.Context) error { return svc.Capture(ctx, f) }); err != nil {
		t.Fatalf("capture: %v", err)
	}
}

func crossings(t *testing.T, db *database.DB, branch uuid.UUID) []int64 {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT total_cents FROM large_cash_events WHERE branch_id = $1 ORDER BY created_at`, branch)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// TestCaptureCountsCashThatBeganBeforeTheCrossing: completion A begins, then
// completion B crosses on its own and commits, then A captures. A came after
// the crossing in lock order, so its cash opens the next running total, and
// the next sale that takes the pair over the threshold crosses on both.
func TestCaptureCountsCashThatBeganBeforeTheCrossing(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	svc := keyedService(t, db)
	branch, sales := usBranch(t, db, 3)
	const big, part = largecash.ThresholdCents * 110 / 100, largecash.ThresholdCents * 60 / 100
	captureAfter(t, db, svc, cashFacts(branch, sales[0], part), func() {
		capture(t, db, svc, cashFacts(branch, sales[1], big))
	})
	capture(t, db, svc, cashFacts(branch, sales[2], part))
	got := crossings(t, db, branch)
	if len(got) != 2 || got[0] != big || got[1] != 2*part {
		t.Fatalf("crossings = %v, want [%d %d]: the cash that began before the first crossing counts toward the next", got, big, 2*part)
	}
}

// TestCaptureNeverCountsATenderTwice: crossing completion C begins, then a
// tender T commits, then C takes the lock, counts T and crosses. T was
// consumed by C's crossing, so it never counts again: the next sale alone is
// under the threshold and records no second crossing.
func TestCaptureNeverCountsATenderTwice(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	svc := keyedService(t, db)
	branch, sales := usBranch(t, db, 3)
	const first, second = largecash.ThresholdCents * 50 / 100, largecash.ThresholdCents * 60 / 100
	captureAfter(t, db, svc, cashFacts(branch, sales[0], second), func() {
		capture(t, db, svc, cashFacts(branch, sales[1], first))
	})
	capture(t, db, svc, cashFacts(branch, sales[2], second))
	got := crossings(t, db, branch)
	if len(got) != 1 || got[0] != first+second {
		t.Fatalf("crossings = %v, want one of %d: a tender a crossing consumed counts again toward the next", got, first+second)
	}
}

// TestCaptureRefusesWithoutAVaultKey: with no vault key, or with a malformed
// one, a capture that carries the payer's identity is refused with
// large_cash_vault_unavailable, so the taxpayer identification number and the
// ID number are never stored in clear nor keyed by an unkeyed digest, and the
// refusal writes nothing.
func TestCaptureRefusesWithoutAVaultKey(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	for name, key := range map[string]string{"no key": "", "a malformed key": "not-a-key"} {
		t.Run(name, func(t *testing.T) {
			v, _ := payment.NewVault(key)
			svc := largecash.NewService(db, v, key)
			branch, sales := usBranch(t, db, 1)
			facts := cashFacts(branch, sales[0], largecash.ThresholdCents*2)
			for act, run := range map[string]func(ctx context.Context) error{
				"the capture": func(ctx context.Context) error { return svc.Capture(ctx, facts) },
				"the preview": func(ctx context.Context) error { _, err := svc.Preview(ctx, facts); return err },
			} {
				err := db.RunInTx(ctx, run)
				var he *httpx.Error
				if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable ||
					len(he.Details) == 0 || he.Details[0].Code != "large_cash_vault_unavailable" {
					t.Fatalf("%s without a vault key = %v, want 503 large_cash_vault_unavailable", act, err)
				}
			}
			for table, sql := range map[string]string{
				"large_cash_payers":  `SELECT count(*) FROM large_cash_payers WHERE branch_id = $1`,
				"large_cash_tenders": `SELECT count(*) FROM large_cash_tenders WHERE branch_id = $1`,
				"large_cash_events":  `SELECT count(*) FROM large_cash_events WHERE branch_id = $1`,
			} {
				var n int
				if err := db.Pool.QueryRow(ctx, sql, branch).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Errorf("the refusal wrote %d rows to %s", n, table)
				}
			}
		})
	}
}
