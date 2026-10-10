// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
	guard   LocationGuard // optional; nil leaves a payload location unchecked (unit tests)
}

// LocationGuard applies the payload branch rule (ADR 0007 section 2.3) to a
// location id a body names. *middleware.BranchGuard satisfies it.
type LocationGuard interface {
	CheckPayloadLocation(ctx context.Context, locationID uuid.UUID) error
}

// WithBranchGuard makes the stock routes refuse a location whose branch the
// caller may not target. Without it a payload location is not checked, so
// serve always sets it.
func (h *Handler) WithBranchGuard(g LocationGuard) *Handler {
	h.guard = g
	return h
}

// checkLocations answers 403 and returns false when a location the body names
// sits in a branch the caller may not target.
func (h *Handler) checkLocations(w http.ResponseWriter, r *http.Request, field string, ids ...*uuid.UUID) bool {
	if h.guard == nil {
		return true
	}
	for _, id := range ids {
		if id == nil {
			continue
		}
		err := h.guard.CheckPayloadLocation(r.Context(), *id)
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httputil.RespondError(w, r, field+" is in a branch this caller may not target", http.StatusForbidden, err)
			return false
		}
		if err != nil {
			httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
			return false
		}
	}
	return true
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

	mux.HandleFunc("POST /api/v1/inventory/adjust", guard(h.AdjustStock))
	mux.HandleFunc("POST /api/v1/inventory/transfer", guard(h.MoveStock))
	mux.HandleFunc("GET /api/v1/inventory", guard(h.ListInventory))
}

func (h *Handler) AdjustStock(w http.ResponseWriter, r *http.Request) {
	var req StockAdjustmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid input", http.StatusBadRequest, err)
		return
	}

	if !h.checkLocations(w, r, "location_id", req.LocationID) {
		return
	}

	if err := h.service.AdjustStock(r.Context(), req); err != nil {
		slog.Error("AdjustStock failed", "error", err)
		httputil.RespondError(w, r, "Internal Server Error", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) MoveStock(w http.ResponseWriter, r *http.Request) {
	var req StockMovementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid input", http.StatusBadRequest, err)
		return
	}

	if !h.checkLocations(w, r, "from_location_id or to_location_id", req.FromLocationID, &req.ToLocationID) {
		return
	}

	if err := h.service.MoveStock(r.Context(), req); err != nil {
		slog.Error("MoveStock failed", "error", err)
		httputil.RespondError(w, r, "Internal Server Error", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// levelsOrdering is the levels list's cursor scope: `created_at DESC, id DESC`
// on the column migration 098 gave the table.
const levelsOrdering = "inventory.created_at_id_desc"

// levelQuery is the parsed levels list request: the filters, the keyset
// position and the include names.
type levelQuery struct {
	filters     LevelFilters
	after       *time.Time
	afterID     *uuid.UUID
	limit       int
	wantTotal   bool
	wantProduct bool
}

var errBadLevelCursor = httpx.BadRequest("cursor keyset is malformed",
	httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})

// parseLevelQuery reads the levels request: the strict guard on the route's
// names (cursor, limit, include and the two filters), the page, the cursor's
// keyset position and the include set (total, and the product summary
// expansion ADR 0001 section 1 names).
func parseLevelQuery(r *http.Request) (levelQuery, error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "product_id", "location_id")
	if err != nil {
		return levelQuery{}, err
	}
	page, err := httpx.ParseListQuery(r, levelsOrdering)
	if err != nil {
		return levelQuery{}, err
	}
	out := levelQuery{limit: page.Limit}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return levelQuery{}, errBadLevelCursor
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return levelQuery{}, errBadLevelCursor
		}
		out.after, out.afterID = &at, &id
	}
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			return levelQuery{}, httpx.BadRequest("include parameter is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"})
		}
		set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal, "product")
		if ierr != nil {
			return levelQuery{}, ierr
		}
		out.wantTotal = set.Has(httpx.IncludeTotal)
		out.wantProduct = set.Has("product")
	}
	v := &httpx.Validator{}
	if vals := q["product_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "product_id", "parameter is repeated")
		} else if id, ok := v.UUID("product_id", &vals[0], true); ok {
			out.filters.ProductID = &id
		}
	}
	if vals := q["location_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "location_id", "parameter is repeated")
		} else if id, ok := v.UUID("location_id", &vals[0], true); ok {
			out.filters.LocationID = &id
		}
	}
	if err := v.Err(); err != nil {
		return levelQuery{}, err
	}
	return out, nil
}

// ListInventory serves GET /api/v1/inventory: the levels list on the contract
// (ADR 0001, ADR 0006 7.2), one page of the envelope, held to the caller's
// branch wall by the repository's query.
func (h *Handler) ListInventory(w http.ResponseWriter, r *http.Request) {
	query, err := parseLevelQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rows, more, err := h.service.ListLevelsPage(r.Context(), query.filters, query.after, query.afterID, query.limit)
	if err != nil {
		if errors.Is(err, ErrNoLevelStore) {
			httpx.WriteError(w, r, httpx.Unavailable(err.Error()))
			return
		}
		slog.Error("ListLevelsPage failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	if !query.wantProduct {
		for i := range rows {
			rows[i].Product = nil
		}
	}
	next := ""
	if more && len(rows) > 0 {
		last := rows[len(rows)-1]
		next, err = httpx.MintCursor(levelsOrdering,
			httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	opts := []httpx.ListOption{}
	if query.wantTotal {
		total, err := h.service.CountLevels(r.Context(), query.filters)
		if err != nil {
			slog.Error("CountLevels failed", "error", err)
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(total))
	}
	httpx.WriteList(w, rows, next, query.limit, opts...)
}
