// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin

import (
	"encoding/json"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// cursorScope names the key list's ordering: created_at then id, newest
// first. A cursor minted for any other ordering is refused (ADR 0001
// section 2).
const cursorScope = "api_keys.created_at_id_desc"

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

	// All admin routes require admin/owner role. A machine key is refused on
	// the key routes themselves (they are user only, ADR 0002 section 4) and
	// needs the finer admin:settings scope on the settings routes (ADR 0009).
	mux.HandleFunc("POST /api/v1/admin/keys", guard(h.CreateKey))
	mux.HandleFunc("GET /api/v1/admin/keys", guard(h.ListKeys))
	mux.HandleFunc("DELETE /api/v1/admin/keys/{id}", guard(h.RevokeKey))
	mux.HandleFunc("GET /api/v1/admin/settings/ai", guard(h.GetAISettings))
	mux.HandleFunc("PUT /api/v1/admin/settings/ai", guard(h.SaveAISettings))
	mux.HandleFunc("DELETE /api/v1/admin/settings/ai", guard(h.DeleteAISettings))
	mux.HandleFunc("GET /api/v1/admin/settings/routing", guard(h.GetRoutingSettings))
	mux.HandleFunc("PUT /api/v1/admin/settings/routing", guard(h.SaveRoutingSettings))
	mux.HandleFunc("DELETE /api/v1/admin/settings/routing", guard(h.DeleteRoutingSettings))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// noQuery refuses every query parameter: these routes declare none.
func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid key id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

// notFound maps the repository's sentinel to the wire's 404.
func notFound(err error) error {
	if err == ErrNotFound {
		return httpx.NotFound("no such machine key")
	}
	return err
}

func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req CreateKeyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	name, scopes, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	raw, key, err := h.service.GenerateKey(r.Context(), name, scopes)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/admin/keys/"+key.ID.String())
	writeJSON(w, http.StatusCreated, CreatedKey{APIKey: raw, Key: *key})
}

func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	_, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, cursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := ListFilter{Limit: page.Limit}
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, cursorError())
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			httpx.WriteError(w, r, cursorError())
			return
		}
		f.AfterAt, f.AfterID = &at, id
	}
	wantTotal := false
	if q := r.URL.Query(); len(q["include"]) > 0 {
		set, ierr := httpx.ParseInclude(q["include"][0])
		if ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}

	items, hasMore, total, err := h.service.ListKeys(r.Context(), f, wantTotal)
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
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func (h *Handler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := h.service.RevokeKey(r.Context(), id); err != nil {
		httpx.WriteError(w, r, notFound(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- AI settings ---

func (h *Handler) GetAISettings(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.GetAISettings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, out.Revision)
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) SaveAISettings(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req SaveAISettingsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	apiKey, baseURL, revision, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.SaveAISettings(r.Context(), apiKey, baseURL,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, out.Revision)
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteAISettings(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	err := h.service.DeleteAISettings(r.Context(),
		Precondition{IfMatch: r.Header.Get("If-Match")})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- routing settings ---

func (h *Handler) GetRoutingSettings(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.GetRoutingSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, out.Revision)
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) SaveRoutingSettings(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req SaveRoutingSettingsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	apiKey, revision, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.SaveRoutingSettings(r.Context(), apiKey,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, out.Revision)
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteRoutingSettings(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	err := h.service.DeleteRoutingSettings(r.Context(),
		Precondition{IfMatch: r.Header.Get("If-Match")})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
