// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// cursorScope names the list's ordering: created_at then id, newest first.
// A cursor minted for any other ordering is refused (ADR 0001 section 2).
const cursorScope = "drafts.created_at_id_desc"

// Handler serves the seven draft routes of every registered kind. Each kind
// registers its own literal routes, so the route census lists exactly which
// kinds exist and an unknown kind is the router's 404.
type Handler struct {
	service *Service
	feeds   *FeedHandler
}

// NewHandler builds the handler over the drafts core.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// WithFeedHandler wires the change feed endpoint (the feed route is one of
// the seven each kind registers).
func (h *Handler) WithFeedHandler(f *FeedHandler) *Handler {
	h.feeds = f
	return h
}

// Guarded wraps one handler func with a guard middleware (the kind's roles
// composed with the branch middleware, exactly as the module's entity
// routes), so a kind can register its literal routes with the same wrap the
// module's own registration uses.
func Guarded(guard func(http.Handler) http.Handler, handler http.HandlerFunc) http.HandlerFunc {
	if guard == nil {
		return handler
	}
	return func(w http.ResponseWriter, r *http.Request) {
		guard(handler).ServeHTTP(w, r)
	}
}

// The seven per-kind endpoints. Each kind registers its own LITERAL routes
// in its own package through them (ADR 0007 section 2.3: the route census
// then lists exactly which kinds exist and an unknown kind is the router's
// 404); see the quotes kind's RegisterDraftRoutes for the shape.

// List is the kind's list endpoint.
func (h *Handler) List(k Kind) http.HandlerFunc { return h.list(k) }

// Create is the kind's create endpoint.
func (h *Handler) Create(k Kind) http.HandlerFunc { return h.create(k) }

// Feed is the kind's change feed endpoint; without a wired feed handler it
// answers 503.
func (h *Handler) Feed(k Kind) http.HandlerFunc {
	if h.feeds != nil {
		return h.feeds.feed(k)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, httpx.Unavailable("the draft change feed is not wired"))
	}
}

// Get is the kind's read endpoint.
func (h *Handler) Get(k Kind) http.HandlerFunc { return h.get(k) }

// Update is the kind's payload replace endpoint.
func (h *Handler) Update(k Kind) http.HandlerFunc { return h.update(k) }

// Transitions is the kind's transition endpoint.
func (h *Handler) Transitions(k Kind) http.HandlerFunc { return h.transitions(k) }

// Promote is the kind's promotion endpoint.
func (h *Handler) Promote(k Kind) http.HandlerFunc { return h.promote(k) }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeDoc(w http.ResponseWriter, status int, d *Document) {
	httpx.WriteRevisionETag(w, d.Revision)
	writeJSON(w, status, d)
}

// noQuery refuses every query parameter: these routes declare none.
func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

// pathID reads the {id} path value; a malformed one is a 400.
func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid draft id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func (h *Handler) create(k Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := noQuery(r); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var req CreateRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d, err := h.service.Create(r.Context(), k.Module(), req)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		w.Header().Set("Location", "/api/v1/drafts/"+k.Module()+"/"+d.ID.String())
		writeDoc(w, http.StatusCreated, d)
	}
}

func (h *Handler) get(k Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := noQuery(r); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		id, err := pathID(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d, err := h.service.Get(r.Context(), k.Module(), id)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		writeDoc(w, http.StatusOK, d)
	}
}

func (h *Handler) list(k Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status", "subject_id", "created_by_kind")
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		page, err := httpx.ParseListQuery(r, cursorScope)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		f := ListFilterDTO{Limit: page.Limit}

		v := &httpx.Validator{}
		if vals := q["status"]; len(vals) > 0 {
			if len(vals) > 1 {
				v.Check(false, "status", "parameter is repeated; send a comma separated list")
			}
			seen := map[Status]bool{}
			for _, name := range strings.Split(vals[0], ",") {
				st, ok := ParseStatus(name)
				if !ok {
					v.Check(false, "status", "must be a comma separated list of: "+strings.Join(StatusNames, ", "))
					break
				}
				if !seen[st] {
					seen[st] = true
					f.Statuses = append(f.Statuses, st)
				}
			}
		}
		if vals := q["subject_id"]; len(vals) > 0 {
			if len(vals) > 1 {
				v.Check(false, "subject_id", "parameter is repeated")
			} else if id, ok := v.UUID("subject_id", &vals[0], true); ok {
				f.SubjectID = &id
			}
		}
		if vals := q["created_by_kind"]; len(vals) > 0 {
			if len(vals) > 1 {
				v.Check(false, "created_by_kind", "parameter is repeated")
			} else {
				switch vals[0] {
				case "user", "key", "agent", "anonymous":
					f.CreatedByKind = vals[0]
				default:
					v.Check(false, "created_by_kind", "must be one of: user, key, agent, anonymous")
				}
			}
		}
		if err := v.Err(); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if vals := q["include"]; len(vals) > 0 {
			if len(vals) > 1 {
				httpx.WriteError(w, r, httpx.BadRequest("include parameter is repeated",
					httpx.FieldError{Field: "include", Message: "parameter is repeated"}))
				return
			}
			set, ierr := httpx.ParseInclude(vals[0])
			if ierr != nil {
				httpx.WriteError(w, r, ierr)
				return
			}
			f.WantTotal = set.Has(httpx.IncludeTotal)
		}
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
			f.AfterTime, f.AfterID = &at, id
		}

		items, hasMore, total, err := h.service.List(r.Context(), k.Module(), f)
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
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func (h *Handler) update(k Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := noQuery(r); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		id, err := pathID(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var req UpdateRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d, err := h.service.Update(r.Context(), k.Module(), id, req, r.Header.Get("If-Match"))
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		writeDoc(w, http.StatusOK, d)
	}
}

// TransitionRequest is the body of POST /drafts/{kind}/{id}/transitions.
type TransitionRequest struct {
	To       *string         `json:"to"`
	Revision json.RawMessage `json:"revision"`
}

func (h *Handler) transitions(k Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		var to Status
		if req.To == nil {
			v.Check(false, "to", "is required")
		} else if st, ok := ParseStatus(*req.To); ok {
			to = st
		} else {
			v.Check(false, "to", "must be one of: "+strings.Join(StatusNames, ", "))
		}
		var revision *int64
		if n, ok := v.Int("revision", req.Revision, false); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			revision = &n
		}
		if err := v.Err(); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d, err := h.service.Transition(r.Context(), k.Module(), id, to,
			Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		writeDoc(w, http.StatusOK, d)
	}
}

// PromoteRequest is the body of POST /drafts/{kind}/{id}/promote. The body
// carries nothing but the revision: what is committed is exactly the
// revision the committer read.
type PromoteRequest struct {
	Revision json.RawMessage `json:"revision"`
}

func (h *Handler) promote(k Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := noQuery(r); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		id, err := pathID(r)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		var req PromoteRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		v := &httpx.Validator{}
		var revision *int64
		if n, ok := v.Int("revision", req.Revision, false); ok {
			v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
			revision = &n
		}
		if err := v.Err(); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		d, created, err := h.service.Promote(r.Context(), k.Module(), id,
			Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if created && d.Promoted != nil {
			w.Header().Set("Location", "/api/v1/"+k.Module()+"/"+d.Promoted.EntityID.String())
			writeDoc(w, http.StatusCreated, d)
			return
		}
		writeDoc(w, http.StatusOK, d)
	}
}
