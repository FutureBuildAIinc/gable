// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The invoice list's ordering: created_at then id, newest first (ADR 0001
// section 2).
const cursorScope = "ap.invoices.created_at_id_desc"

// Handler handles the AP HTTP endpoints: the vendor invoice routes on the
// wire contract (ADR 0001, ADR 0008 7.4), and the payment and aging routes in
// their today shape until their own conversion (ADR 0008 section 11).
type Handler struct {
	service *Service
}

// NewHandler creates a new AP handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes registers the AP routes. roleGuard protects all endpoints;
// pass the branch wall's scoped("admin", "owner", "finance") in production.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	// Vendor invoices
	mux.HandleFunc("POST /api/v1/ap/invoices", guard(h.HandleCreate))
	mux.HandleFunc("GET /api/v1/ap/invoices", guard(h.HandleList))
	mux.HandleFunc("GET /api/v1/ap/invoices/{id}", guard(h.HandleGet))
	mux.HandleFunc("POST /api/v1/ap/invoices/{id}/transitions", guard(h.HandleTransition))

	// AP Payments
	mux.HandleFunc("POST /api/v1/ap/payments", guard(h.HandlePayVendor))
	mux.HandleFunc("GET /api/v1/ap/payments", guard(h.HandleListPayments))

	// Aging Report
	mux.HandleFunc("GET /api/v1/ap/aging", guard(h.HandleGetAgingSummary))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid "+what+" id", httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

// caller reads who is acting from the auth chain: the audit subject.
func caller(r *http.Request) Caller {
	if claims, ok := r.Context().Value(middleware.UserContextKey).(*middleware.UserClaims); ok && claims != nil {
		return Caller{Actor: claims.Subject, Role: claims.Role}
	}
	return Caller{}
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func single(v *httpx.Validator, q map[string][]string, name string) (string, bool) {
	vals := q[name]
	if len(vals) == 0 {
		return "", false
	}
	if len(vals) > 1 {
		v.Check(false, name, "parameter is repeated")
		return "", false
	}
	return vals[0], true
}

func uuidParam(v *httpx.Validator, q map[string][]string, name string) *uuid.UUID {
	raw, ok := single(v, q, name)
	if !ok {
		return nil
	}
	if id, ok := v.UUID(name, &raw, true); ok {
		return &id
	}
	return nil
}

// parseListFilter reads the invoice list's filters beside the platform's
// cursor, limit and include: vendor_id, status and po_id. Unknown parameters
// and uppercase status values are refused.
func parseListFilter(r *http.Request) (f ListFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "vendor_id", "status", "po_id")
	if err != nil {
		return f, false, err
	}
	page, err := httpx.ParseListQuery(r, cursorScope)
	if err != nil {
		return f, false, err
	}
	f.Limit = page.Limit
	v := &httpx.Validator{}
	f.VendorID = uuidParam(v, q, "vendor_id")
	f.POID = uuidParam(v, q, "po_id")
	if raw, ok := single(v, q, "status"); ok {
		for _, name := range strings.Split(raw, ",") {
			st, ok := ParseStatus(name)
			if !ok {
				v.Check(false, "status", "must be a comma separated list of: pending, approved, partial, paid, voided")
				break
			}
			f.Statuses = append(f.Statuses, st)
		}
	}
	if raw, ok := single(v, q, "include"); ok {
		set, ierr := httpx.ParseInclude(raw)
		if ierr != nil {
			return f, false, ierr
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if err := v.Err(); err != nil {
		return f, false, err
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return f, false, cursorError()
		}
		t, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return f, false, cursorError()
		}
		f.AfterTime, f.AfterID = &t, id
	}
	return f, wantTotal, nil
}

// HandleList answers GET /api/v1/ap/invoices: the cursor envelope.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	f, wantTotal, err := parseListFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.List(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next, err = httpx.MintCursor(cursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if total != nil {
		opts = append(opts, httpx.WithTotal(*total))
	}
	httpx.WriteList(w, items, next, f.Limit, opts...)
}

// HandleGet answers GET /api/v1/ap/invoices/{id}.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "vendor invoice")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	inv, err := h.service.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, inv.Revision)
	writeJSON(w, http.StatusOK, inv)
}

// HandleCreate answers POST /api/v1/ap/invoices: the bill starts pending.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req CreateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	inv, err := h.service.Create(r.Context(), in, caller(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, inv.Revision)
	w.Header().Set("Location", "/api/v1/ap/invoices/"+inv.ID.String())
	writeJSON(w, http.StatusCreated, inv)
}

// HandleTransition answers POST /api/v1/ap/invoices/{id}/transitions: the
// approve and the void of ADR 0008 7.4, replacing the old /approve route.
func (h *Handler) HandleTransition(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "vendor invoice")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req TransitionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	inv, err := h.service.Transition(r.Context(), id, in, Precondition{IfMatch: r.Header.Get("If-Match"), Revision: in.Revision}, caller(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, inv.Revision)
	writeJSON(w, http.StatusOK, inv)
}

// HandlePayVendor answers POST /api/v1/ap/payments: the payment applies only
// to approved or partial bills (ADR 0008 7.4).
func (h *Handler) HandlePayVendor(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req PayRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pmt, err := h.service.PayVendor(r.Context(), in, caller(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pmt)
}

// HandleListPayments answers GET /api/v1/ap/payments.
func (h *Handler) HandleListPayments(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "vendor_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	var vendorID *uuid.UUID
	if raw, ok := single(v, q, "vendor_id"); ok {
		if id, ok := v.UUID("vendor_id", &raw, true); ok {
			vendorID = &id
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	payments, err := h.service.ListPayments(r.Context(), vendorID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if payments == nil {
		payments = []APPayment{}
	}
	writeJSON(w, http.StatusOK, payments)
}

// HandleGetAgingSummary answers GET /api/v1/ap/aging.
func (h *Handler) HandleGetAgingSummary(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	summary, err := h.service.GetAgingSummary(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if summary == nil {
		summary = []APAgingSummary{}
	}
	writeJSON(w, http.StatusOK, summary)
}
