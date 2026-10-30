// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

// The kit explosion on a quote (QE-05-a): the one shared explosion of
// salesdoc.Explode (ADR 0005 section 2.6), the same one orders and the
// counter run, never a second one. A draft line that names a kit product
// becomes the kit line, priced as a whole, followed by one component line
// per component of the kit's definition at quantity kit x per kit, unit
// price zero, extension zero. The explosion is done once, at the draft's
// create or edit, so a later change of the kit definition does not touch an
// existing quote: the edit re-runs it and replaces the components.

import (
	"context"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// explodeKits replaces every priced line of q that names a kit product with
// the kit line and its component lines, through salesdoc.Explode. The kit
// definitions and the component products are read through the caller's
// executor, so inside the create's or the edit's transaction the reads and
// the write are one act. The explosion's refusals (a kit with no components,
// a kit inside a kit) are the shared 400s and name the request's line.
func (s *Service) explodeKits(ctx context.Context, q *Quote, products map[uuid.UUID]ProductRef) error {
	var kitIDs []uuid.UUID
	for i := range q.Lines {
		l := &q.Lines[i]
		if l.ProductID != nil && products[*l.ProductID].IsKit {
			kitIDs = append(kitIDs, *l.ProductID)
		}
	}
	if len(kitIDs) == 0 {
		return nil
	}
	kits, err := s.repo.LookupKitComponents(ctx, kitIDs)
	if err != nil {
		return err
	}
	// A kit's components default like any product (the component line carries
	// each one's own stocking unit): read the ones the draft did not name.
	var missing []uuid.UUID
	for _, comps := range kits {
		for _, c := range comps {
			if _, ok := products[c.ComponentProductID]; !ok {
				missing = append(missing, c.ComponentProductID)
			}
		}
	}
	if len(missing) > 0 {
		more, err := s.repo.LookupProducts(ctx, missing)
		if err != nil {
			return err
		}
		for id, p := range more {
			products[id] = p
		}
	}
	refs := make(map[string]salesdoc.ProductRef, len(products))
	for id, p := range products {
		refs[id.String()] = salesdoc.ProductRef{
			ID: p.ID, SKU: p.SKU, Description: p.Description, UOMPrimary: p.UOMPrimary,
			IsKit: p.IsKit, AverageCost: p.AverageCost,
		}
	}
	kitComps := make(map[string][]salesdoc.KitComponent, len(kits))
	for id, comps := range kits {
		kitComps[id.String()] = comps
	}
	exploded, err := salesdoc.Explode(quoteLinesToSalesdoc(q.Lines), kitComps, refs)
	if err != nil {
		return err
	}
	q.Lines = salesdocLinesToQuote(exploded, q.Lines, products)
	return nil
}

// quoteLinesToSalesdoc maps the priced lines onto the shared shape the one
// explosion reads. Every line enters as a product line: the request
// vocabulary has no kit or component lines, the explosion writes them.
func quoteLinesToSalesdoc(lines []QuoteLine) []salesdoc.Line {
	out := make([]salesdoc.Line, 0, len(lines))
	for i := range lines {
		l := &lines[i]
		s := salesdoc.Line{
			ID: l.ID, LineType: salesdoc.LineProduct, ParentLineID: l.ParentLineID,
			ProductID: l.ProductID, Description: l.Description,
			Quantity: &l.Quantity, UOMQty: &l.UOMQty, PriceUOMQty: &l.PriceUOMQty,
			UnitPrice: &l.UnitPrice, LineTotal: &l.LineTotal, CreatedAt: l.CreatedAt,
		}
		if uom := string(l.UOM); uom != "" {
			s.UOM = &uom
		}
		if l.PriceUOM != "" {
			priceUOM := l.PriceUOM
			s.PriceUOM = &priceUOM
		}
		if l.SKU != "" {
			sku := l.SKU
			s.SKU = &sku
		}
		s.StockUOM, s.StockQuantity = l.StockUOM, l.StockQuantity
		out = append(out, s)
	}
	return out
}

// salesdocLinesToQuote maps the exploded lines back, keeping everything a
// carried line had that the explosion does not know (the customer note, the
// tally) and writing the component lines the explosion minted with their
// product's average cost, the figure the margin reads.
func salesdocLinesToQuote(exploded []salesdoc.Line, prior []QuoteLine, products map[uuid.UUID]ProductRef) []QuoteLine {
	priorByID := make(map[uuid.UUID]QuoteLine, len(prior))
	for _, p := range prior {
		priorByID[p.ID] = p
	}
	quoteID := uuid.UUID{}
	if len(prior) > 0 {
		quoteID = prior[0].QuoteID
	}
	out := make([]QuoteLine, 0, len(exploded))
	for _, s := range exploded {
		if p, ok := priorByID[s.ID]; ok {
			// A line the draft carried: the explosion settled its kind and,
			// on a kit line, its stocking pair; the rest is what was priced.
			p.LineType = s.LineType
			p.ParentLineID = s.ParentLineID
			if s.StockUOM != nil {
				p.StockUOM, p.StockQuantity = s.StockUOM, s.StockQuantity
			}
			out = append(out, p)
			continue
		}
		// A component line the explosion minted: quantity and cost, no price.
		uom := ""
		if s.UOM != nil {
			uom = *s.UOM
		}
		c := QuoteLine{
			ID: s.ID, QuoteID: quoteID, LineType: s.LineType, ParentLineID: s.ParentLineID,
			ProductID: s.ProductID, Description: s.Description,
			Quantity: derefQty(s.Quantity), UOM: product.UOM(uom), PriceUOM: derefString(s.PriceUOM),
			UOMQty: derefQty(s.UOMQty), PriceUOMQty: derefQty(s.PriceUOMQty),
			UnitPrice: derefPrice(s.UnitPrice), LineTotal: derefCents(s.LineTotal),
			StockUOM: s.StockUOM, StockQuantity: s.StockQuantity,
			CreatedAt: s.CreatedAt,
		}
		if s.SKU != nil {
			c.SKU = *s.SKU
		}
		if s.ProductID != nil {
			c.UnitCost = products[*s.ProductID].AverageCost
		}
		out = append(out, c)
	}
	return out
}

func derefQty(q *httpx.Quantity) httpx.Quantity {
	if q == nil {
		return 0
	}
	return *q
}

func derefPrice(p *httpx.Price) httpx.Price {
	if p == nil {
		return 0
	}
	return *p
}

func derefCents(c *httpx.Cents) httpx.Cents {
	if c == nil {
		return 0
	}
	return *c
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
