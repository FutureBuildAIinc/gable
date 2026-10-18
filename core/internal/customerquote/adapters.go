// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PortalCostSource answers CostSource over the portal's existing price lookup:
// the pricing engine the catalog and the cart price a contractor account with.
// It is the one place the dealer's cost is read; the engine that replaces it
// swaps in here.
type PortalCostSource struct {
	Pricing   *pricing.Service
	Customers *customer.Service
	Products  *product.Service
	DB        *database.DB
}

// CatalogCost prices a product for the account at a quantity.
func (p *PortalCostSource) CatalogCost(ctx context.Context, customerID, productID uuid.UUID, qty httpx.Quantity) (*CatalogItem, error) {
	prod, err := p.Products.GetProduct(ctx, productID)
	if err != nil || prod == nil {
		return nil, nil
	}
	cust, err := p.Customers.GetCustomer(ctx, customerID)
	if err != nil {
		return nil, fmt.Errorf("read contractor account: %w", err)
	}
	// The engine takes a float64 quantity and base price at its edge and
	// answers an exact scale 4 price; the float never leaves this call.
	sp, err := p.Pricing.CalculateScaled(ctx, cust, productID, prod.BasePrice, float64(qty)/10_000, nil)
	if err != nil {
		return nil, fmt.Errorf("price product for the account: %w", err)
	}
	item := &CatalogItem{
		ProductID: productID, SKU: prod.SKU, Description: prod.Description, UOM: string(prod.UOMPrimary),
		UnitCostTenThousandths: int64(sp.Price),
	}
	var cat *uuid.UUID
	if err := p.DB.GetExecutor(ctx).QueryRow(ctx, `SELECT category_id FROM products WHERE id = $1`, productID).Scan(&cat); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read product category: %w", err)
	}
	item.CategoryID = cat
	return item, nil
}

// DealerQuote reads one of the account's own priced dealer quotes. The unit
// cost of a line is its line total over its quantity, per sale unit, at scale
// 4.
func (p *PortalCostSource) DealerQuote(ctx context.Context, customerID, quoteID uuid.UUID) (*SourceQuote, error) {
	ex := p.DB.GetExecutor(ctx)
	src := &SourceQuote{ID: quoteID}
	err := ex.QueryRow(ctx, `SELECT expires_at FROM quotes WHERE id = $1 AND customer_id = $2 AND state IN ('SENT', 'ACCEPTED')`, quoteID, customerID).Scan(&src.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dealer quote: %w", err)
	}
	rows, err := ex.Query(ctx, `
		SELECT ql.id, ql.product_id, ql.sku, ql.description, ql.uom::text, p.category_id, ROUND(ql.quantity * 10000)::bigint,
			ROUND(ql.line_total * 10000 / ql.quantity)::bigint
		FROM quote_lines ql JOIN products p ON p.id = ql.product_id
		WHERE ql.quote_id = $1 AND ql.quantity > 0 ORDER BY ql.position, ql.id`, quoteID)
	if err != nil {
		return nil, fmt.Errorf("read dealer quote lines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l SourceQuoteLine
		var qty int64
		if err := rows.Scan(&l.ID, &l.ProductID, &l.SKU, &l.Description, &l.UOM, &l.CategoryID, &qty, &l.UnitCostTenThousandths); err != nil {
			return nil, err
		}
		l.Quantity = httpx.Quantity(qty)
		src.Lines = append(src.Lines, l)
	}
	return src, rows.Err()
}

// QuoteLifecycle answers DealerQuotes over the quote module: reads through its
// service (under the caller's branch wall on the dealer side, as a system
// caller on the link's side, where the link is the authority) and decisions
// through its lifecycle, the same transition the portal's accept uses.
type QuoteLifecycle struct {
	Quotes *quote.Service
}

func (q QuoteLifecycle) doc(qt *quote.Quote) *DealerQuoteDoc {
	d := &DealerQuoteDoc{ID: qt.ID, Number: qt.Number, State: string(qt.Status), CustomerName: qt.CustomerName,
		FreightCents: int64(qt.FreightCents), TotalCents: int64(qt.TotalCents)}
	if qt.ExpiresAt != nil {
		t := qt.ExpiresAt.Time
		d.ExpiresAt = &t
	}
	for _, l := range qt.Lines {
		price := l.PriceUOM
		if price == "" {
			price = string(l.UOM)
		}
		d.Lines = append(d.Lines, DealerQuoteLine{Description: l.Description, Quantity: l.Quantity, UOM: string(l.UOM),
			PriceUOM: price, UnitPrice: l.UnitPrice, LineTotal: int64(l.LineTotal)})
	}
	return d
}

// Read reads a quote under the caller's wall.
func (q QuoteLifecycle) Read(ctx context.Context, id uuid.UUID) (*DealerQuoteDoc, error) {
	qt, err := q.Quotes.GetQuote(ctx, id)
	if err != nil {
		return nil, err
	}
	return q.doc(qt), nil
}

// ReadByLink reads the quote a link names.
func (q QuoteLifecycle) ReadByLink(ctx context.Context, id uuid.UUID) (*DealerQuoteDoc, error) {
	qt, err := q.Quotes.GetQuote(branchctx.WithSystem(ctx), id)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) && he.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return q.doc(qt), nil
}

// Decide moves the quote through the lifecycle.
func (q QuoteLifecycle) Decide(ctx context.Context, id uuid.UUID, accept bool) error {
	to := quote.QuoteStateRejected
	if accept {
		to = quote.QuoteStateAccepted
	}
	return q.Quotes.UpdateState(branchctx.WithSystem(ctx), id, to)
}
