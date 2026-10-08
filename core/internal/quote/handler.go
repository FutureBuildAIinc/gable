// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// cursorScope names the list's ordering: created_at then id, newest first.
// A cursor minted for any other ordering is refused (ADR 0001 section 2).
const cursorScope = "quotes.created_at_id_desc"

// safeFilename strips any characters that are not alphanumeric, hyphens,
// underscores, or dots to prevent header injection in Content-Disposition.
var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func sanitizeFilename(name string) string {
	return unsafeFilenameChars.ReplaceAllString(name, "_")
}

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

	mux.HandleFunc("POST /api/v1/quotes", guard(h.HandleCreateQuote))
	mux.HandleFunc("GET /api/v1/quotes/analytics", guard(h.HandleGetAnalytics))
	mux.HandleFunc("GET /api/v1/quotes", guard(h.HandleListQuotes))
	mux.HandleFunc("GET /api/v1/quotes/{id}", guard(h.HandleGetQuotePath))
	mux.HandleFunc("GET /api/v1/quotes/{id}/file", guard(h.HandleDownloadOriginalFile))
	mux.HandleFunc("PUT /api/v1/quotes/{id}", guard(h.HandleUpdateQuote))
	mux.HandleFunc("POST /api/v1/quotes/{id}/transitions", guard(h.HandleTransition))
	mux.HandleFunc("POST /api/v1/quotes/{id}/convert", guard(h.HandleConvertToOrder))
}

// writeJSON writes a 2xx JSON body. A document write also carries the
// document's revision as its ETag (ADR 0001 section 11).
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeQuote(w http.ResponseWriter, status int, q *Quote) {
	httpx.WriteRevisionETag(w, q.Revision)
	writeJSON(w, status, q)
}

// pathID reads the {id} path value; a malformed one is a 400.
func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid quote id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

// noQuery refuses every query parameter: these routes declare none.
func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

func (h *Handler) HandleCreateQuote(w http.ResponseWriter, r *http.Request) {
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
	q, err := h.service.Create(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/quotes/"+q.ID.String())
	writeQuote(w, http.StatusCreated, q)
}

func (h *Handler) HandleGetQuotePath(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q, err := h.service.GetQuote(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeQuote(w, http.StatusOK, q)
}

// parseListFilter reads the list's filters from the query. The allowed set is
// the three platform names (cursor, limit, include) plus status and
// customer_id (ADR 0001 section 5).
func parseListFilter(r *http.Request) (f ListFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status", "customer_id")
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
		seen := map[QuoteState]bool{}
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

func (h *Handler) HandleListQuotes(w http.ResponseWriter, r *http.Request) {
	f, wantTotal, err := parseListFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.ListQuotes(r.Context(), f, wantTotal)
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

// precondition reads the client's revision: the If-Match header and the
// body's revision, both optional here; the service refuses a write with
// neither (428) and one whose revision is behind (409).
func precondition(r *http.Request, body *int64) Precondition {
	return Precondition{IfMatch: r.Header.Get("If-Match"), Revision: body}
}

func (h *Handler) HandleUpdateQuote(w http.ResponseWriter, r *http.Request) {
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
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q, err := h.service.Update(r.Context(), id, draft, precondition(r, draft.Revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeQuote(w, http.StatusOK, q)
}

// TransitionRequest is the body of POST /quotes/{id}/transitions.
type TransitionRequest struct {
	To       *string         `json:"to"`
	Revision json.RawMessage `json:"revision"`
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
	var to QuoteState
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
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	q, err := h.service.Transition(r.Context(), id, to, precondition(r, revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeQuote(w, http.StatusOK, q)
}

// HandleConvertToOrder accepts the quote and returns the order payload the
// client POSTs to /orders itself; no order is created here.
func (h *Handler) HandleConvertToOrder(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	payload, err := h.service.Convert(r.Context(), id, precondition(r, nil))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, payload.Revision)
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handler) HandleGetAnalytics(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	analytics, err := h.service.GetAnalytics(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, analytics)
}

func (h *Handler) HandleDownloadOriginalFile(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	data, filename, contentType, err := h.service.GetOriginalFile(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(data) == 0 {
		httpx.WriteError(w, r, httpx.NotFound("no original file is stored for this quote"))
		return
	}

	if filename == "" {
		filename = "original-upload"
	}
	filename = sanitizeFilename(filename)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "inline; filename=\""+filename+"\"")
	w.Write(data)
}
