// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Migration 142 (paint tinting, row CP-16): it applies twice to the same
// effect, its down removes everything it added (the tables, the return
// line's two flags, the two seeded volume units), and it applies again
// after the down with the tables intact.

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func mig142Files(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "142_"):
			target = f
		case base < "142_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 142 not found")
	}
	return before, target, "../../migrations/down/142_paint_tint_catalogue_and_line_tints_down.sql"
}

const mig142Present = `
	SELECT
	  (SELECT count(*) FROM information_schema.tables WHERE table_name IN (
	     'paint_tint_systems', 'paint_bases', 'paint_colorants', 'paint_colors', 'paint_formulas',
	     'paint_formula_components', 'paint_line_tints', 'paint_line_tint_components', 'paint_saved_colors'))
	+ (SELECT count(*) FROM information_schema.columns WHERE
	     table_name = 'pos_return_lines' AND column_name IN ('mis_tint', 'tinted_return'))
	+ (SELECT count(*) FROM units WHERE code IN ('QT', 'PT'))`

func TestMigration142_UpIsIdempotentAndDownReversesIt(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, down := mig142Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	// the up
	mig105Apply(t, c, target)
	const want = 9 + 2 + 2
	if n := mig105Scalar[int](t, c, mig142Present); n != want {
		t.Fatalf("after the up, %d of the migration's objects exist, want %d", n, want)
	}
	// a second apply changes nothing
	mig105Apply(t, c, target)
	if n := mig105Scalar[int](t, c, mig142Present); n != want {
		t.Fatalf("after a second up, %d objects, want %d", n, want)
	}
	// the down removes every one of them
	mig105Apply(t, c, down)
	if n := mig105Scalar[int](t, c, mig142Present); n != 0 {
		t.Fatalf("after the down, %d of the migration's objects remain, want 0", n)
	}
	// and the up applies again
	mig105Apply(t, c, target)
	if n := mig105Scalar[int](t, c, mig142Present); n != want {
		t.Fatalf("after the down and up again, %d objects, want %d", n, want)
	}
}

// RULE: a formula cannot be stored without a base and a container size (the
// table's own NOT NULLs and its check hold it).
func TestMigration142_FormulaNeedsABaseAndASize(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, _ := mig142Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Apply(t, c, target)
	if _, err := c.Exec(t.Context(), `
		INSERT INTO paint_tint_systems (id, code, name, unit_denominator) VALUES ('00000000-0000-0000-0000-000000000142', 'GEN', 'Generic', 384);
		INSERT INTO paint_colors (id, tint_system_id, name) VALUES ('00000000-0000-0000-0000-000000000143', '00000000-0000-0000-0000-000000000142', 'Test Hill');
	`); err != nil {
		t.Fatalf("seed the system and colour: %v", err)
	}
	if _, err := c.Exec(t.Context(), `
		INSERT INTO paint_formulas (color_id, base_id, container_size_code, container_volume_oz)
		VALUES ('00000000-0000-0000-0000-000000000143', NULL, 'GAL', 128)`); err == nil {
		t.Fatal("a formula without a base was stored")
	}
	if _, err := c.Exec(t.Context(), `
		INSERT INTO paint_formulas (color_id, base_id, container_size_code, container_volume_oz)
		VALUES ('00000000-0000-0000-0000-000000000143', NULL, '', 128)`); err == nil {
		t.Fatal("a formula without a container size was stored")
	}
}

// RULE: a component cannot be stored with a denominator outside the trade's
// list (the check holds it), and the tinted line record holds exactly one
// line reference.
func TestMigration142_ComponentDenominatorAndOneLineReference(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	before, target, _ := mig142Files(t)
	for _, f := range before {
		mig105Apply(t, c, f)
	}
	mig105Apply(t, c, target)
	if _, err := c.Exec(t.Context(), `
		INSERT INTO paint_tint_systems (id, code, name, unit_denominator) VALUES ('00000000-0000-0000-0000-000000000142', 'GEN', 'Generic', 384);
		INSERT INTO paint_colorants (id, tint_system_id, code, name) VALUES ('00000000-0000-0000-0000-000000000144', '00000000-0000-0000-0000-000000000142', 'B1', 'Black');
	`); err != nil {
		t.Fatalf("seed the system and colorant: %v", err)
	}
	// a formula to hang a component on
	if _, err := c.Exec(t.Context(), `
		INSERT INTO paint_tint_systems (id, code, name, unit_denominator) VALUES ('00000000-0000-0000-0000-000000000145', 'GEN2', 'Generic Two', 384);
		INSERT INTO paint_colors (id, tint_system_id, name) VALUES ('00000000-0000-0000-0000-000000000146', '00000000-0000-0000-0000-000000000145', 'Test Hill');
		INSERT INTO paint_bases (id, tint_system_id, code, name, fill_volume_oz) VALUES ('00000000-0000-0000-0000-000000000147', '00000000-0000-0000-0000-000000000145', 'DEEP', 'Deep', 122);
		INSERT INTO paint_formulas (id, color_id, base_id, container_size_code, container_volume_oz)
		VALUES ('00000000-0000-0000-0000-000000000148', '00000000-0000-0000-0000-000000000146', '00000000-0000-0000-0000-000000000147', 'GAL', 128);
	`); err != nil {
		t.Fatalf("seed the formula: %v", err)
	}
	if _, err := c.Exec(t.Context(), `
		INSERT INTO paint_formula_components (formula_id, colorant_id, position, whole_oz, increments_coarse, increments_fine, unit_denominator)
		VALUES ('00000000-0000-0000-0000-000000000148', '00000000-0000-0000-0000-000000000144', 0, 1, 0, 0, 100)`); err == nil {
		t.Fatal("a component with a denominator of 100 was stored")
	}
	if _, err := c.Exec(t.Context(), `
		INSERT INTO paint_line_tints (color_name, container_size_code)
		VALUES ('Test Hill', 'GAL')`); err == nil {
		t.Fatal("a line tint with no line reference was stored")
	}
}
