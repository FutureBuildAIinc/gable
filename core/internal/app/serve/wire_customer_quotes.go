// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/portal"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/clientip"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// quoteOrders places the dealer's order for a confirmed customer quote
// through the portal's own order path.
type quoteOrders struct{ portal *portal.Service }

func (q quoteOrders) PlaceOrder(ctx context.Context, p customerquote.OrderPlacement) (uuid.UUID, error) {
	lines := make([]salesdoc.ParsedLine, 0, len(p.Lines))
	for _, l := range p.Lines {
		pid := l.ProductID
		lines = append(lines, salesdoc.ParsedLine{LineType: salesdoc.LineProduct, ProductID: &pid, Quantity: httpx.Quantity(l.Quantity), UOM: l.UOM})
	}
	return q.portal.PlaceOrderFromLines(ctx, p.CustomerID, p.ProjectID, p.Reference, lines)
}

// customerQuoteDeps is what the customer quote surface is built from.
type customerQuoteDeps struct {
	DB        *database.DB
	Logger    *slog.Logger
	Portal    *portal.Service
	Pricing   *pricing.Service
	Customers *customer.Service
	Products  *product.Service
	Email     notification.EmailService
	Docs      *document.Service
	PublicURL string
	Trusted   clientip.Trusted
	// Quotes, Audit and Wall serve the dealer's own quote link (PE-05): the
	// quote lifecycle the link decides through, the audit log the decision is
	// recorded in, and the branch wall the dealer's issuing route sits behind.
	Quotes *quote.Service
	Audit  *audit.Logger
	Wall   *branchWall
}

// wireCustomerQuotes mounts the three surfaces of the contractor's customer
// quotes: the contractor's own routes behind the portal chain, the capability
// link's public routes (no authentication layer, the token is the authority:
// /api/public/v1/ is a public path) and the dealer's desk routes behind the
// desk role guard.
func wireCustomerQuotes(mux *http.ServeMux, d customerQuoteDeps, portalChain func(http.Handler) http.Handler) *customerquote.Service {
	store := customerquote.NewStore(d.DB)
	svc := customerquote.NewService(d.DB, store, &customerquote.PortalCostSource{
		Pricing: d.Pricing, Customers: d.Customers, Products: d.Products, DB: d.DB,
	}, d.Logger).
		WithOrders(quoteOrders{portal: d.Portal}).
		// There is no live SMS: the stub takes the message and sends nothing.
		WithNotifier(customerquote.NewMailNotifier(d.Email, customerquote.NewStubSMS(d.Logger), d.Logger)).
		WithPDF(d.Docs).
		WithPublicURL(d.PublicURL)
	if d.Quotes != nil {
		svc.WithDealerQuotes(customerquote.QuoteLifecycle{Quotes: d.Quotes}, d.Audit)
	}
	customerquote.NewHandler(svc).RegisterRoutes(mux, portalChain)
	customerquote.NewPublicHandler(svc, d.Trusted).RegisterRoutes(mux)
	dealer := customerquote.NewDealerHandler(svc)
	// The dealer's desk reads the contractors' projects behind the branch
	// wall: the role guard, the branch middleware, and in the service the
	// customer module's own reach predicate, so a user granted another branch
	// sees none of them.
	deskGuard := middleware.RequireRole("admin", "owner", "sales")
	if d.Wall != nil {
		deskGuard = d.Wall.scoped("admin", "owner", "sales")
	}
	dealer.RegisterRoutes(mux, deskGuard, deskGuard)
	if d.Wall != nil {
		// The dealer's own quote link sits behind the quote module's role and
		// branch wall: the quote is read under the caller's branches, so the desk
		// reaches only the quotes it may already read. A branch bound machine key
		// is refused it outright (the route allowlist): the link is a public
		// credential, issued by a named desk user.
		dealer.RegisterShareLinkRoute(mux, d.Wall.scoped("admin", "owner", "sales"))
	}
	return svc
}
