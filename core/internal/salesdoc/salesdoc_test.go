// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"encoding/json"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

func mustQ(t *testing.T, s string) httpx.Quantity {
	t.Helper()
	q, err := httpx.ParseQuantity(s)
	if err != nil {
		t.Fatalf("ParseQuantity(%q): %v", s, err)
	}
	return q
}

func fieldErrors(t *testing.T, v *httpx.Validator) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := v.Err()
	if err == nil {
		return out
	}
	he, ok := err.(*httpx.Error)
	if !ok {
		t.Fatalf("validator error is %T, want *httpx.Error", err)
	}
	for _, d := range he.Details {
		out[d.Field] = d.Message
	}
	return out
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func rawq(s string) json.RawMessage { return json.RawMessage(`"` + s + `"`) }

// RULE (ADR 0005 2.1, one row per line type): each line type's priced fields
// are required or forbidden exactly as the table says. A text line that sends
// a price, a charge line without a code and a product line without a unit or
// a product are each refused with the field named.
func TestParseLinesPerTypeRules(t *testing.T) {
	prod := uuid.New()
	cases := []struct {
		name  string
		lines []LineRequest
		want  map[string]string
	}{
		{"a text line with a price is refused", []LineRequest{{LineType: strp("text"), Description: strp("note"), UnitPriceTenThou: raw("1000")}},
			map[string]string{"lines[0].unit_price_ten_thousandths": "is a priced field: a text line carries only a description"}},
		{"a text line without a description is refused", []LineRequest{{}},
			map[string]string{"lines[0].description": "is required on a text line"}},
		{"a charge line without a code is refused", []LineRequest{{LineType: strp("charge"), Description: strp("freight"), Quantity: rawq("1"), UnitPriceTenThou: raw("1000")}},
			map[string]string{"lines[0].charge_code": "is required on a charge line"}},
		{"a charge line with a lowercase code is refused", []LineRequest{{Description: strp("freight"), ChargeCode: strp("freight")}},
			map[string]string{"lines[0].charge_code": "must be a charge code of one to sixteen capital letters, digits or underscores"}},
		{"a product line with neither unit nor product is refused", []LineRequest{{Description: strp("thing"), Quantity: rawq("1")}},
			map[string]string{"lines[0].uom": "is required when the line names no product"}},
		{"a non stock line without a price is refused", []LineRequest{{Description: strp("thing"), Quantity: rawq("1"), UOM: strp("EA")}},
			map[string]string{"lines[0].unit_price_ten_thousandths": "is required on a non stock line: there is no product to price it from"}},
		{"a kit line is never a request", []LineRequest{{LineType: strp("kit"), ProductID: idp(prod)}},
			map[string]string{"lines[0].line_type": "is written by the server when a line names a kit product; send a product line"}},
		{"a charge line cannot name a product", []LineRequest{{ChargeCode: strp("FREIGHT"), ProductID: idp(prod)}},
			map[string]string{"lines[0].product_id": "belongs to a product line; a charge line names a charge_code"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &httpx.Validator{}
			got := ParseLines(v, tc.lines)
			errs := fieldErrors(t, v)
			if len(errs) == 0 {
				t.Fatalf("parse accepted the lines: %+v", got)
			}
			for field, want := range tc.want {
				if errs[field] != want {
					t.Errorf("field %s = %q, want %q (all: %v)", field, errs[field], want, errs)
				}
			}
		})
	}
}

// RULE (the carried item of R1-15's review): when price_uom equals uom the
// conversion pair must be 1 and 1; any other pair is a 400 on uom_qty.
func TestParseLinesEqualUnitsRequirePairOneAndOne(t *testing.T) {
	v := &httpx.Validator{}
	ParseLines(v, []LineRequest{{
		ProductID: idp(uuid.New()), Description: strp("stud"), Quantity: rawq("10"),
		UOM: strp("EA"), PriceUOM: strp("EA"), UOMQty: rawq("12"), PriceUOMQty: rawq("1"),
		UnitPriceTenThou: raw("1000"), OverrideReason: strp("match the quote"),
	}})
	errs := fieldErrors(t, v)
	if errs["lines[0].uom_qty"] != "must be 1 when price_uom equals uom: the units agree, so the pair is 1 and 1" {
		t.Fatalf("uom_qty error = %q, want the equal units pair rule (all: %v)", errs["lines[0].uom_qty"], errs)
	}

	// A different price_uom keeps requiring the pair; an equal pair of 1 and 1
	// passes.
	v = &httpx.Validator{}
	ParseLines(v, []LineRequest{{
		ProductID: idp(uuid.New()), Description: strp("stud"), Quantity: rawq("10"),
		UOM: strp("EA"), UOMQty: rawq("1"), PriceUOMQty: rawq("1"), UnitPriceTenThou: raw("1000"), OverrideReason: strp("x"),
	}})
	if err := v.Err(); err != nil {
		t.Fatalf("a 1 and 1 pair on equal units was refused: %v", err)
	}
}

// RULE (ADR 0005 2.3): an override is a price beside the engine's, and it
// names its reason; a percent and an amount discount never travel together,
// and either carries its reason.
func TestParseLinesOverrideAndDiscounts(t *testing.T) {
	base := func(mut func(*LineRequest)) LineRequest {
		l := LineRequest{
			ProductID: idp(uuid.New()), Description: strp("stud"), Quantity: rawq("10"),
			UOM: strp("EA"), UnitPriceTenThou: raw("1000"),
		}
		mut(&l)
		return l
	}
	cases := []struct {
		name string
		line LineRequest
		want string
	}{
		{"an override without a reason", base(func(l *LineRequest) {}), "lines[0].override_reason"},
		{"a reason without a price", base(func(l *LineRequest) { l.UnitPriceTenThou = nil; l.OverrideReason = strp("x") }), "lines[0].override_reason"},
		{"both discounts", base(func(l *LineRequest) {
			l.OverrideReason = strp("x")
			l.DiscountPercent = rawq("5")
			l.DiscountCents = raw("100")
		}), "lines[0].discount_percent"},
		{"a discount without a reason", base(func(l *LineRequest) {
			l.OverrideReason = strp("x")
			l.DiscountPercent = rawq("5")
		}), "lines[0].discount_reason"},
		{"a discount above one hundred percent", base(func(l *LineRequest) {
			l.OverrideReason = strp("x")
			l.DiscountPercent = rawq("101")
		}), "lines[0].discount_percent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &httpx.Validator{}
			ParseLines(v, []LineRequest{tc.line})
			errs := fieldErrors(t, v)
			if _, ok := errs[tc.want]; !ok {
				t.Fatalf("no error on %s (all: %v)", tc.want, errs)
			}
		})
	}

	// A manual price on a non stock or charge line needs no reason.
	v := &httpx.Validator{}
	ParseLines(v, []LineRequest{{
		Description: strp("special thing"), Quantity: rawq("2"), UOM: strp("EA"),
		UnitPriceTenThou: raw("25000"),
	}})
	if err := v.Err(); err != nil {
		t.Fatalf("a non stock manual price was refused: %v", err)
	}
}

// RULE (ADR 0005 2.4): the extension of each discount kind, and the refusal
// of an amount discount larger than the extension.
func TestExtendLine(t *testing.T) {
	line := func(mut func(*Line)) *Line {
		qty, uq, pq := mustQ(t, "10"), One, One
		price := httpx.Price(15000)
		total := httpx.Cents(1500)
		l := Line{ID: uuid.New(), LineType: LineProduct, Quantity: &qty, UOMQty: &uq, PriceUOMQty: &pq,
			UnitPrice: &price, LineTotal: &total}
		mut(&l)
		return &l
	}
	l := line(func(*Line) {})
	if err := ExtendLine(l); err != nil {
		t.Fatal(err)
	}
	if *l.LineTotal != 1500 {
		t.Errorf("plain extension = %d, want 1500", *l.LineTotal)
	}

	pct := mustQ(t, "10")
	l = line(func(l *Line) { l.DiscountPercent = &pct })
	if err := ExtendLine(l); err != nil {
		t.Fatal(err)
	}
	if *l.LineTotal != 1350 {
		t.Errorf("percent extension = %d, want 1350", *l.LineTotal)
	}

	amt := httpx.Cents(200)
	l = line(func(l *Line) { l.DiscountAmount = &amt })
	if err := ExtendLine(l); err != nil {
		t.Fatal(err)
	}
	if *l.LineTotal != 1300 {
		t.Errorf("amount extension = %d, want 1300", *l.LineTotal)
	}

	big := httpx.Cents(9999)
	l = line(func(l *Line) { l.DiscountAmount = &big })
	if err := ExtendLine(l); err == nil {
		t.Fatal("a discount larger than the extension was accepted")
	}

	text := Line{ID: uuid.New(), LineType: LineText}
	if err := ExtendLine(&text); err != nil {
		t.Fatal(err)
	}
	if text.LineTotal != nil {
		t.Errorf("text line total = %v, want nil", text.LineTotal)
	}
}

// RULE (ADR 0005 2.6): a kit line explodes into its components, quantity =
// kit quantity x per kit quantity, unit price 0, extension 0, taxable false;
// a kit containing a kit is refused.
func TestExplode(t *testing.T) {
	kit := uuid.New()
	comp := uuid.New()
	inner := uuid.New()
	refs := map[string]ProductRef{
		kit.String():   {ID: kit, SKU: "KIT-1", Description: "a kit", UOMPrimary: "EA", IsKit: true},
		comp.String():  {ID: comp, SKU: "C-1", Description: "a component", UOMPrimary: "PCS"},
		inner.String(): {ID: inner, SKU: "K-2", Description: "a nested kit", UOMPrimary: "EA", IsKit: true},
	}
	kits := map[string][]KitComponent{
		kit.String(): {{KitProductID: kit, ComponentProductID: comp, Quantity: mustQ(t, "24"), Position: 0}},
	}
	qty, uq, pq := mustQ(t, "2"), One, One
	price := httpx.Price(50000)
	ea := "EA"
	lines := []Line{{
		ID: uuid.New(), LineType: LineProduct, ProductID: &kit, Description: "a kit",
		Quantity: &qty, UOM: &ea, PriceUOM: &ea, UOMQty: &uq, PriceUOMQty: &pq,
		UnitPrice: &price, PriceSource: PriceSourceList, Taxable: true,
	}}
	out, err := Explode(lines, kits, refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("explosion made %d lines, want 2 (kit + component)", len(out))
	}
	if out[0].LineType != LineKit || out[1].LineType != LineComponent {
		t.Fatalf("line types = %s, %s, want KIT, COMPONENT", out[0].LineType, out[1].LineType)
	}
	if out[1].ParentLineID == nil || *out[1].ParentLineID != out[0].ID {
		t.Error("the component's parent is not the kit line")
	}
	if got := *out[1].Quantity; got != mustQ(t, "48") {
		t.Errorf("component quantity = %s, want 48", got.WireString())
	}
	if *out[1].UnitPrice != 0 || *out[1].LineTotal != 0 || out[1].Taxable {
		t.Error("a component carries no price, no extension and no tax")
	}
	if out[1].PriceSource != PriceSourceNone {
		t.Errorf("component price source = %s, want NONE", out[1].PriceSource)
	}
	if out[0].Position != 0 || out[1].Position != 1 {
		t.Errorf("positions = %d, %d, want 0, 1", out[0].Position, out[1].Position)
	}

	nested := map[string][]KitComponent{
		kit.String(): {{KitProductID: kit, ComponentProductID: inner, Quantity: One}},
	}
	if _, err := Explode(lines, nested, refs); err == nil {
		t.Fatal("a kit containing a kit was accepted")
	}
}

// RULE (review P3-17): a component's quantity is rounded to scale 4 once,
// half away from zero, so a 0.3333 kit of a 0.3333 component stores 0.1111
// (the exact product is 0.11108889). ADR 0005 2.6 is silent on a kit that
// does not land on scale 4; the rounding is the stated behaviour.
func TestExplodeRoundsAComponentQuantityToScale4(t *testing.T) {
	kit, comp := uuid.New(), uuid.New()
	refs := map[string]ProductRef{
		kit.String():  {ID: kit, SKU: "KIT-1", Description: "a kit", UOMPrimary: "EA", IsKit: true},
		comp.String(): {ID: comp, SKU: "C-1", Description: "a component", UOMPrimary: "PCS"},
	}
	kits := map[string][]KitComponent{
		kit.String(): {{KitProductID: kit, ComponentProductID: comp, Quantity: mustQ(t, "0.3333")}},
	}
	qty, uq, pq := mustQ(t, "0.3333"), One, One
	price := httpx.Price(50000)
	ea := "EA"
	out, err := Explode([]Line{{
		ID: uuid.New(), LineType: LineProduct, ProductID: &kit, Description: "a kit",
		Quantity: &qty, UOM: &ea, PriceUOM: &ea, UOMQty: &uq, PriceUOMQty: &pq,
		UnitPrice: &price, PriceSource: PriceSourceList, Taxable: true,
	}}, kits, refs)
	if err != nil {
		t.Fatal(err)
	}
	if got := out[1].Quantity.WireString(); got != "0.1111" {
		t.Errorf("component quantity = %s, want 0.1111", got)
	}
}

// RULE (ADR 0005 3): totals sum the non text lines, the taxable base, and the
// tax is rounded once from the rate; the resolver walks exemption, ship-to
// rate, branch rate, refusal; zero is a configured rate.
func TestTotalsAndTax(t *testing.T) {
	taxable := httpx.Cents(1000)
	untaxed := httpx.Cents(500)
	lines := []Line{
		{LineType: LineProduct, LineTotal: &taxable, Taxable: true},
		{LineType: LineProduct, LineTotal: &untaxed, Taxable: false},
		{LineType: LineText},
	}
	got := SumTotals(lines)
	if got.SubtotalCents != 1500 || got.TaxableCents != 1000 {
		t.Fatalf("subtotal=%d taxable=%d, want 1500 and 1000", got.SubtotalCents, got.TaxableCents)
	}

	// 0.08875 of 1000 cents is 88.75, rounded once to 89.
	rate, ok, err := ParseTaxRate("0.088750")
	if err != nil || !ok {
		t.Fatalf("ParseTaxRate: ok=%v err=%v", ok, err)
	}
	if tax := TaxAt(1000, rate); tax != 89 {
		t.Errorf("TaxAt(1000, 0.088750) = %d, want 89", tax)
	}
	// A 0.0825 rate of 100000 cents is exactly 8250.
	rate, _, _ = ParseTaxRate("0.082500")
	if tax := TaxAt(100000, rate); tax != 8250 {
		t.Errorf("TaxAt(100000, 0.0825) = %d, want 8250", tax)
	}

	shipTo := "0.100000"
	branch := "0.050000"
	zero := "0.000000"
	cases := []struct {
		name string
		in   TaxInputs
		want TaxSource
		rate string
	}{
		{"exempt first", TaxInputs{Exempt: true, Delivery: true, ShipToRate: &shipTo, BranchRate: &branch}, TaxSourceExempt, "0.000000"},
		{"a delivery takes the ship-to rate", TaxInputs{Delivery: true, ShipToRate: &shipTo, BranchRate: &branch}, TaxSourceShipToRate, "0.100000"},
		{"a pickup takes the branch rate", TaxInputs{Delivery: false, ShipToRate: &shipTo, BranchRate: &branch}, TaxSourceBranchRate, "0.050000"},
		{"a delivery with no ship-to rate falls to the branch", TaxInputs{Delivery: true, BranchRate: &branch}, TaxSourceBranchRate, "0.050000"},
		{"zero is a configured rate", TaxInputs{BranchRate: &zero}, TaxSourceBranchRate, "0.000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveTax(tc.in)
			if err != nil {
				t.Fatalf("ResolveTax: %v", err)
			}
			if got.Source != tc.want || got.Rate == nil || *got.Rate != tc.rate {
				t.Errorf("ResolveTax = %s %v, want %s %s", got.Source, got.Rate, tc.want, tc.rate)
			}
		})
	}
	if _, err := ResolveTax(TaxInputs{}); err != ErrTaxRateNotConfigured {
		t.Errorf("ResolveTax with nothing configured = %v, want ErrTaxRateNotConfigured", err)
	}
}

func strp(s string) *string { return &s }
func idp(id uuid.UUID) *string {
	s := id.String()
	return &s
}
func uom(s string) *string { return &s }
