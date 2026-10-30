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
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
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
	// Accounts is the AR core the fulfilment applies the order's deposits
	// through (ADR 0005 5.6 step 7); nil leaves deposits unapplied.
	Accounts *account.Service
	// Pricing backs the price engine seam (ADR 0006 section 7.5): the order
	// prices through Resolve, the engine's one entry point.
	Pricing *pricing.Service
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

	svc := order.NewService(order.NewRepository(d.DB)).
		WithOutbox(outbox.NewWriter(d.DB, d.Config.EventsOrg)).
		WithTxRunner(d.DB).
		WithAuditLog(auditLog).
		WithPriceEngine(&PriceEngineAdapter{Pricing: d.Pricing}).
		WithInventory(d.Inventory).
		WithInvoices(d.Invoices).
		WithTaxProvider(&TaxProviderAdapter{Svc: NewTaxService(d.DB, d.Config, logger)}).
		WithExposureGate(exposureChecker, &ExposureOverriderAdapter{Svc: exposureSvc})
	// The invoice void reaches back into the order (its row lock, its lines,
	// its stock and status): the invoice module cannot import this one, so the
	// seam is wired here, in the one constructor both roles use.
	d.Invoices.WithOrders(svc).WithStock(d.Inventory)
	if d.Accounts != nil {
		svc.WithDeposits(d.Accounts)
	}
	return svc
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

// PriceEngineAdapter is the order boundary on pricing.Resolve (ADR 0006
// sections 5.3 and 7.5): the engine's answer carries its own unit and the
// line's conversion pair, exact, and nothing here converts a price.
type PriceEngineAdapter struct {
	Pricing *pricing.Service
}

func (a *PriceEngineAdapter) PriceFor(ctx context.Context, customerID, productID uuid.UUID, quantity httpx.Quantity, uom string, jobID *uuid.UUID) (salesdoc.EngineAnswer, error) {
	req := pricing.Request{
		CustomerID: customerID,
		ProductID:  productID,
		Quantity:   quantity,
		UOM:        uom,
		JobID:      jobID,
		BranchID:   middleware.BranchIDForQuery(ctx),
	}
	res, err := a.Pricing.Resolve(ctx, req)
	if err != nil {
		return salesdoc.EngineAnswer{}, fmt.Errorf("price engine: %w", err)
	}
	return salesdoc.EngineAnswer{
		Price:       res.UnitPrice,
		PriceUOM:    res.PriceUOM,
		UOMQty:      res.UOMQty,
		PriceUOMQty: res.PriceUOMQty,
		Basis:       string(res.PriceBasis),
	}, nil
}

// SheetPrice is the offline price sheet's row (ADR 0011 section 5.4): the
// engine with rung 2 skipped, for the customer, or for the level the
// customer levelOf holds, at the branch and its business date, in the unit
// asked, for a quantity of one (the rungs that answer take no quantity).
func (a *PriceEngineAdapter) SheetPrice(ctx context.Context, customerID uuid.UUID, levelOf *uuid.UUID, productID, branch uuid.UUID,
	uom string, on time.Time) (salesdoc.EngineAnswer, error) {
	req := pricing.Request{CustomerID: customerID, ProductID: productID, BranchID: &branch, UOM: uom,
		Quantity: httpx.Quantity(10_000), On: on, SkipRules: true}
	if levelOf != nil {
		program, err := a.Pricing.CustomerProgram(ctx, *levelOf)
		if err != nil {
			return salesdoc.EngineAnswer{}, fmt.Errorf("price engine: %w", err)
		}
		if program == nil {
			return salesdoc.EngineAnswer{}, fmt.Errorf("price engine: the customer holds no level")
		}
		req.CustomerID, req.Level = uuid.Nil, program
	}
	res, err := a.Pricing.Resolve(ctx, req)
	if err != nil {
		return salesdoc.EngineAnswer{}, fmt.Errorf("price engine: %w", err)
	}
	return salesdoc.EngineAnswer{Price: res.UnitPrice, PriceUOM: res.PriceUOM, UOMQty: res.UOMQty,
		PriceUOMQty: res.PriceUOMQty, Basis: string(res.PriceBasis)}, nil
}

// AccountLevel names the level the customer prices at, when it holds one.
func (a *PriceEngineAdapter) AccountLevel(ctx context.Context, customerID uuid.UUID) (uuid.UUID, bool, error) {
	program, err := a.Pricing.CustomerProgram(ctx, customerID)
	if err != nil || program == nil {
		return uuid.Nil, false, err
	}
	return program.LevelID, true, nil
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
