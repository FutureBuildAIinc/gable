// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Handler serves the large cash routes: the counter's preview before Pay
// and the dealer's threshold settings.
type Handler struct {
	service *Service
	guard   BranchGuard // see WithBranchGuard
}

// BranchGuard holds a record's branch to the caller's wall (ADR 0007 section
// 2.3). *middleware.BranchGuard satisfies it.
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// WithBranchGuard holds the sale a preview names to the caller's wall. The
// preview reads a sale by id, so a handler wired with no guard refuses
// (fail closed) rather than answer for any branch's sale.
func (h *Handler) WithBranchGuard(g BranchGuard) *Handler {
	h.guard = g
	return h
}

// NewHandler builds the handler.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterRoutes mounts the routes. roleGuard wraps the settings routes and
// the Form 8300 export (admin or owner: the export carries the payer's
// taxpayer identification number, so it is the dealer's own and admin's
// only); managerGuard wraps the manager's worklist, the filing and the
// customer statement (admin, owner or finance); previewGuard wraps the
// preview with the counter's own roles and the branch middleware, the same
// composition the pos routes take.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard, managerGuard, previewGuard func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/pos/large-cash/preview", previewGuard(http.HandlerFunc(h.Preview)))
	if roleGuard != nil {
		mux.Handle("GET /api/v1/admin/large-cash/settings", roleGuard(http.HandlerFunc(h.GetSettings)))
		mux.Handle("PUT /api/v1/admin/large-cash/settings", roleGuard(http.HandlerFunc(h.PutSettings)))
		mux.Handle("GET /api/v1/large-cash/events/{id}/form-8300", roleGuard(http.HandlerFunc(h.Form8300)))
	} else {
		mux.HandleFunc("GET /api/v1/admin/large-cash/settings", h.GetSettings)
		mux.HandleFunc("PUT /api/v1/admin/large-cash/settings", h.PutSettings)
		mux.HandleFunc("GET /api/v1/large-cash/events/{id}/form-8300", h.Form8300)
	}
	if managerGuard != nil {
		mux.Handle("GET /api/v1/large-cash/worklist", managerGuard(http.HandlerFunc(h.Worklist)))
		mux.Handle("POST /api/v1/large-cash/events/{id}/filing", managerGuard(http.HandlerFunc(h.RecordFiling)))
		mux.Handle("GET /api/v1/large-cash/statements", managerGuard(http.HandlerFunc(h.Statements)))
		mux.Handle("POST /api/v1/large-cash/events/{id}/statement", managerGuard(http.HandlerFunc(h.IssueStatement)))
	} else {
		mux.HandleFunc("GET /api/v1/large-cash/worklist", h.Worklist)
		mux.HandleFunc("POST /api/v1/large-cash/events/{id}/filing", h.RecordFiling)
		mux.HandleFunc("GET /api/v1/large-cash/statements", h.Statements)
		mux.HandleFunc("POST /api/v1/large-cash/events/{id}/statement", h.IssueStatement)
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
	// Payer is the identity the counter already holds for this payer, the
	// walk-in it identified earlier in this till session. The preview keys
	// the same person the completion will key, so the two agree on whose
	// cash this is and what it comes to.
	Payer *previewPayerRequest `json:"payer"`
}

// previewPayerRequest is the captured payer identity on the wire, the same
// shape the completion carries. Only the values that key the payer are read
// here; the preview writes nothing.
type previewPayerRequest struct {
	Name            string `json:"name"`
	Address         string `json:"address"`
	Occupation      string `json:"occupation"`
	DateOfBirth     string `json:"date_of_birth"`
	IDType          string `json:"id_type"`
	IDNumber        string `json:"id_number"`
	TaxpayerID      string `json:"taxpayer_id"`
	ThirdPartyPayer bool   `json:"third_party_payer"`
}

// payer answers the captured identity, nil when the request carries none.
func (p *previewPayerRequest) payer() *PayerIn {
	if p == nil {
		return nil
	}
	return &PayerIn{
		Name:            p.Name,
		Address:         p.Address,
		Occupation:      p.Occupation,
		DateOfBirth:     p.DateOfBirth,
		IDType:          p.IDType,
		IDNumber:        p.IDNumber,
		TaxpayerID:      p.TaxpayerID,
		ThirdPartyPayer: p.ThirdPartyPayer,
	}
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
	if h.guard == nil {
		httpx.WriteError(w, r, httpx.Unavailable(
			"the branch wall is not wired on this handler: a sale is previewed only inside the caller's branch"))
		return
	}
	facts, err := h.service.FactsOfSale(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// A sale outside the caller's wall answers what a missing one does.
	if err := h.guard.CheckPayloadBranch(r.Context(), facts.BranchID); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httpx.WriteError(w, r, httpx.NotFound("sale not found"))
			return
		}
		httpx.WriteError(w, r, fmt.Errorf("branch grant lookup: %w", err))
		return
	}
	if req.SuspectedAvoidance != nil {
		facts.SuspectedAvoidance = *req.SuspectedAvoidance
	}
	facts.Payer = req.Payer.payer()
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
	if err := h.service.SaveSettings(r.Context(), in, actorOf(r)); err != nil {
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

// actorOf names who made the request, for the audit row.
func actorOf(r *http.Request) string {
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		return claims.Subject
	}
	return ""
}
