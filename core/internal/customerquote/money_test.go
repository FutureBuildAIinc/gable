// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"errors"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

func TestSellCents_MarkupOnCost(t *testing.T) {
	cases := []struct {
		name string
		cost int64
		bps  int64
		want int64
	}{
		{"twenty five percent", 10000, 2500, 12500},
		{"zero markup is cost", 7777, 0, 7777},
		{"half a cent rounds up", 1, 5000, 2},
		{"just under half a cent rounds down", 1, 4999, 1},
		{"zero cost stays zero", 0, 5000, 0},
		{"a markup past 100 percent", 1000, 15000, 2500},
		{"repeating third", 100, 3333, 133},
	}
	for _, c := range cases {
		got, err := SellCents(c.cost, Markup{Kind: KindMarkup, BPS: c.bps})
		if err != nil || got != c.want {
			t.Errorf("%s: SellCents(%d, markup %d) = %d, %v; want %d", c.name, c.cost, c.bps, got, err, c.want)
		}
	}
}

func TestSellCents_TargetMargin(t *testing.T) {
	cases := []struct {
		name string
		cost int64
		bps  int64
		want int64
	}{
		{"twenty percent margin is a quarter over cost", 10000, 2000, 12500},
		{"fifty percent margin doubles cost", 10000, 5000, 20000},
		{"zero margin is cost", 4321, 0, 4321},
		{"half a cent rounds up", 3, 3333, 4},
		{"ninety nine point ninety nine percent", 1, 9999, 10000},
	}
	for _, c := range cases {
		got, err := SellCents(c.cost, Markup{Kind: KindMargin, BPS: c.bps})
		if err != nil || got != c.want {
			t.Errorf("%s: SellCents(%d, margin %d) = %d, %v; want %d", c.name, c.cost, c.bps, got, err, c.want)
		}
	}
}

func TestMarkupValidate_RefusesMarginAtOrAbove100Percent(t *testing.T) {
	for _, bps := range []int64{10000, 10001, 20000, 1_000_000} {
		err := Markup{Kind: KindMargin, BPS: bps}.Validate()
		if !errors.Is(err, ErrMarginTooHigh) {
			t.Errorf("margin %d bps: err = %v, want ErrMarginTooHigh", bps, err)
		}
		if _, err := SellCents(100, Markup{Kind: KindMargin, BPS: bps}); !errors.Is(err, ErrMarginTooHigh) {
			t.Errorf("SellCents with margin %d bps: err = %v, want ErrMarginTooHigh", bps, err)
		}
	}
	if err := (Markup{Kind: KindMargin, BPS: 9999}).Validate(); err != nil {
		t.Errorf("margin 9999 bps refused: %v", err)
	}
	for _, bad := range []Markup{
		{Kind: KindMarkup, BPS: -1},
		{Kind: KindMargin, BPS: -1},
		{Kind: KindMarkup, BPS: MaxMarkupBPS + 1},
		{Kind: "discount", BPS: 10},
		{Kind: "", BPS: 10},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}

func TestSellCents_RefusesNegativeAndOverflow(t *testing.T) {
	if _, err := SellCents(-1, Markup{Kind: KindMarkup, BPS: 100}); err == nil {
		t.Error("a negative cost was accepted")
	}
	if _, err := SellCents(MaxCostCents+1, Markup{Kind: KindMarkup, BPS: 100}); err == nil {
		t.Error("a cost past the bound was accepted")
	}
	if got, err := SellCents(MaxCostCents, Markup{Kind: KindMarkup, BPS: MaxMarkupBPS}); err != nil || got <= 0 {
		t.Errorf("the largest cost at the largest markup: %d, %v", got, err)
	}
	if got, err := SellCents(MaxCostCents, Markup{Kind: KindMargin, BPS: 9999}); err != nil || got <= 0 {
		t.Errorf("the largest cost at the largest margin: %d, %v", got, err)
	}
}

func TestResolveMarkup_MostSpecificWins(t *testing.T) {
	quote := Markup{Kind: KindMarkup, BPS: 1000}
	category := &Markup{Kind: KindMargin, BPS: 2500}
	line := &Markup{Kind: KindMarkup, BPS: 4000}

	if m, tier := ResolveMarkup(line, category, quote); tier != TierLine || m != *line {
		t.Errorf("line override: got %+v at %s", m, tier)
	}
	if m, tier := ResolveMarkup(nil, category, quote); tier != TierCategory || m != *category {
		t.Errorf("category rule: got %+v at %s", m, tier)
	}
	if m, tier := ResolveMarkup(nil, nil, quote); tier != TierQuote || m != quote {
		t.Errorf("quote default: got %+v at %s", m, tier)
	}
	// A zero override is still an override: the line says "no markup".
	zero := &Markup{Kind: KindMarkup, BPS: 0}
	if m, tier := ResolveMarkup(zero, category, quote); tier != TierLine || m.BPS != 0 {
		t.Errorf("a zero line override lost to a wider tier: %+v at %s", m, tier)
	}
}

func TestPriceLines_AppliesEachTierAndSumsRoundedLines(t *testing.T) {
	catA, catB := uuid.New(), uuid.New()
	lines := []MaterialLine{
		{ID: uuid.New(), CostCents: 10000, Override: &Markup{Kind: KindMarkup, BPS: 5000}, CategoryID: &catA}, // line wins: 15000
		{ID: uuid.New(), CostCents: 10000, CategoryID: &catA},                                                 // category margin 20%: 12500
		{ID: uuid.New(), CostCents: 10000, CategoryID: &catB},                                                 // quote default markup 10%: 11000
		{ID: uuid.New(), CostCents: 333},                                                                      // no category: quote default: 366 (366.3)
	}
	rules := map[uuid.UUID]Markup{catA: {Kind: KindMargin, BPS: 2000}}
	if err := PriceLines(lines, rules, Markup{Kind: KindMarkup, BPS: 1000}); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		tier Tier
		sell int64
	}{{TierLine, 15000}, {TierCategory, 12500}, {TierQuote, 11000}, {TierQuote, 366}}
	for i, w := range want {
		if lines[i].Tier != w.tier || lines[i].SellCents != w.sell {
			t.Errorf("line %d: tier %s sell %d, want %s %d", i, lines[i].Tier, lines[i].SellCents, w.tier, w.sell)
		}
	}
}

func TestPriceLines_RefusesAMarginAt100PercentInAnyTier(t *testing.T) {
	cat := uuid.New()
	base := func() []MaterialLine { return []MaterialLine{{ID: uuid.New(), CostCents: 100, CategoryID: &cat}} }
	if err := PriceLines(base(), nil, Markup{Kind: KindMargin, BPS: 10000}); !errors.Is(err, ErrMarginTooHigh) {
		t.Errorf("quote default: %v", err)
	}
	if err := PriceLines(base(), map[uuid.UUID]Markup{cat: {Kind: KindMargin, BPS: 10000}}, Markup{Kind: KindMarkup}); !errors.Is(err, ErrMarginTooHigh) {
		t.Errorf("category rule: %v", err)
	}
	l := base()
	l[0].Override = &Markup{Kind: KindMargin, BPS: 12000}
	if err := PriceLines(l, nil, Markup{Kind: KindMarkup}); !errors.Is(err, ErrMarginTooHigh) {
		t.Errorf("line override: %v", err)
	}
}

func TestExtendedCostCents(t *testing.T) {
	q := func(s string) httpx.Quantity {
		v, err := httpx.ParseQuantity(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	got, err := ExtendedCostCents(q("10"), 12500)
	if err != nil || got != 1250 {
		t.Errorf("10 x $1.25 = %d, %v; want 1250", got, err)
	}
	got, err = ExtendedCostCents(q("1.5"), 14025) // $2.10375
	if err != nil || got != 210 {
		t.Errorf("1.5 x $1.4025 = %d, %v; want 210", got, err)
	}
	got, err = ExtendedCostCents(q("3"), 3333) // $0.9999
	if err != nil || got != 100 {
		t.Errorf("3 x $0.3333 = %d, %v; want 100", got, err)
	}
	got, err = ExtendedCostCents(q("1000"), 4567) // $456.70
	if err != nil || got != 45670 {
		t.Errorf("1000 x $0.4567 = %d, %v; want 45670", got, err)
	}
	if _, err := ExtendedCostCents(q("0"), 100); err == nil {
		t.Error("a zero quantity was accepted")
	}
	if _, err := ExtendedCostCents(q("1"), -1); err == nil {
		t.Error("a negative unit cost was accepted")
	}
}

func TestLabourAndOverheadTotals(t *testing.T) {
	hours := func(s string) httpx.Quantity { v, _ := httpx.ParseQuantity(s); return v }
	if got, err := LabourTotalCents(LabourLine{RateType: RateFlat, RateCents: 50000}); err != nil || got != 50000 {
		t.Errorf("flat labour: %d, %v", got, err)
	}
	if got, err := LabourTotalCents(LabourLine{RateType: RateHourly, RateCents: 6500, Hours: hours("7.5")}); err != nil || got != 48750 {
		t.Errorf("hourly labour 7.5 x $65: %d, %v", got, err)
	}
	if got, err := LabourTotalCents(LabourLine{RateType: RateHourly, RateCents: 3333, Hours: hours("0.25")}); err != nil || got != 833 {
		t.Errorf("hourly labour 0.25 x $33.33 = 8.3325 -> 833: %d, %v", got, err)
	}
	if _, err := LabourTotalCents(LabourLine{RateType: RateHourly, RateCents: 100}); err == nil {
		t.Error("hourly labour with no hours was accepted")
	}
	if _, err := LabourTotalCents(LabourLine{RateType: "weekly", RateCents: 100}); err == nil {
		t.Error("an unknown labour rate type was accepted")
	}
	if got, err := OverheadTotalCents(OverheadLine{AmountType: AmountFlat, Value: 2500}, 99999); err != nil || got != 2500 {
		t.Errorf("flat overhead: %d, %v", got, err)
	}
	if got, err := OverheadTotalCents(OverheadLine{AmountType: AmountPercent, Value: 750}, 100000); err != nil || got != 7500 {
		t.Errorf("percent overhead 7.5%% of $1000: %d, %v", got, err)
	}
	if _, err := OverheadTotalCents(OverheadLine{AmountType: "other", Value: 1}, 1); err == nil {
		t.Error("an unknown overhead type was accepted")
	}
}

func TestCompute_RefusesLabourAndOverheadOverflow(t *testing.T) {
	hours, _ := httpx.ParseQuantity("900000")
	// Single hourly labour line past MaxCostCents.
	if _, err := LabourTotalCents(LabourLine{RateType: RateHourly, RateCents: MaxCostCents, Hours: hours}); err == nil {
		t.Error("an hourly labour line past MaxCostCents was accepted")
	}
	// Single percent overhead line past MaxCostCents.
	if _, err := OverheadTotalCents(OverheadLine{AmountType: AmountPercent, Value: MaxMarkupBPS}, MaxCostCents); err == nil {
		t.Error("a percent overhead line past MaxCostCents was accepted")
	}
	// Eleven large labour lines that would exceed MaxCostCents in Compute.
	var manyLabour []LabourLine
	for i := 0; i < 11; i++ {
		manyLabour = append(manyLabour, LabourLine{RateType: RateHourly, RateCents: MaxCostCents, Hours: hours})
	}
	if _, err := Compute(TotalsInput{Labour: manyLabour, TaxMode: TaxAtCheckout}); err == nil {
		t.Error("eleven huge hourly labour lines were accepted by Compute")
	}
	// Running sum of flat labour lines past MaxCostCents in Compute.
	if _, err := Compute(TotalsInput{
		Labour: []LabourLine{
			{RateType: RateFlat, RateCents: MaxCostCents},
			{RateType: RateFlat, RateCents: 1},
		},
		TaxMode: TaxAtCheckout,
	}); err == nil {
		t.Error("a running labour sum past MaxCostCents was accepted by Compute")
	}
}

// totalsFixture is the quote the totals tests share: two standalone material
// lines, one two option group, labour (one customer facing, one contractor
// only), overhead (flat customer facing, percent contractor only).
func totalsFixture() (TotalsInput, map[string]uuid.UUID) {
	ids := map[string]uuid.UUID{}
	id := func(n string) uuid.UUID { ids[n] = uuid.New(); return ids[n] }
	g := id("group")
	gp := &g
	hours, _ := httpx.ParseQuantity("10")
	in := TotalsInput{
		Lines: []MaterialLine{
			{ID: id("a"), CostCents: 10000, SellCents: 12000},
			{ID: id("b"), CostCents: 5000, SellCents: 6000},
			{ID: id("opt1"), GroupID: gp, CostCents: 20000, SellCents: 24000},
			{ID: id("opt2"), GroupID: gp, CostCents: 30000, SellCents: 36000},
		},
		Labour: []LabourLine{
			{RateType: RateHourly, RateCents: 5000, Hours: hours},        // customer facing 50000
			{RateType: RateFlat, RateCents: 10000, ContractorOnly: true}, // own cost 10000
		},
		Overhead: []OverheadLine{
			{AmountType: AmountFlat, Value: 3000},                          // customer facing 3000
			{AmountType: AmountPercent, Value: 1000, ContractorOnly: true}, // 10% of the customer facing base
		},
		TaxMode:    TaxSiteRate,
		TaxRateBPS: 500,
		DepositBPS: 2500,
	}
	return in, ids
}

func TestCompute_PendingIncludesStandaloneAndFirstOfGroup(t *testing.T) {
	in, _ := totalsFixture()
	tot, err := Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	// materials: a 12000 + b 6000 + opt1 24000 = 42000
	if tot.MaterialSellCents != 42000 || tot.MaterialCostCents != 35000 {
		t.Errorf("materials sell %d cost %d, want 42000 and 35000", tot.MaterialSellCents, tot.MaterialCostCents)
	}
	if tot.LabourCents != 50000 || tot.OverheadCents != 3000 {
		t.Errorf("customer facing labour %d overhead %d, want 50000 and 3000", tot.LabourCents, tot.OverheadCents)
	}
	if tot.SubtotalCents != 95000 {
		t.Errorf("subtotal %d, want 95000", tot.SubtotalCents)
	}
	// tax: 5% of materials sell only = 2100
	if tot.TaxCents != 2100 || tot.TotalCents != 97100 {
		t.Errorf("tax %d total %d, want 2100 and 97100", tot.TaxCents, tot.TotalCents)
	}
	if tot.DepositCents != 24275 {
		t.Errorf("deposit %d, want 24275 (25%% of 97100)", tot.DepositCents)
	}
}

func TestCompute_ContractorOnlyLinesNeverReachTheCustomerTotalButEnterTheMargin(t *testing.T) {
	in, _ := totalsFixture()
	tot, err := Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	// contractor only: labour 10000 + 10% of base (materials sell 42000 + customer labour 50000 = 92000) = 9200
	if tot.ContractorCostCents != 19200 {
		t.Errorf("contractor only cost %d, want 19200", tot.ContractorCostCents)
	}
	// margin = subtotal 95000 - material cost 35000 - contractor only 19200 = 40800
	if tot.MarginCents != 40800 {
		t.Errorf("margin %d, want 40800", tot.MarginCents)
	}
	want := int64(40800) * 10000 / 95000 // 4294 bps, rounded below
	if tot.MarginBPS < want || tot.MarginBPS > want+1 {
		t.Errorf("margin bps %d, want about %d", tot.MarginBPS, want)
	}
}

func TestCompute_SelectionStates(t *testing.T) {
	in, ids := totalsFixture()
	in.States = map[uuid.UUID]SelectionState{
		ids["a"]:    SelectionDeclined,
		ids["opt2"]: SelectionApproved,
		ids["opt1"]: SelectionDeclined,
	}
	tot, err := Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	// a declined, b pending (kept), opt2 chosen: 6000 + 36000
	if tot.MaterialSellCents != 42000 {
		t.Errorf("materials sell %d, want 42000", tot.MaterialSellCents)
	}
	// the whole group declined leaves nothing of it
	in.States[ids["opt2"]] = SelectionDeclined
	tot, _ = Compute(in)
	if tot.MaterialSellCents != 6000 {
		t.Errorf("with the group declined: %d, want 6000", tot.MaterialSellCents)
	}
}

func TestCompute_TaxModes(t *testing.T) {
	in, _ := totalsFixture()
	in.TaxMode = TaxIncluded
	tot, _ := Compute(in)
	if tot.TaxCents != 0 || tot.TotalCents != tot.SubtotalCents {
		t.Errorf("tax included: tax %d total %d subtotal %d", tot.TaxCents, tot.TotalCents, tot.SubtotalCents)
	}
	in.TaxMode = TaxAtCheckout
	tot, _ = Compute(in)
	if tot.TaxCents != 0 || tot.TotalCents != tot.SubtotalCents {
		t.Errorf("material tax at checkout: tax %d total %d subtotal %d", tot.TaxCents, tot.TotalCents, tot.SubtotalCents)
	}
	in.TaxMode = "vat"
	if _, err := Compute(in); err == nil {
		t.Error("an unknown tax mode was accepted")
	}
	in.TaxMode = TaxSiteRate
	in.TaxRateBPS = 3333 // 33.33% of 42000 = 13998.6 -> 13999
	tot, _ = Compute(in)
	if tot.TaxCents != 13999 {
		t.Errorf("tax at 33.33%%: %d, want 13999", tot.TaxCents)
	}
}

func TestCompute_DepositBounds(t *testing.T) {
	in, _ := totalsFixture()
	in.DepositBPS = 0
	if tot, _ := Compute(in); tot.DepositCents != 0 {
		t.Errorf("no deposit: %d", tot.DepositCents)
	}
	in.DepositBPS = 10000
	tot, _ := Compute(in)
	if tot.DepositCents != tot.TotalCents {
		t.Errorf("full deposit %d vs total %d", tot.DepositCents, tot.TotalCents)
	}
	in.DepositBPS = 10001
	if _, err := Compute(in); err == nil {
		t.Error("a deposit past 100 percent was accepted")
	}
}

func TestCompute_TotalsAreSumsOfRoundedLines(t *testing.T) {
	// Three lines of 1 cent cost at a 50 percent markup: each rounds to 2, the
	// total is 6, not the 4 (4.5 rounded) a single rounding of the cost sum gives.
	lines := []MaterialLine{{ID: uuid.New(), CostCents: 1}, {ID: uuid.New(), CostCents: 1}, {ID: uuid.New(), CostCents: 1}}
	if err := PriceLines(lines, nil, Markup{Kind: KindMarkup, BPS: 5000}); err != nil {
		t.Fatal(err)
	}
	tot, err := Compute(TotalsInput{Lines: lines, TaxMode: TaxAtCheckout})
	if err != nil {
		t.Fatal(err)
	}
	if tot.MaterialSellCents != 6 {
		t.Errorf("sum of rounded lines = %d, want 6", tot.MaterialSellCents)
	}
}

func TestBoundValidUntil(t *testing.T) {
	now := time.Date(2030, 1, 10, 12, 0, 0, 0, time.UTC)
	sourceExpiry := now.Add(5 * 24 * time.Hour)
	catalogHorizon := now.Add(30 * 24 * time.Hour)

	// A source dealer quote bounds the request; catalog lines add the configured horizon.
	got := BoundValidUntil(now.Add(20*24*time.Hour), &sourceExpiry, false, catalogHorizon)
	if !got.Equal(sourceExpiry) {
		t.Errorf("source quote expiry should bound: %v", got)
	}
	// Catalog lines only: the configured horizon bounds.
	got = BoundValidUntil(now.Add(60*24*time.Hour), nil, true, catalogHorizon)
	if !got.Equal(catalogHorizon) {
		t.Errorf("catalog horizon should bound: %v", got)
	}
	// A request inside both is kept.
	req := now.Add(2 * 24 * time.Hour)
	if got := BoundValidUntil(req, &sourceExpiry, true, catalogHorizon); !got.Equal(req) {
		t.Errorf("an inside request moved: %v", got)
	}
	// Both: the tighter of the two.
	got = BoundValidUntil(now.Add(90*24*time.Hour), &sourceExpiry, true, catalogHorizon)
	if !got.Equal(sourceExpiry) {
		t.Errorf("tighter of source and catalog: %v", got)
	}
	// A source quote with no expiry (nil) and no catalog lines bounds nothing.
	if got := BoundValidUntil(req, nil, false, catalogHorizon); !got.Equal(req) {
		t.Errorf("nothing to bound by: %v", got)
	}
}
