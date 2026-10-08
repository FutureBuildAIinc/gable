// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package serve is the server role of the core binary: it wires every
// module's repository, service and handler onto one ServeMux and runs the
// HTTP API with graceful shutdown. It is the body of the old cmd/server
// entry point, moved here so it can be imported; cmd/server and the one
// core binary's `serve` subcommand both call Run.
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/bankrecon"
	"github.com/gablelbm/gable/internal/chargecode"
	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/configurator"
	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customer/customeraudit"
	"github.com/gablelbm/gable/internal/dashboard"
	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/deposit"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/edi"
	"github.com/gablelbm/gable/internal/events"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/governance"
	"github.com/gablelbm/gable/internal/integrations"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/matching"
	"github.com/gablelbm/gable/internal/millwork"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/parsing"
	"github.com/gablelbm/gable/internal/partner"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pim"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/portal"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/project"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/salesteam"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/internal/vision"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/apps"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/clientip"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/metrics"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// publicAPIPaths are the paths the auth layer skips: health and metrics, the
// portal's public endpoints, the integration seam (which keeps its own
// X-Integration-Key authentication) and the a2a JWS seam. Both auth mounts
// (the JWT middleware and the standalone machine-key mount) share the list,
// so a machine key is never consulted on a seam that does not know it.
var publicAPIPaths = []string{
	"/health",
	"/healthz/live",
	"/healthz/ready",
	"/metrics",
	"/api/portal/v1/login",
	"/api/portal/v1/config",
	"/api/portal/v1/",
	"/api/integration/",
	"/api/v1/a2a/",
}

// machineKeyValidator adapts the techadmin service to the machine-key auth
// core's KeyValidator seam: a credential failure (unknown, revoked or
// malformed key) crosses as middleware.ErrInvalidMachineKey, anything else as
// an infrastructure fault.
type machineKeyValidator struct {
	svc *techadmin.Service
}

func (v machineKeyValidator) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	k, err := v.svc.ValidateKey(ctx, rawKey)
	if err != nil {
		if errors.Is(err, techadmin.ErrInvalidKey) {
			return middleware.KeyPrincipal{}, middleware.ErrInvalidMachineKey
		}
		return middleware.KeyPrincipal{}, err
	}
	return middleware.KeyPrincipal{ID: k.ID, Scopes: k.Scopes}, nil
}

// Run starts the HTTP API server and blocks until SIGINT or SIGTERM, then
// shuts down gracefully. It is the body of the old cmd/server entry point;
// that entry point and the one core binary (cmd/core, `core serve`) both
// call it, so the two run the same code.
func Run() {
	startTime := time.Now()

	// 1. Setup Structured Logging (JSON) with configurable level
	logLevel := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	// 2. Load Config
	cfg, err := config.Load()
	if err != nil {
		logger.Error("Configuration error", "error", err)
		os.Exit(1)
	}
	// Configure log level
	switch strings.ToUpper(cfg.LogLevel) {
	case "DEBUG":
		logLevel.Set(slog.LevelDebug)
	case "WARN":
		logLevel.Set(slog.LevelWarn)
	case "ERROR":
		logLevel.Set(slog.LevelError)
	default:
		logLevel.Set(slog.LevelInfo)
	}

	// Validate CORS_ORIGINS in production mode (fail-closed like JWKS_URL)
	if !strings.EqualFold(cfg.AuthMode, "dev") && os.Getenv("CORS_ORIGINS") == "" {
		logger.Error("CORS_ORIGINS not set and AUTH_MODE != dev; set CORS_ORIGINS for production or AUTH_MODE=dev for development")
		os.Exit(1)
	}

	logger.Info("Starting server...", "port", cfg.Port, "auth_mode", cfg.AuthMode, "log_level", cfg.LogLevel)
	cfg.TrustedProxies.LogConfigured(logger)

	// 3. Database Connection
	db, err := database.Connect(cfg.DatabaseURL, database.PoolConfig{
		MaxConns:          cfg.DBMaxConns,
		MinConns:          cfg.DBMinConns,
		MaxConnLifetime:   time.Duration(cfg.DBMaxConnLifetime) * time.Minute,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: 1 * time.Minute,
	})
	if err != nil {
		logger.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	logger.Info("Connected to database")

	// 3b. Initialize Prometheus Metrics
	metrics.Register()
	metricsCtx, metricsCancel := context.WithCancel(context.Background())
	defer metricsCancel()
	metrics.StartDBPoolCollector(metricsCtx, db.Pool, 15*time.Second)
	logger.Info("Prometheus metrics initialized")

	// 3c. Initialize Audit Logger (financial operation tracking)
	auditLog := audit.NewLogger(db)
	logger.Info("Audit logger initialized")

	// 3d. Machine-key auth core (R1-13). Scoped machine keys authenticate in
	// both auth modes: the JWT middleware dispatches to this core on a
	// machine-key-shaped Bearer token, and AUTH_MODE=dev mounts it standalone,
	// so a key grants the same reach in dev as in production and a keyed
	// integration is developed against the dev stack without a JWKS. The
	// service is shared with the tech admin handler wired further down.
	techAdminSvc := techadmin.NewService(techadmin.NewRepository(db))
	machineKeyAuth := middleware.NewMachineKeyAuth(
		machineKeyValidator{svc: techAdminSvc},
		auditLog,
		publicAPIPaths,
		logger,
	)

	// 4. Initialize Auth Middleware
	// Fail-closed: JWKS_URL is required unless AUTH_MODE=dev is explicitly set.
	var authMw *middleware.AuthMiddleware
	if cfg.JWKSURL != "" {
		logger.Info("Initializing Auth Middleware", "jwks_url", cfg.JWKSURL)
		am, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{
			JWKSURL:     cfg.JWKSURL,
			Issuer:      cfg.AuthIssuer,
			PublicPaths: publicAPIPaths,
			MachineKeys: machineKeyAuth,
		}, logger)
		if err != nil {
			logger.Error("Failed to initialize Auth Middleware", "error", err)
			os.Exit(1)
		}
		authMw = am
	} else if strings.EqualFold(cfg.AuthMode, "dev") {
		logger.Warn("AUTH_MODE=dev: authentication disabled (development only)")
	} else {
		logger.Error("JWKS_URL not set and AUTH_MODE != dev; set JWKS_URL for production or AUTH_MODE=dev for development")
		os.Exit(1)
	}

	// 4b. Branch Context Middleware — enforces multi-branch scoping per
	// the user_locations grant table. Controlled by system_settings keys
	// `multi_branch_enabled` (kill switch) and `default_branch_required`.
	wall := newBranchWall(db)
	scoped := wall.scoped // role guard plus branch middleware, for any module group whose entities carry a branch_id

	// 5. Setup Router & Modules
	mux := http.NewServeMux()

	// 5a. Apps registry — the installable-apps platform layer
	// (docs/modularization-blueprint.md). Converted modules register through
	// it (gated on per-instance enablement); everything else is declared in
	// catalog.go as core until converted.
	appRegistry := apps.NewRegistry(db, logger).WithAudit(auditLog)

	// Initialize Modules

	// Product Module
	productRepo := product.NewRepository(db)
	productSvc := product.NewService(productRepo)
	productHandler := product.NewHandler(productSvc)
	productHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales", "warehouse"))

	// Unified AI client — one OpenRouter key (DB-first via system_settings, env
	// fallback) powers all AI features: material-list/freight OCR, PIM content, and
	// product image generation. Base URL and per-task model slugs are admin-overridable.
	aiKeyStore := ai.NewKeyStore(db.Pool, "openrouter_api_key", cfg.OpenRouterAPIKey)
	aiBaseURLStore := ai.NewKeyStore(db.Pool, "openrouter_base_url", cfg.OpenRouterBaseURL)
	aiClient := ai.NewClientWithKeyStore(aiKeyStore).
		WithBaseURLStore(aiBaseURLStore).
		WithModels(ai.NewModelRouter(db.Pool, ai.ModelDefaults{
			Text:   cfg.AIModelText,
			Vision: cfg.AIModelVision,
			Cheap:  cfg.AIModelCheap,
			Image:  cfg.AIModelImage,
		}))
	if cfg.OpenRouterAPIKey != "" {
		logger.Info("AI initialized via OpenRouter (env key present, admin can override via Tech Admin > AI Settings)")
	} else {
		logger.Info("AI initialized via OpenRouter (no env key — admin can configure via Tech Admin > AI Settings)")
	}

	// AI Parsing Module (Material List Intake)
	parsingSvc := parsing.NewService(productRepo, aiClient)
	parsingHandler := parsing.NewHandler(parsingSvc)
	parsingHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales"))

	// PIM Module (AI-Powered Product Information Management) — text generation and
	// image generation both run through the unified OpenRouter client.
	pimRepo := pim.NewRepository(db)
	pimSvc := pim.NewService(pimRepo, productSvc)
	pimSvc.WithAI(aiClient)

	pimHandler := pim.NewHandler(pimSvc)
	pimHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	locationSvc := location.NewService(location.NewRepository(db))
	locationUserRepo := location.NewUserRepository(db)
	locationHandler := location.NewHandler(
		locationSvc,
		locationUserRepo,
		middleware.RequireRole("admin", "owner"),
	)
	// Location routes run behind the branch middleware through
	// branchWall.locations: POST /locations writes into a branch's tree, the
	// by-id reads and the list are held to the caller's branches (the branch
	// switcher reads /me/branches, not the list), and branch CRUD stays
	// unscoped directory data.
	wall.locations(mux, locationHandler)

	// Inventory Service needs to be shared to Order Service
	inventoryRepo := inventory.NewRepository(db)
	inventorySvc := inventory.NewService(inventoryRepo)
	wall.inventory(mux, inventorySvc)

	customerRepo := customer.NewRepository(db)
	customerSvc := customer.NewService(customerRepo).
		WithOutbox(outbox.NewWriter(db, cfg.EventsOrg)).
		WithTxRunner(db).
		WithAudit(customeraudit.New(auditLog))
	wall.customers(mux, customerSvc)

	// Sales Team Module
	salesTeamRepo := salesteam.NewRepository(db)
	salesTeamHandler := salesteam.NewHandler(salesTeamRepo)
	salesTeamHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales"))

	// CRM Module
	crmRepo := crm.NewRepository(db)
	crmHandler := crm.NewHandler(crmRepo)
	crmHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales"))

	// Account Module
	accountRepo := account.NewRepository(db)
	accountSvc := account.NewService(accountRepo, db, logger)
	accountHandler := account.NewHandler(accountSvc)
	accountHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales", "finance"))

	quoteRepo := quote.NewRepository(db)
	// The quote module is the wire template (docs/refactor/MODULE-RECIPE.md):
	// its writes run in one transaction with their quote.* outbox event as the
	// last statement.
	quoteSvc := quote.NewService(quoteRepo).
		WithOutbox(outbox.NewWriter(db, cfg.EventsOrg)).
		WithTxRunner(db)
	wall.quotes(mux, quoteSvc)

	// GL Module (Full General Ledger)
	glAdapter := glint.NewMockGLAdapter()
	glRepo := gl.NewRepository(db)
	glSvc := gl.NewService(glRepo, glAdapter, logger)
	glHandler := gl.NewHandler(glSvc)
	glHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// Invoice Module
	invoiceRepo := invoice.NewRepository(db)
	invoiceSvc := invoice.NewService(invoiceRepo, glSvc, accountSvc, db)
	invoiceSvc.WithAuditLog(auditLog)
	invoiceHandler := invoice.NewHandler(invoiceSvc)
	invoiceHandler.RegisterRoutes(mux, scoped("admin", "owner", "sales", "finance"))

	// Deposit Module (customer prepayments held as 2200 liability, applied to AR)
	depositRepo := deposit.NewRepository(db)
	depositSvc := deposit.NewService(db, depositRepo, glSvc, accountSvc, logger)
	depositSvc.WithAuditLog(auditLog)
	depositHandler := deposit.NewHandler(depositSvc)
	depositHandler.RegisterRoutes(mux, scoped("admin", "owner", "sales", "finance"))

	// Pricing Module
	pricingRepo := pricing.NewRepository(db)
	pricingSvc := pricing.NewService(pricingRepo)
	pricingHandler := pricing.NewHandler(pricingSvc, customerSvc, productSvc)
	pricingHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// Category Pricing Engine (feature-flagged)
	if strings.EqualFold(os.Getenv("CATEGORY_PRICING_ENABLED"), "true") {
		catPricingRepo := pricing.NewCategoryRepository(db)
		catPricingSvc := pricing.NewCategoryPricingService(catPricingRepo)
		pricingSvc.WithCategoryPricing(catPricingSvc)

		catPricingHandler := pricing.NewCategoryHandler(catPricingSvc, customerSvc)
		catPricingHandler.RegisterCategoryRoutes(mux, middleware.RequireRole("admin", "owner"))

		logger.Info("Category-based pricing engine enabled")
	} else {
		logger.Info("Category-based pricing disabled (set CATEGORY_PRICING_ENABLED=true to enable)")
	}

	// Rebate Module
	rebateRepo := pricing.NewRebateRepository(db)
	rebateSvc := pricing.NewRebateService(rebateRepo)
	rebateHandler := pricing.NewRebateHandler(rebateSvc)
	rebateHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// Escalator Pricing Module (Market Indices + Price Escalators)
	escalatorRepo := pricing.NewEscalatorRepository(db)
	escalatorSvc := pricing.NewEscalatorService(escalatorRepo)
	escalatorHandler := pricing.NewEscalatorHandler(escalatorSvc)
	escalatorHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// Vendor Module
	vendorRepo := vendor.NewRepository(db)
	vendorSvc := vendor.NewService(vendorRepo)
	vendorHandler := vendor.NewHandler(vendorSvc)
	vendorHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "purchasing"))

	// Back-wire vendor service into product service so CreateProduct can
	// auto-resolve a free-text vendor name to a canonical vendor_id.
	productSvc.WithVendorService(vendorSvc)

	// Order Module - injected with InventoryService and InvoiceService
	orderRepo := order.NewRepository(db)
	poRepo := purchase_order.NewRepository(db)

	// EDI Module
	// EDI_OUTPUT_DIR, defaulting to a path relative to the working directory.
	// This was hardcoded to "/app/edi_out" — a path that only exists inside the
	// container image — so every local run and every non-container self-host
	// logged "Failed to create EDI output dir: permission denied" at boot and
	// carried on with EDI output silently broken. Set EDI_OUTPUT_DIR=/app/edi_out
	// in container deployments.
	ediSvc := edi.NewService(cfg.EDIOutputDir, logger)

	poSvc := purchase_order.NewService(poRepo, db, ediSvc, inventorySvc, productSvc, vendorSvc)
	poSvc.WithAIClient(aiClient)
	// The receive writes purchase_order.received in its transaction (ADR 0005 5.4).
	poSvc.WithOutbox(outbox.NewWriter(db, cfg.EventsOrg))
	velocityRepo := purchase_order.NewVelocityRepository(db)
	poSvc.WithVelocityRepo(velocityRepo)
	poRecSvc := purchase_order.NewRecommendationService(poRepo, inventorySvc, productSvc, vendorSvc).
		WithVelocityRepo(velocityRepo)
	wall.purchaseOrders(mux, purchase_order.NewHandler(poSvc, poRecSvc))

	// Auto-reorder scheduler. Disabled by default; an operator activates it
	// by setting reorder.enabled=true in system_settings. Stops in step 3.5
	// of graceful shutdown (before DB pool close).
	reorderScheduler := purchase_order.NewScheduler(db, poSvc)
	if err := reorderScheduler.Start(context.Background()); err != nil {
		logger.Error("reorder scheduler failed to start", "error", err)
	}

	// Auto-PO: wire quote service to create POs when quotes are accepted
	quoteSvc.WithAutoPO(&autoPOAdapter{poSvc: poSvc, productSvc: productSvc})

	// Buying Group EDI Service (832/846 catalog sync)
	bgSvc := edi.NewBuyingGroupService(logger)

	// EDI Trading Partner Admin (vendor-agnostic)
	ediRepo := edi.NewEDIRepository(db)
	ediHandler := edi.NewEDIHandler(ediRepo, bgSvc, ediSvc)
	ediHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// The order module on the wire contract (ADR 0005 section 5): every write
	// one transaction with its order.* outbox event, the payload branch rule,
	// the pricing engine wrapped at the boundary, and the tax provider behind
	// the rate resolver.
	orderSvc := order.NewService(orderRepo).
		WithOutbox(outbox.NewWriter(db, cfg.EventsOrg)).
		WithTxRunner(db).
		WithAuditLog(auditLog).
		WithPriceEngine(&priceEngineAdapter{pricing: pricingSvc, customers: customerSvc}).
		WithInventory(inventorySvc).
		WithInvoices(invoiceSvc)
	wall.orders(mux, orderSvc)
	// The charge code master (ADR 0005 section 2.5), contract born.
	chargecode.NewHandler(chargecode.NewService(chargecode.NewRepository(db))).
		RegisterRoutes(mux, scoped("admin", "owner", "sales", "finance"), scoped("admin", "owner", "finance"))
	// Quote conversion creates the order in one act (ADR 0005 section 5.8).
	quoteSvc.WithOrderCreator(orderSvc)

	// Notification Module
	emailSvc := notification.NewLogEmailService(logger)

	// Document Module
	docSvc := document.NewService(productRepo)
	docHandler := document.NewHandler(docSvc, orderSvc, invoiceSvc, customerSvc, emailSvc)
	wall.documents(mux, docHandler)

	// Sales Tax Module (exemptions + Avalara when configured; wired before
	// Payment/POS because both consume the tax service).
	taxExemptionRepo := tax.NewExemptionRepo(db)
	var avalaraClient *tax.AvalaraClient
	if cfg.AvalaraAccountID != "" {
		avalaraClient = tax.NewAvalaraClient(tax.AvalaraConfig{
			AccountID:   cfg.AvalaraAccountID,
			LicenseKey:  cfg.AvalaraLicenseKey,
			Environment: cfg.AvalaraEnvironment,
			CompanyCode: cfg.AvalaraCompanyCode,
		}, logger)
		logger.Info("Avalara AvaTax initialized", "environment", cfg.AvalaraEnvironment)
	} else {
		logger.Info("AVALARA_ACCOUNT_ID not set — POS/invoice tax uses the branch default rate (locations.default_tax_rate)")
	}
	taxSvc := tax.NewService(taxExemptionRepo, avalaraClient, cfg.AvalaraCompanyCode, 0.0, logger)
	// The configured provider path sits behind the order rate resolver
	// (ADR 0005 section 3).
	orderSvc.WithTaxProvider(&taxProviderAdapter{svc: taxSvc})
	taxHandler := tax.NewHandler(taxSvc)
	taxHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "finance"))

	// Payment Module (with Run Payments gateway)
	paymentRepo := payment.NewRepository(db)
	paymentSvc := payment.NewService(db, paymentRepo, invoiceRepo, accountSvc)
	paymentSvc.WithAuditLog(auditLog)

	// Run Payments gateway — always constructed; credentials resolve at call
	// time, DB-first (system_settings run_payments_* keys, settable in Tech
	// Admin) with RUN_PAYMENTS_* env fallback. Card processing lights up the
	// moment a key exists, no restart needed.
	// Credential vault: seals Run api_key/refresh_token at rest (AES-256-GCM).
	// A malformed key is logged and the store falls back to plaintext with a
	// prominent warning rather than bricking boot.
	paymentVault, verr := payment.NewVault(cfg.PaymentVaultKey)
	if verr != nil {
		logger.Error("PAYMENT_VAULT_KEY invalid — payment credentials will NOT be encrypted at rest", "error", verr)
		paymentVault = &payment.Vault{}
	} else if paymentVault.Present() {
		logger.Info("payment credential vault active (AES-256-GCM at rest)")
	} else {
		logger.Warn("PAYMENT_VAULT_KEY not set — Run Payments credentials stored plaintext; set a 32-byte hex key to encrypt at rest")
	}
	paymentKeys := payment.NewKeyStore(db, payment.GatewayConfig{
		APIKey:      cfg.RunPaymentsAPIKey,
		PublicKey:   cfg.RunPaymentsPublicKey,
		MID:         cfg.RunPaymentsMID,
		BaseURL:     cfg.RunPaymentsBaseURL,
		Environment: cfg.RunPaymentsEnvironment,
	}).WithVault(paymentVault).WithLogger(logger)
	rpGateway := payment.NewRunPaymentsGatewayDynamic(paymentKeys.Resolve, logger).
		OnKeyRotated(func(apiKey, refreshToken string) {
			// The Run api_key is an expiring JWT — persist the refreshed one.
			if err := paymentKeys.PersistRotatedKey(apiKey, refreshToken); err != nil {
				logger.Error("failed to persist rotated Run Payments api_key", "error", err)
			} else {
				logger.Info("Run Payments api_key rotated and persisted")
			}
		})
	paymentSvc.WithGateway(rpGateway, cfg.RunPaymentsPublicKey)
	paymentSvc.WithKeyStore(paymentKeys)
	if paymentKeys.Configured() {
		logger.Info("Run Payments gateway configured", "environment", cfg.RunPaymentsEnvironment)
	} else {
		logger.Warn("Run Payments key not set (env or settings) — card charges will fail until run_payments_api_key is configured")
	}

	paymentHandler := payment.NewHandler(paymentSvc)
	paymentHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales", "finance", "cashier"))

	// POS Module (Retail Counter Sales)
	posRepo := pos.NewRepository(db)
	posSvc := pos.NewService(db, posRepo, productSvc, inventorySvc, invoiceSvc, paymentSvc, logger)
	posSvc.WithPricing(&posCalcAdapter{pricingSvc: pricingSvc, customerSvc: customerSvc})
	posSvc.WithAuditLog(auditLog)
	posSvc.WithTax(taxSvc, invoiceRepo)
	posSvc.WithTillLedger(glSvc) // post drawer over/short to the GL at close
	// POS card tenders ride the CARD-PRESENT rail (Clover merchant terminal /
	// Run Terminal API), NOT the Run online/keyed-web charge API (rpGateway) —
	// that rail is the Contractor Portal / invoice online-payment path. Until
	// the Clover terminal integration lands, counter CARD tenders are recorded
	// as externally-captured (the device settles); posSvc.WithGateway is where
	// the Clover terminal gateway wires in.
	posHandler := pos.NewHandler(posSvc)
	posHandler.RegisterRoutes(mux, scoped("admin", "owner", "cashier"))

	// Accounts Payable Module
	apRepo := ap.NewRepository(db)
	apSvc := ap.NewService(db, apRepo, glSvc, logger)
	apHandler := ap.NewHandler(apSvc)
	apHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "finance"))

	// 3-Way PO Matching Module
	matchingRepo := matching.NewRepository(db)
	matchingSvc := matching.NewService(db, matchingRepo, poSvc, apSvc, logger)
	wall.matching(mux, matchingSvc)

	// Bank Reconciliation Module
	bankreconRepo := bankrecon.NewRepository(db)
	bankreconSvc := bankrecon.NewService(db, bankreconRepo, glSvc, logger)
	bankreconHandler := bankrecon.NewHandler(bankreconSvc)
	bankreconHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "finance"))

	// Reporting Module
	reportingRepo := reporting.NewRepository(db)
	reportingSvc := reporting.NewService(reportingRepo)
	reportingHandler := reporting.NewHandler(reportingSvc)
	reportingHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "finance"))

	// Scheduled report delivery. Loads every ACTIVE row from report_schedules,
	// runs each on the cron engine, and emails the rendered CSV/XLSX to its
	// recipients via emailSvc — which is the log-only LogEmailService, so the
	// report is genuinely generated and the send is recorded in the log rather
	// than transmitted. The schedule API publishes that distinction itself
	// (execution.enabled plus execution.delivery), derived from this executor,
	// so wiring it here is what flips the API from "saved but never run" to
	// "runs". Stops in step 3.6 of graceful shutdown, before the DB pool closes.
	reportScheduler := reporting.NewScheduler(reportingSvc, emailSvc)
	if err := reportScheduler.Start(context.Background()); err != nil {
		logger.Error("report scheduler failed to start", "error", err)
	}
	wireReportSchedules(mux, reportingHandler, reportScheduler)
	logger.Info("Scheduled report delivery enabled",
		"cron_dialect", "six fields, seconds first",
		"delivery", reportScheduler.DeliveryDescription())

	reportingHandler.RegisterBIIntegrationRoutes(mux, middleware.RequireRole("admin", "owner"))

	// The idempotency retention purge no longer runs here: R1-4 moved it to
	// the worker role (internal/app/worker), so `core worker` owns it.

	// Delivery Module
	deliveryRepo := delivery.NewRepository(db)
	deliverySvc := delivery.NewService(deliveryRepo)

	// Wire OpenRouteService for route optimization + geocoding. The client reads
	// its key dynamically (DB system_settings → env fallback), so an admin can
	// enable real routing at runtime via Tech Admin > Routing without a restart.
	// Until a key is set, optimization falls back to a deterministic mock and
	// delivery addresses are mock-geocoded, so the demo map still populates.
	orsKeyStore := ai.NewKeyStore(db.Pool, "openrouteservice_api_key", cfg.ORSAPIKey)
	orsClient := delivery.NewORSClientWithKeyStore(orsKeyStore.Get, cfg.ORSBaseURL, cfg.ORSProfile, logger)
	deliverySvc.WithRouting(orsClient, logger)
	if orsKeyStore.IsConfigured(context.Background()) {
		logger.Info("OpenRouteService routing + geocoding enabled", "profile", cfg.ORSProfile)
	} else {
		logger.Warn("OpenRouteService key not set — mock routing until configured via Tech Admin > Routing")
	}

	deliveryHandler := delivery.NewHandler(deliverySvc)
	deliveryHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "warehouse", "driver"))

	// Lumber index-aware quote price protection (the exposure module).
	// Snapshots a baseline index when a quote is sent, detects moves past the
	// per-customer threshold, applies the snapshotted policy, and gates order
	// confirm/fulfil and delivery route assignment. Notification events are
	// recorded in the transactional outbox inside the mutation's transaction
	// and the worker role's drain delivers the committed rows; see
	// wire_exposure.go and pkg/outbox.
	// Must come after deliverySvc, its last dependency.
	exposureWiring := wireExposure(exposureDeps{
		Mux:           mux,
		DB:            db,
		Logger:        logger,
		AuditLog:      auditLog,
		EscalatorRepo: escalatorRepo,
		QuoteRepo:     quoteRepo,
		QuoteSvc:      quoteSvc,
		OrderSvc:      orderSvc,
		DeliverySvc:   deliverySvc,
		EventsOrg:     cfg.EventsOrg,
	})

	// SMS Notification Service
	var smsSvc notification.SMSService
	if cfg.TwilioAccountSID != "" {
		smsSvc = notification.NewTwilioSMSService(notification.TwilioConfig{
			AccountSID: cfg.TwilioAccountSID,
			AuthToken:  cfg.TwilioAuthToken,
			FromNumber: cfg.TwilioFromNumber,
		}, logger)
		logger.Info("Twilio SMS service initialized")
	} else {
		smsSvc = notification.NewLogSMSService(logger)
		logger.Warn("TWILIO_ACCOUNT_SID not set — using mock SMS service")
	}

	// Delivery Notification Orchestrator
	deliveryNotifier := notification.NewDeliveryNotifier(smsSvc, emailSvc, logger)
	deliverySvc.WithNotifier(&deliveryNotifierAdapter{notifier: deliveryNotifier})

	// Wire invoice service for auto-invoicing on delivery POD
	deliverySvc.WithInvoiceService(&invoiceServiceAdapter{invoiceSvc: invoiceSvc, orderSvc: orderSvc})

	// Millwork App (converted — reference conversion #1)
	// One app, two backend modules: millwork (option catalogs) + configurator
	// (rules/validation/build-sku) both gate on the "millwork" app key.
	millworkRepo := millwork.NewRepository(db)
	millworkSvc := millwork.NewService(millworkRepo)
	millworkHandler := millwork.NewHandler(millworkSvc)
	configuratorRepo := configurator.NewRepository(db)
	configuratorSvc := configurator.NewService(configuratorRepo)
	configuratorHandler := configurator.NewHandler(configuratorSvc)
	appRegistry.Add(apps.App{Manifest: millwork.App, Register: func(r apps.Router) {
		millworkHandler.RegisterRoutes(r, middleware.RequireRole("admin", "owner", "sales"))
		configuratorHandler.RegisterRoutes(r, middleware.RequireRole("admin", "owner", "sales"))
	}})

	// AI Vision Module (Sprint 19: Blueprint Verification Prototype)
	visionSvc := vision.NewService()
	visionHandler := vision.NewHandler(visionSvc)
	visionHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// Governance App (converted — reference conversion #2)
	governanceRepo := governance.NewRepository(db)
	aiProvider := governance.NewTemplateAIProvider()
	governanceSvc := governance.NewService(governanceRepo, aiProvider)
	governanceHandler := governance.NewHandler(governanceSvc)
	appRegistry.Add(apps.App{Manifest: governance.App, Register: func(r apps.Router) {
		governanceHandler.RegisterRoutes(r, middleware.RequireRole("admin", "owner"))
	}})

	// Partner Module
	partnerSvc := partner.NewService(customerRepo, quoteRepo, logger)
	partnerHandler := partner.NewHandler(partnerSvc)
	partnerAuthMw := middleware.NewPartnerAuthMiddleware(customerRepo, logger)
	partnerHandler.RegisterRoutes(mux, partnerAuthMw.Handler)

	// Dashboard Module (Executive Analytics)
	dashboardRepo := dashboard.NewRepository(db)
	dashboardSvc := dashboard.NewService(dashboardRepo)
	dashboardHandler := dashboard.NewHandler(dashboardSvc)
	dashboardHandler.RegisterRoutes(mux, scoped("admin", "owner", "finance"))

	// Tech Admin Module (the service was built at startup, shared with the
	// machine-key validator)
	techAdminHandler := techadmin.NewHandler(techAdminSvc)
	techAdminHandler.WithAIKeyStore(aiKeyStore)
	techAdminHandler.WithAIBaseURLStore(aiBaseURLStore)
	techAdminHandler.WithORSKeyStore(orsKeyStore)
	techAdminHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// Events feed: the outbox read API (item R1-12), the one cursor-paginated
	// feed of every domain event. Role gated admin/owner like the other admin
	// reads; agents and integrations poll it with their own cursors.
	eventsHandler := events.NewHandler(db)
	eventsHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))

	// Portal Module (Sovereign Dealer Portal)
	// Resolve JWT secret: required in production, uses dev default in dev mode only.
	portalJWTSecret := os.Getenv("PORTAL_JWT_SECRET")
	if portalJWTSecret == "" {
		if strings.EqualFold(cfg.AuthMode, "dev") {
			portalJWTSecret = "portal-dev-secret-do-not-use-in-production"
			logger.Warn("PORTAL_JWT_SECRET not set — using dev-only default (AUTH_MODE=dev)")
		} else {
			logger.Error("PORTAL_JWT_SECRET not set — required for portal authentication")
			os.Exit(1)
		}
	}

	portalRepo := portal.NewRepository(db).WithOutbox(outbox.NewWriter(db, cfg.EventsOrg))
	portalSvc := portal.NewService(portalRepo, portalJWTSecret, logger, pricingSvc, customerSvc, inventorySvc, orderSvc, productSvc)
	// Reuse the module-level quoteSvc so a portal accept/decline runs the same
	// state machine — and the same auto-PO and price-protection side effects —
	// as a counter salesperson closing the quote from the ERP.
	portalSvc.WithQuoteService(quoteSvc)
	portalHandler := portal.NewHandler(portalSvc)

	// Portal auth middleware
	var portalMw func(http.Handler) http.Handler
	if strings.EqualFold(cfg.AuthMode, "dev") {
		logger.Warn("AUTH_MODE=dev: Portal auth bypassed — injecting Kelbrook demo customer claims")
		// Look up the Kelbrook Construction demo account; fall back to the
		// first customer in the table so a non-Kelowna fork can still boot.
		var demoCustomerID uuid.UUID
		row := db.Pool.QueryRow(context.Background(),
			"SELECT id FROM customers WHERE account_number = 'KELBROOK-001' LIMIT 1")
		if err := row.Scan(&demoCustomerID); err != nil {
			logger.Warn("Kelbrook demo customer not found, falling back to first customer", "error", err)
			fallback := db.Pool.QueryRow(context.Background(), "SELECT id FROM customers LIMIT 1")
			if err := fallback.Scan(&demoCustomerID); err != nil {
				logger.Error("Failed to load demo customer", "error", err)
				demoCustomerID = uuid.New() // Last-resort fallback
			}
		}
		portalMw = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				claims := &middleware.PortalClaims{
					CustomerID: demoCustomerID,
					Email:      "demo@kelbrook.ca",
					Name:       "Sam Kelbrook",
					// Role is load-bearing and was previously left empty, so
					// getPortalUserRole returned "" and requireAdmin refused
					// every request: GET /users, GET/POST /invites and the
					// role/status endpoints all returned 403 in dev mode, which
					// is the only mode the demo runs in. Team management looked
					// broken rather than bypassed. "Admin" matches the
					// vocabulary internal/portal enforces.
					Role: "Admin",
				}
				ctx := context.WithValue(r.Context(), middleware.PortalClaimsKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		}
	} else {
		portalAuthMw := middleware.NewPortalAuthMiddleware([]byte(portalJWTSecret), logger)
		portalMw = portalAuthMw.Handler
	}
	// Portal routes get their idempotency layer INSIDE portalMw: the claim is
	// scoped on the customer and user the portal auth chain establishes,
	// which the global layer (already inside the JWT auth middleware) cannot
	// see. The global layer skips the portal prefix, so nothing runs twice.
	portalIdem := middleware.IdempotencyForPortalAuth(db)
	portalChain := func(next http.Handler) http.Handler { return portalMw(portalIdem(next)) }
	portalHandler.RegisterRoutes(mux, portalChain, middleware.StrictRateLimit(10, cfg.TrustedProxies))

	// Project Module (Sprint 34: Project Management Dashboard)
	projectRepo := project.NewRepository(db)
	projectSvc := project.NewService(projectRepo)
	projectHandler := project.NewHandler(projectSvc)
	projectHandler.RegisterRoutes(mux, portalChain)

	// Staff roster and per-module access grants. This is the write side of
	// AI_LM's login path: it edits the rows POST /api/integration/validate-staff
	// reads back. Route list and the admin/owner guard: wire_staff.go.
	wireStaffAdmin(mux, db, auditLog)

	// Integration API. One X-Integration-Key-gated surface shared by the
	// FB-Brain cross-system endpoints and by AI_LM (github.com/gablelbm/
	// gable-ai-lm), which pulls fleet/orders/catalog, writes approved delivery
	// routes back, and authenticates its own operators through
	// POST /api/integration/validate-staff.
	//
	// Every route is guarded inside integrations.Handler.authMiddleware (a
	// constant-time compare of the X-Integration-Key header) rather than by the
	// JWT/role middleware used for the human-facing API, because these callers
	// are services, not sessions. With no key configured the whole surface
	// answers 503 — including AI_LM's login path, so AI_LM cannot sign anyone in.
	integrationAPIKey := os.Getenv("INTEGRATION_API_KEY")
	if integrationAPIKey == "" {
		if strings.EqualFold(cfg.AuthMode, "dev") {
			integrationAPIKey = "fb-brain-demo-key-2026"
		} else {
			logger.Warn("INTEGRATION_API_KEY not set — integration endpoints disabled (AI_LM cannot authenticate)")
		}
	}
	// Reuse the module-level quoteSvc (a second quote.NewService instance was
	// constructed inline here historically — two live services over one repo).
	integrationHandler := integrations.NewHandler(db, pricingSvc, quoteSvc, orderSvc, customerSvc, productSvc, integrationAPIKey)
	// The integration surface's idempotency layer runs INSIDE the
	// X-Integration-Key check (RegisterRoutes puts the wrap there): the claim
	// is scoped on the caller's tenant, and a caller that failed auth never
	// claims a key. The global layer skips the integration prefix.
	integrationHandler.RegisterRoutes(mux, middleware.IdempotencyForIntegrationAuth(db))

	// 5z. Apps platform: catalog the unconverted modules, mount converted
	// apps through the enablement gate, expose the Apps API, and sync
	// manifests to the `apps` table (operator-owned `enabled` is preserved).
	appRegistry.AddStatic(unconvertedAppCatalog...)
	appRegistry.Mount(mux)
	apps.NewHandler(appRegistry).RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
	{
		syncCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := appRegistry.Sync(syncCtx); err != nil {
			// Non-fatal: gating fails open and the catalog syncs on next boot
			// (e.g. first boot before migration 074 has been applied).
			logger.Warn("apps registry sync failed (continuing; run migrations)", "error", err)
		}
		cancel()
	}

	// F-04: FB Brain Integration — all Brain components gated behind FBBrainEnabled kill switch
	if cfg.FBBrainEnabled {
		logger.Info("FB Brain integration enabled", "base_url", cfg.FBBrainBaseURL)

		// Maestro AI Gateway — routes AI calls through Brain for metering
		maestroClient := ai.NewMaestroClient(cfg.FBBrainBaseURL, logger)
		_ = maestroClient // Available for AI module injection

		// Brain Notifier — sends financial events (invoice payments) to Brain
		brainNotifier := payment.NewBrainNotifier(cfg.FBBrainBaseURL, cfg.FBBrainIntegrationKey, logger)
		paymentSvc.WithBrainNotifier(brainNotifier, cfg.FBBrainOrgID)

		// A2A Receiver — inbound purchase order webhooks from Brain
		if cfg.FBBrainPublicKeyPath != "" {
			brainPubKey, err := purchase_order.LoadBrainPublicKey(cfg.FBBrainPublicKeyPath)
			if err != nil {
				logger.Error("Failed to load Brain public key for A2A receiver", "error", err, "path", cfg.FBBrainPublicKeyPath)
			} else {
				a2aReceiver := purchase_order.NewA2AReceiver(brainPubKey, poSvc, db.Pool, logger)
				mux.HandleFunc("POST /api/v1/a2a/purchase-order", a2aReceiver.ReceiveWebhook)
				logger.Info("A2A purchase order receiver mounted", "path", "/api/v1/a2a/purchase-order")
			}
		} else {
			logger.Warn("FB_BRAIN_PUBLIC_KEY_PATH not set — A2A receiver disabled (no JWS verification key)")
		}
	} else {
		logger.Info("FB Brain integration disabled (FB_BRAIN_ENABLED=false)")
	}

	// Static file serving for uploaded photos (auth-protected, no directory listing)
	uploadFS := noListingFileSystem{fs: http.Dir("uploads")}
	fileServer := http.FileServer(uploadFS)
	mux.Handle("/uploads/", middleware.RequireRole("admin", "owner", "user")(http.StripPrefix("/uploads/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		fileServer.ServeHTTP(w, r)
	}))))

	// Health Check — liveness (always 200 if process is running)
	mux.HandleFunc("GET /healthz/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Health Check — readiness (checks dependencies)
	mux.HandleFunc("GET /healthz/ready", func(w http.ResponseWriter, r *http.Request) {
		status := "ok"
		httpStatus := http.StatusOK
		dbStatus := "connected"
		if err := db.Pool.Ping(r.Context()); err != nil {
			status = "degraded"
			httpStatus = http.StatusServiceUnavailable
			dbStatus = "disconnected"
			logger.Error("Readiness check failed", "error", err)
		}

		poolStat := db.Pool.Stat()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": status,
			"uptime": time.Since(startTime).String(),
			"checks": map[string]interface{}{
				"database": map[string]interface{}{
					"status":      dbStatus,
					"pool_total":  poolStat.TotalConns(),
					"pool_idle":   poolStat.IdleConns(),
					"pool_in_use": poolStat.AcquiredConns(),
					"pool_max":    poolStat.MaxConns(),
				},
			},
		})
	})

	// Legacy /health endpoint (backward compat)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		status := "ok"
		dbStatus := "connected"
		if err := db.Pool.Ping(r.Context()); err != nil {
			status = "error"
			dbStatus = "disconnected"
			logger.Error("Health check failed", "error", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": status, "db": dbStatus})
	})

	// Prometheus metrics endpoint (public — scrape target)
	mux.Handle("GET /metrics", promhttp.Handler())

	// 6. Wrap Middleware (outermost first)
	var finalHandler http.Handler = mux

	// Cache-Control headers (innermost — runs after auth, before response)
	finalHandler = middleware.CacheControl(finalHandler)

	// Idempotency keys (POST/PUT with Idempotency-Key), one layer per surface
	// where the principal that scopes a claim is established. The global
	// layer here covers the ERP API only: it runs inside auth (the JWT
	// subject is the principal) and inside the request size limit (the
	// fingerprint read honours it), and it skips /api/portal/v1/ and
	// /api/integration/ because those surfaces carry their own layer inside
	// their auth chains (see the portal and integration wiring below), so
	// nothing runs twice. Claims live in Postgres (migration 087), so a
	// replay survives a restart.
	finalHandler = middleware.Idempotency(db)(finalHandler)

	// Request size limit (10MB default)
	finalHandler = middleware.MaxRequestSize(10 << 20)(finalHandler)

	// Auth (JWT verification; a Bearer machine key dispatches to the
	// machine-key core inside it). In AUTH_MODE=dev the JWT layer is off but
	// machine keys still authenticate and scope check exactly as behind it.
	if authMw != nil {
		finalHandler = authMw.Handler(finalHandler)
	} else {
		finalHandler = machineKeyAuth.Handler(finalHandler)
	}

	// Actor identity (agent headers → context for audit attribution).
	// Outside auth on purpose: this middleware wraps auth, so it runs before
	// it and the context it builds flows through auth to the handler; it
	// records who acted, it never grants anything.
	finalHandler = actor.Middleware(finalHandler)

	// CORS — must be outside auth so OPTIONS preflight is handled before auth
	finalHandler = middleware.CORSMiddleware(finalHandler)

	// Rate limiting (RATE_LIMIT_PER_MINUTE requests per IP, default 120)
	finalHandler = middleware.RateLimit(cfg.RateLimitPerMinute, cfg.TrustedProxies)(finalHandler)

	// Panic recovery
	finalHandler = middleware.Recovery(logger)(finalHandler)

	// Request ID generation
	finalHandler = middleware.RequestID(finalHandler)

	// Prometheus HTTP metrics
	finalHandler = metrics.HTTPMetrics(finalHandler)

	// Access logging (outermost — captures full request lifecycle)
	finalHandler = RequestLogger(logger, cfg.TrustedProxies, finalHandler)

	// 7. Start Server with Graceful Shutdown
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%s", cfg.Port),
		Handler:           finalHandler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// Run server in goroutine
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal using a buffered channel
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	logger.Info("Shutdown signal received", "signal", sig.String())

	// Create a deadline to wait for in-flight requests
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Step 1: Stop accepting new HTTP connections and drain in-flight requests
	logger.Info("Shutdown step 1/4: draining HTTP connections...")
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("HTTP server forced to shutdown", "error", err)
		os.Exit(1)
	}
	logger.Info("Shutdown step 1/4: HTTP server stopped")

	// Step 2: Cancel metrics collector goroutine
	logger.Info("Shutdown step 2/4: stopping metrics collector...")
	metricsCancel()
	logger.Info("Shutdown step 2/4: metrics collector stopped")

	// Step 3: Drain audit logger (wait for in-flight audit writes)
	slog.Info("draining audit logger")
	auditLog.Drain()

	// Step 3.5: Stop the auto-reorder cron scheduler so no new jobs tick
	// against a draining DB pool. In-flight jobs continue to completion;
	// their reorder_runs row is finalized before the pool closes.
	logger.Info("Shutdown step 3.5/4: stopping reorder scheduler...")
	reorderScheduler.Stop()
	logger.Info("Shutdown step 3.5/4: reorder scheduler stopped")

	// Step 3.6: Stop the scheduled-report cron. Same reasoning as 3.5: no new
	// report query may start against a draining pool, and an in-flight run
	// finishes (including its last_run_at/next_run_at write) before step 4.
	logger.Info("Shutdown step 3.6/4: stopping report scheduler...")
	reportScheduler.Stop()
	logger.Info("Shutdown step 3.6/4: report scheduler stopped")

	// Step 3.7: Stop the exposure safety-net cron and drain the in-process
	// event bus before the pool closes, so a queued notification handler
	// cannot fire against a dead pool.
	logger.Info("Shutdown step 3.7/4: stopping exposure wiring...")
	exposureWiring.Shutdown(ctx)
	logger.Info("Shutdown step 3.7/4: exposure wiring stopped")

	// (Step 3.8, the idempotency retention cron, moved to the worker role
	// with the purge itself; see internal/app/worker.)

	// Step 4: Close database connection pool
	logger.Info("Shutdown step 4/4: closing database pool...")
	db.Close()
	logger.Info("Shutdown step 4/4: database pool closed")

	logger.Info("Server exiting — clean shutdown complete")
}

// noListingFileSystem wraps http.Dir to prevent directory listing.
type noListingFileSystem struct {
	fs http.FileSystem
}

func (nfs noListingFileSystem) Open(path string) (http.File, error) {
	f, err := nfs.fs.Open(path)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if stat.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

// deliveryNotifierAdapter bridges delivery.DeliveryNotifierInterface and notification.DeliveryNotifier.
type deliveryNotifierAdapter struct {
	notifier *notification.DeliveryNotifier
}

func (a *deliveryNotifierAdapter) Notify(ctx context.Context, event delivery.DeliveryEvent) {
	a.notifier.Notify(ctx, notification.DeliveryEvent{
		EventType:     notification.DeliveryEventType(event.EventType),
		DeliveryID:    event.DeliveryID,
		OrderNumber:   event.OrderNumber,
		CustomerName:  event.CustomerName,
		CustomerPhone: event.CustomerPhone,
		CustomerEmail: event.CustomerEmail,
		ETA:           event.ETA,
		ReceiptURL:    event.ReceiptURL,
	})
}

// statusResponseWriter wraps http.ResponseWriter to capture the status code and bytes written.
type statusResponseWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int
	wroteHeader  bool
}

func (w *statusResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}

func (w *statusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// RequestLogger logs incoming requests with status code, bytes written, and request ID.
// remote_addr is the resolved client (see clientip), and a forwarding header
// from a peer outside the trusted proxies is reported as a warning.
func RequestLogger(logger *slog.Logger, trusted clientip.Trusted, next http.Handler) http.Handler {
	watch := clientip.NewForwardingWatch(logger, trusted)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		watch.Observe(r)
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytesWritten,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", trusted.Of(r),
			"request_id", middleware.GetRequestID(r.Context()),
		)
	})
}

// priceEngineAdapter wraps today's pricing engine at the order boundary
// (ADR 0005 section 1): the engine answers in dollars, and its result is
// converted to a scale 4 price once, here, never inside the order module.
type priceEngineAdapter struct {
	pricing   *pricing.Service
	customers *customer.Service
}

func (a *priceEngineAdapter) PriceFor(ctx context.Context, customerID, productID uuid.UUID, basePrice httpx.Price, quantity httpx.Quantity, jobID *uuid.UUID) (httpx.Price, error) {
	cust, err := a.customers.GetCustomer(ctx, customerID)
	if err != nil {
		return 0, fmt.Errorf("price engine: customer: %w", err)
	}
	base := float64(basePrice) / 10000
	cp, err := a.pricing.CalculatePriceWithQty(ctx, cust, productID, base, float64(quantity)/10000, jobID)
	if err != nil {
		return 0, fmt.Errorf("price engine: %w", err)
	}
	return httpx.Price(int64(math.Round(cp.FinalPrice * 10000))), nil
}

// taxProviderAdapter is the configured provider path behind the rate
// resolver (ADR 0005 section 3).
type taxProviderAdapter struct {
	svc *tax.Service
}

func (a *taxProviderAdapter) Configured() bool { return a.svc.ProviderConfigured() }
func (a *taxProviderAdapter) PreviewTax(ctx context.Context, req *tax.TaxPreviewRequest) (*tax.TaxResult, error) {
	return a.svc.PreviewTax(ctx, req)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// invoiceServiceAdapter bridges invoice.Service to delivery.InvoiceServiceInterface.
type invoiceServiceAdapter struct {
	invoiceSvc *invoice.Service
	orderSvc   *order.Service
}

func (a *invoiceServiceAdapter) CreateFromOrder(ctx context.Context, orderID uuid.UUID) error {
	// Double-invoice guard: if the order was already invoiced (the normal path
	// invoices it at fulfilment), do NOT create a second invoice on delivery.
	// AR is summed from invoices, so a duplicate would double-bill the customer.
	if exists, err := a.invoiceSvc.ExistsInvoiceForOrder(ctx, orderID); err != nil {
		return fmt.Errorf("check existing invoice: %w", err)
	} else if exists {
		return nil
	}

	ord, err := a.orderSvc.GetOrder(ctx, orderID)
	if err != nil {
		return fmt.Errorf("get order for invoice: %w", err)
	}

	// Build the invoice from the order's lines. The invoice module still
	// holds whole cents per sale unit, so a line whose conversion pair is not
	// 1 to 1 cannot be handed over without rounding its money away: the
	// adapter refuses it (the recipe's rule for a helper feeding an
	// unconverted neighbour) until the fulfilment route replaces this path
	// (ADR 0005 5.5, C2-2b).
	var lines []invoice.InvoiceLine
	for i := range ord.Lines {
		ol := &ord.Lines[i]
		if ol.LineType == salesdoc.LineText || ol.LineType == salesdoc.LineCharge {
			continue
		}
		if ol.UOMQty == nil || ol.PriceUOMQty == nil || *ol.UOMQty != salesdoc.One || *ol.PriceUOMQty != salesdoc.One {
			return fmt.Errorf("order %s has a line priced per %s: the delivery invoice path cannot carry a conversion pair until the fulfilment route lands",
				orderID, derefString(ol.PriceUOM))
		}
		if ol.ProductID == nil || ol.Quantity == nil || ol.UnitPrice == nil || ol.LineTotal == nil {
			continue
		}
		lines = append(lines, invoice.InvoiceLine{
			ProductID: *ol.ProductID,
			Quantity:  float64(*ol.Quantity) / 10000,
			PriceEach: int64(math.Round(float64(*ol.UnitPrice) / 100)),
		})
	}

	inv := &invoice.Invoice{
		CustomerID: ord.CustomerID,
		OrderID:    ord.ID,
		BranchID:   ord.BranchID, // invoice + tax rate come from the order's branch
		Lines:      lines,
	}

	if err := a.invoiceSvc.CreateInvoice(ctx, inv); err != nil {
		return err
	}
	// Book a delivery-created invoice to the GL + AR subledger too.
	return a.invoiceSvc.PostInvoiceToLedger(ctx, inv)
}

// autoPOAdapter bridges purchase_order.Service to quote.AutoPOService.
type autoPOAdapter struct {
	poSvc      *purchase_order.Service
	productSvc *product.Service
}

func (a *autoPOAdapter) CreatePOFromSpecialOrderLine(ctx context.Context, productID uuid.UUID, vendorID *uuid.UUID, quantity float64, unitCost float64, linkedSOLineID uuid.UUID) error {
	// Resolve product description for the PO line
	desc := productID.String()
	if a.productSvc != nil {
		p, err := a.productSvc.GetProduct(ctx, productID)
		if err == nil && p != nil {
			desc = fmt.Sprintf("%s - %s", p.SKU, p.Description)
		}
	}
	return a.poSvc.CreateFromSOLine(ctx, linkedSOLineID, vendorID, desc, quantity, unitCost)
}

// posCalcAdapter bridges pricing.Service + customer.Service to pos.PriceCalculator.
type posCalcAdapter struct {
	pricingSvc  *pricing.Service
	customerSvc *customer.Service
}

func (a *posCalcAdapter) CalculateItemPrice(ctx context.Context, customerID uuid.UUID, productID uuid.UUID, basePrice float64, quantity float64) (float64, error) {
	cust, err := a.customerSvc.GetCustomer(ctx, customerID)
	if err != nil {
		return basePrice, nil // Fallback to base price if customer lookup fails
	}
	cp, err := a.pricingSvc.CalculatePriceWithQty(ctx, cust, productID, basePrice, quantity, nil)
	if err != nil {
		return basePrice, nil
	}
	return cp.FinalPrice, nil
}
