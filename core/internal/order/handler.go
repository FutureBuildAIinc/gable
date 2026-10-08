// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// cursorScope names the list's ordering: created_at then id, newest first
// (ADR 0001 section 2).
const cursorScope = "orders.created_at_id_desc"

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/orders", guard(h.HandleListOrders))
	mux.HandleFunc("POST /api/v1/orders", guard(h.HandleCreateOrder))
	mux.HandleFunc("GET /api/v1/orders/{id}", guard(h.HandleGetOrder))
	mux.HandleFunc("PUT /api/v1/orders/{id}", guard(h.HandleUpdateOrder))
	mux.HandleFunc("POST /api/v1/orders/{id}/transitions", guard(h.HandleTransition))
	mux.HandleFunc("GET /api/v1/orders/{id}/exposure-gate", guard(h.HandleExposureGate))
	mux.HandleFunc("POST /api/v1/orders/{id}/exposure-override", guard(h.HandleExposureOverride))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeOrder(w http.ResponseWriter, status int, o *Order) {
	httpx.WriteRevisionETag(w, o.Revision)
	writeJSON(w, status, o)
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid order id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

// actor reads the caller's subject for the audit rows (ADR 0005 section 2.3).
func actor(r *http.Request) string {
	if claims, ok := r.Context().Value(middleware.UserContextKey).(*middleware.UserClaims); ok && claims != nil {
		return claims.Subject
	}
	return ""
}

// callerRole reads the caller's role; empty when the auth chain set none (an
// in process caller, a machine key).
func callerRole(r *http.Request) string {
	if claims, ok := r.Context().Value(middleware.UserContextKey).(*middleware.UserClaims); ok && claims != nil {
		return claims.Role
	}
	return ""
}

func (h *Handler) HandleCreateOrder(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req Request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	o, err := h.service.Create(r.Context(), draft, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/orders/"+o.ID.String())
	writeOrder(w, http.StatusCreated, o)
}

func (h *Handler) HandleGetOrder(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	o, err := h.service.GetOrder(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeOrder(w, http.StatusOK, o)
}

func (h *Handler) HandleUpdateOrder(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req Request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.ParseUpdate()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pre := Precondition{IfMatch: r.Header.Get("If-Match"), Revision: draft.Revision}
	o, err := h.service.Update(r.Context(), id, draft, pre, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeOrder(w, http.StatusOK, o)
}

// parseListFilter reads the list's filters (ADR 0005 section 5.7): status,
// customer_id, job_id, ship_to_id, delivery_type and quote_id beside the
// platform's cursor, limit and include.
func parseListFilter(r *http.Request) (f ListFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status", "customer_id",
		"job_id", "ship_to_id", "delivery_type", "quote_id")
	if err != nil {
		return f, false, err
	}
	page, err := httpx.ParseListQuery(r, cursorScope)
	if err != nil {
		return f, false, err
	}
	f.Limit = page.Limit

	v := &httpx.Validator{}
	if vals := q["status"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "status", "parameter is repeated; send a comma separated list")
		}
		seen := map[OrderStatus]bool{}
		for _, name := range strings.Split(vals[0], ",") {
			st, ok := ParseStatus(name)
			if !ok {
				v.Check(false, "status", "must be a comma separated list of: "+strings.Join(statusNames, ", "))
				break
			}
			if !seen[st] {
				seen[st] = true
				f.Statuses = append(f.Statuses, st)
			}
		}
	}
	if vals := q["customer_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "customer_id", "parameter is repeated")
		} else if id, ok := v.UUID("customer_id", &vals[0], true); ok {
			f.CustomerID = &id
		}
	}
	if vals := q["job_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "job_id", "parameter is repeated")
		} else if id, ok := v.UUID("job_id", &vals[0], true); ok {
			f.JobID = &id
		}
	}
	if vals := q["ship_to_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "ship_to_id", "parameter is repeated")
		} else if id, ok := v.UUID("ship_to_id", &vals[0], true); ok {
			f.ShipToID = &id
		}
	}
	if vals := q["quote_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "quote_id", "parameter is repeated")
		} else if id, ok := v.UUID("quote_id", &vals[0], true); ok {
			f.QuoteID = &id
		}
	}
	if vals := q["delivery_type"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "delivery_type", "parameter is repeated")
		} else if dt, ok := ParseDeliveryType(vals[0]); ok {
			f.DeliveryType = &dt
		} else {
			v.Check(false, "delivery_type", "must be one of: pickup, delivery")
		}
	}
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "include", "parameter is repeated")
		} else {
			set, ierr := httpx.ParseInclude(vals[0])
			if ierr != nil {
				return f, false, ierr
			}
			wantTotal = set.Has(httpx.IncludeTotal)
		}
	}
	if err := v.Err(); err != nil {
		return f, false, err
	}

	if page.Key != nil {
		if len(page.Key) != 2 {
			return f, false, cursorError()
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return f, false, cursorError()
		}
		f.AfterTime, f.AfterID = &at, id
	}
	return f, wantTotal, nil
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func (h *Handler) HandleListOrders(w http.ResponseWriter, r *http.Request) {
	f, wantTotal, err := parseListFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.ListOrders(r.Context(), f, wantTotal)
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

func (h *Handler) HandleTransition(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req TransitionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	v := &httpx.Validator{}
	var to OrderStatus
	if req.To == nil {
		v.Check(false, "to", "is required")
	} else if st, ok := ParseStatus(*req.To); ok {
		to = st
	} else {
		v.Check(false, "to", "must be one of: "+strings.Join(statusNames, ", "))
	}
	var revision *int64
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		revision = &n
	}
	var reason, holdNote string
	if req.Reason != nil {
		reason = strings.TrimSpace(*req.Reason)
	}
	if req.HoldNote != nil {
		holdNote = strings.TrimSpace(*req.HoldNote)
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	// The release of a hold is held to the finance roles inside the
	// transition, under the order lock (ADR 0005 5.2).
	body := TransitionBody{Reason: reason, HoldNote: holdNote, Actor: actor(r), Role: callerRole(r)}
	pre := Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision}
	o, err := h.service.Transition(r.Context(), id, to, pre, body)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeOrder(w, http.StatusOK, o)
}

// unresolvedExposure is the duck-typed interface pricing.ErrUnresolvedExposure
// satisfies, so the exposure payload renders as blockers inside the wire's
// error envelope without the order package importing pricing.
type unresolvedExposure interface {
	UnresolvedExposurePayload() map[string]any
}

// exposureBlockers maps an unresolved exposure gate error to the wire's 409
// envelope with its payload as blockers; false when err is something else.
func exposureBlockers(err error) map[string]any {
	var ue unresolvedExposure
	if errors.As(err, &ue) {
		return ue.UnresolvedExposurePayload()
	}
	return nil
}

// HandleExposureGate answers whether the pre-ship exposure gate blocks the
// order: 200 {"blocked": false} when clear, the wire's 409 with the exposure
// payload as blockers when it does (behaviour kept, onto the envelope).
func (h *Handler) HandleExposureGate(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.CheckExposureGate(r.Context(), id); err != nil {
		if payload := exposureBlockers(err); payload != nil {
			writeExposureConflict(w, r, payload)
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocked": false})
}

func (h *Handler) HandleExposureOverride(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body struct {
		Notes string `json:"notes"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	err = h.service.OverrideExposure(r.Context(), id, body.Notes, actor(r), callerRole(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"overridden": true})
}

func writeExposureConflict(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	details := []httpx.FieldError{}
	for k, val := range payload {
		details = append(details, httpx.FieldError{Code: "exposure_" + k, Message: fmt.Sprint(val)})
	}
	httpx.WriteError(w, r, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
		Message: "the order's source quote has unresolved index exposure",
		Details: details})
}
