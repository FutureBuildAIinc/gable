// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"context"
	"fmt"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
)

// createTestSequence drops and recreates a throwaway sequence so each test
// starts from a known value. The name is fixed per test: package tests run
// serially against the one throwaway database this suite is pointed at.
func createTestSequence(t *testing.T, db *database.DB, name string, startWith int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, fmt.Sprintf("DROP SEQUENCE IF EXISTS %s", name)); err != nil {
		t.Fatalf("drop sequence: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, fmt.Sprintf("CREATE SEQUENCE %s START WITH %d", name, startWith)); err != nil {
		t.Fatalf("create sequence: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, fmt.Sprintf("DROP SEQUENCE IF EXISTS %s", name))
	})
}

// RULE (ADR 0001 §8): the document number is the next value of the entity's
// dedicated sequence, formatted with the entity's prefix, zero padded to
// the width, taken through the caller's own connection (the pool here).
func TestNextDocumentNumberFromPool(t *testing.T) {
	db := testutil.RequireDB(t)
	createTestSequence(t, db, "docnum_pool_seq", 1)

	ctx := context.Background()
	first, err := NextDocumentNumber(ctx, db.Pool, "docnum_pool_seq", "Q", 6)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := NextDocumentNumber(ctx, db.Pool, "docnum_pool_seq", "Q", 6)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != "Q-000001" {
		t.Errorf("first = %q, want Q-000001", first)
	}
	if second != "Q-000002" {
		t.Errorf("second = %q, want Q-000002", second)
	}
}

// RULE: the helper runs through the caller's transaction, the seam every
// create path already holds. Inside a transaction that rolls back, the
// number is spent: sequences are not transactional, gaps are expected, and
// the next caller through the pool gets the following value.
func TestNextDocumentNumberInsideTransactionRollbackSpendsIt(t *testing.T) {
	db := testutil.RequireDB(t)
	createTestSequence(t, db, "docnum_tx_seq", 1)

	ctx := context.Background()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	got, err := NextDocumentNumber(ctx, tx, "docnum_tx_seq", "Q", 6)
	if err != nil {
		t.Fatalf("inside tx: %v", err)
	}
	if got != "Q-000001" {
		t.Errorf("inside tx = %q, want Q-000001", got)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	after, err := NextDocumentNumber(ctx, db.Pool, "docnum_tx_seq", "Q", 6)
	if err != nil {
		t.Fatalf("after rollback: %v", err)
	}
	if after != "Q-000002" {
		t.Errorf("after rollback = %q, want Q-000002 (the rolled back number is spent)", after)
	}
}

// RULE: a sequence that outgrows the width produces a longer number, never
// truncation or wraparound.
func TestNextDocumentNumberPastTheWidth(t *testing.T) {
	db := testutil.RequireDB(t)
	createTestSequence(t, db, "docnum_wide_seq", 1234567)

	got, err := NextDocumentNumber(context.Background(), db.Pool, "docnum_wide_seq", "Q", 6)
	if err != nil {
		t.Fatalf("wide: %v", err)
	}
	if got != "Q-1234567" {
		t.Errorf("wide = %q, want Q-1234567", got)
	}
}

// RULE: the helper refuses a prefix that is not one to four uppercase
// letters, a sequence name that is not a plain identifier, or a width below
// one: these are server-side constants, and a bug there must surface.
func TestNextDocumentNumberValidatesItsArguments(t *testing.T) {
	cases := []struct {
		sequence string
		prefix   string
		width    int
	}{
		{"", "Q", 6},
		{"bad name", "Q", 6},
		{"seq;DROP TABLE x", "Q", 6},
		{"1starts_with_digit", "Q", 6},
		{"quote_number_seq", "", 6},
		{"quote_number_seq", "q", 6},
		{"quote_number_seq", "Quote", 6},
		{"quote_number_seq", "QUOTES", 6},
		{"quote_number_seq", "Q-1", 6},
		{"quote_number_seq", "Q", 0},
		{"quote_number_seq", "Q", -1},
	}
	for _, tc := range cases {
		if _, err := NextDocumentNumber(context.Background(), nil, tc.sequence, tc.prefix, tc.width); err == nil {
			t.Errorf("NextDocumentNumber(%q, %q, %d) succeeded, want a refusal", tc.sequence, tc.prefix, tc.width)
		}
	}
}

// RULE: valid constants with no database seam is a server bug that must
// surface, not a panic.
func TestNextDocumentNumberNilQuerier(t *testing.T) {
	if _, err := NextDocumentNumber(context.Background(), nil, "quote_number_seq", "Q", 6); err == nil {
		t.Error("nil querier succeeded, want a refusal")
	}
}

// RULE: a sequence the migration never created is an error naming the
// sequence, not a zero-prefixed zero.
func TestNextDocumentNumberMissingSequence(t *testing.T) {
	db := testutil.RequireDB(t)
	if _, err := NextDocumentNumber(context.Background(), db.Pool, "docnum_missing_seq", "Q", 6); err == nil {
		t.Fatal("missing sequence succeeded, want an error")
	}
}
