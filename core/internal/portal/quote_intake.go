// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/parsing"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// QuoteIntakeRequest submits a pasted material list (text) or a photo/file
// for parsing and catalog matching, and optionally creates a portal quote
// request in the same step.
type QuoteIntakeRequest struct {
	Text           string     `json:"text,omitempty"`
	FileBase64     string     `json:"file_base64,omitempty"`
	FileBytes      []byte     `json:"-"`
	ContentType    string     `json:"content_type,omitempty"`
	ProjectID      *uuid.UUID `json:"project_id,omitempty"`
	Notes          string     `json:"notes,omitempty"`
	DeliveryType   string     `json:"delivery_type,omitempty"`
	CreateQuote    bool       `json:"create_quote,omitempty"`
	AllowSynthetic bool       `json:"allow_synthetic,omitempty"`
}

// QuoteIntakeItemDTO is one extracted and matched line from a material list or photo.
type QuoteIntakeItemDTO struct {
	RawText            string     `json:"raw_text"`
	ParsedQty          float64    `json:"parsed_qty"`
	ParsedUnit         string     `json:"parsed_unit"`
	ParsedDescription  string     `json:"parsed_description"`
	MatchedProductID   *uuid.UUID `json:"matched_product_id,omitempty"`
	MatchedProductName string     `json:"matched_product_name,omitempty"`
	MatchedSKU         string     `json:"matched_sku,omitempty"`
	Confidence         float64    `json:"confidence"`
}

// QuoteIntakeResponse is the result of parsing a material list or photo.
type QuoteIntakeResponse struct {
	Source    string               `json:"source"`
	Synthetic bool                 `json:"synthetic"`
	Items     []QuoteIntakeItemDTO `json:"items"`
	Quote     *PortalQuoteDTO      `json:"quote,omitempty"`
}

// ConvertPortalQuoteResponse is returned when a priced or accepted quote is
// converted into a live ERP order.
type ConvertPortalQuoteResponse struct {
	QuoteID     uuid.UUID `json:"quote_id"`
	OrderID     uuid.UUID `json:"order_id"`
	OrderNumber string    `json:"order_number"`
	Status      string    `json:"status"`
}

// IntakeQuote parses a contractor's pasted text list (without AI) or photo/file
// (with AI through Gable's parsing service), matches items against the dealer
// catalog, and optionally creates a portal quote request.
func (s *Service) IntakeQuote(ctx context.Context, customerID uuid.UUID, req QuoteIntakeRequest) (*QuoteIntakeResponse, error) {
	if s.parsingSvc == nil {
		return nil, fmt.Errorf("material list parsing service is not configured")
	}

	fileBytes := req.FileBytes
	if len(fileBytes) == 0 && strings.TrimSpace(req.FileBase64) != "" {
		raw := strings.TrimSpace(req.FileBase64)
		if idx := strings.Index(raw, ","); idx >= 0 && strings.HasPrefix(raw, "data:") {
			raw = raw[idx+1:]
		}
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid base64 file payload", ErrInvalidRequest)
		}
		fileBytes = decoded
	}

	var (
		matched   []parsing.ParsedItem
		synthetic bool
		source    string
		err       error
	)

	trimmedText := strings.TrimSpace(req.Text)
	switch {
	case len(fileBytes) > 0:
		source = "photo"
		ct := strings.TrimSpace(req.ContentType)
		if ct == "" {
			ct = "image/jpeg"
		}
		extracted, syn, extractErr := s.parsingSvc.ExtractItemsWithAI(ctx, fileBytes, ct)
		if extractErr != nil {
			return nil, fmt.Errorf("failed to extract items from upload: %w", extractErr)
		}
		synthetic = syn
		matched, err = s.parsingSvc.MatchProducts(ctx, extracted)
		if err != nil {
			return nil, fmt.Errorf("failed to match products: %w", err)
		}
	case trimmedText != "":
		source = "text"
		synthetic = false
		matched, err = s.parsingSvc.MatchProducts(ctx, s.parsingSvc.ExtractItems(trimmedText))
		if err != nil {
			return nil, fmt.Errorf("failed to match products: %w", err)
		}
	default:
		return nil, fmt.Errorf("%w: provide either text or a photo/file upload", ErrInvalidRequest)
	}

	items := make([]QuoteIntakeItemDTO, 0, len(matched))
	quoteLines := make([]QuoteRequestLine, 0, len(matched))
	for _, m := range matched {
		qty := m.Quantity
		if qty <= 0 {
			qty = 1
		}
		uom := strings.ToUpper(strings.TrimSpace(m.UOM))
		if !validUOMs[uom] {
			uom = "EA"
		}
		var (
			matchedID   *uuid.UUID
			matchedName string
			matchedSKU  string
		)
		if m.MatchedProduct != nil && m.MatchedProduct.ProductID != uuid.Nil {
			pid := m.MatchedProduct.ProductID
			matchedID = &pid
			matchedName = m.MatchedProduct.Description
			matchedSKU = m.MatchedProduct.SKU
		}
		desc := strings.TrimSpace(matchedName)
		if desc == "" {
			desc = strings.TrimSpace(m.RawText)
		}

		items = append(items, QuoteIntakeItemDTO{
			RawText:            m.RawText,
			ParsedQty:          qty,
			ParsedUnit:         uom,
			ParsedDescription:  desc,
			MatchedProductID:   matchedID,
			MatchedProductName: matchedName,
			MatchedSKU:         matchedSKU,
			Confidence:         m.Confidence,
		})
		quoteLines = append(quoteLines, QuoteRequestLine{
			ProductID:   matchedID,
			Description: desc,
			Quantity:    qty,
			UOM:         uom,
		})
	}

	resp := &QuoteIntakeResponse{
		Source:    source,
		Synthetic: synthetic,
		Items:     items,
	}

	if req.CreateQuote {
		if synthetic && !req.AllowSynthetic {
			return nil, ErrSyntheticIntakeNotConfirmed
		}
		if len(quoteLines) == 0 {
			return nil, fmt.Errorf("%w: no material lines could be extracted", ErrInvalidRequest)
		}
		created, err := s.CreateQuoteRequest(ctx, customerID, CreateQuoteRequest{
			ProjectID:    req.ProjectID,
			Notes:        req.Notes,
			DeliveryType: req.DeliveryType,
			Lines:        quoteLines,
		})
		if err != nil {
			return nil, err
		}
		resp.Quote = created
	}

	return resp, nil
}

// ConvertPortalQuote converts a priced (SENT) or ACCEPTED quote belonging to
// customerID into a live ERP order via quote.Service.ConvertInProcess inside a
// single transaction, preserving any portal project_id linked to the quote.
func (s *Service) ConvertPortalQuote(ctx context.Context, quoteID, customerID uuid.UUID) (*ConvertPortalQuoteResponse, error) {
	if s.quoteSvc == nil {
		return nil, fmt.Errorf("quote lifecycle service is not configured")
	}

	var resp *ConvertPortalQuoteResponse
	err := s.repo.InTx(ctx, func(txCtx context.Context) error {
		current, err := s.repo.GetPortalQuote(txCtx, quoteID, customerID)
		if err != nil {
			return err
		}

		if current.ExpiresAt != nil && current.ExpiresAt.Before(time.Now().UTC()) {
			return fmt.Errorf("%w: quote has expired", ErrQuoteNotConvertible)
		}

		erpState := quote.QuoteState(current.ERPState)
		if erpState != quote.QuoteStateSent && erpState != quote.QuoteStateAccepted {
			return fmt.Errorf("%w: quote state is %s", ErrQuoteNotConvertible, current.Status)
		}

		res, err := s.quoteSvc.ConvertInProcess(branchctx.WithSystem(txCtx), quoteID)
		if err != nil {
			var hErr *httpx.Error
			if errors.As(err, &hErr) && (hErr.Status == 409 || hErr.Status == 422) {
				return fmt.Errorf("%w: %s", ErrQuoteNotConvertible, hErr.Message)
			}
			return err
		}

		if current.ProjectID != nil {
			if err := s.repo.SetOrderProject(txCtx, res.ID, customerID, current.ProjectID); err != nil {
				return fmt.Errorf("failed to attach project to converted order: %w", err)
			}
		}

		resp = &ConvertPortalQuoteResponse{
			QuoteID:     quoteID,
			OrderID:     res.ID,
			OrderNumber: res.Number,
			Status:      string(res.Status),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.logger.Info("Portal quote converted to order",
		"customer_id", customerID, "quote_id", quoteID, "order_id", resp.OrderID, "order_number", resp.OrderNumber)

	return resp, nil
}

