// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tint

// The catalogue's store and rules (row CP-16): a formula reads back with its
// components in guide order, a component whose denominator is not the one
// its system declares is refused (so two systems in one catalogue do not
// collide), the rescale answers through the service, and a saved colour is
// unique per customer name.

import (
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

type seeded struct {
	sys      *System
	base     *Base
	colorant *Colorant
	color    *Color
}

func seed(t *testing.T) (*Service, seeded) {
	t.Helper()
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	svc := NewService(repo).WithTxRunner(db)
	ctx := t.Context()
	sys, err := svc.CreateSystem(ctx, &SystemDraft{Code: "GEN" + testCode(t), Name: "Generic",
		Manufacturer: "Generic Paints", UnitDenominator: 384, UnitLabel: "shot"})
	if err != nil {
		t.Fatalf("create system: %v", err)
	}
	base, err := svc.CreateBase(ctx, &BaseDraft{TintSystemID: sys.ID, Code: "DEEP", Name: "Deep base", FillVolumeOZ: 122})
	if err != nil {
		t.Fatalf("create base: %v", err)
	}
	colorant, err := svc.CreateColorant(ctx, &ColorantDraft{TintSystemID: sys.ID, Code: "B1", Name: "Black"})
	if err != nil {
		t.Fatalf("create colorant: %v", err)
	}
	color, err := svc.CreateColor(ctx, &ColorDraft{TintSystemID: sys.ID, ManufacturerCode: strPtr("HC-188"), Name: "Essex Green"})
	if err != nil {
		t.Fatalf("create colour: %v", err)
	}
	return svc, seeded{sys, base, colorant, color}
}

func testCode(t *testing.T) string {
	t.Helper()
	return "T" + uuid.NewString()[:8]
}

func newUUID() uuid.UUID { return uuid.New() }

func strPtr(s string) *string { return &s }

// RULE: a formula reads back with its components in guide order, and its
// total is the exact sum of the components.
func TestFormulaRoundTrip(t *testing.T) {
	svc, s := seed(t)
	ctx := t.Context()
	f, err := svc.CreateFormula(ctx, &FormulaDraft{
		ColorID: s.color.ID, BaseID: s.base.ID, ContainerSizeCode: "GAL", ContainerVolumeOZ: 128,
		Source: "book",
		Components: []StoredComponent{
			{ColorantID: s.colorant.ID, Code: "B1", Name: "Black", Amount: Amount{WholeOZ: 1, Coarse: 12, Denominator: 384}},
		},
	})
	if err != nil {
		t.Fatalf("create formula: %v", err)
	}
	if f.TotalColorantOZ != "1.3750" {
		t.Errorf("total = %s, want 1.3750 (1 OZ + 12/32)", f.TotalColorantOZ)
	}
	back, err := svc.GetFormula(ctx, f.ID)
	if err != nil {
		t.Fatalf("read formula: %v", err)
	}
	if len(back.Components) != 1 || back.Components[0].Amount.FineUnits() != 528 {
		t.Errorf("the component read back wrong: %+v (want 528 fine units)", back.Components)
	}
}

// RULE: a component's denominator must be the one its tint system declares.
func TestFormulaRefusesAComponentOfAnotherDenominator(t *testing.T) {
	svc, s := seed(t)
	_, err := svc.CreateFormula(t.Context(), &FormulaDraft{
		ColorID: s.color.ID, BaseID: s.base.ID, ContainerSizeCode: "GAL", ContainerVolumeOZ: 128,
		Components: []StoredComponent{
			{ColorantID: s.colorant.ID, Amount: Amount{WholeOZ: 1, Denominator: 48}},
		},
	})
	if err == nil {
		t.Fatal("a component in a 1/48 grid was stored on a 1/384 system")
	}
}

// RULE: a formula and its colorant amounts are one fact. A formula whose
// amounts cannot all be written leaves no formula row behind: a half written
// formula (a row with no amounts) would be sold at the counter.
func TestAFailedFormulaLeavesNoFormulaRow(t *testing.T) {
	svc, s := seed(t)
	ctx := t.Context()
	db := testutil.RequireDB(t)
	// the same colorant twice: the table's own unique index refuses the
	// second amount, after the formula row is already written
	_, err := svc.CreateFormula(ctx, &FormulaDraft{
		ColorID: s.color.ID, BaseID: s.base.ID, ContainerSizeCode: "GAL", ContainerVolumeOZ: 128,
		Components: []StoredComponent{
			{ColorantID: s.colorant.ID, Amount: Amount{WholeOZ: 1, Denominator: 384}},
			{ColorantID: s.colorant.ID, Amount: Amount{WholeOZ: 2, Denominator: 384}},
		},
	})
	if err == nil {
		t.Fatal("the same colorant twice in one formula was stored")
	}
	var n int
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM paint_formulas WHERE color_id = $1`, s.color.ID).Scan(&n); err != nil {
		t.Fatalf("count the formulas: %v", err)
	}
	if n != 0 {
		t.Errorf("%d formula rows survived a failed write, want 0", n)
	}
}

// RULE: the rescale through the service multiplies by the exact ratio and
// reports the rounding.
func TestServiceRescale(t *testing.T) {
	svc, s := seed(t)
	ctx := t.Context()
	f, err := svc.CreateFormula(ctx, &FormulaDraft{
		ColorID: s.color.ID, BaseID: s.base.ID, ContainerSizeCode: "GAL", ContainerVolumeOZ: 128,
		Components: []StoredComponent{
			{ColorantID: s.colorant.ID, Amount: Amount{WholeOZ: 1, Denominator: 384}},
		},
	})
	if err != nil {
		t.Fatalf("create formula: %v", err)
	}
	_, comps, rounded, err := svc.RescaleFormula(ctx, f.ID, 640) // one gallon to five
	if err != nil {
		t.Fatalf("rescale: %v", err)
	}
	if rounded || len(comps) != 1 || comps[0].Amount.WholeOZ != 5 {
		t.Errorf("the exact five fold rescale = %+v rounded=%v", comps, rounded)
	}
}

// RULE: a saved colour is unique per customer name; the second of the same
// name is a duplicate.
func TestSavedColorUniquePerName(t *testing.T) {
	svc, s := seed(t)
	ctx := t.Context()
	customer := newUUID()
	if _, err := testutil.RequireDB(t).Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, 'Tint Test Co', $2, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		customer, "TINT-"+customer.String()[:8]); err != nil {
		t.Fatalf("seed the customer: %v", err)
	}
	first, err := svc.CreateSavedColor(ctx, &SavedColorDraft{CustomerID: customer, ColorID: s.color.ID,
		Name: "Living room", JobNote: "living room"})
	if err != nil {
		t.Fatalf("save colour: %v", err)
	}
	if _, err := svc.CreateSavedColor(ctx, &SavedColorDraft{CustomerID: customer, ColorID: s.color.ID,
		Name: "Living room"}); err == nil {
		t.Fatal("the same name saved twice for one customer")
	}
	// the job note filters
	list, err := svc.ListSavedColors(ctx, &first.CustomerID, "living")
	if err != nil || len(list) != 1 {
		t.Errorf("list by job note = %v, %v", list, err)
	}
	if err := svc.DeleteSavedColor(ctx, first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
