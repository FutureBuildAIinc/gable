// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"encoding/json"
	"net/http"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/pagination"
	"github.com/google/uuid"
)

// Handler manages HTTP requests for products
type Handler struct {
	service *Service
}

// NewHandler creates a new Product Handler
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes adds handlers to the mux
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/products", guard(h.HandleListProducts))
	mux.HandleFunc("POST /api/v1/products", guard(h.HandleCreateProduct))
	mux.HandleFunc("GET /api/v1/products/reorder-alerts", guard(h.HandleReorderAlerts))
	mux.HandleFunc("GET /api/v1/products/{id}", guard(h.HandleGetProduct))
	mux.HandleFunc("PATCH /api/v1/products/{id}/margins", guard(h.HandleUpdateMarginRules))
	mux.HandleFunc("PATCH /api/v1/products/{id}/dimensions", guard(h.HandleUpdateDimensions))
	mux.HandleFunc("PATCH /api/v1/products/{id}/lead-time", guard(h.HandleUpdateLeadTime))
}

// LeadTimeRequest is the body of PATCH /products/{id}/lead-time.
//
// LeadTimeDays is a pointer so `{"lead_time_days": null}` clears the value
// back to "unpublished" and is distinguishable from `{"lead_time_days": 0}`,
// which is a dealer asserting same-day availability.
type LeadTimeRequest struct {
	LeadTimeDays *int `json:"lead_time_days"`
}

// HandleUpdateLeadTime handles PATCH /products/{id}/lead-time — the dealer-side
// write for the lead time the portal catalog publishes (migration 084).
func (h *Handler) HandleUpdateLeadTime(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid id format", http.StatusBadRequest, err)
		return
	}

	var req LeadTimeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "invalid request body", http.StatusBadRequest, err)
		return
	}

	if err := h.service.UpdateLeadTime(r.Context(), id, req.LeadTimeDays); err != nil {
		httputil.RespondError(w, r, "failed to update lead time", http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(req)
}

// HandleGetProduct handles GET /products/{id}
func (h *Handler) HandleGetProduct(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid id format", http.StatusBadRequest, err)
		return
	}

	p, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "product not found", http.StatusNotFound, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

// HandleCreateProduct handles POST /products
func (h *Handler) HandleCreateProduct(w http.ResponseWriter, r *http.Request) {
	var p Product
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}

	if err := h.service.CreateProduct(r.Context(), &p); err != nil {
		httputil.RespondError(w, r, "failed to create product", http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(p)
}

// HandleReorderAlerts handles GET /products/reorder-alerts
func (h *Handler) HandleReorderAlerts(w http.ResponseWriter, r *http.Request) {
	alerts, err := h.service.ListBelowReorder(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "Failed to fetch reorder alerts", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(alerts)
}

// HandleListProducts handles GET /products
func (h *Handler) HandleListProducts(w http.ResponseWriter, r *http.Request) {
	page := pagination.FromRequest(r)
	products, total, err := h.service.ListProductsPaginated(r.Context(), page.Limit, page.Offset)
	if err != nil {
		httputil.RespondError(w, r, "Failed to fetch products", http.StatusInternalServerError, err)
		return
	}

	resp := pagination.PagedResponse[Product]{
		Data:   products,
		Total:  total,
		Limit:  page.Limit,
		Offset: page.Offset,
	}
	if resp.Data == nil {
		resp.Data = []Product{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleUpdateMarginRules handles PATCH /products/{id}/margins
func (h *Handler) HandleUpdateMarginRules(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if idStr == "" {
		httputil.RespondError(w, r, "id is required", http.StatusBadRequest, nil)
		return
	}

	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "invalid id format", http.StatusBadRequest, err)
		return
	}

	var req struct {
		TargetMargin   float64 `json:"target_margin"`
		CommissionRate float64 `json:"commission_rate"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "invalid request body", http.StatusBadRequest, err)
		return
	}

	if err := h.service.UpdateMarginRules(r.Context(), id, req.TargetMargin, req.CommissionRate); err != nil {
		httputil.RespondError(w, r, "Failed to update margin rules", http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// HandleUpdateDimensions handles PATCH /products/{id}/dimensions — the write
// side of the PIM's canonical parametric 3D geometry, which AI_LM's Load
// Builder reads back over GET /api/integration/products.
//
// The request body is decoded straight into a Geometry, whose fields are all
// pointers. That is what makes an omitted or explicitly-null field clear the
// column to SQL NULL instead of writing a zero:
//
//	{"length_in": null}  -> length_in IS NULL   ("no geometry recorded")
//	{"length_in": 0}     -> length_in = 0       (a real, if odd, measurement)
//	{}                   -> every column NULL   (the editor's "clear" path)
//
// A 200 with the persisted Geometry is returned so a client can see exactly
// which fields ended up null without a follow-up GET.
func (h *Handler) HandleUpdateDimensions(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if idStr == "" {
		httputil.RespondError(w, r, "id is required", http.StatusBadRequest, nil)
		return
	}

	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "invalid id format", http.StatusBadRequest, err)
		return
	}

	var g Geometry
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		httputil.RespondError(w, r, "invalid request body", http.StatusBadRequest, err)
		return
	}

	if err := h.service.UpdateDimensions(r.Context(), id, g); err != nil {
		httputil.RespondError(w, r, "Failed to update dimensions", http.StatusInternalServerError, err)
		return
	}

	updated, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		// The write succeeded; only the read-back failed. Reporting 500 here
		// would tell the operator their edit was lost when it was not.
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Geometry{
		LengthIn:       updated.LengthIn,
		WidthIn:        updated.WidthIn,
		HeightIn:       updated.HeightIn,
		Stackable:      updated.Stackable,
		GeometrySource: updated.GeometrySource,
	})
}
