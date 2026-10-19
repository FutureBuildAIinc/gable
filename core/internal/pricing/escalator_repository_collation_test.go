// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// ListMarketIndices must order the two seeded "Random Lengths S..." names the
// same way on every server collation. The pair shares the 16 character prefix
// "Random Lengths S" and diverges on case at character 17 ("SYP" vs
// "Structural"): a plain ORDER BY name follows the database's default
// collation, which puts "SYP" first under musl's C locale (postgres:16-alpine,
// CI) but second under glibc's en_US.utf8 (postgres:16, local dev), so the API
// list order flips with the container image. This test plants the pair (and a
// control name that sorts before both under either collation) and pins the
// byte order the COLLATE "C" clause buys; with the clause removed it fails on
// any glibc server.

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/testutil"
)

func TestListMarketIndices_CollationCOrder_Postgres(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	seed := []struct {
		name string
		code string
	}{
		{"Random Lengths Structural Panel Composite", "TEST_COLLATE_PANEL"},
		{"Random Lengths SYP 2x4", "TEST_COLLATE_SYP"},
		{"Aardvark Control Index", "TEST_COLLATE_AARDVARK"},
	}
	for _, s := range seed {
		_, err := db.Pool.Exec(ctx, `
			INSERT INTO market_indices (name, source, current_value, previous_value, unit, index_code)
			VALUES ($1, 'RANDOM_LENGTHS', 1, 1, 'MBF', $2)
			ON CONFLICT (index_code) DO UPDATE SET name = EXCLUDED.name`,
			s.name, s.code)
		if err != nil {
			t.Fatalf("seed %s: %v", s.code, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM market_indices WHERE index_code IN ('TEST_COLLATE_PANEL', 'TEST_COLLATE_SYP', 'TEST_COLLATE_AARDVARK')`)
	})

	repo := pricing.NewEscalatorRepository(db)
	indices, err := repo.ListMarketIndices(ctx)
	if err != nil {
		t.Fatalf("ListMarketIndices: %v", err)
	}

	sypAt, panelAt := -1, -1
	for i, idx := range indices {
		switch idx.Name {
		case "Random Lengths SYP 2x4":
			sypAt = i
		case "Random Lengths Structural Panel Composite":
			panelAt = i
		}
	}
	if sypAt == -1 || panelAt == -1 {
		t.Fatalf("seeded pair missing from list (sypAt=%d panelAt=%d)", sypAt, panelAt)
	}
	if sypAt > panelAt {
		t.Errorf("ListMarketIndices ordered %q before %q: under COLLATE \"C\" the uppercase Y of \"SYP\" (0x59) must sort before the lowercase t of \"Structural\" (0x74), whatever the server's default collation",
			"Random Lengths Structural Panel Composite", "Random Lengths SYP 2x4")
	}
}
