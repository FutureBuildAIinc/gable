// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// projectsScope names the dealer list's ordering: updated_at then id.
const projectsScope = "pro_projects.updated_at_id_desc"

// DealerHandler serves the dealer's desk routes under /api/v1/pro-projects:
// the shared project pipeline of the contractors' customer quotes, and the
// dealer's acceptance. Everything it writes is a DealerProject (price wall 1).
type DealerHandler struct {
	svc *Service
}

// NewDealerHandler builds the dealer's handler.
func NewDealerHandler(svc *Service) *DealerHandler { return &DealerHandler{svc: svc} }

// RegisterRoutes registers the dealer routes. read guards the pipeline reads,
// write the acceptance.
func (h *DealerHandler) RegisterRoutes(mux *http.ServeMux, read, write func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/pro-projects", read(http.HandlerFunc(h.List)))
	mux.Handle("GET /api/v1/pro-projects/{id}", read(http.HandlerFunc(h.Get)))
	mux.Handle("POST /api/v1/pro-projects/{id}/accept", write(http.HandlerFunc(h.Accept)))
}

func (h *DealerHandler) List(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "cursor", "limit"); err != nil {
		fail(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, projectsScope)
	if err != nil {
		fail(w, r, err)
		return
	}
	var after *time.Time
	var afterID uuid.UUID
	if page.Key != nil {
		if len(page.Key) != 2 {
			fail(w, r, httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"}))
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			fail(w, r, httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"}))
			return
		}
		after, afterID = &at, id
	}
	items, qs, err := h.svc.DealerList(r.Context(), page.Limit, after, afterID)
	if err != nil {
		fail(w, r, err)
		return
	}
	next := ""
	if len(items) > page.Limit {
		items = items[:page.Limit]
		last := qs[page.Limit-1]
		if next, err = httpx.MintCursor(projectsScope, httpx.FormatKeyTime(last.UpdatedAt), last.ID.String()); err != nil {
			fail(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, page.Limit)
}

func (h *DealerHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	d, err := h.svc.DealerGet(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *DealerHandler) Accept(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	who := deskActor(r)
	d, err := h.svc.DealerAccept(r.Context(), who, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ShareLinkBody is the optional body of issuing a dealer quote link.
type ShareLinkBody struct {
	SendTo string `json:"send_to"`
}

// shareLinkBody is the answer: the raw token appears here, once.
type shareLinkAnswer struct {
	Path      string          `json:"path"`
	URL       string          `json:"url,omitempty"`
	ExpiresAt httpx.Timestamp `json:"expires_at"`
	Delivered bool            `json:"delivered"`
}

// RegisterShareLinkRoute registers the dealer's route that issues the link of
// one of its own quotes. guard is the quote module's own role and branch
// wall, so the desk reaches only the quotes it may already read.
func (h *DealerHandler) RegisterShareLinkRoute(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("POST /api/v1/quotes/{id}/share-link", guard(http.HandlerFunc(h.ShareLink)))
}

// ShareLink issues (or rotates) the link of a sent dealer quote.
func (h *DealerHandler) ShareLink(w http.ResponseWriter, r *http.Request) {
	id, err := idOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body ShareLinkBody
	if r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := httpx.DecodeJSON(r, &body); err != nil {
			fail(w, r, err)
			return
		}
	}
	who := deskActor(r)
	res, err := h.svc.ShareDealerQuote(r.Context(), who, id, body.SendTo)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, shareLinkAnswer{Path: res.Path, URL: res.URL, ExpiresAt: httpx.TimestampOf(res.ExpiresAt), Delivered: res.Delivered})
}

// deskActor is the desk user the request names: the id is the token's
// subject; the email is only the key to the display name.
func deskActor(r *http.Request) DealerActor {
	who := DealerActor{Ref: "desk"}
	if c := middleware.ClaimsFromContext(r.Context()); c != nil {
		if c.Subject != "" {
			who.Ref = c.Subject
		}
		who.Email = c.Email
	}
	return who
}
