// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Dimension is a unit's dimension (ADR 0006 section 2.2): the storage
// vocabulary is uppercase, the wire lowercase.
type Dimension string

// The six dimensions, each with its reference unit: COUNT EA, LENGTH LF,
// AREA SF, VOLUME CF, WEIGHT LBS, BOARD_MEASURE BF.
const (
	DimCount        Dimension = "COUNT"
	DimLength       Dimension = "LENGTH"
	DimArea         Dimension = "AREA"
	DimVolume       Dimension = "VOLUME"
	DimWeight       Dimension = "WEIGHT"
	DimBoardMeasure Dimension = "BOARD_MEASURE"
)

// CatalogueUnit is the slice of a catalogue row the set arithmetic reads:
// the unit's dimension, its standard size (both sides, or neither for a unit
// whose size is per product) and whether it may enter new rows and lines.
type CatalogueUnit struct {
	Code       string
	Dimension  Dimension
	StdUnitQty httpx.Quantity
	StdRefQty  httpx.Quantity
	HasStdSize bool
	IsActive   bool
}

// refPerUnit is the standard size as a rational: how much of the dimension's
// reference unit one of this unit is (MBF: 1000 BF; GAL: 77/576 CF).
func (c CatalogueUnit) refPerUnit() (*big.Rat, bool) {
	if !c.HasStdSize || c.StdUnitQty <= 0 || c.StdRefQty <= 0 {
		return nil, false
	}
	return new(big.Rat).SetFrac64(int64(c.StdRefQty), int64(c.StdUnitQty)), true
}

// SetRow is one row of a product's unit set (ADR 0006 section 3.1): unit_qty
// of the row's unit is stock_qty of the product's stocking unit, canonical
// (R2), with the flags naming what the unit may be used for on this product.
type SetRow struct {
	UOM      string
	UnitQty  httpx.Quantity
	StockQty httpx.Quantity
	Sell     bool
	Purchase bool
	Price    bool
}

// PairOf is the row as a Pair, its left side the row's unit.
func (r SetRow) PairOf() Pair { return Pair{A: r.UnitQty, B: r.StockQty} }

// PairOf is the input row as a Pair.
func (r SetInput) PairOf() Pair { return Pair{A: r.UnitQty, B: r.StockQty} }

// SetFacts is the product data the derivations of section 3.2 read: the
// stocking unit and the nominal board measure columns.
type SetFacts struct {
	StockUOM        string
	HasCrossSection bool
	ThicknessIn     httpx.Quantity
	WidthIn         httpx.Quantity
	HasBoardLength  bool
	BoardLengthFT   httpx.Quantity
	RandomLength    bool
}

// SetInput is one row of a unit set PUT: the row with its pair either sent
// (HasPair) or left for the derivations. ResolveSet completes the slice in
// place.
type SetInput struct {
	UOM      string
	UnitQty  httpx.Quantity
	StockQty httpx.Quantity
	HasPair  bool
	Sell     bool
	Purchase bool
	Price    bool

	// derivedBy names the rule that filled this row's pair, empty when the
	// client sent it.
	derivedBy string
}

// DeriveError is a row the write refuses: Field is the wire path to name
// (units[k] or units[k].unit_qty).
type DeriveError struct {
	Field   string
	Message string
}

func (e *DeriveError) Error() string { return e.Message }

// ResolveSet completes a unit set write (ADR 0006 section 3.2): the stocking
// unit's own row is (1, 1); rows without a pair are derived by the rules
// below, in the order they apply, until nothing more derives; a row the
// client sent with a pair must agree, as a ratio, with every derivation rule
// that applies to it; and every ordered pair of rows must resolve within the
// pair's bound. It answers the completed rows in the input's order and the
// warnings of section 3.2. A pair is free only where no rule applies (a box
// of 100, a bundle of 21 pieces).
//
// The rules, from the record:
//
//  1. the stocking unit's own row is (1, 1);
//  2. a unit with a standard size in the same dimension as another row whose
//     pair is known converts through the two standard sizes (MBF from BF, EA
//     from PCS, SQ from SF);
//  3. a LENGTH unit on a product with board_length_ft converts through the
//     piece: 1 PCS = board_length_ft LF;
//  4. a BOARD_MEASURE unit on a product with a cross section converts
//     through LF (12 LF of the product = thickness x width BF) or, on a
//     fixed length product, through the piece (12 PCS = thickness x width x
//     length BF), then through rule 2 for MBF.
func ResolveSet(inputs []SetInput, facts SetFacts, catalogue map[string]CatalogueUnit) ([]SetRow, []Warning, error) {
	if len(inputs) == 0 {
		return nil, nil, &DeriveError{Field: "units", Message: "a product's unit set has at least its stocking unit's row"}
	}
	byUOM := map[string]*SetInput{}
	for i := range inputs {
		in := &inputs[i]
		if in.UOM == "" {
			return nil, nil, &DeriveError{Field: fmt.Sprintf("units[%d].uom", i), Message: "is required"}
		}
		if _, dup := byUOM[in.UOM]; dup {
			return nil, nil, &DeriveError{Field: fmt.Sprintf("units[%d].uom", i), Message: in.UOM + " appears twice"}
		}
		byUOM[in.UOM] = in
	}
	stock, hasStockRow := byUOM[facts.StockUOM]
	if !hasStockRow {
		return nil, nil, &DeriveError{Field: "units", Message: "the stocking unit " + facts.StockUOM + " has no row in the set"}
	}
	// Rule 1: the stocking row is (1, 1), sent or not.
	if stock.HasPair && !(stock.UnitQty == 10_000 && stock.StockQty == 10_000) {
		return nil, nil, &DeriveError{Field: "units.unit_qty",
			Message: "the stocking unit's own row is 1 and 1; send no pair for it"}
	}
	stock.UnitQty, stock.StockQty, stock.HasPair = 10_000, 10_000, true

	// A sent pair is stored canonically.
	for i := range inputs {
		in := &inputs[i]
		if !in.HasPair || in.UOM == facts.StockUOM {
			continue
		}
		if in.UnitQty <= 0 || in.StockQty <= 0 {
			return nil, nil, &DeriveError{Field: fmt.Sprintf("units[%d].unit_qty", i),
				Message: "a pair's sides are positive"}
		}
		p, cerr := Canonical(in.UnitQty, in.StockQty)
		if cerr != nil {
			return nil, nil, &DeriveError{Field: fmt.Sprintf("units[%d].unit_qty", i), Message: cerr.Error()}
		}
		in.UnitQty, in.StockQty = p.A, p.B
	}

	// Derive until nothing more derives. Each pass applies every rule whose
	// anchors are known; a rule never overwrites a known pair.
	for changed := true; changed; {
		changed = false
		for i := range inputs {
			in := &inputs[i]
			if in.HasPair {
				continue
			}
			p, ok := deriveOne(in.UOM, inputs, facts, catalogue)
			if !ok {
				continue
			}
			in.UnitQty, in.StockQty, in.HasPair = p.A, p.B, true
			in.derivedBy = "derived"
			changed = true
		}
	}
	// A row no rule reaches is a 400 naming the pair.
	for i := range inputs {
		if !inputs[i].HasPair {
			return nil, nil, &DeriveError{Field: fmt.Sprintf("units[%d].unit_qty", i),
				Message: "is required: no derivation rule of the product reaches " + inputs[i].UOM +
					"; send the pair (for example a box of 100 or a bundle of 21 pieces)"}
		}
	}
	// A sent pair must agree with every applicable derivation (rules 2 to
	// 4): a product cannot hold BF (1, 1.5) beside MBF (1, 1400), which
	// would break the standard 1000 to 1. Every anchor that derives the
	// unit is checked, so a pair that agrees one way and disagrees another
	// (a cross section that gives another MBF pair) is still refused.
	for i := range inputs {
		in := &inputs[i]
		if !in.HasPair || in.derivedBy != "" || in.UOM == facts.StockUOM {
			continue
		}
		others := make([]SetInput, len(inputs))
		copy(others, inputs)
		others[i].HasPair = false
		others[i].UnitQty, others[i].StockQty = 0, 0
		for _, p := range deriveAll(in.UOM, others, facts, catalogue) {
			if !p.SameRatio(Pair{A: in.UnitQty, B: in.StockQty}) {
				return nil, nil, &DeriveError{Field: fmt.Sprintf("units[%d].unit_qty", i),
					Message: fmt.Sprintf("does not match the product's unit set: the derived pair is (%s, %s)",
						p.A.WireString(), p.B.WireString())}
			}
		}
	}
	// Every ordered pair of rows must resolve within the bound (R2 step 4),
	// so a line can never meet an unrepresentable pair.
	for i := range inputs {
		for j := range inputs {
			if i == j {
				continue
			}
			if _, rerr := ResolveLinePair(inputs[i].PairOf(), inputs[j].PairOf()); rerr != nil {
				return nil, nil, &DeriveError{Field: fmt.Sprintf("units[%d]", j),
					Message: fmt.Sprintf("the conversion between %s and %s does not fit the pair's bound",
						inputs[i].UOM, inputs[j].UOM)}
			}
		}
	}

	rows := make([]SetRow, len(inputs))
	for i := range inputs {
		rows[i] = SetRow{UOM: inputs[i].UOM, UnitQty: inputs[i].UnitQty, StockQty: inputs[i].StockQty,
			Sell: inputs[i].Sell, Purchase: inputs[i].Purchase, Price: inputs[i].Price}
	}
	return rows, sellWarnings(inputs, facts), nil
}

// deriveOne applies the first derivation rule that reaches uom from the rows
// whose pairs are known, and answers the pair it gives.
func deriveOne(uom string, inputs []SetInput, facts SetFacts, catalogue map[string]CatalogueUnit) (Pair, bool) {
	all := deriveAll(uom, inputs, facts, catalogue)
	if len(all) == 0 {
		return Pair{}, false
	}
	return all[0], true
}

// deriveAll answers every pair the derivation rules can give uom from the
// rows whose pairs are known, one per anchor, rule order first.
func deriveAll(uom string, inputs []SetInput, facts SetFacts, catalogue map[string]CatalogueUnit) []Pair {
	unit, known := catalogue[uom]
	if !known {
		return nil
	}
	var out []Pair
	knownRows := func() []SetInput {
		var rows []SetInput
		for i := range inputs {
			if inputs[i].HasPair && inputs[i].UOM != uom {
				rows = append(rows, inputs[i])
			}
		}
		return rows
	}
	// Rule 2: through two standard sizes in one dimension. k_u of K is k_s
	// of stock, one of the unit is refU of the reference and one of K is
	// refK of it, so (k_u x refK) of the unit is (k_s x refU) of stock.
	if refU, ok := unit.refPerUnit(); ok {
		for _, k := range knownRows() {
			ku, kok := catalogue[k.UOM]
			if !kok || ku.Dimension != unit.Dimension {
				continue
			}
			if refK, kok2 := ku.refPerUnit(); kok2 {
				if p, err := canonicalOfProducts(k.UnitQty, refK, k.StockQty, refU); err == nil {
					out = append(out, p)
				}
			}
		}
	}
	// Rule 3: a LENGTH unit through the piece on a product with a fixed
	// length: 1 PCS = board_length_ft LF, so (pcs_u x blf) of the unit is
	// (pcs_s x refU) of stock.
	if unit.Dimension == DimLength && facts.HasBoardLength {
		if stockRow, ok := rowOf(inputs, facts.StockUOM); ok && stockRow.HasPair {
			if p, err := canonicalOfProducts(stockRow.UnitQty, ratOf(facts.BoardLengthFT), stockRow.StockQty, refPerUnitOrOne(unit)); err == nil {
				out = append(out, p)
			}
		}
	}
	// Rule 4: a BOARD_MEASURE unit through LF (12 LF is t x w BF), or
	// through the piece on a fixed length product (12 PCS is t x w x l BF).
	if unit.Dimension == DimBoardMeasure && facts.HasCrossSection {
		tw := new(big.Rat).Mul(ratOf(facts.ThicknessIn), ratOf(facts.WidthIn))
		for _, k := range knownRows() {
			kd, kok := catalogue[k.UOM]
			if !kok {
				continue
			}
			switch {
			case kd.Dimension == DimLength:
				// (lf_u x t x w) of the unit is (lf_s x 12 x refU) of stock.
				num := new(big.Rat).Mul(ratOf(k.UnitQty), tw)
				den := new(big.Rat).Mul(ratOf(k.StockQty), big.NewRat(12, 1))
				den.Mul(den, refPerUnitOrOne(unit))
				if p, err := canonicalOfRats(num, den); err == nil {
					out = append(out, p)
				}
			case kd.Dimension == DimCount && facts.HasBoardLength:
				// (pcs_u x t x w x l) of the unit is (pcs_s x 12 x refU) of stock.
				num := new(big.Rat).Mul(ratOf(k.UnitQty), tw)
				num.Mul(num, ratOf(facts.BoardLengthFT))
				den := new(big.Rat).Mul(ratOf(k.StockQty), big.NewRat(12, 1))
				den.Mul(den, refPerUnitOrOne(unit))
				if p, err := canonicalOfRats(num, den); err == nil {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// rowOf finds one row by unit.
func rowOf(inputs []SetInput, uom string) (SetInput, bool) {
	for i := range inputs {
		if inputs[i].UOM == uom {
			return inputs[i], true
		}
	}
	return SetInput{}, false
}

// refPerUnitOrOne is the unit's reference ratio, or 1 for the reference unit
// itself (BF is 1 BF per BF).
func refPerUnitOrOne(u CatalogueUnit) *big.Rat {
	if r, ok := u.refPerUnit(); ok {
		return r
	}
	return big.NewRat(1, 1)
}

// ratOf is a scale 4 quantity as the exact rational it names.
func ratOf(q httpx.Quantity) *big.Rat { return new(big.Rat).SetFrac64(int64(q), 10_000) }

// canonicalOfProducts canonicalizes (a x ra) : (b x rb) for rationals ra, rb.
func canonicalOfProducts(a httpx.Quantity, ra *big.Rat, b httpx.Quantity, rb *big.Rat) (Pair, error) {
	num := new(big.Rat).Mul(ratOf(a), ra)
	den := new(big.Rat).Mul(ratOf(b), rb)
	return canonicalOfRats(num, den)
}

// canonicalOfRats canonicalizes the positive ratio num : den.
func canonicalOfRats(num, den *big.Rat) (Pair, error) {
	frac := new(big.Rat).Quo(num, den)
	if frac.Sign() <= 0 {
		return Pair{}, fmt.Errorf("a pair's sides are positive")
	}
	return canonicalRatio(frac.Num(), frac.Denom())
}

// Warning is one entry of the unit set PUT's warnings array, always present
// on the wire (empty when there is nothing to say).
type Warning struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// sellWarnings writes section 3.2's warnings: a sell row whose one unit does
// not convert exactly into the stocking unit (its stock_qty / unit_qty is
// not an exact scale 4 decimal), naming a finer stocking unit in the set
// when one would make every sell row exact. The warning does not refuse: a
// dealer who sells the paver by SF only in whole multiples of 3 SF is served.
func sellWarnings(inputs []SetInput, facts SetFacts) []Warning {
	stock, ok := rowOf(inputs, facts.StockUOM)
	if !ok {
		return nil
	}
	inexact := false
	for i := range inputs {
		in := &inputs[i]
		if !in.Sell || !in.HasPair {
			continue
		}
		if _, exact := exactAtScale4(big.NewInt(int64(in.StockQty)), big.NewInt(int64(in.UnitQty))); !exact {
			inexact = true
		}
	}
	if !inexact {
		return nil
	}
	// A unit of the set that would make every sell row exact as the
	// stocking unit: the paver stocked in PCS and sold by SF is served
	// exactly by stocking in SF (1 PCS = 0.375 SF).
	finer, finerPair := "", ""
	for i := range inputs {
		c := &inputs[i]
		if !c.HasPair || c.UOM == facts.StockUOM {
			continue
		}
		allExact := true
		for j := range inputs {
			s := &inputs[j]
			if !s.Sell || !s.HasPair {
				continue
			}
			// One of s.UOM is (s.StockQty / s.UnitQty) of stock, and one of
			// stock is (c.UnitQty / c.StockQty) of c.UOM.
			r := new(big.Rat).SetFrac64(int64(s.StockQty), int64(s.UnitQty))
			r.Mul(r, new(big.Rat).SetFrac64(int64(c.UnitQty), int64(c.StockQty)))
			if !ratExactAtScale4(r) {
				allExact = false
				break
			}
		}
		if allExact {
			// The current stocking unit expressed against the candidate:
			// one of stock is (c.UnitQty / c.StockQty) of c.UOM, so one of
			// stock is (stock.StockQty / stock.UnitQty) x that of c.UOM.
			swap := new(big.Rat).SetFrac64(int64(c.UnitQty), int64(c.StockQty))
			swap.Mul(swap, new(big.Rat).SetFrac64(int64(stock.StockQty), int64(stock.UnitQty)))
			finer = c.UOM
			finerPair = "1 " + facts.StockUOM + " = " + ratWireString(swap) + " " + c.UOM
			break
		}
	}
	var out []Warning
	for i := range inputs {
		in := &inputs[i]
		if !in.Sell || !in.HasPair {
			continue
		}
		if _, exact := exactAtScale4(big.NewInt(int64(in.StockQty)), big.NewInt(int64(in.UnitQty))); exact {
			continue
		}
		msg := "1 " + in.UOM + " is " + ratWireString(new(big.Rat).SetFrac64(int64(in.StockQty), int64(in.UnitQty))) +
			" " + facts.StockUOM + ", which is not exact at scale 4: most " + in.UOM + " quantities will be refused at the counter"
		if finer != "" {
			msg += "; stocking in " + finer + " instead (" + finerPair + ") makes every sell row exact"
		}
		out = append(out, Warning{Field: fmt.Sprintf("units[%d]", i), Message: msg})
	}
	return out
}

func ratExactAtScale4(r *big.Rat) bool {
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt64(10_000))
	return scaled.IsInt()
}

// ratWireString renders a non negative rational as the shortest decimal
// string when it is exact at scale 4, and as the rational itself otherwise
// (never a rounding).
func ratWireString(r *big.Rat) string {
	if r.Sign() < 0 {
		return r.RatString()
	}
	if r.IsInt() {
		return new(big.Int).Quo(r.Num(), r.Denom()).String()
	}
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt64(10_000))
	if scaled.IsInt() && scaled.Num().IsInt64() {
		return httpx.Quantity(scaled.Num().Int64()).WireString()
	}
	return r.RatString()
}

// SortedUnits answers the set's unit codes sorted, for stable messages.
func SortedUnits(rows []SetRow) []string {
	out := make([]string, len(rows))
	for i := range rows {
		out[i] = rows[i].UOM
	}
	sort.Strings(out)
	return out
}
