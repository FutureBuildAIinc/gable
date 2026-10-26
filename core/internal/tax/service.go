// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tax

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// TaxCalculator is the interface for tax calculation services.
type TaxCalculator interface {
	// PreviewTax calculates tax without committing (for cart/invoice previews).
	PreviewTax(ctx context.Context, req *TaxPreviewRequest) (*TaxResult, error)
	// CommitTax calculates and commits tax for a finalized transaction.
	CommitTax(ctx context.Context, req *TaxPreviewRequest) (*TaxResult, error)
	// VoidTax voids a previously committed tax document (for returns/cancellations).
	VoidTax(ctx context.Context, documentCode string) error
	// GetExemptions returns all exemptions for a customer.
	GetExemptions(ctx context.Context, customerID uuid.UUID) ([]TaxExemption, error)
	// SaveExemption creates a new tax exemption certificate.
	SaveExemption(ctx context.Context, req *CreateExemptionRequest) (*TaxExemption, error)
	// DeleteExemption removes an exemption by ID.
	DeleteExemption(ctx context.Context, id uuid.UUID) error
	// EndExemption marks an exemption inactive: it stays on file.
	EndExemption(ctx context.Context, id uuid.UUID) (bool, error)
}

// Service implements TaxCalculator with Avalara or flat-rate fallback.
type Service struct {
	exemptionRepo ExemptionRepo
	avalara       *AvalaraClient // nil if Avalara is not configured
	companyCode   string
	flatRate      float64 // Fallback flat tax rate (0.0 = no tax)
	logger        *slog.Logger
}

// NewService creates a new tax service.
// If avalaraClient is nil, falls back to flat-rate calculation.
func NewService(exemptionRepo ExemptionRepo, avalaraClient *AvalaraClient, companyCode string, flatRate float64, logger *slog.Logger) *Service {
	return &Service{
		exemptionRepo: exemptionRepo,
		avalara:       avalaraClient,
		companyCode:   companyCode,
		flatRate:      flatRate,
		logger:        logger,
	}
}

// ProviderConfigured reports whether a tax provider answers (the Avalara
// path). The ADR 0005 rate resolver asks before it calls: an unconfigured
// provider must never fall back to a silent rate.
func (s *Service) ProviderConfigured() bool { return s.avalara != nil }

func (s *Service) PreviewTax(ctx context.Context, req *TaxPreviewRequest) (*TaxResult, error) {
	// Check for tax exemption
	if req.CustomerID != nil {
		exempt, err := s.isExempt(ctx, *req.CustomerID)
		if err != nil {
			s.logger.Warn("Failed to check exemption, proceeding with tax calculation", "error", err)
		} else if exempt {
			return s.zeroTaxResult(req), nil
		}
	}

	// Use Avalara if configured
	if s.avalara != nil {
		customerCode := "WALKUP"
		if req.CustomerID != nil {
			customerCode = req.CustomerID.String()
		}
		return s.avalara.CalculateTax(ctx, req, customerCode, false)
	}

	// Flat-rate fallback
	return s.flatRateCalc(req), nil
}

func (s *Service) CommitTax(ctx context.Context, req *TaxPreviewRequest) (*TaxResult, error) {
	// Check for tax exemption
	if req.CustomerID != nil {
		exempt, err := s.isExempt(ctx, *req.CustomerID)
		if err != nil {
			s.logger.Warn("Failed to check exemption, proceeding with tax calculation", "error", err)
		} else if exempt {
			return s.zeroTaxResult(req), nil
		}
	}

	// Use Avalara if configured
	if s.avalara != nil {
		customerCode := "WALKUP"
		if req.CustomerID != nil {
			customerCode = req.CustomerID.String()
		}
		return s.avalara.CalculateTax(ctx, req, customerCode, true)
	}

	// Flat-rate fallback — no commit necessary
	return s.flatRateCalc(req), nil
}

func (s *Service) VoidTax(ctx context.Context, documentCode string) error {
	if s.avalara == nil {
		s.logger.Info("VoidTax: no Avalara configured, nothing to void", "document_code", documentCode)
		return nil
	}
	return s.avalara.VoidTransaction(ctx, s.companyCode, documentCode)
}

func (s *Service) GetExemptions(ctx context.Context, customerID uuid.UUID) ([]TaxExemption, error) {
	exemptions, err := s.exemptionRepo.GetByCustomer(ctx, customerID)
	if err != nil {
		return nil, err
	}
	// each row carries its standing on the customer's branch's business day,
	// the day the window reads, so the screen never states a day of its own
	day, err := s.exemptionRepo.CustomerBranchDay(ctx, customerID)
	if err != nil {
		return nil, err
	}
	for i := range exemptions {
		exemptions[i].Status = StatusOf(&exemptions[i], day)
	}
	return exemptions, nil
}

// invalidField is the create's own field refusal: a 400 naming the field,
// the shape of ADR 0001, so the dialog's rules and the server's are one.
func invalidField(field, message string) error {
	return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
		Message: "one or more fields failed validation", Details: []httpx.FieldError{{Field: field, Message: message}}}
}

// validateExemption holds the create's rules on the server, not only in the
// dialog: the certificate's number, its dates' shape and order, the issuing
// state's two letters, and that the customer exists. Nothing is written
// until every rule passes.
func validateExemption(req *CreateExemptionRequest, effective, expiry *time.Time) error {
	if strings.TrimSpace(req.CertificateNumber) == "" {
		return invalidField("certificate_number", "the certificate number is required")
	}
	if state := strings.TrimSpace(req.IssuingState); state != "" && !stateCode.MatchString(state) {
		return invalidField("issuing_state", "the issuing state or province is two letters")
	}
	if effective != nil && expiry != nil && sameDay(*expiry, *effective) == before {
		return invalidField("expiry_date", "the expiry day is on or after the effective day")
	}
	return nil
}

func (s *Service) SaveExemption(ctx context.Context, req *CreateExemptionRequest) (*TaxExemption, error) {
	var effective, expiry *time.Time
	if req.EffectiveDate != nil {
		t, err := time.Parse("2006-01-02", *req.EffectiveDate)
		if err != nil {
			return nil, invalidField("effective_date", "the effective day is a date, YYYY-MM-DD")
		}
		effective = &t
	}
	if req.ExpiryDate != nil {
		t, err := time.Parse("2006-01-02", *req.ExpiryDate)
		if err != nil {
			return nil, invalidField("expiry_date", "the expiry day is a date, YYYY-MM-DD")
		}
		expiry = &t
	}
	if err := validateExemption(req, effective, expiry); err != nil {
		return nil, err
	}
	exists, err := s.exemptionRepo.CustomerExists(ctx, req.CustomerID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, invalidField("customer_id", "no such customer")
	}
	if effective == nil {
		day, err := s.exemptionRepo.CustomerBranchDay(ctx, req.CustomerID)
		if err != nil {
			return nil, err
		}
		effective = &day
		if expiry != nil && sameDay(*expiry, day) == before {
			return nil, invalidField("expiry_date", "the expiry day is on or after the effective day")
		}
	}
	ex := &TaxExemption{
		ID:                uuid.New(),
		CustomerID:        req.CustomerID,
		ExemptReason:      req.ExemptReason,
		CertificateNumber: req.CertificateNumber,
		IssuingState:      req.IssuingState,
		EffectiveDate:     *effective,
		ExpiryDate:        expiry,
		IsActive:          true,
	}

	if err := s.exemptionRepo.Create(ctx, ex); err != nil {
		return nil, err
	}

	s.logger.Info("Tax exemption created",
		"id", ex.ID,
		"customer_id", ex.CustomerID,
		"reason", ex.ExemptReason,
	)

	return ex, nil
}

func (s *Service) DeleteExemption(ctx context.Context, id uuid.UUID) error {
	return s.exemptionRepo.Delete(ctx, id)
}

// EndExemption ends a certificate: it stays on file, inactive, no longer
// exempting a sale (CP-10). False when no such id exists; a certificate
// that was already ended answers its own refusal.
func (s *Service) EndExemption(ctx context.Context, id uuid.UUID) (bool, error) {
	ended, err := s.exemptionRepo.MarkInactive(ctx, id)
	if errors.Is(err, errAlreadyEnded) {
		return false, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
			Message: "the certificate was already ended",
			Details: []httpx.FieldError{httpx.Blocker("already_ended", "the certificate was already ended: it stays on file, inactive")}}
	}
	return ended, err
}

// --- Internal helpers ---

func (s *Service) isExempt(ctx context.Context, customerID uuid.UUID) (bool, error) {
	standing, err := s.exemptionRepo.CoveringNow(ctx, customerID)
	if err != nil {
		return false, err
	}
	return standing != nil, nil
}

func (s *Service) zeroTaxResult(req *TaxPreviewRequest) *TaxResult {
	var totalAmount int64
	var lines []TaxLine

	for _, line := range req.Lines {
		totalAmount += line.Amount
		lines = append(lines, TaxLine{
			LineNumber:  line.LineNumber,
			ItemCode:    line.ItemCode,
			Description: line.Description,
			Quantity:    line.Quantity,
			Amount:      line.Amount,
			TaxAmount:   0,
			TaxRate:     0,
			Exempt:      true,
		})
	}

	return &TaxResult{
		TotalAmount: totalAmount,
		TotalTax:    0,
		GrandTotal:  totalAmount,
		Lines:       lines,
		IsEstimate:  true,
	}
}

func (s *Service) flatRateCalc(req *TaxPreviewRequest) *TaxResult {
	var totalAmount, totalTax int64
	var lines []TaxLine

	// The caller's jurisdiction-resolved rate (e.g. branch default_tax_rate)
	// beats the service-wide flat rate; both are estimates vs. Avalara.
	rate := s.flatRate
	if req.RateHint > 0 {
		rate = req.RateHint
	}

	for _, line := range req.Lines {
		lineTax := int64(math.Round(float64(line.Amount) * rate))
		totalAmount += line.Amount
		totalTax += lineTax

		lines = append(lines, TaxLine{
			LineNumber:  line.LineNumber,
			ItemCode:    line.ItemCode,
			Description: line.Description,
			Quantity:    line.Quantity,
			Amount:      line.Amount,
			TaxAmount:   lineTax,
			TaxRate:     rate,
			Exempt:      false,
		})
	}

	return &TaxResult{
		TotalAmount: totalAmount,
		TotalTax:    totalTax,
		GrandTotal:  totalAmount + totalTax,
		Lines:       lines,
		IsEstimate:  true, // Flat rate is always an estimate
	}
}
