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
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestPostEntry(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	svc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	ref := uuid.New()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id = $1)`, ref)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id = $1`, ref)
	})
	in := func(legs ...gl.Leg) gl.PostingInput {
		return gl.PostingInput{EntryDate: time.Now(), Memo: "postentry test", Source: gl.SourceInvoice, SourceRefID: &ref, Currency: "CAD", Legs: legs}
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
