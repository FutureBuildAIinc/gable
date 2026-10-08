// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package orderwire builds the order service once, for both roles of the core
// binary: serve (the HTTP surface that confirms and fulfils at the desk) and
// the worker (whose queue jobs bill delivery completions). The tax provider
// behind the rate resolver and the exposure gate are wired here and nowhere
// else, so the two roles cannot drift: a delivery completion invoice was
// billed by the branch rate resolver because the worker's copy of the service
// was built without the provider (PR 43 review round 1, P2-1).
package orderwire

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Deps is everything the order service needs beyond the database and the
// config. Both roles pass their own instances; the provider and the gate are
// built inside, from the config and the database alone, so a role cannot
// forget them.
type Deps struct {
	DB     *database.DB
	Config *config.Config
	Logger *slog.Logger
	// AuditLog is the shared audit logger; nil builds one on the database.
	AuditLog *audit.Logger
	// Inventory is the stock service (required: allocation and fulfilment).
	Inventory *inventory.Service
	// Invoices is the invoice writer the fulfilment bills through (required).
	Invoices *invoice.Service
	// Pricing and Customers back the price engine adapter (ADR 0005 section 1).
	Pricing   *pricing.Service
	Customers *customer.Service
	// Escalators and QuoteLines back the exposure service behind the
	// pre-ship gate's override route.
	Escalators pricing.EscalatorRepository
	QuoteLines quote.QuoteLineReader
}

// New builds the order service with every seam both roles need: the outbox,
// the transaction runner, the audit log, the pricing engine wrapped at the
// boundary, inventory, the invoice writer, the configured tax provider behind
// the rate resolver (ADR 0005 section 3) and the pre-ship exposure gate.
func New(d Deps) *order.Service {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	auditLog := d.AuditLog
	if auditLog == nil {
		auditLog = audit.NewLogger(d.DB)
	}

	// The exposure gate (wire_exposure in serve before this package existed):
	// the checker the confirm and fulfilment paths consult, and the service
	// behind the exposure-override route.
	exposureChecker := pricing.NewExposureChecker(d.DB)
	exposureSvc := pricing.NewExposureService(
		pricing.NewExposureRepository(d.DB), d.Escalators, d.QuoteLines,
		&auditAdapter{auditLog: auditLog}, exposureChecker, logger,
	).WithOutbox(outbox.NewWriter(d.DB, d.Config.EventsOrg)).WithTxRunner(d.DB)

	return order.NewService(order.NewRepository(d.DB)).
		WithOutbox(outbox.NewWriter(d.DB, d.Config.EventsOrg)).
		WithTxRunner(d.DB).
		WithAuditLog(auditLog).
		WithPriceEngine(&PriceEngineAdapter{Pricing: d.Pricing, Customers: d.Customers}).
		WithInventory(d.Inventory).
		WithInvoices(d.Invoices).
		WithTaxProvider(&TaxProviderAdapter{Svc: NewTaxService(d.DB, d.Config, logger)}).
		WithExposureGate(exposureChecker, &ExposureOverriderAdapter{Svc: exposureSvc})
}

// NewTaxService builds the tax service both the order provider adapter and
// the tax routes use: the exemption repository always, the Avalara client
// when the deployment carries its settings (ADR 0005 section 3).
func NewTaxService(db *database.DB, cfg *config.Config, logger *slog.Logger) *tax.Service {
	var avalaraClient *tax.AvalaraClient
	if cfg.AvalaraAccountID != "" {
		avalaraClient = tax.NewAvalaraClient(tax.AvalaraConfig{
			AccountID:       cfg.AvalaraAccountID,
			LicenseKey:      cfg.AvalaraLicenseKey,
			Environment:     cfg.AvalaraEnvironment,
			CompanyCode:     cfg.AvalaraCompanyCode,
			BaseURLOverride: cfg.AvalaraBaseURL,
		}, logger)
	}
	return tax.NewService(tax.NewExemptionRepo(db), avalaraClient, cfg.AvalaraCompanyCode, 0.0, logger)
}

// PriceEngineAdapter wraps today's pricing engine at the order boundary
// (ADR 0005 section 1): the engine answers in dollars, and its result is
// converted to a scale 4 price once, here, never inside the order module.
type PriceEngineAdapter struct {
	Pricing   *pricing.Service
	Customers *customer.Service
}

func (a *PriceEngineAdapter) PriceFor(ctx context.Context, customerID, productID uuid.UUID, basePrice httpx.Price, quantity httpx.Quantity, jobID *uuid.UUID) (httpx.Price, error) {
	cust, err := a.Customers.GetCustomer(ctx, customerID)
	if err != nil {
		return 0, fmt.Errorf("price engine: customer: %w", err)
	}
	base := float64(basePrice) / 10000
	cp, err := a.Pricing.CalculatePriceWithQty(ctx, cust, productID, base, float64(quantity)/10000, jobID)
	if err != nil {
		return 0, fmt.Errorf("price engine: %w", err)
	}
	return httpx.Price(int64(math.Round(cp.FinalPrice * 10000))), nil
}

// TaxProviderAdapter is the configured provider path behind the rate
// resolver (ADR 0005 section 3).
type TaxProviderAdapter struct {
	Svc *tax.Service
}

func (a *TaxProviderAdapter) Configured() bool { return a.Svc.ProviderConfigured() }
func (a *TaxProviderAdapter) PreviewTax(ctx context.Context, req *tax.TaxPreviewRequest) (*tax.TaxResult, error) {
	return a.Svc.PreviewTax(ctx, req)
}

// ExposureOverriderAdapter bridges pricing.ExposureService.OverrideForOrder
// (which returns the created event) to order.ExposureOverrider (error-only).
type ExposureOverriderAdapter struct {
	Svc *pricing.ExposureService
}

func (a *ExposureOverriderAdapter) OverrideForOrder(ctx context.Context, orderID uuid.UUID, notes, actor, role string) error {
	_, err := a.Svc.OverrideForOrder(ctx, orderID, notes, actor, role)
	return err
}

// auditAdapter bridges the synchronous pkg/audit.Logger to the narrow
// pricing.AuditWriter interface (which re-declares the audit entry to avoid a
// pricing->pkg/audit import). EntityID arrives as a string; non-UUID values map
// to uuid.Nil rather than dropping the audit entry.
type auditAdapter struct {
	auditLog *audit.Logger
}

func (a *auditAdapter) LogEntry(ctx context.Context, e pricing.AuditEntry) {
	if a.auditLog == nil {
		return
	}
	entityID, err := uuid.Parse(e.EntityID)
	if err != nil {
		entityID = uuid.Nil
	}
	a.auditLog.Log(ctx, audit.Entry{
		Action:     e.Action,
		EntityType: e.EntityType,
		EntityID:   entityID,
		UserID:     e.UserID,
		Changes:    e.Changes,
	})
}
