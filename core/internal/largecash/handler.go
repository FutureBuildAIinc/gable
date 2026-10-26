// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Handler serves the large cash routes: the counter's preview before Pay
// and the dealer's threshold settings.
type Handler struct {
	service *Service
}

// NewHandler builds the handler.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterRoutes mounts the routes. roleGuard wraps the settings routes
// (admin or owner); the preview rides the counter's own guard, the same
// middleware the pos routes take.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard func(http.Handler) http.Handler) {
	mux.HandleFunc("POST /api/v1/pos/large-cash/preview", h.Preview)
	if roleGuard != nil {
		mux.Handle("GET /api/v1/admin/large-cash/settings", roleGuard(http.HandlerFunc(h.GetSettings)))
		mux.Handle("PUT /api/v1/admin/large-cash/settings", roleGuard(http.HandlerFunc(h.PutSettings)))
	} else {
		mux.HandleFunc("GET /api/v1/admin/large-cash/settings", h.GetSettings)
		mux.HandleFunc("PUT /api/v1/admin/large-cash/settings", h.PutSettings)
	}
}

type previewTenderRequest struct {
	Method      string          `json:"method"`
	Instrument  *string         `json:"instrument"`
	AmountCents json.RawMessage `json:"amount_cents"`
}

type previewRequest struct {
	TransactionID      string                 `json:"transaction_id"`
	SuspectedAvoidance *bool                  `json:"suspected_avoidance"`
	Tenders            []previewTenderRequest `json:"tenders"`
}

// Preview is POST /pos/large-cash/preview: what the counter asks before
// Pay. The server classifies the named tenders against the sale it holds,
// so the clerk's screen never decides a rule on its own.
func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	id, ok := v.UUID("transaction_id", &req.TransactionID, false)
	if !ok {
		httpx.WriteError(w, r, v.Err())
		return
	}
	facts, err := h.service.FactsOfSale(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.SuspectedAvoidance != nil {
		facts.SuspectedAvoidance = *req.SuspectedAvoidance
	}
	facts.Payer = nil
	for i := range req.Tenders {
		t := &req.Tenders[i]
		in := TenderFacts{Method: strings.ToLower(t.Method)}
		if t.Instrument != nil {
			in.Instrument = strings.ToLower(*t.Instrument)
		}
		if n, ok := v.Int("tenders.amount_cents", t.AmountCents, true); ok {
			v.Check(n > 0, "tenders.amount_cents", "must be greater than zero")
			in.AmountCents = n
		}
		if in.Instrument != "" {
			v.Enum("tenders.instrument", in.Instrument, Instruments()...)
		}
		facts.Tenders = append(facts.Tenders, in)
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	preview, err := h.service.Preview(r.Context(), facts)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// GetSettings is GET /admin/large-cash/settings.
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := h.service.ReadSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type settingsRequest struct {
	CountryDefault   *string `json:"country_default"`
	CAEnabled        *bool   `json:"ca_enabled"`
	CAThresholdCents int64   `json:"ca_threshold_cents"`
}

// PutSettings is PUT /admin/large-cash/settings: the default country and
// the Canadian dealer threshold, on or off and its amount.
func (h *Handler) PutSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	current, err := h.service.ReadSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in := &Settings{CountryDefault: current.CountryDefault, CAEnabled: current.CAEnabled,
		CAThresholdCents: current.CAThresholdCents}
	if req.CountryDefault != nil {
		in.CountryDefault = strings.ToUpper(strings.TrimSpace(*req.CountryDefault))
	}
	if req.CAEnabled != nil {
		in.CAEnabled = *req.CAEnabled
	}
	if req.CAThresholdCents > 0 {
		in.CAThresholdCents = req.CAThresholdCents
	}
	if err := h.service.SaveSettings(r.Context(), in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	st, err := h.service.ReadSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// FactsOfSale loads a sale's large cash facts: its branch, its named
// customer, its currency and its billed lines' product and total. The
// completion passes the facts it already holds; this read is the preview's
// path.
func (s *Service) FactsOfSale(ctx context.Context, transactionID uuid.UUID) (*CompletionFacts, error) {
	return s.repo.SaleFacts(ctx, transactionID)
}
