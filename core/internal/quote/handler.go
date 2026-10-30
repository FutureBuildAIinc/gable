// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// cursorScope names the list's ordering: created_at then id, newest first.
// A cursor minted for any other ordering is refused (ADR 0001 section 2).
const cursorScope = "quotes.created_at_id_desc"

// maxAttachBody bounds the quote file route's body: the same 5 MiB bound
// the create applies to a decoded original upload.
const maxAttachBody = 5 << 20

// safeFilename strips any characters that are not alphanumeric, hyphens,
// underscores, or dots to prevent header injection in Content-Disposition.
var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func sanitizeFilename(name string) string {
	return unsafeFilenameChars.ReplaceAllString(name, "_")
}

type Handler struct {
	service    *Service
	activities ActivityService
}

// ActivityService is the crm module's seam for the quote activity routes
// (QE-04-a): an activity logged on a quote carries the quote's customer, so
// it shows on the customer's feed too, and the quote's own feed lists it.
// *crm.Service satisfies it.
type ActivityService interface {
	Create(ctx context.Context, customerID uuid.UUID, d *crm.Draft) (*crm.Activity, error)
	List(ctx context.Context, customerID uuid.UUID, f crm.ListFilter, wantTotal bool) ([]crm.Activity, bool, *int64, error)
}

// WithActivities wires the activity seam; without it the activity routes
// answer 503 (serve always wires it).
func (h *Handler) WithActivities(a ActivityService) *Handler {
	h.activities = a
	return h
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
	mux.HandleFunc("GET /api/v1/quotes/{id}/activities", guard(h.HandleListQuoteActivities))
	mux.HandleFunc("POST /api/v1/quotes/{id}/activities", guard(h.HandleCreateQuoteActivity))
	mux.HandleFunc("GET /api/v1/quotes/{id}/file", guard(h.HandleDownloadOriginalFile))
	mux.HandleFunc("PUT /api/v1/quotes/{id}/file", guard(h.HandleAttachFile))
	mux.HandleFunc("PUT /api/v1/quotes/{id}/follow-up", guard(h.HandleSetFollowUp))
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

// pathRecord reads the {id} path value of a read route: a UUID first, then
// the quote's document number (ADR 0007 section 7). A well formed number of
// another entity, or anything else, is a 400 naming id; a number that names
// no visible row is the read's 404.
func pathRecord(r *http.Request) string {
	return r.PathValue("id")
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
	q, err := h.service.GetQuoteByIDOrNumber(r.Context(), pathRecord(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeQuote(w, http.StatusOK, q)
}

// parseListFilter reads the list's filters from the query. The allowed set is
// the three platform names (cursor, limit, include) plus status, customer_id
// and follow_up (ADR 0001 section 5). follow_up=mine resolves the caller's
// salesperson from the sign in's email; a caller with no sign in, or one no
// salesperson row carries, owns no follow ups, so the page is empty.
func (h *Handler) parseListFilter(r *http.Request) (f ListFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status", "customer_id", "follow_up")
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
	if vals := q["follow_up"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "follow_up", "parameter is repeated")
		} else if kind, ok := ParseFollowUpKind(vals[0]); ok {
			f.FollowUp = &kind
		} else {
			v.Check(false, "follow_up", "must be one of: "+strings.Join(followUpNames, ", "))
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

	if f.FollowUp != nil && *f.FollowUp == FollowUpMine {
		email := ""
		if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
			email = claims.Email
		}
		owner, rerr := h.service.SalespersonByEmail(r.Context(), email)
		if rerr != nil {
			return f, false, rerr
		}
		f.MineOwner = owner
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
	f, wantTotal, err := h.parseListFilter(r)
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
	draft, err := req.ParseUpdate()
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

// HandleConvertToOrder accepts the quote and creates the order in one
// transaction, answering 201 with the order and its Location (ADR 0005
// section 5.8): no payload crosses the client, so a retry cannot create a
// second order.
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
	o, err := h.service.Convert(r.Context(), id, precondition(r, nil))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/orders/"+o.ID.String())
	httpx.WriteRevisionETag(w, o.Revision)
	writeJSON(w, http.StatusCreated, o)
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
	q, err := h.service.GetQuoteByIDOrNumber(r.Context(), pathRecord(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	data, filename, contentType, err := h.service.GetOriginalFile(r.Context(), q.ID)
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

// HandleAttachFile is the quote file route (ADR 0007 section 10): the raw
// body with its content type, the same 5 MiB bound the create applies, on
// the revision precondition, only while the quote is in status draft. The
// parse flow's original file attaches here, by the committer, right after a
// promotion. Being an entity write on a gated module, an agent marked
// session is refused it by the confirm gate.
func (h *Handler) HandleAttachFile(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAttachBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, r, httpx.PayloadTooLarge("the file is past the 5 MiB bound"))
			return
		}
		httpx.WriteError(w, r, httpx.BadRequest("the file could not be read"))
		return
	}
	filename := sanitizeFilename(r.URL.Query().Get("filename"))
	if filename == "" {
		filename = "original-upload"
	}
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	q, err := h.service.AttachFile(r.Context(), id, body, filename, contentType,
		precondition(r, nil))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeQuote(w, http.StatusOK, q)
}

// FollowUpRequest is the body of PUT /quotes/{id}/follow-up: the date to
// chase the quote and the salesperson who owns the chase, both null (or the
// date null) to clear. revision is the precondition beside If-Match.
type FollowUpRequest struct {
	FollowUpAt      json.RawMessage `json:"follow_up_at"`
	FollowUpOwnerID *string         `json:"follow_up_owner_id"`
	Revision        json.RawMessage `json:"revision"`
}

// HandleSetFollowUp sets or clears the quote's follow up on the client's
// revision (QE-04-a). Clearing is the completed state: what happened is
// logged as an activity, and the queue holds only quotes with a date.
func (h *Handler) HandleSetFollowUp(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req FollowUpRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	v := &httpx.Validator{}
	at, _ := v.Timestamp("follow_up_at", req.FollowUpAt, false)
	var owner *uuid.UUID
	if id, ok := v.UUID("follow_up_owner_id", req.FollowUpOwnerID, false); ok {
		owner = &id
		v.Check(at != nil, "follow_up_owner_id",
			"needs a follow up date: send follow_up_at too, or null both to clear")
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

	q, err := h.service.SetFollowUp(r.Context(), id, &FollowUpDraft{At: at, Owner: owner},
		precondition(r, revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeQuote(w, http.StatusOK, q)
}

// HandleListQuoteActivities lists the quote's activity feed: the quote is
// resolved behind the branch wall first (a quote the caller cannot see is
// the read's own 404), then the customer's activities are narrowed to it
// with the same query validation the customer feed applies.
func (h *Handler) HandleListQuoteActivities(w http.ResponseWriter, r *http.Request) {
	if h.activities == nil {
		httpx.WriteError(w, r, httpx.Unavailable("the activity service is not wired"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f, wantTotal, err := crm.ParseActivitiesQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q, err := h.service.GetQuote(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f.QuoteID = &q.ID
	items, hasMore, total, err := h.activities.List(r.Context(), q.CustomerID, f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = crm.NextActivitiesCursor(hasMore, last.CreatedAt, last.ID); err != nil {
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

// HandleCreateQuoteActivity logs an activity on the quote: the quote is
// resolved behind the branch wall first, then the activity is written
// carrying the quote and the quote's customer, so it shows on the customer's
// feed too. The quote is named by the path; a body quote_id is a 400 unknown
// field, the same posture as the customer route's body customer_id.
func (h *Handler) HandleCreateQuoteActivity(w http.ResponseWriter, r *http.Request) {
	if h.activities == nil {
		httpx.WriteError(w, r, httpx.Unavailable("the activity service is not wired"))
		return
	}
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req crm.Request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q, err := h.service.GetQuote(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft.QuoteID = &q.ID
	a, err := h.activities.Create(r.Context(), q.CustomerID, draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/activities/"+a.ID.String())
	httpx.WriteRevisionETag(w, a.Revision)
	writeJSON(w, http.StatusCreated, a)
}
