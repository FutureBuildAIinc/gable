// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The lists' orderings (ADR 0001 section 2).
const (
	transactionCursorScope = "account_transactions.created_at_id_desc"
	agingCursorScope       = "ar_aging.customer"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes mounts the account and AR routes (ADR 0005 sections 9 and 10).
// guard is the role guard of the reads; finance is the guard of the acts that
// need the admin, owner or finance roles.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, guard, finance func(http.Handler) http.Handler) {
	wrap := func(g func(http.Handler) http.Handler, f http.HandlerFunc) http.HandlerFunc {
		if g == nil {
			return f
		}
		return func(w http.ResponseWriter, r *http.Request) { g(f).ServeHTTP(w, r) }
	}
	mux.HandleFunc("GET /api/v1/accounts/{id}", wrap(guard, h.HandleSummary))
	mux.HandleFunc("GET /api/v1/accounts/{id}/transactions", wrap(guard, h.HandleTransactions))
	mux.HandleFunc("GET /api/v1/ar/aging", wrap(guard, h.HandleAging))
	mux.HandleFunc("GET /api/v1/ar/aging/summary", wrap(guard, h.HandleAgingSummary))
	mux.HandleFunc("GET /api/v1/ar/customers/{id}/statement", wrap(guard, h.HandleStatement))
	mux.HandleFunc("GET /api/v1/ar/reconciliation", wrap(finance, h.HandleReconciliation))
	mux.HandleFunc("POST /api/v1/ar/applications/{id}/reverse", wrap(finance, h.HandleReverse))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid "+what+" id", httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

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

func actor(r *http.Request) string {
	if claims, ok := r.Context().Value(middleware.UserContextKey).(*middleware.UserClaims); ok && claims != nil {
		return claims.Subject
	}
	return ""
}

func callerRole(r *http.Request) string {
	if claims, ok := r.Context().Value(middleware.UserContextKey).(*middleware.UserClaims); ok && claims != nil {
		return claims.Role
	}
	return ""
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "account")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sum, err := h.svc.GetSummary(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (h *Handler) HandleTransactions(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "account")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, transactionCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := TransactionFilter{CustomerID: id, Limit: page.Limit + 1}
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, cursorError())
			return
		}
		t, terr := httpx.ParseKeyTime(page.Key[0])
		aid, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			httpx.WriteError(w, r, cursorError())
			return
		}
		f.AfterTime, f.AfterID = &t, aid
	}
	v := &httpx.Validator{}
	wantTotal := false
	if raw, ok := single(v, q, "include"); ok {
		set, ierr := httpx.ParseInclude(raw)
		if ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := h.svc.GetSummary(r.Context(), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.svc.ListTransactions(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > page.Limit {
		items = items[:page.Limit]
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(transactionCursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if wantTotal {
		n, err := h.svc.CountTransactions(r.Context(), id)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(n))
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

// agingQuery reads the aging parameters shared by the list and its summary.
func (h *Handler) agingQuery(r *http.Request, extra ...string) (AgingQuery, map[string][]string, error) {
	allowed := append([]string{"group_by", "as_of", "basis", "customer_id"}, extra...)
	q, err := httpx.StrictQuery(r, allowed...)
	if err != nil {
		return AgingQuery{}, nil, err
	}
	out := AgingQuery{GroupBy: "customer", Basis: "due_date"}
	v := &httpx.Validator{}
	if raw, ok := single(v, q, "group_by"); ok {
		switch raw {
		case "customer", "job", "ship_to":
			out.GroupBy = raw
		default:
			v.Check(false, "group_by", "must be one of: customer, job, ship_to")
		}
	}
	if raw, ok := single(v, q, "basis"); ok {
		switch raw {
		case "due_date", "invoice_date":
			out.Basis = raw
		default:
			v.Check(false, "basis", "must be one of: due_date, invoice_date")
		}
	}
	if raw, ok := single(v, q, "as_of"); ok {
		t, perr := time.Parse("2006-01-02", raw)
		if perr != nil || t.Format("2006-01-02") != raw {
			v.Check(false, "as_of", "must be a date, YYYY-MM-DD")
		} else {
			out.AsOf = t
		}
	}
	if raw, ok := single(v, q, "customer_id"); ok {
		if id, ok := v.UUID("customer_id", &raw, true); ok {
			out.CustomerID = &id
		}
	}
	if err := v.Err(); err != nil {
		return AgingQuery{}, nil, err
	}
	if out.AsOf.IsZero() {
		today, terr := h.svc.todayInDefaultBranch(r.Context())
		if terr != nil {
			return AgingQuery{}, nil, terr
		}
		out.AsOf = today
	}
	return out, q, nil
}

func (h *Handler) HandleAging(w http.ResponseWriter, r *http.Request) {
	q, qs, err := h.agingQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, agingCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	wantTotal := false
	if raw, ok := single(v, qs, "include"); ok {
		set, ierr := httpx.ParseInclude(raw)
		if ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var key []string
	if page.Key != nil {
		if len(page.Key) != 4 {
			httpx.WriteError(w, r, cursorError())
			return
		}
		key = page.Key
	}
	all, err := h.svc.Aging(r.Context(), q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items := []AgingItem{}
	more := false
	for i := range all {
		it := all[i]
		if key != nil && !agingAfter(&it, key) {
			continue
		}
		if len(items) == page.Limit {
			more = true
			break
		}
		items = append(items, it)
	}
	next := ""
	if more {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(agingCursorScope, last.CustomerName, last.CustomerID.String(), last.GroupID(), last.Currency); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if wantTotal {
		opts = append(opts, httpx.WithTotal(int64(len(all))))
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

// agingAfter reports whether the item sorts after the cursor's key
// (customer name, customer id, group id, currency).
func agingAfter(it *AgingItem, key []string) bool {
	if it.CustomerName != key[0] {
		return it.CustomerName > key[0]
	}
	if id := it.CustomerID.String(); id != key[1] {
		return id > key[1]
	}
	if g := it.GroupID(); g != key[2] {
		return g > key[2]
	}
	return it.Currency > key[3]
}

func (h *Handler) HandleAgingSummary(w http.ResponseWriter, r *http.Request) {
	q, _, err := h.agingQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sum, err := h.svc.AgingSummary(r.Context(), q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (h *Handler) HandleStatement(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "from", "to", "job_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sq := StatementQuery{CustomerID: id}
	v := &httpx.Validator{}
	parseDate := func(name string) time.Time {
		raw, ok := single(v, q, name)
		if !ok {
			return time.Time{}
		}
		t, perr := time.Parse("2006-01-02", raw)
		if perr != nil || t.Format("2006-01-02") != raw {
			v.Check(false, name, "must be a date, YYYY-MM-DD")
			return time.Time{}
		}
		return t
	}
	sq.From, sq.To = parseDate("from"), parseDate("to")
	if raw, ok := single(v, q, "job_id"); ok {
		if jid, ok := v.UUID("job_id", &raw, true); ok {
			sq.JobID = &jid
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if sq.To.IsZero() {
		today, terr := h.svc.todayInDefaultBranch(r.Context())
		if terr != nil {
			httpx.WriteError(w, r, terr)
			return
		}
		sq.To = today
	}
	if sq.From.IsZero() {
		sq.From = sq.To.AddDate(0, -1, 0)
	}
	if sq.From.After(sq.To) {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "from", Message: "must not be after to"}))
		return
	}
	st, err := h.svc.Statement(r.Context(), sq)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *Handler) HandleReconciliation(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rec, err := h.svc.Reconcile(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// ReverseBody is the body of an application reversal.
type ReverseBody struct {
	Reason *string `json:"reason"`
}

func (h *Handler) HandleReverse(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "application")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body ReverseBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	reason := ""
	if body.Reason != nil {
		reason = strings.TrimSpace(*body.Reason)
	}
	v := &httpx.Validator{}
	v.Check(reason != "", "reason", "is required")
	v.Check(len(reason) <= 500, "reason", "must be at most 500 characters")
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	app, err := h.svc.ReverseApplication(r.Context(), id, ReverseRequest{Reason: reason, Actor: actor(r), Role: callerRole(r)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}
