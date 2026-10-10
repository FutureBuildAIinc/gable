// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The lists' orderings: created_at then id, newest first (ADR 0001 section 2).
const (
	invoiceCursorScope = "invoices.created_at_id_desc"
	creditCursorScope  = "credit_memos.created_at_id_desc"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the invoice and credit memo routes (ADR 0005 6.2 and
// 6.3). The document print and email routes keep their paths in the document
// module. The payments of an invoice stay with the payment module until C2-4
// converts them into applications.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/invoices", guard(h.HandleList))
	mux.HandleFunc("GET /api/v1/invoices/{id}", guard(h.HandleGet))
	mux.HandleFunc("POST /api/v1/invoices/{id}/transitions", guard(h.HandleTransition))

	mux.HandleFunc("GET /api/v1/credit-memos", guard(h.HandleListCreditMemos))
	mux.HandleFunc("POST /api/v1/credit-memos", guard(h.HandleCreateCreditMemo))
	mux.HandleFunc("GET /api/v1/credit-memos/{id}", guard(h.HandleGetCreditMemo))
	mux.HandleFunc("PUT /api/v1/credit-memos/{id}", guard(h.HandleUpdateCreditMemo))
	mux.HandleFunc("POST /api/v1/credit-memos/{id}/transitions", guard(h.HandleCreditTransition))
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

// callerRole reads the caller's role; empty when the auth chain set none (an
// in process caller, a machine key).
func callerRole(r *http.Request) string {
	if claims, ok := r.Context().Value(middleware.UserContextKey).(*middleware.UserClaims); ok && claims != nil {
		return claims.Role
	}
	return ""
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

// repeated reports a query parameter sent more than once.
func single(v *httpx.Validator, q map[string][]string, name string) (string, bool) {
	vals := q[name]
	if len(vals) == 0 {
		return "", false
	}
	if len(vals) > 1 {
		v.Check(false, name, "parameter is repeated")
		return "", false
	}
	return vals[0], true
}

func uuidParam(v *httpx.Validator, q map[string][]string, name string) *uuid.UUID {
	raw, ok := single(v, q, name)
	if !ok {
		return nil
	}
	if id, ok := v.UUID(name, &raw, true); ok {
		return &id
	}
	return nil
}

// parseKey reads the keyset position of a cursor.
func parseKey(page httpx.CursorPage) (at *timeAndID, err error) {
	if page.Key == nil {
		return nil, nil
	}
	if len(page.Key) != 2 {
		return nil, cursorError()
	}
	t, terr := httpx.ParseKeyTime(page.Key[0])
	id, uerr := httpx.ParseKeyUUID(page.Key[1])
	if terr != nil || uerr != nil {
		return nil, cursorError()
	}
	return &timeAndID{t, id}, nil
}

// parseListFilter reads the invoice list's filters (ADR 0005 6.1): status,
// customer_id, job_id, ship_to_id, order_id and overdue beside the platform's
// cursor, limit and include. Unknown parameters and uppercase values are
// refused, never ignored.
func parseListFilter(r *http.Request) (f ListFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status", "customer_id", "job_id", "ship_to_id", "order_id", "overdue")
	if err != nil {
		return f, false, err
	}
	page, err := httpx.ParseListQuery(r, invoiceCursorScope)
	if err != nil {
		return f, false, err
	}
	f.Limit = page.Limit
	v := &httpx.Validator{}
	if raw, ok := single(v, q, "status"); ok {
		seen := map[InvoiceStatus]bool{}
		for _, name := range strings.Split(raw, ",") {
			st, ok := ParseStatus(name)
			if !ok {
				v.Check(false, "status", "must be a comma separated list of: "+strings.Join(invoiceStatusNames, ", "))
				break
			}
			if !seen[st] {
				seen[st] = true
				f.Statuses = append(f.Statuses, st)
			}
		}
	}
	f.CustomerID = uuidParam(v, q, "customer_id")
	f.JobID = uuidParam(v, q, "job_id")
	f.ShipToID = uuidParam(v, q, "ship_to_id")
	f.OrderID = uuidParam(v, q, "order_id")
	if raw, ok := single(v, q, "overdue"); ok {
		switch raw {
		case "true":
			t := true
			f.Overdue = &t
		case "false":
			t := false
			f.Overdue = &t
		default:
			v.Check(false, "overdue", "must be true or false")
		}
	}
	wantTotal, ierr := includeTotal(v, q)
	if ierr != nil {
		return f, false, ierr
	}
	if err := v.Err(); err != nil {
		return f, false, err
	}
	key, err := parseKey(page)
	if err != nil {
		return f, false, err
	}
	if key != nil {
		f.AfterTime, f.AfterID = &key.at, key.id
	}
	return f, wantTotal, nil
}

func includeTotal(v *httpx.Validator, q map[string][]string) (bool, error) {
	raw, ok := single(v, q, "include")
	if !ok {
		return false, nil
	}
	set, err := httpx.ParseInclude(raw)
	if err != nil {
		return false, err
	}
	return set.Has(httpx.IncludeTotal), nil
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	f, wantTotal, err := parseListFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.svc.ListInvoices(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next, err = httpx.MintCursor(invoiceCursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
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

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	inv, err := h.svc.GetInvoiceByIDOrNumber(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, notFound(err))
		return
	}
	httpx.WriteRevisionETag(w, inv.Revision)
	writeJSON(w, http.StatusOK, inv)
}

// transitionBody parses the shared transition body: the target, the
// revision and the reason (a void's reason is required, 1 to 500 characters).
func transitionBody(r *http.Request) (to string, rev *int64, reason string, err error) {
	var req TransitionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return "", nil, "", err
	}
	v := &httpx.Validator{}
	if req.To == nil {
		v.Check(false, "to", "is required")
	} else {
		to = *req.To
	}
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		rev = &n
	}
	if req.Reason != nil {
		reason = strings.TrimSpace(*req.Reason)
		v.Check(len(reason) <= 500, "reason", "must be at most 500 characters")
	}
	if err := v.Err(); err != nil {
		return "", nil, "", err
	}
	return to, rev, reason, nil
}

// HandleTransition runs POST /invoices/{id}/transitions. Only void is a
// client's transition: unpaid, partial, paid and written_off are derived by
// the AR core from applications (never sent by a client), so asking for one
// is a 409 invalid_state_transition, and a word that is no status a 400.
func (h *Handler) HandleTransition(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "invoice")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	to, rev, reason, err := transitionBody(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status, ok := ParseStatus(to)
	if !ok {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "to", Message: "must be one of: " + strings.Join(invoiceStatusNames, ", ")}))
		return
	}
	if status != InvoiceStatusVoid {
		httpx.WriteError(w, r, httpx.InvalidStateTransition("an invoice is moved to "+status.Status()+" by its payments and credit memos, not by a transition; the only transition is void"))
		return
	}
	if reason == "" {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "reason", Message: "is required to void an invoice"}))
		return
	}
	inv, err := h.svc.VoidInvoice(r.Context(), id, Precondition{IfMatch: r.Header.Get("If-Match"), Revision: rev},
		Transition{Reason: reason, Actor: actor(r), Role: callerRole(r)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, inv.Revision)
	writeJSON(w, http.StatusOK, inv)
}

// ---------------------------------------------------------------------------
// Credit memos.
// ---------------------------------------------------------------------------

func writeCredit(w http.ResponseWriter, status int, cm *CreditMemo) {
	httpx.WriteRevisionETag(w, cm.Revision)
	writeJSON(w, status, cm)
}

func parseCreditFilter(r *http.Request) (f CreditFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status", "customer_id", "invoice_id", "job_id")
	if err != nil {
		return f, false, err
	}
	page, err := httpx.ParseListQuery(r, creditCursorScope)
	if err != nil {
		return f, false, err
	}
	f.Limit = page.Limit
	v := &httpx.Validator{}
	if raw, ok := single(v, q, "status"); ok {
		seen := map[CreditStatus]bool{}
		for _, name := range strings.Split(raw, ",") {
			st, ok := ParseCreditStatus(name)
			if !ok {
				v.Check(false, "status", "must be a comma separated list of: "+strings.Join(creditStatusNames, ", "))
				break
			}
			if !seen[st] {
				seen[st] = true
				f.Statuses = append(f.Statuses, st)
			}
		}
	}
	f.CustomerID = uuidParam(v, q, "customer_id")
	f.InvoiceID = uuidParam(v, q, "invoice_id")
	f.JobID = uuidParam(v, q, "job_id")
	wantTotal, ierr := includeTotal(v, q)
	if ierr != nil {
		return f, false, ierr
	}
	if err := v.Err(); err != nil {
		return f, false, err
	}
	key, err := parseKey(page)
	if err != nil {
		return f, false, err
	}
	if key != nil {
		f.AfterTime, f.AfterID = &key.at, key.id
	}
	return f, wantTotal, nil
}

func (h *Handler) HandleListCreditMemos(w http.ResponseWriter, r *http.Request) {
	f, wantTotal, err := parseCreditFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.svc.ListCreditMemos(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next, err = httpx.MintCursor(creditCursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
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

func (h *Handler) HandleCreateCreditMemo(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req CreditRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cm, err := h.svc.CreateCreditMemo(r.Context(), draft, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/credit-memos/"+cm.ID.String())
	writeCredit(w, http.StatusCreated, cm)
}

func (h *Handler) HandleGetCreditMemo(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "credit memo")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cm, err := h.svc.GetCreditMemo(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, notFound(err))
		return
	}
	writeCredit(w, http.StatusOK, cm)
}

func (h *Handler) HandleUpdateCreditMemo(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "credit memo")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req CreditRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.ParseUpdate()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cm, err := h.svc.UpdateCreditMemo(r.Context(), id, draft, Precondition{IfMatch: r.Header.Get("If-Match"), Revision: draft.Revision}, actor(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCredit(w, http.StatusOK, cm)
}

func (h *Handler) HandleCreditTransition(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "credit memo")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	to, rev, reason, err := transitionBody(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status, ok := ParseCreditStatus(to)
	if !ok {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "to", Message: "must be one of: " + strings.Join(creditStatusNames, ", ")}))
		return
	}
	if status == CreditVoid && reason == "" {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "reason", Message: "is required to void a credit memo"}))
		return
	}
	cm, err := h.svc.TransitionCreditMemo(r.Context(), id, status, Precondition{IfMatch: r.Header.Get("If-Match"), Revision: rev},
		Transition{Reason: reason, Actor: actor(r), Role: callerRole(r)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCredit(w, http.StatusOK, cm)
}

// timeAndID is a keyset position.
type timeAndID struct {
	at time.Time
	id uuid.UUID
}
