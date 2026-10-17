// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// priceFloatOfQty renders a scale 4 quantity as the float the cart's stored
// pair carries.
func priceFloatOfQty(q httpx.Quantity) float64 { return float64(q) / 10000 }

// GetCart returns the current cart for a customer, creating one if it doesn't exist.
func (s *Service) GetCart(ctx context.Context, customerID uuid.UUID) (*CartDTO, error) {
	cart, err := s.repo.GetCartByCustomer(ctx, customerID)
	if err != nil {
		// Create new cart
		cartID, cErr := s.repo.CreateCart(ctx, customerID)
		if cErr != nil {
			return nil, fmt.Errorf("failed to create cart: %w", cErr)
		}
		return &CartDTO{
			ID:    cartID,
			Items: []CartItemDTO{},
		}, nil
	}
	return cart, nil
}

// AddToCart adds a product to the customer's cart with customer-specific pricing.
func (s *Service) AddToCart(ctx context.Context, customerID uuid.UUID, req AddToCartRequest) (*CartDTO, error) {
	if req.Quantity <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}

	// Ensure cart exists
	cart, err := s.GetCart(ctx, customerID)
	if err != nil {
		return nil, err
	}

	// Get customer-specific price
	prod, pErr := s.productSvc.GetProduct(ctx, req.ProductID)
	if pErr != nil {
		return nil, fmt.Errorf("product not found: %w", pErr)
	}
	unitPrice := prod.BasePrice
	// The line shape's unit fields (ADR 0006 section 7.6): the quantity is
	// in the product's sale unit, the price per the engine's own unit, the
	// pair resolved here and stored on the item. The engine did not answer:
	// the base price stands per the product's own unit, pair 1 and 1.
	units := CartItemUnits{UOM: string(prod.UOMPrimary), PriceUOM: string(prod.UOMPrimary), UOMQty: 1, PriceUOMQty: 1}
	if res, rErr := s.pricingSvc.Resolve(ctx, pricing.Request{
		CustomerID: customerID, ProductID: req.ProductID,
		Quantity: httpx.Quantity(int64(req.Quantity*10000 + 0.5)), UOM: string(prod.UOMPrimary),
	}); rErr == nil {
		units = CartItemUnits{
			UOM: res.UOM, PriceUOM: res.PriceUOM,
			UOMQty: priceFloatOfQty(res.UOMQty), PriceUOMQty: priceFloatOfQty(res.PriceUOMQty),
		}
		if price, _, ok := s.resolveCustomerPrice(ctx, customerID, req.ProductID, string(prod.UOMPrimary), req.Quantity); ok {
			unitPrice = price
		}
	}

	err = s.repo.AddCartItem(ctx, cart.ID, req.ProductID, req.Quantity, unitPrice, units)
	if err != nil {
		return nil, fmt.Errorf("failed to add to cart: %w", err)
	}

	return s.GetCart(ctx, customerID)
}

// UpdateCartItem updates the quantity of a cart item.
func (s *Service) UpdateCartItem(ctx context.Context, customerID uuid.UUID, itemID uuid.UUID, req UpdateCartItemRequest) (*CartDTO, error) {
	if req.Quantity <= 0 {
		return nil, fmt.Errorf("quantity must be positive")
	}

	err := s.repo.UpdateCartItemQty(ctx, itemID, req.Quantity, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to update cart item: %w", err)
	}

	return s.GetCart(ctx, customerID)
}

// RemoveCartItem removes an item from the cart.
func (s *Service) RemoveCartItem(ctx context.Context, customerID uuid.UUID, itemID uuid.UUID) (*CartDTO, error) {
	err := s.repo.RemoveCartItem(ctx, itemID, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to remove cart item: %w", err)
	}
	return s.GetCart(ctx, customerID)
}

// Checkout converts the current cart into an order, then clears the cart.
func (s *Service) Checkout(ctx context.Context, customerID uuid.UUID, req CheckoutRequest) (*CheckoutResponse, error) {
	cart, err := s.GetCart(ctx, customerID)
	if err != nil {
		return nil, err
	}

	if len(cart.Items) == 0 {
		return nil, fmt.Errorf("cart is empty")
	}

	// Tenancy: project_id arrives in the request body and is therefore
	// caller-controlled. Verify it against the session's customer before the
	// order is written, or a portal user could file an order against another
	// contractor's job — and then read that job's name back on the order DTO.
	if req.ProjectID != nil {
		ok, pErr := s.repo.ProjectBelongsToCustomer(ctx, *req.ProjectID, customerID)
		if pErr != nil {
			return nil, fmt.Errorf("failed to verify project: %w", pErr)
		}
		if !ok {
			return nil, ErrProjectNotFound
		}
	}

	// Build the order draft from the cart. The lines carry no price: the
	// order prices them through the same engine the catalog priced the cart
	// with (price_source PRICE_LIST), so the money never crosses the portal
	// as a float (ADR 0005 sections 2.3 and 2.7).
	deliveryType := order.DeliveryDelivery
	if strings.EqualFold(req.DeliveryMethod, "PICKUP") {
		deliveryType = order.DeliveryPickup
	}
	lines := make([]salesdoc.ParsedLine, 0, len(cart.Items))
	for _, item := range cart.Items {
		productID := item.ProductID
		qty := httpx.Quantity(int64(math.Round(item.Quantity * 10000)))
		line := salesdoc.ParsedLine{
			LineType:  salesdoc.LineProduct,
			ProductID: &productID,
			Quantity:  qty,
		}
		// The units the item was added in (ADR 0006 section 7.6): the order
		// prices the line in the cart's own unit and pair, so the money the
		// customer saw is the money the order resolves.
		if item.UOM != "" {
			line.UOM = item.UOM
		}
		if item.PriceUOM != "" {
			line.PriceUOM = item.PriceUOM
		}
		if item.UOMQty > 0 && item.PriceUOMQty > 0 && (item.UOMQty != 1 || item.PriceUOMQty != 1) {
			line.UOMQty = httpx.Quantity(int64(math.Round(item.UOMQty * 10000)))
			line.PriceUOMQty = httpx.Quantity(int64(math.Round(item.PriceUOMQty * 10000)))
		}
		lines = append(lines, line)
	}
	draft := &order.Draft{
		CustomerID:   customerID,
		DeliveryType: deliveryType,
		Lines:        lines,
	}
	// The order is written at the customer's branch: the portal calls the
	// order service in process, with no branch context of its own to inherit.
	if cust, cErr := s.customerSvc.GetCustomer(ctx, customerID); cErr == nil && cust.PrimaryBranchID != uuid.Nil {
		branch := cust.PrimaryBranchID
		draft.BranchID = &branch
	}

	// The portal is a trusted in process caller naming the customer's own
	// branch; it carries no branch context of its own, so the payload branch
	// rule (ADR 0007 2.3) would refuse it. The customer scoping is the
	// portal's own wall.
	newOrder, err := s.orderSvc.Create(branchctx.WithSystem(ctx), draft, "portal:customer:"+customerID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to create order: %w", err)
	}

	// The project association is written after the order exists rather than
	// threaded through order.CreateOrderRequest: project is a portal concept
	// (projects belong to a customer, not to a branch), and order.Service has
	// no business knowing about it. A failure here is logged, not fatal — the
	// order is real and the customer's stock is committed; losing the board
	// grouping is recoverable via PUT /orders/{id}/project, losing the order
	// is not.
	if req.ProjectID != nil {
		if pErr := s.repo.SetOrderProject(ctx, newOrder.ID, customerID, req.ProjectID); pErr != nil {
			s.logger.Error("Checkout: failed to attach order to project",
				"order_id", newOrder.ID, "project_id", *req.ProjectID, "error", pErr)
		}
	}

	// Clear cart after successful order
	if clearErr := s.repo.ClearCart(ctx, cart.ID); clearErr != nil {
		s.logger.Error("Failed to clear cart after checkout", "cart_id", cart.ID, "error", clearErr)
	}

	s.logger.Info("Portal checkout complete",
		"customer_id", customerID,
		"order_id", newOrder.ID,
		"items", len(lines),
		"delivery_method", req.DeliveryMethod,
	)

	return &CheckoutResponse{
		OrderID: newOrder.ID,
		Message: "Order placed successfully",
	}, nil
}

// Ensure imported packages are used.
var (
	_ = (*order.Service)(nil)
	_ = (*customer.Service)(nil)
)
