// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
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

// productsOrdering is the list's ordering scope: created_at DESC, id DESC,
// the ordering migration 093 indexed.
const productsOrdering = "products.created_at_id_desc"

func writeProductError(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteError(w, r, err)
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request) (uuid.UUID, *httpx.Error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"})
	}
	return id, nil
}

// HandleGetProduct handles GET /products/{id}
func (h *Handler) HandleGetProduct(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(r)
	if bad != nil {
		writeProductError(w, r, bad)
		return
	}
	p, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeProductError(w, r, httpx.NotFound("no such product"))
			return
		}
		writeProductError(w, r, err)
		return
	}
	view := ViewOf(p)
	httpx.WriteRevisionETag(w, view.Revision)
	writeJSON(w, http.StatusOK, view)
}

// HandleCreateProduct handles POST /products
func (h *Handler) HandleCreateProduct(w http.ResponseWriter, r *http.Request) {
	p, err := ParseCreate(r)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	if err := h.service.CreateProduct(r.Context(), p); err != nil {
		writeProductError(w, r, err)
		return
	}
	view := ViewOf(p)
	w.Header().Set("Location", "/api/v1/products/"+view.ID.String())
	httpx.WriteRevisionETag(w, view.Revision)
	writeJSON(w, http.StatusCreated, view)
}

// HandleReorderAlerts handles GET /products/reorder-alerts
func (h *Handler) HandleReorderAlerts(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		writeProductError(w, r, err)
		return
	}
	alerts, err := h.service.ListBelowReorder(r.Context())
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	httpx.WriteList(w, alerts, "", 0)
}

// HandleListProducts handles GET /products
func (h *Handler) HandleListProducts(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, productsOrdering)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	wantTotal := false
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			writeProductError(w, r, httpx.BadRequest("include parameter is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"}))
			return
		}
		set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal)
		if ierr != nil {
			writeProductError(w, r, ierr)
			return
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}

	var after *time.Time
	var afterID *uuid.UUID
	if page.Key != nil {
		if len(page.Key) != 2 {
			writeProductError(w, r, cursorError())
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			writeProductError(w, r, cursorError())
			return
		}
		after, afterID = &at, &id
	}

	rows, more, err := h.service.ListProductsPage(r.Context(), after, afterID, page.Limit)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	views := make([]View, len(rows))
	for i := range rows {
		views[i] = ViewOf(&rows[i])
	}
	next := ""
	if more && len(views) > 0 {
		last := views[len(views)-1]
		next, err = httpx.MintCursor(productsOrdering,
			httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			writeProductError(w, r, err)
			return
		}
	}
	opts := []httpx.ListOption{}
	if wantTotal {
		total, err := h.service.CountProducts(r.Context())
		if err != nil {
			writeProductError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(total))
	}
	httpx.WriteList(w, views, next, page.Limit, opts...)
}

// HandleUpdateMarginRules handles PATCH /products/{id}/margins
func (h *Handler) HandleUpdateMarginRules(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(r)
	if bad != nil {
		writeProductError(w, r, bad)
		return
	}
	targetMargin, commissionRate, revision, err := parseMargins(r)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	p, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		writeProductError(w, r, mapRead(err))
		return
	}
	if err := httpx.CheckRevision(p.Revision, r.Header.Get("If-Match"), revision); err != nil {
		writeProductError(w, r, err)
		return
	}
	newRevision, err := h.service.UpdateMarginRules(r.Context(), id, targetMargin, commissionRate, p.Revision)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	p.TargetMargin, p.CommissionRate = targetMargin, commissionRate
	p.Revision = newRevision
	view := ViewOf(p)
	httpx.WriteRevisionETag(w, view.Revision)
	writeJSON(w, http.StatusOK, view)
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
func (h *Handler) HandleUpdateDimensions(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(r)
	if bad != nil {
		writeProductError(w, r, bad)
		return
	}
	g, revision, err := parseDimensions(r)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	p, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		writeProductError(w, r, mapRead(err))
		return
	}
	if err := httpx.CheckRevision(p.Revision, r.Header.Get("If-Match"), revision); err != nil {
		writeProductError(w, r, err)
		return
	}
	newRevision, err := h.service.UpdateDimensions(r.Context(), id, g, p.Revision)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	// The service settles the geometry's provenance on its own copy; the
	// answer is the row as stored, so read it back. A read-back failure does
	// not fail the write: the geometry is persisted, and the operator is
	// handed the revision the write answered.
	if updated, rerr := h.service.GetProduct(r.Context(), id); rerr == nil {
		p = updated
	} else {
		p.LengthIn, p.WidthIn, p.HeightIn = g.LengthIn, g.WidthIn, g.HeightIn
		p.Stackable, p.GeometrySource = g.Stackable, g.GeometrySource
		p.Revision = newRevision
	}
	view := ViewOf(p)
	httpx.WriteRevisionETag(w, view.Revision)
	writeJSON(w, http.StatusOK, view)
}

// HandleUpdateLeadTime handles PATCH /products/{id}/lead-time, the dealer-side
// write for the lead time the portal catalog publishes (migration 084).
func (h *Handler) HandleUpdateLeadTime(w http.ResponseWriter, r *http.Request) {
	id, bad := pathID(r)
	if bad != nil {
		writeProductError(w, r, bad)
		return
	}
	days, revision, err := parseLeadTime(r)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	p, err := h.service.GetProduct(r.Context(), id)
	if err != nil {
		writeProductError(w, r, mapRead(err))
		return
	}
	if err := httpx.CheckRevision(p.Revision, r.Header.Get("If-Match"), revision); err != nil {
		writeProductError(w, r, err)
		return
	}
	newRevision, err := h.service.UpdateLeadTime(r.Context(), id, days, p.Revision)
	if err != nil {
		writeProductError(w, r, err)
		return
	}
	p.LeadTimeDays = days
	p.Revision = newRevision
	view := ViewOf(p)
	httpx.WriteRevisionETag(w, view.Revision)
	writeJSON(w, http.StatusOK, view)
}

func mapRead(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("no such product")
	}
	return err
}
