// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package events serves GET /api/v1/events, the events read API of the
// transactional outbox (ADR 0003): one cursor-paginated feed of every
// domain event, in the ADR 0001 list envelope with the event envelope
// named in snake_case (event_id, type, org, branch_id, entity, data, at).
package events

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// cursorScope is the ordering scope of the feed's cursors: position order,
// which ADR 0003 makes commit order, so a page resumed from a cursor never
// skips an event that commits after the page was served.
const cursorScope = "events.position"

// EntityRef is the entity the event is about, the envelope's
// entity {kind, id}.
type EntityRef struct {
	Kind string    `json:"kind"`
	ID   uuid.UUID `json:"id"`
}

// Item is one event on the wire: the envelope of ADR 0001 section 12.
// Optional fields are present with null, never omitted.
type Item struct {
	EventID  uuid.UUID       `json:"event_id"`
	Type     string          `json:"type"`
	Org      string          `json:"org"`
	BranchID *uuid.UUID      `json:"branch_id"`
	Entity   EntityRef       `json:"entity"`
	Data     json.RawMessage `json:"data"`
	At       string          `json:"at"`
}

// Handler serves the events feed.
type Handler struct {
	db *database.DB
}

// NewHandler builds the feed handler over the outbox tables.
func NewHandler(db *database.DB) *Handler {
	return &Handler{db: db}
}

// RegisterRoutes attaches the feed. The role guard is applied at
// registration like every module route; the feed is an administration and
// integration surface over the whole database, gated admin and owner, the
// narrowest gate the admin reads use.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}
	mux.HandleFunc("GET /api/v1/events", guard(h.List))
}

// List serves one page of the feed. Strictness is the platform's: an
// unknown query parameter, a malformed cursor, a bad limit or an unknown
// include name is a 400 naming the field, never a silent no-op.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "types", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, cursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	types, err := parseTypes(q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	var includeTotal bool
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			httpx.WriteError(w, r, &httpx.Error{
				Status:  http.StatusBadRequest,
				Code:    httpx.CodeValidationFailed,
				Message: "include parameter is repeated",
				Details: []httpx.FieldError{{Field: "include", Message: "parameter is repeated"}},
			})
			return
		}
		set, err := httpx.ParseInclude(vals[0])
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		includeTotal = set.Has(httpx.IncludeTotal)
	}

	var after int64
	if page.Key != nil {
		n, err := strconv.ParseInt(page.Key[0], 10, 64)
		if err != nil || n < 0 {
			httpx.WriteError(w, r, httpx.BadRequest("cursor keyset is not a position",
				httpx.FieldError{Field: "cursor", Message: "cursor keyset is not a position"}))
			return
		}
		after = n
	}

	rows, err := outbox.ListEvents(r.Context(), h.db.Pool, after, types, page.Limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// The feed always returns next_cursor, an explicit exception to ADR
	// 0001's null-at-end rule (ADR 0003 section 5): a poller must be able to
	// adopt the tail cursor without re-reading its previous page, and a
	// first-time consumer of a feed shorter than the limit must get a cursor
	// at all. The cursor is the last served position, or the request's
	// cursor echoed back when the page is empty.
	nextPos := after
	if len(rows) > 0 {
		nextPos = rows[len(rows)-1].Position
	}
	next, err := httpx.MintCursor(cursorScope, strconv.FormatInt(nextPos, 10))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, Item{
			EventID:  row.Event.ID,
			Type:     row.Event.Type,
			Org:      row.Event.Org,
			BranchID: row.Event.BranchID,
			Entity:   EntityRef{Kind: row.Event.EntityType, ID: row.Event.EntityID},
			Data:     row.Event.Data,
			At:       httpx.FormatKeyTime(row.Event.At),
		})
	}

	opts := []httpx.ListOption{}
	if includeTotal {
		total, err := outbox.CountEvents(r.Context(), h.db.Pool, types)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(total))
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

// parseTypes reads the types filter: repeatable, comma separated, exact
// matches only (the read API takes no wildcard syntax; the drain's pattern
// matching is its own). Every name must be a type the writer could have
// recorded, or the request is a 400 validation_failed naming types.
func parseTypes(q map[string][]string) ([]string, error) {
	vals := q["types"]
	if len(vals) == 0 {
		return nil, nil
	}
	if len(vals) == 1 && vals[0] == "" {
		return nil, &httpx.Error{
			Status:  http.StatusBadRequest,
			Code:    httpx.CodeValidationFailed,
			Message: "types is empty",
			Details: []httpx.FieldError{{Field: "types", Message: "must name at least one event type"}},
		}
	}
	var types []string
	seen := map[string]bool{}
	for _, val := range vals {
		for _, name := range strings.Split(val, ",") {
			if !outbox.ValidType(name) || seen[name] {
				return nil, &httpx.Error{
					Status:  http.StatusBadRequest,
					Code:    httpx.CodeValidationFailed,
					Message: "types carries an unsupported or repeated name",
					Details: []httpx.FieldError{{Field: "types",
						Message: "must be a comma separated list of dot-delimited lowercase event types, each name once"}},
				}
			}
			seen[name] = true
			types = append(types, name)
		}
	}
	return types, nil
}
