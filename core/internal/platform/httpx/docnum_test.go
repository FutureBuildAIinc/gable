// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"context"
	"fmt"
	"sync"
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

// RULE: the sequence is drawn from many request goroutines at once; every
// concurrent nextval yields a distinct number, so two concurrent creates
// never share a document number.
func TestNextDocumentNumberConcurrent(t *testing.T) {
	db := testutil.RequireDB(t)
	createTestSequence(t, db, "docnum_concurrent_seq", 1)

	const workers = 8
	const each = 25
	nums := make(chan string, workers*each)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for i := 0; i < each; i++ {
				n, err := NextDocumentNumber(ctx, db.Pool, "docnum_concurrent_seq", "Q", 6)
				if err != nil {
					t.Errorf("concurrent nextval: %v", err)
					return
				}
				nums <- n
			}
		}()
	}
	wg.Wait()
	close(nums)

	seen := make(map[string]bool, workers*each)
	for n := range nums {
		if seen[n] {
			t.Fatalf("document number %s minted twice", n)
		}
		seen[n] = true
	}
	if len(seen) != workers*each {
		t.Errorf("%d distinct numbers minted, want %d", len(seen), workers*each)
	}
}

// createTestCounter makes a throwaway gapless series (the table is the
// migration's document_counters; created here when absent so the platform
// package tests stand alone).
func createTestCounter(t *testing.T, db *database.DB, series string, next int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS document_counters (series TEXT PRIMARY KEY, next_value BIGINT NOT NULL)`); err != nil {
		t.Fatalf("counter table: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO document_counters (series, next_value) VALUES ($1, $2)
		ON CONFLICT (series) DO UPDATE SET next_value = EXCLUDED.next_value`, series, next); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM document_counters WHERE series = $1`, series) })
}

// RULE (ADR 0005 4.1): a gapless number is the counter's next value through
// the caller's transaction; a rollback gives the number back, so the next mint
// takes it (the whole difference from a sequence).
func TestNextGaplessNumberRollbackGivesTheNumberBack(t *testing.T) {
	db := testutil.RequireDB(t)
	createTestCounter(t, db, "gapless_rollback", 5)
	ctx := context.Background()

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NextGaplessNumber(ctx, tx, "gapless_rollback", "IN", 6)
	if err != nil || got != "IN-000005" {
		t.Fatalf("in tx = %q, %v, want IN-000005", got, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := NextGaplessNumber(ctx, db.Pool, "gapless_rollback", "IN", 6)
	if err != nil || again != "IN-000005" {
		t.Fatalf("after rollback = %q, %v, want IN-000005 again (no gap)", again, err)
	}
	next, err := NextGaplessNumber(ctx, db.Pool, "gapless_rollback", "IN", 6)
	if err != nil || next != "IN-000006" {
		t.Fatalf("next = %q, %v, want IN-000006", next, err)
	}
}

// RULE: contenders mint distinct, consecutive numbers: the counter row
// serializes them.
func TestNextGaplessNumberConcurrentConsecutive(t *testing.T) {
	db := testutil.RequireDB(t)
	createTestCounter(t, db, "gapless_concurrent", 1)
	const workers = 8
	nums := make(chan string, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			n, err := NextGaplessNumber(ctx, tx, "gapless_concurrent", "IN", 6)
			if err != nil {
				_ = tx.Rollback(ctx)
				t.Error(err)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				t.Error(err)
				return
			}
			nums <- n
		}()
	}
	wg.Wait()
	close(nums)
	seen := map[string]bool{}
	for n := range nums {
		if seen[n] {
			t.Fatalf("number %s minted twice", n)
		}
		seen[n] = true
	}
	for i := 1; i <= workers; i++ {
		if want := fmt.Sprintf("IN-%06d", i); !seen[want] {
			t.Errorf("number %s was not minted: the series has a gap", want)
		}
	}
}

// RULE: a series with no counter row, a bad series name, prefix, width or no
// querier are refusals, never a number.
func TestNextGaplessNumberRefusals(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	createTestCounter(t, db, "gapless_anchor", 1) // makes sure the table exists
	if _, err := NextGaplessNumber(ctx, db.Pool, "gapless_missing", "IN", 6); err == nil {
		t.Error("missing series succeeded, want an error")
	}
	bad := []struct {
		series, prefix string
		width          int
	}{{"", "IN", 6}, {"bad name", "IN", 6}, {"1x", "IN", 6}, {"invoice", "", 6}, {"invoice", "in", 6}, {"invoice", "INVOI", 6}, {"invoice", "IN", 0}}
	for _, tc := range bad {
		if _, err := NextGaplessNumber(ctx, nil, tc.series, tc.prefix, tc.width); err == nil {
			t.Errorf("NextGaplessNumber(%q, %q, %d) succeeded, want a refusal", tc.series, tc.prefix, tc.width)
		}
	}
	if _, err := NextGaplessNumber(ctx, nil, "invoice", "IN", 6); err == nil {
		t.Error("nil querier succeeded")
	}
	if got, err := NextGaplessNumber(ctx, db.Pool, "gapless_anchor", "IN", 3); err != nil || got != "IN-001" {
		t.Errorf("got %q, %v, want IN-001", got, err)
	}
}
