// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// TxRunner runs fn in one transaction (database.DB.RunInTx). Every store
// call made with the context fn receives joins that transaction.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// CatalogItem is a dealer product priced for one contractor account.
type CatalogItem struct {
	ProductID              uuid.UUID
	SKU                    string
	Description            string
	UOM                    string
	CategoryID             *uuid.UUID
	UnitCostTenThousandths int64
}

// SourceQuoteLine is a line of the dealer's own quote to the contractor.
type SourceQuoteLine struct {
	ID                     uuid.UUID
	ProductID              uuid.UUID
	SKU                    string
	Description            string
	UOM                    string
	CategoryID             *uuid.UUID
	Quantity               httpx.Quantity
	UnitCostTenThousandths int64
}

// SourceQuote is the dealer's quote the contractor builds from.
type SourceQuote struct {
	ID        uuid.UUID
	ExpiresAt *time.Time
	Lines     []SourceQuoteLine
}

// CostSource is the one seam to the dealer's prices: what the dealer charges
// this contractor account. The portal's existing price lookup answers it
// today (PortalCostSource); the engine that replaces it swaps in behind this
// interface without touching the domain.
type CostSource interface {
	// CatalogCost prices one product for the account at a quantity.
	CatalogCost(ctx context.Context, customerID, productID uuid.UUID, qty httpx.Quantity) (*CatalogItem, error)
	// DealerQuote reads one of the account's own dealer quotes, nil when the
	// account has no such quote.
	DealerQuote(ctx context.Context, customerID, quoteID uuid.UUID) (*SourceQuote, error)
}

// OrderLine is one line of the order a confirmation places.
type OrderLine struct {
	ProductID uuid.UUID
	Quantity  httpx.Quantity
	UOM       string
}

// OrderPlacement is what a confirmation asks the dealer's order path for.
type OrderPlacement struct {
	CustomerID  uuid.UUID
	ProjectID   uuid.UUID
	Reference   string
	SiteAddress string
	Lines       []OrderLine
}

// OrderPlacer places the dealer order for the approved material, through the
// portal's own checkout path (the order service prices every line at the
// account's dealer prices). It joins the transaction of the context.
type OrderPlacer interface {
	PlaceOrder(ctx context.Context, p OrderPlacement) (uuid.UUID, error)
}

// Notifier sends the two messages the loop sends. It is told what to say and
// to whom; whether anything really leaves the process is its business.
type Notifier interface {
	// SendLink delivers the capability link to the customer. delivered is
	// false when nothing real was sent (no address, or a log only sender):
	// the contractor then shares the link themselves.
	SendLink(ctx context.Context, to Contact, contractor, title, link string) (delivered bool, err error)
	// SendCode delivers a verification code by email or sms.
	SendCode(ctx context.Context, channel, destination, contractor, code string) error
}

// PDFRenderer renders the customer's PDF from the public view alone.
type PDFRenderer interface {
	CustomerQuotePDF(ctx context.Context, v *PublicView, logo *Logo) ([]byte, error)
}
