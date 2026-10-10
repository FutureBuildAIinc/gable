// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The ordering scopes of the module's five lists (ADR 0001 section 2): a
// cursor minted for any other ordering is refused.
const (
	vehiclesScope = "vehicles.created_at_id_desc"
	driversScope  = "drivers.created_at_id_desc"
	routesScope   = "delivery_routes.scheduled_date_id_desc"
	stopsScope    = "deliveries.stop_sequence_id_asc"
	photosScope   = "delivery_pod_photos.uploaded_at_id_asc"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
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

	// Fleet
	mux.HandleFunc("GET /api/v1/delivery/vehicles", guard(h.HandleListVehicles))
	mux.HandleFunc("POST /api/v1/delivery/vehicles", guard(h.HandleCreateVehicle))
	mux.HandleFunc("GET /api/v1/delivery/vehicles/{id}", guard(h.HandleGetVehicle))
	mux.HandleFunc("PUT /api/v1/delivery/vehicles/{id}", guard(h.HandleUpdateVehicle))
	mux.HandleFunc("DELETE /api/v1/delivery/vehicles/{id}", guard(h.HandleDeleteVehicle))
	mux.HandleFunc("POST /api/v1/delivery/vehicles/{id}/photo", guard(h.HandleUploadVehiclePhoto))
	mux.HandleFunc("GET /api/v1/delivery/drivers", guard(h.HandleListDrivers))
	mux.HandleFunc("POST /api/v1/delivery/drivers", guard(h.HandleCreateDriver))
	mux.HandleFunc("GET /api/v1/delivery/drivers/{id}", guard(h.HandleGetDriver))
	mux.HandleFunc("PUT /api/v1/delivery/drivers/{id}", guard(h.HandleUpdateDriver))
	mux.HandleFunc("DELETE /api/v1/delivery/drivers/{id}", guard(h.HandleDeleteDriver))
	mux.HandleFunc("POST /api/v1/delivery/drivers/{id}/photo", guard(h.HandleUploadDriverPhoto))

	// Routes
	mux.HandleFunc("GET /api/v1/delivery/routes", guard(h.HandleListRoutes))
	mux.HandleFunc("POST /api/v1/delivery/routes", guard(h.HandleCreateRoute))
	mux.HandleFunc("GET /api/v1/delivery/routes/{id}", guard(h.HandleGetRoute))
	mux.HandleFunc("POST /api/v1/delivery/routes/{id}/transitions", guard(h.HandleRouteTransition))
	mux.HandleFunc("POST /api/v1/delivery/routes/{id}/reorder", guard(h.HandleReorderStops))
	mux.HandleFunc("POST /api/v1/delivery/routes/{id}/optimize", guard(h.HandleOptimizeRoute))

	// Stops
	mux.HandleFunc("GET /api/v1/delivery/routes/{id}/deliveries", guard(h.HandleListDeliveries))
	mux.HandleFunc("POST /api/v1/delivery/deliveries", guard(h.HandleAssignOrder))
	mux.HandleFunc("GET /api/v1/delivery/deliveries/{id}", guard(h.HandleGetDelivery))
	mux.HandleFunc("POST /api/v1/delivery/deliveries/{id}/transitions", guard(h.HandleStopTransition))
	mux.HandleFunc("POST /api/v1/delivery/deliveries/{id}/adjust-qty", guard(h.HandleAdjustQuantity))
	mux.HandleFunc("POST /api/v1/delivery/deliveries/{id}/pod-photo", guard(h.HandleUploadPODPhoto))
	mux.HandleFunc("GET /api/v1/delivery/deliveries/{id}/pod-photos", guard(h.HandleListPODPhotos))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid "+what+" id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

// actor reads the caller's subject for the audit rows.
func actor(r *http.Request) string {
	if claims, ok := r.Context().Value(middleware.UserContextKey).(*middleware.UserClaims); ok && claims != nil {
		return claims.Subject
	}
	return ""
}

func writeVehicle(w http.ResponseWriter, status int, v *Vehicle) {
	httpx.WriteRevisionETag(w, v.Revision)
	writeJSON(w, status, v)
}

func writeDriver(w http.ResponseWriter, status int, d *Driver) {
	httpx.WriteRevisionETag(w, d.Revision)
	writeJSON(w, status, d)
}

func writeRoute(w http.ResponseWriter, status int, route *Route) {
	httpx.WriteRevisionETag(w, route.Revision)
	writeJSON(w, status, route)
}

func writeStop(w http.ResponseWriter, status int, s *Stop) {
	httpx.WriteRevisionETag(w, s.Revision)
	writeJSON(w, status, s)
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

// pageList reads the platform's list parameters (cursor, limit, include) and
// the keyset position for the time-then-id orderings the fleet lists use.
func pageList(r *http.Request, q map[string][]string) (limit int, after *time.Time, afterID uuid.UUID, wantTotal bool, err error) {
	page, err := httpx.ParseListQuery(r, vehiclesScope)
	if err != nil {
		return 0, nil, uuid.Nil, false, err
	}
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			return 0, nil, uuid.Nil, false, httpx.BadRequest("include is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"})
		}
		set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal)
		if ierr != nil {
			return 0, nil, uuid.Nil, false, ierr
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return 0, nil, uuid.Nil, false, cursorError()
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return 0, nil, uuid.Nil, false, cursorError()
		}
		after, afterID = &at, id
	}
	return page.Limit, after, afterID, wantTotal, nil
}

// nextTimeCursor mints the time-then-id continuation.
func nextTimeCursor(scope string, hasMore bool, created time.Time, id uuid.UUID) (string, error) {
	if !hasMore {
		return "", nil
	}
	return httpx.MintCursor(scope, httpx.FormatKeyTime(created), id.String())
}

// Fleet: vehicles

func (h *Handler) HandleListVehicles(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, after, afterID, wantTotal, err := pageList(r, q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := FleetListFilter{Limit: limit, AfterTime: after, AfterID: afterID}
	items, more, total, err := h.service.ListVehicles(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = nextTimeCursor(vehiclesScope, more, last.CreatedAt.Time, last.ID); err != nil {
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

func (h *Handler) HandleCreateVehicle(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req VehicleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.service.CreateVehicle(r.Context(), draft, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/delivery/vehicles/"+v.ID.String())
	writeVehicle(w, http.StatusCreated, v)
}

func (h *Handler) HandleGetVehicle(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "vehicle")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.service.GetVehicle(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeVehicle(w, http.StatusOK, v)
}

func (h *Handler) HandleUpdateVehicle(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "vehicle")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req VehicleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.service.UpdateVehicle(r.Context(), id, draft,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: draft.Revision}, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeVehicle(w, http.StatusOK, v)
}

func (h *Handler) HandleDeleteVehicle(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "vehicle")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.DeleteVehicle(r.Context(), id,
		Precondition{IfMatch: r.Header.Get("If-Match")}, actor(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Fleet: drivers

func (h *Handler) HandleListDrivers(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, after, afterID, wantTotal, err := pageList(r, q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := FleetListFilter{Limit: limit, AfterTime: after, AfterID: afterID}
	items, more, total, err := h.service.ListDrivers(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = nextTimeCursor(driversScope, more, last.CreatedAt.Time, last.ID); err != nil {
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

func (h *Handler) HandleCreateDriver(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req DriverRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	d, err := h.service.CreateDriver(r.Context(), draft, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/delivery/drivers/"+d.ID.String())
	writeDriver(w, http.StatusCreated, d)
}

func (h *Handler) HandleGetDriver(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "driver")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	d, err := h.service.GetDriver(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeDriver(w, http.StatusOK, d)
}

func (h *Handler) HandleUpdateDriver(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "driver")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req DriverRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	d, err := h.service.UpdateDriver(r.Context(), id, draft,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: draft.Revision}, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeDriver(w, http.StatusOK, d)
}

func (h *Handler) HandleDeleteDriver(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "driver")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.DeleteDriver(r.Context(), id,
		Precondition{IfMatch: r.Header.Get("If-Match")}, actor(r)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Routes

// parseRouteList reads the route list's parameters: the platform's three,
// the date, driver_id and status filters, and include=stops (the board
// read: routes and stops in one payload) and include=total.
func parseRouteList(r *http.Request) (f RouteListFilter, wantStops, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "date", "driver_id", "status")
	if err != nil {
		return f, false, false, err
	}
	page, err := httpx.ParseListQuery(r, routesScope)
	if err != nil {
		return f, false, false, err
	}
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			return f, false, false, httpx.BadRequest("include is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"})
		}
		set, ierr := httpx.ParseInclude(vals[0], "stops", httpx.IncludeTotal)
		if ierr != nil {
			return f, false, false, ierr
		}
		wantStops, wantTotal = set.Has("stops"), set.Has(httpx.IncludeTotal)
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return f, false, false, cursorError()
		}
		at, terr := time.Parse("2006-01-02", page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil || !datePattern.MatchString(page.Key[0]) {
			return f, false, false, cursorError()
		}
		key := at.Format("2006-01-02")
		f.AfterDate, f.AfterID = &key, id
	}
	f.Limit = page.Limit
	v := &httpx.Validator{}
	if vals := q["date"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "date", "parameter is repeated")
		} else if at, terr := time.Parse("2006-01-02", vals[0]); terr != nil || !datePattern.MatchString(vals[0]) {
			v.Check(false, "date", "must be a date as YYYY-MM-DD")
		} else {
			day := at.Format("2006-01-02")
			f.Date = &day
		}
	}
	if vals := q["driver_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "driver_id", "parameter is repeated")
		} else if id, ok := v.UUID("driver_id", &vals[0], true); ok {
			f.DriverID = &id
		}
	}
	if vals := q["status"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "status", "parameter is repeated")
		} else if st, ok := ParseRouteStatus(vals[0]); ok {
			f.Status = &st
		} else {
			v.Check(false, "status", "must be one of: "+strings.Join(RouteStatusNames(), ", "))
		}
	}
	if err := v.Err(); err != nil {
		return f, false, false, err
	}
	return f, wantStops, wantTotal, nil
}

func (h *Handler) HandleListRoutes(w http.ResponseWriter, r *http.Request) {
	f, wantStops, wantTotal, err := parseRouteList(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, more, total, err := h.service.ListRoutes(r.Context(), f, wantStops, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 && more {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(routesScope, last.ScheduledDate, last.ID.String()); err != nil {
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

func (h *Handler) HandleCreateRoute(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req RouteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	route, err := h.service.CreateRoute(r.Context(), draft, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/delivery/routes/"+route.ID.String())
	writeRoute(w, http.StatusCreated, route)
}

func (h *Handler) HandleGetRoute(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "route")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	route, err := h.service.GetRoute(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeRoute(w, http.StatusOK, route)
}

func (h *Handler) HandleRouteTransition(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "route")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req RouteTransitionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	route, err := h.service.TransitionRoute(r.Context(), id, draft,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: draft.Revision}, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeRoute(w, http.StatusOK, route)
}

func (h *Handler) HandleReorderStops(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "route")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ReorderRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	route, err := h.service.ReorderStops(r.Context(), id, draft,
		Precondition{IfMatch: r.Header.Get("If-Match")}, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeRoute(w, http.StatusOK, route)
}

func (h *Handler) HandleOptimizeRoute(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "route")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	route, err := h.service.OptimizeRoute(r.Context(), id,
		Precondition{IfMatch: r.Header.Get("If-Match")}, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeRoute(w, http.StatusOK, route)
}

// Stops

func (h *Handler) HandleListDeliveries(w http.ResponseWriter, r *http.Request) {
	routeID, err := pathID(r, "route")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, stopsScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	wantTotal := false
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			httpx.WriteError(w, r, httpx.BadRequest("include is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"}))
			return
		}
		set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal)
		if ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	f := StopListFilter{Limit: page.Limit}
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, cursorError())
			return
		}
		seq, serr := strconv.Atoi(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if serr != nil || uerr != nil || seq < 1 {
			httpx.WriteError(w, r, cursorError())
			return
		}
		f.AfterStopSeq, f.AfterID = &seq, id
	}
	items, more, err := h.service.ListDeliveries(r.Context(), routeID, f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 && more {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(stopsScope, strconv.Itoa(last.StopSequence), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if wantTotal {
		total, terr := h.service.CountDeliveries(r.Context(), routeID)
		if terr != nil {
			httpx.WriteError(w, r, terr)
			return
		}
		opts = append(opts, httpx.WithTotal(total))
	}
	httpx.WriteList(w, items, next, f.Limit, opts...)
}

func (h *Handler) HandleAssignOrder(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req AssignStopRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	stop, warning, err := h.service.AssignOrderToRoute(r.Context(), draft, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	response := struct {
		Delivery        *Stop            `json:"delivery"`
		CapacityWarning *CapacityWarning `json:"capacity_warning"`
	}{Delivery: stop, CapacityWarning: warning}
	w.Header().Set("Location", "/api/v1/delivery/deliveries/"+stop.ID.String())
	httpx.WriteRevisionETag(w, stop.Revision)
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) HandleGetDelivery(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "delivery")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	stop, err := h.service.GetDelivery(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeStop(w, http.StatusOK, stop)
}

func (h *Handler) HandleStopTransition(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "delivery")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req StopTransitionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	stop, err := h.service.TransitionStop(r.Context(), id, draft,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: draft.Revision}, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeStop(w, http.StatusOK, stop)
}

func (h *Handler) HandleAdjustQuantity(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "delivery")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req AdjustRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	stop, err := h.service.AdjustDeliveryQuantity(r.Context(), id, draft, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeStop(w, http.StatusOK, stop)
}

// Photos

const maxUploadSize = 10 << 20 // 10 MB

var photoExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

// saveUpload stores one multipart photo on disk and returns its public URL.
// Every client mistake is a 400 naming photo; a body over the bound is 413.
func saveUpload(w http.ResponseWriter, r *http.Request, subdir string) (string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	file, header, err := r.FormFile("photo")
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			return "", httpx.PayloadTooLarge("the photo is over 10 MB")
		}
		return "", httpx.BadRequest("the photo field is required and must hold a file",
			httpx.FieldError{Field: "photo", Message: "a file is required"})
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !photoExtensions[ext] {
		return "", httpx.BadRequest("unsupported file type",
			httpx.FieldError{Field: "photo", Message: "must be a jpg, jpeg, png or webp file"})
	}

	dir := filepath.Join("uploads", subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}

	filename := uuid.New().String() + ext
	path := filepath.Join(dir, filename)
	out, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		return "", fmt.Errorf("copy file: %w", err)
	}

	return "/uploads/" + subdir + "/" + filename, nil
}

func (h *Handler) HandleUploadVehiclePhoto(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "vehicle")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	url, err := saveUpload(w, r, "vehicles")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v, err := h.service.SetVehiclePhoto(r.Context(), id, url, actor(r))
	if err != nil {
		slog.Error("SetVehiclePhoto failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	writeVehicle(w, http.StatusOK, v)
}

func (h *Handler) HandleUploadDriverPhoto(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "driver")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	url, err := saveUpload(w, r, "drivers")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	d, err := h.service.SetDriverPhoto(r.Context(), id, url, actor(r))
	if err != nil {
		slog.Error("SetDriverPhoto failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	writeDriver(w, http.StatusOK, d)
}

// HandleUploadPODPhoto attaches one proof-of-delivery photo to a stop.
func (h *Handler) HandleUploadPODPhoto(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "delivery")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// RULE (PR 70 review round 4 P3-1): a refused photo upload must leave
	// no file on disk. Validate the stop through the walled read before
	// the multipart body is saved; a stop the wall hides answers 404 and
	// saveUpload is never called, so the upload directory never sees a
	// stray write for a cross-branch or unknown id.
	if _, gerr := h.service.GetDelivery(r.Context(), id); gerr != nil {
		httpx.WriteError(w, r, gerr)
		return
	}
	url, err := saveUpload(w, r, "pod")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	photoType := r.FormValue("photo_type")
	if photoType == "" {
		photoType = "site"
	}
	if photoType != "signature" && photoType != "site" && photoType != "damage" {
		httpx.WriteError(w, r, httpx.BadRequest("photo_type must be signature, site or damage",
			httpx.FieldError{Field: "photo_type", Message: "must be one of: signature, site, damage"}))
		return
	}
	photo, _, err := h.service.UploadPODPhoto(r.Context(), id, url, photoType, actor(r))
	if err != nil {
		slog.Error("UploadPODPhoto failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, photo)
}

// HandleListPODPhotos lists a stop's proof-of-delivery photos.
func (h *Handler) HandleListPODPhotos(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "delivery")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := httpx.StrictQuery(r, "cursor", "limit", "include"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := h.service.GetDelivery(r.Context(), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, photosScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := PhotoListFilter{Limit: page.Limit}
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, cursorError())
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		pid, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			httpx.WriteError(w, r, cursorError())
			return
		}
		f.AfterTime, f.AfterID = &at, pid
	}
	items, more, err := h.service.GetPODPhotos(r.Context(), id, f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 && more {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(photosScope, httpx.FormatKeyTime(last.UploadedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, f.Limit)
}
