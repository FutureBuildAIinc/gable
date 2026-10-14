// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package parsing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gablelbm/gable/internal/ai"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
)

// Handler manages HTTP requests for material list parsing.
type Handler struct {
	service  *Service
	auditLog *audit.Logger
}

// NewHandler creates a new parsing Handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// WithAuditLog sets the audit logger every upload records its row through
// (ADR 0007 section 5.5, the PD-6 ruling): the parse spends the dealer's AI
// credit, so each call is audited with whoever spent it, a user or the
// machine key.
func (h *Handler) WithAuditLog(l *audit.Logger) *Handler {
	h.auditLog = l
	return h
}

// recordUpload audits one upload; a missing logger or a failed row never
// blocks the answer.
func (h *Handler) recordUpload(ctx context.Context, filename string) {
	if h.auditLog == nil {
		return
	}
	if err := h.auditLog.Log(ctx, audit.Entry{
		Action:     "ai.parsing.upload",
		EntityType: "ai_spend",
		EntityID:   uuid.Nil,
		Changes:    map[string]interface{}{"filename": filename},
	}); err != nil {
		slog.Error("failed to audit the parsing upload", "error", err)
	}
}

// RegisterRoutes adds parsing handlers to the mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("POST /api/v1/parsing/upload", guard(h.HandleUpload))
}

// HandleUpload processes a material list image upload and returns parsed items.
// Accepts multipart/form-data with a "file" field.
func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Limit upload to 10MB
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		httputil.RespondError(w, r, "File too large or invalid form data", http.StatusBadRequest, err)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.RespondError(w, r, "Missing 'file' field in form data", http.StatusBadRequest, err)
		return
	}
	defer file.Close()

	slog.Info("Parsing material list upload",
		"filename", header.Filename,
		"size_bytes", header.Size,
	)

	// Read uploaded file bytes for the base64 preview
	imageBytes, err := io.ReadAll(io.LimitReader(file, 10<<20))
	if err != nil {
		httputil.RespondError(w, r, "Failed to read uploaded file", http.StatusInternalServerError, err)
		return
	}

	// Detect content type for data URI
	contentType := http.DetectContentType(imageBytes)

	// Normalize content type for spreadsheets (DetectContentType may not identify xlsx properly)
	filename := header.Filename
	if contentType == "application/octet-stream" || contentType == "application/zip" {
		switch {
		case strings.HasSuffix(strings.ToLower(filename), ".xlsx") || strings.HasSuffix(strings.ToLower(filename), ".xls"):
			contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		case strings.HasSuffix(strings.ToLower(filename), ".csv"):
			contentType = "text/csv"
		}
	}

	// For spreadsheets, convert to text before sending to AI
	aiContentType := contentType
	aiBytes := imageBytes
	if strings.Contains(contentType, "spreadsheet") || strings.HasSuffix(strings.ToLower(filename), ".xlsx") {
		textContent, convErr := convertSpreadsheetToText(imageBytes)
		if convErr != nil {
			slog.Error("Failed to convert spreadsheet", "error", convErr)
			httputil.RespondError(w, r, "Failed to process spreadsheet", http.StatusBadRequest, convErr)
			return
		}
		aiBytes = []byte(textContent)
		aiContentType = "text/plain"
	}

	// The spend is audited before the extractor runs, so a failure after the
	// AI call (the product match, the response) still leaves the row that
	// names who spent the credit.
	h.recordUpload(r.Context(), header.Filename)

	// Extract items. synthetic==true means the AI extractor was unavailable
	// and `extracted` is the canned demo list rather than anything read out
	// of the upload — it must reach the client labelled as such.
	extracted, synthetic, extractErr := h.service.ExtractItemsWithAI(r.Context(), aiBytes, aiContentType)
	if extractErr != nil {
		if msg, ok := ai.UnsupportedTypeMessage(extractErr); ok {
			httpx.WriteError(w, r, &httpx.Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_type", Message: msg})
			return
		}
		if msg, ok := ai.RefusedMessage(extractErr); ok {
			httpx.WriteError(w, r, &httpx.Error{Status: http.StatusBadGateway, Code: ai.CodeAIRefused, Message: msg, Operator: true})
			return
		}
		slog.Error("Failed to extract items", "error", extractErr)
		httputil.RespondError(w, r, "Failed to extract items from file", http.StatusInternalServerError, extractErr)
		return
	}

	// Match against product catalog
	items, err := h.service.MatchProducts(r.Context(), extracted)
	if err != nil {
		slog.Error("Failed to match products", "error", err)
		httputil.RespondError(w, r, "Failed to process material list", http.StatusInternalServerError, err)
		return
	}

	// Build base64 data URI for the uploaded image
	sourceImage := fmt.Sprintf("data:%s;base64,%s",
		contentType,
		base64.StdEncoding.EncodeToString(imageBytes),
	)

	resp := ParseResponse{
		Items:       items,
		SourceImage: sourceImage,
		ParseTimeMs: time.Since(start).Milliseconds(),
		ItemCount:   len(items),
		Synthetic:   synthetic,
	}
	if synthetic {
		reason := "AI extraction failed. "
		if h.service.AIOff(r.Context()) {
			reason = ai.AIOffMessage + " "
		}
		resp.SyntheticReason = reason +
			"These line items are a fixed demo list, not the contents of the uploaded file. Do not quote them as-is."
		slog.Warn("Returning synthetic material list for upload",
			"filename", header.Filename, "item_count", len(items))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// convertSpreadsheetToText reads an xlsx file and converts it to plain text
// suitable for AI extraction.
func convertSpreadsheetToText(data []byte) (string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to open spreadsheet: %w", err)
	}
	defer f.Close()

	var sb strings.Builder
	sheets := f.GetSheetList()
	for _, sheet := range sheets {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		for _, row := range rows {
			line := strings.Join(row, "\t")
			if strings.TrimSpace(line) != "" {
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String(), nil
}
