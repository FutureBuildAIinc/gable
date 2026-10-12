// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package vision

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/google/uuid"
)

type Handler struct {
	service  *Service
	auditLog *audit.Logger
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// WithAuditLog sets the audit logger every scan records its row through
// (ADR 0007 section 5.5, the PD-6 ruling): the scan spends the dealer's AI
// credit, so each call is audited with whoever spent it, a user or the
// machine key.
func (h *Handler) WithAuditLog(l *audit.Logger) *Handler {
	h.auditLog = l
	return h
}

// recordScan audits one scan; a missing logger or a failed row never blocks
// the answer (the spend already happened when the service answered).
func (h *Handler) recordScan(ctx context.Context) {
	if h.auditLog == nil {
		return
	}
	if err := h.auditLog.Log(ctx, audit.Entry{
		Action:     "ai.vision.scan",
		EntityType: "ai_spend",
		EntityID:   uuid.Nil,
	}); err != nil {
		slog.Error("failed to audit the vision scan", "error", err)
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("POST /api/v1/vision/scan", guard(h.handleScan))
}

func (h *Handler) handleScan(w http.ResponseWriter, r *http.Request) {
	var req BlueprintScanRequest
	// Limit body to 1MB to prevent DoS from large blueprint payloads
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		slog.Warn("Vision scan: invalid request body", "error", err)
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}

	if req.BlueprintText == "" {
		httputil.RespondError(w, r, "blueprint_text is required", http.StatusBadRequest, nil)
		return
	}

	resp := h.service.ScanBlueprint(req)
	h.recordScan(r.Context())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
