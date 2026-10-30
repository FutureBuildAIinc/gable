// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/parsing"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Service encapsulates portal business logic.
type Service struct {
	repo         Repository
	jwtSecret    []byte
	logger       *slog.Logger
	pricingSvc   *pricing.Service
	customerSvc  *customer.Service
	inventorySvc *inventory.Service
	orderSvc     *order.Service
	productSvc   *product.Service

	// now is the service's clock: the instant the portal dates its money on.
	// A test moves it, so a branch's own day can be read on both sides of
	// its midnight.
	now func() time.Time

	// quoteSvc owns the quote LIFECYCLE. The portal never writes a state
	// transition itself — accept and decline go through
	// quote.Service.UpdateState so quote.validateStateTransition stays the
	// single authority on what may follow what. Optional: nil disables the
	// portal's quote decision endpoints rather than letting them bypass the
	// state machine.
	quoteSvc *quote.Service

	// Phase 2 upstream seams: material list parsing, AR core, invoices,
	// document PDF formatter, and the stubbed payment gateway (kept in
	// payments.go).
	parsingSvc     *parsing.Service
	accountSvc     *account.Service
	invoiceSvc     *invoice.Service
	docSvc         *document.Service
	paymentGateway any

	// paymentGatewayResolver, when wired, decides per call which gateway is
	// active (Run Payments when its keys are configured, the dev stub in
	// dev mode, none otherwise); see payments.go.
	paymentGatewayResolver func() payment.PaymentGateway
}

// NewService creates a new portal service.
// jwtSecret must be provided and non-empty; callers should fail startup if not configured.
func NewService(
	repo Repository,
	jwtSecret string,
	logger *slog.Logger,
	pricingSvc *pricing.Service,
	customerSvc *customer.Service,
	inventorySvc *inventory.Service,
	orderSvc *order.Service,
	productSvc *product.Service,
) *Service {
	return &Service{
		repo:         repo,
		jwtSecret:    []byte(jwtSecret),
		logger:       logger,
		pricingSvc:   pricingSvc,
		customerSvc:  customerSvc,
		inventorySvc: inventorySvc,
		orderSvc:     orderSvc,
		productSvc:   productSvc,
		now:          time.Now,
	}
}

// WithQuoteService wires the ERP quote lifecycle. Kept as a separate setter
// rather than a ninth constructor argument so existing callers — including the
// token-only construction the tests use — keep compiling unchanged.
func (s *Service) WithQuoteService(quoteSvc *quote.Service) *Service {
	s.quoteSvc = quoteSvc
	return s
}

// WithClockForTest sets the clock the portal reads its business day on (the
// service reads time.Now otherwise). The portal dates money on the branch's
// own day, so a test that moves the clock moves that day with it.
func (s *Service) WithClockForTest(now func() time.Time) *Service { s.now = now; return s }

// WithParsingService wires Gable's material list and photo intake service.
func (s *Service) WithParsingService(parsingSvc *parsing.Service) *Service {
	s.parsingSvc = parsingSvc
	return s
}

// WithAccountService wires Gable's AR core for statements, aging, and payments.
func (s *Service) WithAccountService(accountSvc *account.Service) *Service {
	s.accountSvc = accountSvc
	return s
}

// WithInvoiceService wires Gable's invoice service for PDF generation.
func (s *Service) WithInvoiceService(invoiceSvc *invoice.Service) *Service {
	s.invoiceSvc = invoiceSvc
	return s
}

// WithDocumentService wires Gable's PDF document formatter for invoices and statements.
func (s *Service) WithDocumentService(docSvc *document.Service) *Service {
	s.docSvc = docSvc
	return s
}

func (s *Service) pgRepo() (*PostgresRepository, bool) {
	r, ok := s.repo.(*PostgresRepository)
	return r, ok
}

// PortalClaims holds JWT claims for portal auth.
type PortalClaims struct {
	jwt.RegisteredClaims
	CustomerID     uuid.UUID `json:"customer_id"`
	CustomerUserID uuid.UUID `json:"customer_user_id"`
	Email          string    `json:"email"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
}

// LoginResult holds the login response and the raw JWT token (for cookie delivery).
type LoginResult struct {
	Response *LoginResponse
	Token    string
}

// Login authenticates a customer user and returns a LoginResult.
// The token is returned separately so the handler can set it as a cookie
// without exposing it in the JSON response body.
func (s *Service) Login(ctx context.Context, req LoginRequest) (*LoginResult, error) {
	user, err := s.repo.GetCustomerUserByEmail(ctx, req.Email)
	if err != nil {
		s.logger.Warn("Portal login: user not found", "email", req.Email)
		return nil, fmt.Errorf("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		s.logger.Warn("Portal login: invalid password", "email", req.Email)
		return nil, fmt.Errorf("invalid credentials")
	}

	// An Inactive user is refused with the same answer as a bad password; the
	// bcrypt compare above runs first so the response time does not leak status.
	if !strings.EqualFold(user.Status, "Active") {
		s.logger.Warn("Portal login: user is not active", "email", req.Email)
		return nil, fmt.Errorf("invalid credentials")
	}

	// Generate JWT
	now := time.Now()
	claims := PortalClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
			Issuer:    "gable-portal",
		},
		CustomerID:     user.CustomerID,
		CustomerUserID: user.ID,
		Email:          user.Email,
		Name:           user.Name,
		Role:           user.Role,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(s.jwtSecret)
	if err != nil {
		s.logger.Error("Portal login: failed to sign JWT", "error", err)
		return nil, fmt.Errorf("authentication failed")
	}

	// Get portal config for branding
	cfg, err := s.repo.GetPortalConfig(ctx)
	if err != nil {
		s.logger.Error("Portal login: failed to get config", "error", err)
		return nil, fmt.Errorf("authentication failed")
	}

	s.logger.Info("Portal login success", "email", req.Email, "customer_id", user.CustomerID)

	return &LoginResult{
		Response: &LoginResponse{
			User:   *user,
			Config: *cfg,
		},
		Token: tokenStr,
	}, nil
}

// GetConfig returns portal branding config (public).
func (s *Service) GetConfig(ctx context.Context) (*PortalConfig, error) {
	return s.repo.GetPortalConfig(ctx)
}

// GetDashboard returns the contractor dashboard data.
func (s *Service) GetDashboard(ctx context.Context, customerID uuid.UUID) (*PortalDashboardDTO, error) {
	balance, creditLimit, pastDue, err := s.repo.GetCustomerARSummary(ctx, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load dashboard: %w", err)
	}

	orders, err := s.repo.ListOrdersByCustomer(ctx, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to load recent orders: %w", err)
	}

	// Limit to 5 most recent for dashboard
	recentOrders := orders
	if len(recentOrders) > 5 {
		recentOrders = recentOrders[:5]
	}

	return &PortalDashboardDTO{
		BalanceDue:   balance,
		CreditLimit:  creditLimit,
		PastDue:      pastDue,
		RecentOrders: recentOrders,
	}, nil
}

// ListOrders returns all orders for a customer.
func (s *Service) ListOrders(ctx context.Context, customerID uuid.UUID) ([]PortalOrderDTO, error) {
	return s.repo.ListOrdersByCustomer(ctx, customerID)
}

// GetOrder returns a single order scoped to a customer.
func (s *Service) GetOrder(ctx context.Context, orderID, customerID uuid.UUID) (*PortalOrderDTO, error) {
	return s.repo.GetOrderByIDAndCustomer(ctx, orderID, customerID)
}

// ListInvoices returns all invoices for a customer.
func (s *Service) ListInvoices(ctx context.Context, customerID uuid.UUID) ([]PortalInvoiceDTO, error) {
	return s.repo.ListInvoicesByCustomer(ctx, customerID)
}

// GetInvoice returns a single invoice scoped to a customer.
func (s *Service) GetInvoice(ctx context.Context, invoiceID, customerID uuid.UUID) (*PortalInvoiceDTO, error) {
	return s.repo.GetInvoiceByIDAndCustomer(ctx, invoiceID, customerID)
}

// ListDeliveries returns all deliveries for a customer.
func (s *Service) ListDeliveries(ctx context.Context, customerID uuid.UUID) ([]PortalDeliveryDTO, error) {
	return s.repo.ListDeliveriesByCustomer(ctx, customerID)
}

// GetDelivery returns a single delivery scoped to a customer.
func (s *Service) GetDelivery(ctx context.Context, deliveryID, customerID uuid.UUID) (*PortalDeliveryDTO, error) {
	return s.repo.GetDeliveryByIDAndCustomer(ctx, deliveryID, customerID)
}

// CreateReorder duplicates a historical order as a new DRAFT, re-pricing
// product lines through the customer's pricing waterfall (M5) and optionally
// tagging the draft with a customer project inside one transaction.
func (s *Service) CreateReorder(ctx context.Context, customerID uuid.UUID, req ReorderRequest) (*ReorderResponse, error) {
	if req.ProjectID != nil {
		ok, err := s.repo.ProjectBelongsToCustomer(ctx, *req.ProjectID, customerID)
		if err != nil {
			return nil, fmt.Errorf("failed to verify project: %w", err)
		}
		if !ok {
			return nil, ErrProjectNotFound
		}
	}

	var newOrderID uuid.UUID
	err := s.repo.InTx(ctx, func(txCtx context.Context) error {
		var err error
		newOrderID, err = s.repo.CreateReorder(txCtx, customerID, req.OrderID)
		if err != nil {
			return fmt.Errorf("failed to create reorder: %w", err)
		}

		if err := s.repriceReorderedDraft(txCtx, customerID, newOrderID); err != nil {
			return fmt.Errorf("failed to reprice reordered draft: %w", err)
		}
		if req.ProjectID != nil {
			if err := s.repo.SetOrderProject(txCtx, newOrderID, customerID, req.ProjectID); err != nil {
				return fmt.Errorf("failed to attach project to reorder: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.logger.Info("Portal reorder created", "customer_id", customerID, "source_order", req.OrderID, "new_order", newOrderID)

	return &ReorderResponse{
		OrderID: newOrderID,
		Message: "Order draft created successfully",
	}, nil
}

// ParseToken parses and validates a portal JWT. Used by middleware.
func (s *Service) ParseToken(tokenStr string) (*PortalClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &PortalClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*PortalClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}
