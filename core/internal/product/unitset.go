// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

// A product's unit set (ADR 0006 sections 3.1 to 3.3): the units a product
// is stocked, sold, bought and priced in, one row per unit with its pair to
// the stocking unit, and the four default columns the product row carries.
// The PUT replaces the whole set in one transaction that takes the product's
// revision, resolves every row's pair by the derivation rules of section 3.2
// (a row may arrive without one), checks the stocking unit holds of 3.2 and
// 9.1, bumps the revision, writes the audit row and the product.updated
// event with parts ["units"] last, and answers the completed set with its
// warnings, an array always present.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UnitSetRowView is one row of a product's unit set on the wire: unit_qty of
// the row's unit is stock_qty of the product's stocking unit, both decimal
// strings in the canonical form of R2.
type UnitSetRowView struct {
	UOM      string         `json:"uom"`
	UnitQty  httpx.Quantity `json:"unit_qty"`
	StockQty httpx.Quantity `json:"stock_qty"`
	Sell     bool           `json:"sell"`
	Purchase bool           `json:"purchase"`
	Price    bool           `json:"price"`
}

// UnitSetDoc is GET /products/{id}/units and the PUT's answer: the set, the
// four defaults, the board measure facts the derivations read, the base
// price the hold guards, and the PUT's warnings (empty on a read).
type UnitSetDoc struct {
	ProductID        uuid.UUID        `json:"product_id"`
	StockUOM         string           `json:"stock_uom"`
	SaleUOM          string           `json:"sale_uom"`
	PriceUOM         string           `json:"price_uom"`
	PurchaseUOM      string           `json:"purchase_uom"`
	BasePrice        httpx.Price      `json:"base_price_ten_thousandths"`
	BoardThicknessIn *httpx.Quantity  `json:"board_thickness_in"`
	BoardWidthIn     *httpx.Quantity  `json:"board_width_in"`
	BoardLengthFT    *httpx.Quantity  `json:"board_length_ft"`
	RandomLength     bool             `json:"random_length"`
	Units            []UnitSetRowView `json:"units"`
	Warnings         []units.Warning  `json:"warnings"`
	Revision         int64            `json:"revision"`
}

// UnitRowRequest is one row of the PUT body, as the service takes it: the
// pair is optional (the derivations of section 3.2 fill it), and a row
// carrying one is stored canonically after agreeing with every derivation
// that applies to it.
type UnitRowRequest struct {
	UOM      string
	UnitQty  string
	StockQty string
	Sell     bool
	Purchase bool
	Price    bool
}

// PutUnitSetRequest is the body of PUT /products/{id}/units as the service
// takes it. BasePriceTenThousandths is optional and simply sets the base
// price while the price unit cannot leave the stocking unit (9.1).
type PutUnitSetRequest struct {
	StockUOM                string
	SaleUOM                 string
	PriceUOM                string
	PurchaseUOM             string
	BasePriceTenThousandths *int64
	Units                   []UnitRowRequest
}

// putUnitSetBody is the PUT body as it arrives: strings, pointers and raw
// numbers, parsed by parsePutUnitSet with every problem collected into one
// 400. Fields the PUT cannot apply (the board measure columns, read on the
// product's own routes) are refused by the decoder naming them, the recipe's
// rule for a field a write does not apply.
type putUnitSetBody struct {
	Revision    *int64           `json:"revision"`
	StockUOM    *string          `json:"stock_uom"`
	SaleUOM     *string          `json:"sale_uom"`
	PriceUOM    *string          `json:"price_uom"`
	PurchaseUOM *string          `json:"purchase_uom"`
	BasePrice   json.RawMessage  `json:"base_price_ten_thousandths"`
	Units       []putUnitRowBody `json:"units"`
}

type putUnitRowBody struct {
	UOM      *string         `json:"uom"`
	UnitQty  json.RawMessage `json:"unit_qty"`
	StockQty json.RawMessage `json:"stock_qty"`
	Sell     *bool           `json:"sell"`
	Purchase *bool           `json:"purchase"`
	Price    *bool           `json:"price"`
}

// parsePutUnitSet validates the PUT body and fills the service request and
// its revision precondition.
func parsePutUnitSet(r *http.Request) (*PutUnitSetRequest, RevisionPrecondition, error) {
	var body putUnitSetBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return nil, RevisionPrecondition{}, err
	}
	v := &httpx.Validator{}
	req := &PutUnitSetRequest{}

	for _, f := range []struct {
		name string
		val  *string
		dst  *string
	}{
		{"stock_uom", body.StockUOM, &req.StockUOM},
		{"sale_uom", body.SaleUOM, &req.SaleUOM},
		{"price_uom", body.PriceUOM, &req.PriceUOM},
		{"purchase_uom", body.PurchaseUOM, &req.PurchaseUOM},
	} {
		if f.val == nil || *f.val == "" {
			v.Check(false, f.name, "is required")
		} else {
			*f.dst = *f.val
		}
	}
	if !isAbsentRaw(body.BasePrice) {
		if n, ok := v.Int("base_price_ten_thousandths", body.BasePrice, true); ok {
			v.Check(n >= 0, "base_price_ten_thousandths", "a unit price is never negative")
			req.BasePriceTenThousandths = &n
		}
	}
	for i := range body.Units {
		path := fmt.Sprintf("units[%d]", i)
		row := UnitRowRequest{}
		if body.Units[i].UOM == nil || *body.Units[i].UOM == "" {
			v.Check(false, path+".uom", "is required")
		} else {
			row.UOM = *body.Units[i].UOM
		}
		uq, uqOK := v.Quantity(path+".unit_qty", body.Units[i].UnitQty, false)
		sq, sqOK := v.Quantity(path+".stock_qty", body.Units[i].StockQty, false)
		switch {
		case uqOK && sqOK:
			row.UnitQty, row.StockQty = uq.WireString(), sq.WireString()
		case uqOK != sqOK:
			missing := path + ".unit_qty"
			if uqOK {
				missing = path + ".stock_qty"
			}
			v.Check(false, missing, "is required: a row's conversion is a pair, unit_qty and stock_qty together")
		}
		if body.Units[i].Sell != nil {
			row.Sell = *body.Units[i].Sell
		}
		if body.Units[i].Purchase != nil {
			row.Purchase = *body.Units[i].Purchase
		}
		if body.Units[i].Price != nil {
			row.Price = *body.Units[i].Price
		}
		req.Units = append(req.Units, row)
	}
	if err := v.Err(); err != nil {
		return nil, RevisionPrecondition{}, err
	}
	return req, RevisionPrecondition{IfMatch: r.Header.Get("If-Match"), Revision: body.Revision}, nil
}

func isAbsentRaw(raw json.RawMessage) bool {
	return raw == nil || string(raw) == "null"
}

// UnitSetProduct is the product row as the unit set write reads it under its
// lock: the facts the derivations and the holds need. Exported because the
// Repository interface the unit set store belongs to names it.
type UnitSetProduct struct {
	Revision     int64
	SKU          string
	UOMPrimary   string
	SaleUOM      string
	PriceUOM     string
	PurchaseUOM  string
	BasePrice    httpx.Price
	BoardThick   *httpx.Quantity
	BoardWidth   *httpx.Quantity
	BoardLength  *httpx.Quantity
	RandomLength bool
}

// GetUnitSet reads a product's unit set and the facts around it.
func (s *Service) GetUnitSet(ctx context.Context, id uuid.UUID) (*UnitSetDoc, error) {
	p, err := s.repo.GetProduct(ctx, id)
	if err != nil {
		return nil, resolveRevision(err)
	}
	rows, err := s.repo.GetUnitSetRows(ctx, id)
	if err != nil {
		return nil, err
	}
	return unitSetDocOf(p, rows, nil), nil
}

func unitSetDocOf(p *Product, rows []UnitSetRowView, warnings []units.Warning) *UnitSetDoc {
	if rows == nil {
		rows = []UnitSetRowView{}
	}
	if warnings == nil {
		warnings = []units.Warning{}
	}
	return &UnitSetDoc{
		ProductID: p.ID, StockUOM: string(p.UOMPrimary),
		SaleUOM: p.SaleUOM, PriceUOM: p.PriceUOM, PurchaseUOM: p.PurchaseUOM,
		BasePrice:        p.BasePriceScaled,
		BoardThicknessIn: p.BoardThicknessIn, BoardWidthIn: p.BoardWidthIn, BoardLengthFT: p.BoardLengthFT,
		RandomLength: p.RandomLength, Units: rows, Warnings: warnings, Revision: p.Revision,
	}
}

// ReplaceUnitSet replaces the whole set and the four default columns in ONE
// transaction (the recipe's rule): the product row is locked FOR UPDATE and
// its revision checked inside the transaction, the derivations of section
// 3.2 complete the rows, the stocking unit holds of 3.2 and 9.1 refuse their
// cases, the replace and the revision bump are one database act, and the
// audit row and the product.updated event join the same transaction, the
// event last.
func (s *Service) ReplaceUnitSet(ctx context.Context, id uuid.UUID, req *PutUnitSetRequest, pre RevisionPrecondition) (*UnitSetDoc, error) {
	var out *UnitSetDoc
	err := s.inTx(ctx, func(ctx context.Context) error {
		cur, err := s.lockForUnitSet(ctx, id)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		existing, err := s.repo.GetUnitSetRows(ctx, id)
		if err != nil {
			return err
		}
		rows, warnings, err := s.resolveUnitSet(ctx, id, cur, existing, req)
		if err != nil {
			return err
		}
		newRevision, err := s.repo.ReplaceUnitSet(ctx, id, rows, req.StockUOM,
			req.SaleUOM, req.PriceUOM, req.PurchaseUOM, req.BasePriceTenThousandths, cur.Revision)
		if err != nil {
			return resolveRevision(err)
		}
		auditRows := func(rs []UnitSetRowView) []map[string]any {
			out := make([]map[string]any, 0, len(rs))
			for _, r := range rs {
				out = append(out, map[string]any{
					"uom": r.UOM, "unit_qty": r.UnitQty.WireString(), "stock_qty": r.StockQty.WireString(),
					"sell": r.Sell, "purchase": r.Purchase, "price": r.Price,
				})
			}
			return out
		}
		if err := s.auditChange(ctx, id, cur.SKU, newRevision, EventProductUpdated, map[string]any{
			"parts": []string{"units"},
			"before": map[string]any{
				"stock_uom": cur.UOMPrimary, "sale_uom": cur.SaleUOM, "price_uom": cur.PriceUOM,
				"purchase_uom": cur.PurchaseUOM,
			},
			"after": map[string]any{
				"stock_uom": req.StockUOM, "sale_uom": req.SaleUOM, "price_uom": req.PriceUOM,
				"purchase_uom": req.PurchaseUOM, "units": auditRows(rows),
			},
		}); err != nil {
			return err
		}
		if err := s.recordEvent(ctx, id, cur.SKU, newRevision, EventProductUpdated, "units"); err != nil {
			return err
		}
		// The answer is the same document the GET serves: the facts the
		// write read under its lock come back beside the replaced set.
		out = &UnitSetDoc{
			ProductID: id, StockUOM: req.StockUOM,
			SaleUOM: req.SaleUOM, PriceUOM: req.PriceUOM, PurchaseUOM: req.PurchaseUOM,
			BasePrice:        cur.BasePrice,
			BoardThicknessIn: cur.BoardThick, BoardWidthIn: cur.BoardWidth, BoardLengthFT: cur.BoardLength,
			RandomLength: cur.RandomLength, Units: rows, Revision: newRevision,
		}
		if warnings == nil {
			warnings = []units.Warning{}
		}
		out.Warnings = warnings
		if req.BasePriceTenThousandths != nil {
			out.BasePrice = httpx.Price(*req.BasePriceTenThousandths)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// lockForUnitSet takes the product row FOR UPDATE and reads the unit set
// write's facts. Every read goes through the transaction the context
// carries.
func (s *Service) lockForUnitSet(ctx context.Context, id uuid.UUID) (*UnitSetProduct, error) {
	p, err := s.repo.LockProductForUnitSet(ctx, id)
	if err != nil {
		return nil, resolveRevision(err)
	}
	return p, nil
}

// conflictBlocker is one of 3.2's refusals: a 409 conflict whose blocker
// names the business reason, which belongs to no one field.
func conflictBlocker(code, message string) *httpx.Error {
	e := httpx.Conflict(message)
	e.Details = []httpx.FieldError{httpx.Blocker(code, message)}
	return e
}

// resolveUnitSet applies every check of section 3.2 that answers before the
// rows are written, and completes the rows by the derivation rules.
func (s *Service) resolveUnitSet(ctx context.Context, id uuid.UUID, cur *UnitSetProduct, existing []UnitSetRowView, req *PutUnitSetRequest) ([]UnitSetRowView, []units.Warning, error) {
	// The hold's own rule on the wire (A2's CHECK, 9.1): until C3-2B every
	// price is in the stocking unit.
	if req.PriceUOM != req.StockUOM {
		return nil, nil, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "price_uom", Message: "a price unit other than the stocking unit arrives with C3-2B"})
	}
	// The stocking row trigger's third invariant (3.1), named on the wire
	// before the deferred trigger can refuse the commit.
	if cur.RandomLength && req.StockUOM != "LF" {
		return nil, nil, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "stock_uom", Message: "a random length product is stocked in LF: its tallies bill by the linear foot"})
	}
	// The stocking unit comes from stock_uom, never from row order (rule 1
	// of section 3.1): the set carries the stock_uom row, and its pair,
	// when sent, is (1, 1). A second (1, 1) row is harmless once the
	// stocking unit is explicit (EA beside PCS, the natural fastener set).
	stockRowSent := false
	for _, r := range req.Units {
		if r.UOM != req.StockUOM {
			continue
		}
		stockRowSent = true
		if r.UnitQty == "" {
			continue
		}
		unitQty, errA := httpx.ParseQuantity(r.UnitQty)
		stockQty, errB := httpx.ParseQuantity(r.StockQty)
		if errA == nil && errB == nil && !(unitQty == 10_000 && stockQty == 10_000) {
			return nil, nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "stock_uom", Message: "the stocking unit's own row is 1 and 1; send no pair for it"})
		}
	}
	if !stockRowSent {
		return nil, nil, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "stock_uom", Message: req.StockUOM + " must be a row of the set: the stocking unit comes from stock_uom"})
	}

	// The stocking unit holds of 3.2, each a 409 conflict with its blocker.
	if req.StockUOM != cur.UOMPrimary {
		stockInUse, err := s.repo.ProductStockUnitInUse(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if stockInUse {
			return nil, nil, conflictBlocker("stock_unit_in_use",
				"the product holds stock or an open order line in "+cur.UOMPrimary+
					": changing what stock is counted in is a stock conversion, not an edit")
		}
		held, why, err := s.repo.ProductPriceHeld(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if cur.BasePrice != 0 {
			held, why = true, "a nonzero base price"
		}
		if held {
			return nil, nil, conflictBlocker("price_unit_held",
				"the product has "+why+" tied to its stocking unit "+cur.UOMPrimary+
					": zero the base price and remove those rows first, or wait for C3-2B")
		}
	}

	// The catalogue: every row's unit must be a real, active unit of the
	// catalogue, unless the row already exists (an inactive unit cannot enter
	// a NEW row; existing rows keep it, 2.1).
	codes := make([]string, 0, len(req.Units)+1)
	codes = append(codes, req.StockUOM, req.SaleUOM, req.PriceUOM, req.PurchaseUOM)
	for _, r := range req.Units {
		codes = append(codes, r.UOM)
	}
	catalogue, err := s.repo.CatalogueUnits(ctx, codes)
	if err != nil {
		return nil, nil, err
	}
	currentRows := make(map[string]bool, len(existing))
	for _, r := range existing {
		currentRows[r.UOM] = true
	}
	inputs := make([]units.SetInput, 0, len(req.Units))
	for i, r := range req.Units {
		path := fmt.Sprintf("units[%d]", i)
		u, known := catalogue[r.UOM]
		switch {
		case !known:
			return nil, nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: path + ".uom", Message: r.UOM + " is not a unit of the catalogue"})
		case !u.IsActive && !currentRows[r.UOM]:
			return nil, nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: path + ".uom", Message: r.UOM + " is inactive: an inactive unit cannot enter a new unit set row"})
		}
		in := units.SetInput{UOM: r.UOM, Sell: r.Sell, Purchase: r.Purchase, Price: r.Price}
		if r.UnitQty != "" {
			unitQty, err := httpx.ParseQuantity(r.UnitQty)
			if err != nil {
				return nil, nil, httpx.BadRequest("one or more fields failed validation",
					httpx.FieldError{Field: path + ".unit_qty", Message: "must be a decimal of at most 4 fraction digits"})
			}
			stockQty, err := httpx.ParseQuantity(r.StockQty)
			if err != nil {
				return nil, nil, httpx.BadRequest("one or more fields failed validation",
					httpx.FieldError{Field: path + ".stock_qty", Message: "must be a decimal of at most 4 fraction digits"})
			}
			in.UnitQty, in.StockQty, in.HasPair = unitQty, stockQty, true
		}
		inputs = append(inputs, in)
	}

	// The derivations of 3.2 complete the rows, agree with the sent pairs
	// and check the bound; the warnings name the sell rows that do not
	// convert exactly into the stocking unit.
	facts := units.SetFacts{
		StockUOM:        req.StockUOM,
		ThicknessIn:     derefQty(cur.BoardThick),
		WidthIn:         derefQty(cur.BoardWidth),
		HasCrossSection: cur.BoardThick != nil && cur.BoardWidth != nil,
		BoardLengthFT:   derefQty(cur.BoardLength),
		HasBoardLength:  cur.BoardLength != nil,
		RandomLength:    cur.RandomLength,
	}
	resolved, warnings, err := units.ResolveSet(inputs, facts, catalogue)
	if err != nil {
		var de *units.DeriveError
		if errors.As(err, &de) {
			return nil, nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: de.Field, Message: de.Message})
		}
		return nil, nil, err
	}
	rows := make([]UnitSetRowView, len(resolved))
	for i, r := range resolved {
		rows[i] = UnitSetRowView{UOM: r.UOM, UnitQty: r.UnitQty, StockQty: r.StockQty,
			Sell: r.Sell, Purchase: r.Purchase, Price: r.Price}
	}

	// The stocking row carries price (the trigger's second invariant, named
	// on the wire), and each default is a row of the set with its flag.
	for i, r := range rows {
		if r.UOM == req.StockUOM && !r.Price {
			return nil, nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: fmt.Sprintf("units[%d].price", i),
					Message: "the stocking unit's row carries price: a cost derived price answers in the stocking unit"})
		}
	}
	for _, f := range []struct {
		name string
		val  string
		flag func(UnitSetRowView) bool
	}{
		{"stock_uom", req.StockUOM, func(r UnitSetRowView) bool { return true }},
		{"sale_uom", req.SaleUOM, func(r UnitSetRowView) bool { return r.Sell }},
		{"price_uom", req.PriceUOM, func(r UnitSetRowView) bool { return r.Price }},
		{"purchase_uom", req.PurchaseUOM, func(r UnitSetRowView) bool { return r.Purchase }},
	} {
		found := false
		for _, r := range rows {
			if r.UOM == f.val && f.flag(r) {
				found = true
				break
			}
		}
		if !found {
			return nil, nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: f.name, Message: f.val + " must be a row of the set" +
					flagSuffix(f.name)})
		}
	}
	// unit_in_use: a removed row a price names. The composite foreign keys of
	// the pricing tables arrive with C3-2A-pricing (P5); before them, a
	// contract or a fixed price rule is implicitly per the stocking unit
	// (9.1), which the hold above already refuses on a stocking unit change.
	removed := removedCodes(existing, rows)
	if len(removed) > 0 {
		named, which, err := s.repo.ProductUnitInUse(ctx, id, removed)
		if err != nil {
			return nil, nil, err
		}
		if named {
			return nil, nil, conflictBlocker("unit_in_use",
				"a price ("+which+") is expressed in one of the removed units; remove that price first")
		}
	}
	return rows, warnings, nil
}

func flagSuffix(field string) string {
	switch field {
	case "sale_uom":
		return " with the sell flag set"
	case "price_uom":
		return " with the price flag set"
	case "purchase_uom":
		return " with the purchase flag set"
	}
	return ""
}

func removedCodes(existing, next []UnitSetRowView) []string {
	inNext := make(map[string]bool, len(next))
	for _, r := range next {
		inNext[r.UOM] = true
	}
	var out []string
	for _, r := range existing {
		if !inNext[r.UOM] {
			out = append(out, r.UOM)
		}
	}
	return out
}

func derefQty(q *httpx.Quantity) httpx.Quantity {
	if q == nil {
		return 0
	}
	return *q
}

// HandleGetUnitSet answers a product's unit set with its facts.
func (h *Handler) HandleGetUnitSet(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		writeProductError(w, r, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		writeProductError(w, r, bad)
		return
	}
	doc, err := h.service.GetUnitSet(r.Context(), id)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, doc.Revision)
	writeJSON(w, http.StatusOK, doc)
}

// HandlePutUnitSet replaces the whole unit set in one transaction at the
// product's revision: If-Match or the body revision, as every write (ADR
// 0001 section 11), and the new revision and its ETag on the answer.
func (h *Handler) HandlePutUnitSet(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		writeProductError(w, r, err)
		return
	}
	id, bad := pathID(r)
	if bad != nil {
		writeProductError(w, r, bad)
		return
	}
	req, pre, err := parsePutUnitSet(r)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	doc, err := h.service.ReplaceUnitSet(r.Context(), id, req, pre)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, doc.Revision)
	writeJSON(w, http.StatusOK, doc)
}

// The unit set store on the repository. Every statement goes through the
// context's executor, so inside the service's transaction it never reaches
// for a second pool connection.

// GetUnitSetRows reads a product's unit set ordered by unit code.
func (r *PostgresRepository) GetUnitSetRows(ctx context.Context, productID uuid.UUID) ([]UnitSetRowView, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT uom, ROUND(unit_qty * 10000)::bigint, ROUND(stock_qty * 10000)::bigint, sell, purchase, price
		FROM product_units WHERE product_id = $1 ORDER BY uom`, productID)
	if err != nil {
		return nil, fmt.Errorf("failed to read the unit set: %w", err)
	}
	defer rows.Close()
	out := []UnitSetRowView{}
	for rows.Next() {
		var row UnitSetRowView
		var unitQty, stockQty int64
		if err := rows.Scan(&row.UOM, &unitQty, &stockQty, &row.Sell, &row.Purchase, &row.Price); err != nil {
			return nil, fmt.Errorf("failed to scan a unit set row: %w", err)
		}
		row.UnitQty, row.StockQty = httpx.Quantity(unitQty), httpx.Quantity(stockQty)
		out = append(out, row)
	}
	return out, rows.Err()
}

// LockProductForUnitSet takes the product row FOR UPDATE and reads the unit
// set write's facts, through the caller's executor.
func (r *PostgresRepository) LockProductForUnitSet(ctx context.Context, id uuid.UUID) (*UnitSetProduct, error) {
	var p UnitSetProduct
	var basePrice string
	var thick, width, length *string
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `
		SELECT revision, sku, uom_primary, base_price::text, sale_uom, price_uom, purchase_uom,
		       board_thickness_in::text, board_width_in::text, board_length_ft::text, random_length
		FROM products WHERE id = $1 FOR UPDATE`, id).
		Scan(&p.Revision, &p.SKU, &p.UOMPrimary, &basePrice, &p.SaleUOM, &p.PriceUOM, &p.PurchaseUOM,
			&thick, &width, &length, &p.RandomLength)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to lock the product: %w", err)
	}
	if p.BasePrice, err = httpx.ParsePrice(basePrice); err != nil {
		return nil, fmt.Errorf("failed to read the base price: %w", err)
	}
	for _, col := range []struct {
		text *string
		dst  **httpx.Quantity
	}{
		{thick, &p.BoardThick}, {width, &p.BoardWidth}, {length, &p.BoardLength},
	} {
		if col.text == nil {
			continue
		}
		q, err := httpx.ParseQuantity(*col.text)
		if err != nil {
			return nil, fmt.Errorf("failed to read a board measure column: %w", err)
		}
		*col.dst = &q
	}
	return &p, nil
}

// ReplaceUnitSet clears the set, inserts the new rows, moves the product's
// four unit columns and bumps its revision, all through the caller's
// executor: inside the service's transaction they are one database act. The
// stocking unit is the caller's stock_uom, never row order. The deferred
// constraints (the composite foreign keys, the stocking row invariant, the
// hold) settle at the commit the service's checks already proved.
func (r *PostgresRepository) ReplaceUnitSet(ctx context.Context, id uuid.UUID, rows []UnitSetRowView,
	stockUOM, saleUOM, priceUOM, purchaseUOM string, basePrice *int64, revision int64) (int64, error) {
	exec := r.db.GetExecutor(ctx)
	if _, err := exec.Exec(ctx, `DELETE FROM product_units WHERE product_id = $1`, id); err != nil {
		return 0, fmt.Errorf("failed to clear the unit set: %w", err)
	}
	for _, row := range rows {
		if _, err := exec.Exec(ctx, `
			INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
			VALUES ($1, $2, $3::numeric / 10000, $4::numeric / 10000, $5, $6, $7)`,
			id, row.UOM, int64(row.UnitQty), int64(row.StockQty), row.Sell, row.Purchase, row.Price); err != nil {
			return 0, fmt.Errorf("failed to insert a unit set row: %w", err)
		}
	}
	// base_price is always in the statement (a contiguous parameter list),
	// and a NULL leaves the column as it is: the PUT sets the base price
	// only when the body carries it.
	sets := `uom_primary = $2, sale_uom = $3, price_uom = $4, purchase_uom = $5,
		base_price = COALESCE($6::numeric / 10000, base_price)`
	var baseArg any
	if basePrice != nil {
		baseArg = *basePrice
	}
	// The write names all four unit columns itself, so the defaults trigger
	// of migration 099 must not drag them: the transaction local
	// gable.unit_set_write flag holds the trigger's dragging branch back for
	// this transaction only (it serves raw writers, which never set it).
	if _, err := exec.Exec(ctx, `SELECT set_config('gable.unit_set_write', 'on', true)`); err != nil {
		return 0, fmt.Errorf("failed to mark the unit set write: %w", err)
	}
	args := []any{id, stockUOM, saleUOM, priceUOM, purchaseUOM, baseArg}
	var newRevision int64
	err := exec.QueryRow(ctx, `
		UPDATE products SET `+sets+`, revision = revision + 1, updated_at = NOW()
		WHERE id = $1 AND revision = $7 RETURNING revision`,
		append(args, revision)...).Scan(&newRevision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if qerr := exec.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM products WHERE id = $1)`, id).Scan(&exists); qerr == nil && exists {
				return 0, ErrStaleRevision
			}
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("failed to move the product's unit columns: %w", err)
	}
	return newRevision, nil
}

// CatalogueUnits reads the catalogue rows for the given codes: the slice of
// the units table the set arithmetic reads (ADR 0006 section 3.2).
func (r *PostgresRepository) CatalogueUnits(ctx context.Context, codes []string) (map[string]units.CatalogueUnit, error) {
	out := make(map[string]units.CatalogueUnit, len(codes))
	if len(codes) == 0 {
		return out, nil
	}
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT code, dimension,
		       ROUND(COALESCE(std_unit_qty, 0) * 10000)::bigint,
		       ROUND(COALESCE(std_ref_qty, 0) * 10000)::bigint,
		       std_unit_qty IS NOT NULL, is_active
		FROM units WHERE code = ANY($1)`, codes)
	if err != nil {
		return nil, fmt.Errorf("failed to read the unit catalogue: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var u units.CatalogueUnit
		var stdUnit, stdRef int64
		if err := rows.Scan(&u.Code, &u.Dimension, &stdUnit, &stdRef, &u.HasStdSize, &u.IsActive); err != nil {
			return nil, fmt.Errorf("failed to scan a unit catalogue row: %w", err)
		}
		u.StdUnitQty, u.StdRefQty = httpx.Quantity(stdUnit), httpx.Quantity(stdRef)
		out[u.Code] = u
	}
	return out, rows.Err()
}

// ProductStockUnitInUse answers whether any inventory row of the product
// holds a nonzero quantity or allocation, or any open order line names it
// (3.2's stock_unit_in_use: changing what stock is counted in is a stock
// conversion, a cycle 4 adjustment act). The read takes the row locks it
// runs on, every inventory row of the product and its open order lines,
// inside the caller's transaction and under the product row lock the unit
// set write already holds: a concurrent receive on an existing bin or an
// edit of an open line waits for the write to commit instead of landing
// between this check and the replace. A row that first appears after the
// check (a new bin, a new order line; the writers do not lock the product
// row) is the stock identity work of cycle 4 and is not closed here.
func (r *PostgresRepository) ProductStockUnitInUse(ctx context.Context, id uuid.UUID) (bool, error) {
	exec := r.db.GetExecutor(ctx)
	for _, lock := range []string{
		`SELECT count(*) FROM (SELECT 1 FROM inventory WHERE product_id = $1 FOR UPDATE) l`,
		`SELECT count(*) FROM (SELECT 1 FROM order_lines ol JOIN orders o ON o.id = ol.order_id
		   WHERE ol.product_id = $1 AND o.status NOT IN ('FULFILLED', 'CANCELLED') FOR UPDATE OF ol) l`,
	} {
		var n int
		if err := exec.QueryRow(ctx, lock, id).Scan(&n); err != nil {
			return false, fmt.Errorf("failed to lock the product's stock rows: %w", err)
		}
	}
	var inUse bool
	err := exec.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM inventory WHERE product_id = $1 AND (quantity <> 0 OR allocated <> 0)
			UNION ALL
			SELECT 1 FROM order_lines ol JOIN orders o ON o.id = ol.order_id
			 WHERE ol.product_id = $1 AND o.status NOT IN ('FULFILLED', 'CANCELLED')
		)`, id).Scan(&inUse)
	if err != nil {
		return false, fmt.Errorf("failed to check the product's stock: %w", err)
	}
	return inUse, nil
}

// ProductPriceHeld answers whether any price row names the product, and
// which kind (3.2's price_unit_held during the stocking unit hold of 9.1).
// Before C3-2A-pricing a contract or a rule with a fixed price is implicitly
// per the stocking unit, so both count; product_prices arrives with P5 and
// is read once it exists. The base price is the caller's to check (it holds
// the row already).
func (r *PostgresRepository) ProductPriceHeld(ctx context.Context, id uuid.UUID) (bool, string, error) {
	exec := r.db.GetExecutor(ctx)
	var n int
	if err := exec.QueryRow(ctx,
		`SELECT count(*) FROM customer_contracts WHERE product_id = $1`, id).Scan(&n); err != nil {
		return false, "", fmt.Errorf("failed to check the product's contracts: %w", err)
	}
	if n > 0 {
		return true, "a customer contract", nil
	}
	if err := exec.QueryRow(ctx,
		`SELECT count(*) FROM pricing_rules WHERE product_id = $1 AND fixed_price IS NOT NULL`, id).Scan(&n); err != nil {
		return false, "", fmt.Errorf("failed to check the product's pricing rules: %w", err)
	}
	if n > 0 {
		return true, "a pricing rule with a fixed price", nil
	}
	var hasPrices bool
	if err := exec.QueryRow(ctx,
		`SELECT to_regclass('product_prices') IS NOT NULL`).Scan(&hasPrices); err != nil {
		return false, "", fmt.Errorf("failed to look for product_prices: %w", err)
	}
	if hasPrices {
		if err := exec.QueryRow(ctx,
			`SELECT count(*) FROM product_prices WHERE product_id = $1`, id).Scan(&n); err != nil {
			return false, "", fmt.Errorf("failed to check the product's prices: %w", err)
		}
		if n > 0 {
			return true, "a product price row", nil
		}
	}
	return false, "", nil
}

// ProductUnitInUse answers whether one of the removed unit codes is named by
// a price row, and which kind (3.2's unit_in_use). The pricing tables carry
// their units from C3-2A-pricing (P5); each is probed for its table and
// column before it is read, so this deployment's schema answers cleanly
// whichever of them exists, and the composite foreign keys make the database
// refuse the same writes from P5 on.
func (r *PostgresRepository) ProductUnitInUse(ctx context.Context, id uuid.UUID, removed []string) (bool, string, error) {
	exec := r.db.GetExecutor(ctx)
	for _, probe := range []struct {
		table  string
		column string
		kind   string
		sql    string
	}{
		{"customer_contracts", "price_uom", "a customer contract",
			`SELECT EXISTS (SELECT 1 FROM customer_contracts WHERE product_id = $1 AND price_uom = ANY($2))`},
		{"pricing_rules", "price_uom", "a pricing rule with a fixed price",
			`SELECT EXISTS (SELECT 1 FROM pricing_rules WHERE product_id = $1 AND fixed_price IS NOT NULL AND price_uom = ANY($2))`},
		{"product_prices", "price_uom", "a product price row",
			`SELECT EXISTS (SELECT 1 FROM product_prices WHERE product_id = $1 AND price_uom = ANY($2))`},
		{"vendor_product_costs", "purchase_uom", "a vendor cost",
			`SELECT EXISTS (SELECT 1 FROM vendor_product_costs WHERE product_id = $1 AND purchase_uom = ANY($2))`},
	} {
		var present bool
		if err := exec.QueryRow(ctx,
			`SELECT to_regclass($1) IS NOT NULL AND EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2)`,
			probe.table, probe.column).Scan(&present); err != nil {
			return false, "", fmt.Errorf("failed to look for %s: %w", probe.table, err)
		}
		if !present {
			continue
		}
		var found bool
		if err := exec.QueryRow(ctx, probe.sql, id, removed).Scan(&found); err != nil {
			return false, "", fmt.Errorf("failed to check a price's unit: %w", err)
		}
		if found {
			return true, probe.kind, nil
		}
	}
	return false, "", nil
}
