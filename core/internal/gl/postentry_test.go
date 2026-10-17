// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl_test

// PostEntry (ADR 0005 section 8.2) against a real database: a balanced entry
// through the act's transaction with zero legs left out, refused outside a
// transaction, refused unbalanced, refused into a closed period, and rolled
// back with the act that wrote it.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostEntry(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	branch := testBranch(t, db)
	ref := uuid.New()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id = $1)`, ref)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id = $1`, ref)
	})
	in := func(legs ...gl.Leg) gl.PostingInput {
		return gl.PostingInput{EntryDate: time.Now(), Memo: "postentry test", Source: gl.SourceInvoice, SourceRefID: &ref, BranchID: branch, Currency: "CAD", Legs: legs}
	}
	balanced := in(
		gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 1100},
		gl.Leg{AccountCode: gl.AccountCodeCOGS, Debit: 0},
		gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 1000},
		gl.Leg{AccountCode: gl.AccountCodeSalesTax, Credit: 100},
	)

	// Outside a transaction: refused.
	if _, err := svc.PostEntry(ctx, balanced); !errors.Is(err, gl.ErrNoTransaction) {
		t.Fatalf("PostEntry with no transaction = %v, want ErrNoTransaction", err)
	}

	err := db.RunInTx(ctx, func(ctx context.Context) error {
		// Unbalanced: refused.
		if _, err := svc.PostEntry(ctx, in(gl.Leg{AccountCode: gl.AccountCodeAR, Debit: 10}, gl.Leg{AccountCode: gl.AccountCodeRevenue, Credit: 9})); !errors.Is(err, gl.ErrUnbalanced) {
			t.Errorf("unbalanced entry = %v, want ErrUnbalanced", err)
		}
		// All legs zero: not written.
		if e, err := svc.PostEntry(ctx, in(gl.Leg{AccountCode: gl.AccountCodeAR}, gl.Leg{AccountCode: gl.AccountCodeRevenue})); e != nil || err != nil {
			t.Errorf("all zero entry = %v, %v, want nil, nil", e, err)
		}
		e, err := svc.PostEntry(ctx, balanced)
		if err != nil || e == nil {
			t.Fatalf("balanced entry: %v, %v", e, err)
		}
		if len(e.Lines) != 3 {
			t.Errorf("%d legs written, want 3 (the zero COGS leg is left out)", len(e.Lines))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var cur string
	var status string
	var lines int
	if err := db.Pool.QueryRow(ctx, `SELECT currency, status, (SELECT count(*) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id)
		FROM gl_journal_entries e WHERE source_ref_id = $1`, ref).Scan(&cur, &status, &lines); err != nil {
		t.Fatal(err)
	}
	if cur != "CAD" || status != "POSTED" || lines != 3 {
		t.Errorf("entry = %s %s %d legs, want CAD POSTED 3", cur, status, lines)
	}

	// Rolled back with the act that wrote it.
	boom := errors.New("act failed")
	other := uuid.New()
	err = db.RunInTx(ctx, func(ctx context.Context) error {
		p := balanced
		p.SourceRefID = &other
		if _, err := svc.PostEntry(ctx, p); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("act = %v", err)
	}
	var n int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, other).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d entries survived a rolled back act (%v)", n, err)
	}

	// Dated into a closed period: ErrPeriodClosed.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO gl_fiscal_periods (name, start_date, end_date, status) VALUES ('postentry-test', '1990-01-01', '1990-01-31', 'CLOSED')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM gl_fiscal_periods WHERE name = 'postentry-test'`) })
	err = db.RunInTx(ctx, func(ctx context.Context) error {
		p := balanced
		p.EntryDate = time.Date(1990, 1, 15, 0, 0, 0, 0, time.UTC)
		_, err := svc.PostEntry(ctx, p)
		return err
	})
	if !errors.Is(err, gl.ErrPeriodClosed) {
		t.Errorf("entry into a closed period = %v, want ErrPeriodClosed", err)
	}
}

// RULE (ADR 0005 section 8.2): a reversal is the original's legs swapped,
// source REVERSAL, linked to the original, dated and currencied as asked, once
// per entry, inside the act's transaction.
func TestPostReversal(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	branch := testBranch(t, db)
	ref := uuid.New()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id = $1 OR reverses_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id = $1))`, ref)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE reverses_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id = $1)`, ref)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id = $1`, ref)
	})
	var orig uuid.UUID
	day := time.Date(2031, 3, 4, 0, 0, 0, 0, time.UTC)
	err := db.RunInTx(ctx, func(ctx context.Context) error {
		e, err := svc.PostEntry(ctx, gl.PostingInput{EntryDate: time.Now(), Memo: "orig", Source: gl.SourceInvoice, SourceRefID: &ref, BranchID: branch, Currency: "CAD",
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeAR, Debit: 1100}, {AccountCode: gl.AccountCodeRevenue, Credit: 1000}, {AccountCode: gl.AccountCodeSalesTax, Credit: 100}}})
		if err != nil {
			return err
		}
		orig = e.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PostReversal(ctx, gl.ReversalInput{EntryID: orig, EntryDate: day, Currency: "CAD"}); !errors.Is(err, gl.ErrNoTransaction) {
		t.Fatalf("PostReversal outside a transaction = %v, want ErrNoTransaction", err)
	}
	err = db.RunInTx(ctx, func(ctx context.Context) error {
		rev, err := svc.PostReversal(ctx, gl.ReversalInput{EntryID: orig, EntryDate: day, Currency: "CAD", Reason: "void", PostedBy: "tester"})
		if err != nil || rev == nil {
			t.Fatalf("PostReversal = %v, %v", rev, err)
		}
		if _, err := svc.PostReversal(ctx, gl.ReversalInput{EntryID: orig, EntryDate: day, Currency: "CAD"}); !errors.Is(err, gl.ErrAlreadyReversed) {
			t.Errorf("second reversal = %v, want ErrAlreadyReversed", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var source, cur string
	var date time.Time
	var debits, credits int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT e.source, e.currency, e.entry_date, COALESCE(SUM(l.debit * 100), 0)::bigint, COALESCE(SUM(l.credit * 100), 0)::bigint
		FROM gl_journal_entries e JOIN gl_journal_lines l ON l.journal_entry_id = e.id
		WHERE e.reverses_entry_id = $1 GROUP BY e.id`, orig).Scan(&source, &cur, &date, &debits, &credits); err != nil {
		t.Fatal(err)
	}
	if source != "REVERSAL" || cur != "CAD" || !date.Equal(day) || debits != 1100 || credits != 1100 {
		t.Errorf("reversal = %s %s %s debit %d credit %d, want REVERSAL CAD %s 1100/1100", source, cur, date, debits, credits, day)
	}
	var arCredit int64
	if err := db.Pool.QueryRow(ctx, `SELECT (l.credit * 100)::bigint FROM gl_journal_lines l JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		JOIN gl_accounts a ON a.id = l.account_id WHERE e.reverses_entry_id = $1 AND a.code = $2`, orig, gl.AccountCodeAR).Scan(&arCredit); err != nil || arCredit != 1100 {
		t.Errorf("the reversal credits AR %d, %v, want 1100 (the original's debit)", arCredit, err)
	}
}

// RULE (review r5 P3-3): a unique violation on a reversal insert maps to
// ErrAlreadyReversed only when the violated index is the one reversal index
// ux_gl_entry_single_reversal. Any other unique violation on the insert (a
// later unique index on the table) passes through unmapped, so it is never
// misreported as a lost reversal race. The probe index here stands in for
// such a later index.
func TestPostEntryMapsOnlyTheReversalIndexViolation(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	branch := testBranch(t, db)
	ref := uuid.New()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id = $1)`, ref)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id = $1`, ref)
	})
	sentinel := "r5p3 probe " + uuid.NewString()
	index := "gv1_probe_ix_" + uuid.NewString()[:8]
	if _, err := db.Pool.Exec(ctx, `CREATE UNIQUE INDEX `+index+` ON gl_journal_entries (memo) WHERE memo = '`+sentinel+`'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DROP INDEX IF EXISTS `+index) })
	entry := func(memo string, reverses *uuid.UUID) gl.PostingInput {
		return gl.PostingInput{EntryDate: time.Now(), Memo: memo, Source: gl.SourceInvoice, SourceRefID: &ref,
			BranchID: branch, Currency: "USD", ReversesEntryID: reverses,
			Legs: []gl.Leg{{AccountCode: gl.AccountCodeAR, Debit: 1100}, {AccountCode: gl.AccountCodeRevenue, Credit: 1100}}}
	}
	err := db.RunInTx(ctx, func(ctx context.Context) error {
		first, err := svc.PostEntry(ctx, entry(sentinel, nil))
		if err != nil || first == nil {
			t.Fatalf("the first entry: %v, %v", first, err)
		}
		// The second entry collides on the probe index (its memo repeats);
		// the entry it reverses holds no reversal row, so the one reversal
		// index is not the violated one.
		_, err = svc.PostEntry(ctx, entry(sentinel, &first.ID))
		return err
	})
	if err == nil {
		t.Fatal("the colliding insert posted, want the probe index to refuse it")
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23505" || pg.ConstraintName != index {
		t.Fatalf("the collision = %v, want a 23505 on the probe index %s", err, index)
	}
	if errors.Is(err, gl.ErrAlreadyReversed) {
		t.Errorf("a violation of %s mapped to ErrAlreadyReversed: only ux_gl_entry_single_reversal may map there, and this loss is not a reversal race", index)
	}
}

// testBranch answers the default branch the migrations seed, the branch the
// database backed tests post under.
func testBranch(t *testing.T, db *database.DB) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// RULE (migration 125): a posting that names no branch is refused
// (ErrNoBranch) rather than silently posted on the default branch.
func TestPostEntryRefusesNoBranch(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	legs := []gl.Leg{{AccountCode: gl.AccountCodeAR, Debit: 1100}, {AccountCode: gl.AccountCodeRevenue, Credit: 1100}}
	if _, err := svc.PostEntry(ctx, gl.PostingInput{EntryDate: time.Now(), Memo: "no branch", Source: gl.SourceManual, Currency: "CAD", Legs: legs}); !errors.Is(err, gl.ErrNoTransaction) {
		t.Fatalf("PostEntry with no transaction = %v, want ErrNoTransaction (checked before the branch)", err)
	}
	err := db.RunInTx(ctx, func(ctx context.Context) error {
		if _, err := svc.PostEntry(ctx, gl.PostingInput{EntryDate: time.Now(), Memo: "no branch", Source: gl.SourceManual, Currency: "CAD", Legs: legs}); !errors.Is(err, gl.ErrNoBranch) {
			t.Errorf("PostEntry with no branch = %v, want ErrNoBranch", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
