// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

const (
	movementsOrdering = "pos.till_movements.created_at_id_asc"
)

// pathUUID reads a UUID path value, or answers the 400 naming it.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: name, Message: "must be a UUID"}))
		return uuid.Nil, false
	}
	return id, true
}

// keysetPage reads the shared list parameters of a (time, id) keyset list: the
// strict guard on the route's names, the page and the keyset position when a
// cursor was sent.
func keysetPage(r *http.Request, scope string, allowed ...string) (after *time.Time, afterID *uuid.UUID, limit int, err error) {
	names := append([]string{"cursor", "limit"}, allowed...)
	if _, err := httpx.StrictQuery(r, names...); err != nil {
		return nil, nil, 0, err
	}
	page, err := httpx.ParseListQuery(r, scope)
	if err != nil {
		return nil, nil, 0, err
	}
	if page.Key != nil {
		bad := httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
		if len(page.Key) != 2 {
			return nil, nil, 0, bad
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return nil, nil, 0, bad
		}
		after, afterID = &at, &id
	}
	return after, afterID, page.Limit, nil
}

// writeKeysetList answers a page: the next cursor is minted from the last
// row when a row follows.
func writeKeysetList[T any](w http.ResponseWriter, r *http.Request, items []T, more bool, scope string, last func(T) (time.Time, uuid.UUID), limit int) {
	next := ""
	if more && len(items) > 0 {
		at, id := last(items[len(items)-1])
		var err error
		next, err = httpx.MintCursor(scope, httpx.FormatKeyTime(at), id.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, limit)
}

// CreateMovement is POST /pos/till/{id}/movements.
func (h *Handler) CreateMovement(w http.ResponseWriter, r *http.Request) {
	// a movement is a person's act, as the open and the close are
	if _, ok := cashierOf(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var req movementRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m, err := h.service.RecordMovement(r.Context(), id, in, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pos/till/"+id.String()+"/movements")
	writeJSON(w, http.StatusCreated, m)
}

// ListMovements is GET /pos/till/{id}/movements.
func (h *Handler) ListMovements(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	after, afterID, limit, err := keysetPage(r, movementsOrdering)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, more, err := h.service.ListMovements(r.Context(), id, after, afterID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeKeysetList(w, r, items, more, movementsOrdering, func(m TillMovement) (time.Time, uuid.UUID) {
		return m.CreatedAt.Time, m.ID
	}, limit)
}
