// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The AR act and read routes of the credit holds, the credit limit
// workflow, the controller's worklist, the batch statement runs and the
// finance charges (AR-02, AR-08, AR-04, AR-05). The reads ride the same role
// guard as the aging and the statement; every act that moves money or credit
// standing rides the finance guard. The acts write their audit rows and,
// where balances move, their events, inside their own transactions.

// The lists' orderings (ADR 0001 section 2).
const (
	creditHoldCursorScope        = "credit_holds.placed_at_id_desc"
	creditLimitChangeCursorScope = "credit_limit_changes.created_at_id_desc"
	statementRunCursorScope      = "ar_statement_runs.created_at_id_desc"
	financeChargeRunCursorScope  = "ar_finance_charge_runs.created_at_id_desc"
)

func (h *Handler) registerARRoutes(mux *http.ServeMux, guard, finance func(http.Handler) http.Handler) {
	wrap := func(g func(http.Handler) http.Handler, f http.HandlerFunc) http.HandlerFunc {
		if g == nil {
			return f
		}
		return func(w http.ResponseWriter, r *http.Request) { g(f).ServeHTTP(w, r) }
	}
	// Credit holds and the sweep (M25).
	mux.HandleFunc("GET /api/v1/ar/credit-holds", wrap(guard, h.HandleListCreditHolds))
	mux.HandleFunc("POST /api/v1/ar/credit-holds", wrap(finance, h.HandlePlaceCreditHold))
	mux.HandleFunc("POST /api/v1/ar/credit-holds/evaluate", wrap(finance, h.HandleEvaluateCreditHolds))
	mux.HandleFunc("POST /api/v1/ar/credit-holds/{id}/release", wrap(finance, h.HandleReleaseCreditHold))
	mux.HandleFunc("POST /api/v1/ar/credit-holds/{id}/override", wrap(finance, h.HandleOverrideCreditHold))
	// The credit limit change workflow (M25).
	mux.HandleFunc("GET /api/v1/ar/customers/{id}/credit-limit-changes", wrap(guard, h.HandleListCreditLimitChanges))
	mux.HandleFunc("POST /api/v1/ar/customers/{id}/credit-limit-changes", wrap(finance, h.HandleRequestCreditLimitChange))
	// The controller's worklist (M25).
	mux.HandleFunc("GET /api/v1/ar/worklist", wrap(guard, h.HandleWorklist))
	// Batch statement runs (M30).
	mux.HandleFunc("GET /api/v1/ar/statements/runs", wrap(guard, h.HandleListStatementRuns))
	mux.HandleFunc("GET /api/v1/ar/statements/runs/{id}", wrap(guard, h.HandleGetStatementRun))
	mux.HandleFunc("POST /api/v1/ar/statements/runs", wrap(finance, h.HandleCreateStatementRun))
	// Finance charges (M30).
	mux.HandleFunc("GET /api/v1/ar/finance-charges/rules", wrap(guard, h.HandleGetFinanceChargeRules))
	mux.HandleFunc("PUT /api/v1/ar/finance-charges/rules", wrap(finance, h.HandleUpdateFinanceChargeRules))
	mux.HandleFunc("GET /api/v1/ar/finance-charges/runs", wrap(guard, h.HandleListFinanceChargeRuns))
	mux.HandleFunc("GET /api/v1/ar/finance-charges/runs/{id}", wrap(guard, h.HandleGetFinanceChargeRun))
	mux.HandleFunc("POST /api/v1/ar/finance-charges/runs", wrap(finance, h.HandleRunFinanceCharges))
}

// parseDateListQuery reads the cursor page and one optional date parameter.
func parseDateListQuery(r *http.Request, v *httpx.Validator, q map[string][]string, name string) time.Time {
	raw, ok := single(v, q, name)
	if !ok {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil || t.Format("2006-01-02") != raw {
		v.Check(false, name, "must be a date, YYYY-MM-DD")
		return time.Time{}
	}
	return t
}

func (h *Handler) HandleListCreditHolds(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "status", "customer_id", "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, creditHoldCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	f := CreditHoldFilter{Limit: page.Limit + 1}
	wantTotal := false
	if raw, ok := single(v, q, "status"); ok {
		f.Status = raw
	}
	if raw, ok := single(v, q, "customer_id"); ok {
		if id, ok := v.UUID("customer_id", &raw, true); ok {
			f.CustomerID = &id
		}
	}
	wantTotal = wantTotal || includeTotal(v, q)
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, cursorError())
			return
		}
		t, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			httpx.WriteError(w, r, cursorError())
			return
		}
		f.AfterTime, f.AfterID = &t, id
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.svc.ListCreditHolds(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > page.Limit {
		items = items[:page.Limit]
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(creditHoldCursorScope, httpx.FormatKeyTime(last.PlacedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if wantTotal {
		n, err := h.svc.CountCreditHolds(r.Context(), f)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(n))
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

// includeTotal reads the include parameter and answers whether it asks for
// the total (the list envelope's include=total, ADR 0001 section 2).
func includeTotal(v *httpx.Validator, q map[string][]string) bool {
	raw, ok := single(v, q, "include")
	if !ok {
		return false
	}
	set, err := httpx.ParseInclude(raw)
	if err != nil {
		v.Check(false, "include", "is not a known include")
		return false
	}
	return set.Has(httpx.IncludeTotal)
}

// placeHoldBody is the body of POST /api/v1/ar/credit-holds.
type placeHoldBody struct {
	CustomerID string `json:"customer_id"`
	Reason     string `json:"reason"`
	HoldType   string `json:"hold_type"`
}

func (h *Handler) HandlePlaceCreditHold(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body placeHoldBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	customerID, ok := v.UUID("customer_id", &body.CustomerID, true)
	if !ok {
		httpx.WriteError(w, r, v.Err())
		return
	}
	hold, placed, err := h.svc.PlaceHold(r.Context(), PlaceHoldIn{CustomerID: customerID, Reason: body.Reason,
		HoldType: body.HoldType, Actor: actor(r), Role: callerRole(r)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if placed {
		status = http.StatusCreated
	}
	writeJSON(w, status, hold)
}

// evaluateBody is the body of POST /api/v1/ar/credit-holds/evaluate.
type evaluateBody struct {
	DryRun      *bool    `json:"dry_run"`
	CustomerIDs []string `json:"customer_ids"`
}

func (h *Handler) HandleEvaluateCreditHolds(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body evaluateBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dryRun := body.DryRun == nil || *body.DryRun
	v := &httpx.Validator{}
	var only []uuid.UUID
	for _, raw := range body.CustomerIDs {
		id, ok := v.UUID("customer_ids", &raw, true)
		if !ok {
			break
		}
		only = append(only, id)
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ev, err := h.svc.EvaluateHolds(r.Context(), dryRun, only, actor(r), callerRole(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

func (h *Handler) HandleReleaseCreditHold(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "credit hold")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body HoldActIn
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body.Actor, body.Role = actor(r), callerRole(r)
	hold, err := h.svc.ReleaseHold(r.Context(), id, body)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hold)
}

// overrideBody is the body of POST /api/v1/ar/credit-holds/{id}/override.
type overrideBody struct {
	Reason        string     `json:"override_reason"`
	OverrideUntil *time.Time `json:"override_until"`
	MaxOrderCents *int64     `json:"max_order_cents"`
}

func (h *Handler) HandleOverrideCreditHold(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "credit hold")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body overrideBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	hold, err := h.svc.OverrideHold(r.Context(), id, HoldActIn{Reason: body.Reason, OverrideUntil: body.OverrideUntil,
		MaxOrderCents: body.MaxOrderCents, Actor: actor(r), Role: callerRole(r)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hold)
}

// creditLimitChangeBody is the body of POST /api/v1/ar/customers/{id}/credit-limit-changes.
type creditLimitChangeBody struct {
	NewLimitCents *int64 `json:"new_limit_cents"`
	Reason        string `json:"reason"`
	Approve       bool   `json:"approve"`
}

func (h *Handler) HandleRequestCreditLimitChange(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body creditLimitChangeBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	if body.NewLimitCents == nil {
		v.Check(false, "new_limit_cents", "is required")
		httpx.WriteError(w, r, v.Err())
		return
	}
	change, err := h.svc.RequestCreditLimitChange(r.Context(), id, CreditLimitChangeIn{
		NewLimitCents: *body.NewLimitCents, Reason: body.Reason, Approve: body.Approve,
		Actor: actor(r), Role: callerRole(r)})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if !body.Approve {
		status = http.StatusCreated
	}
	writeJSON(w, status, change)
}

func (h *Handler) HandleListCreditLimitChanges(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "cursor", "limit", "include"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, creditLimitChangeCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	f := CreditLimitChangeFilter{CustomerID: id, Limit: page.Limit + 1}
	wantTotal := includeTotal(v, q)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
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
	items, err := h.svc.ListCreditLimitChanges(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > page.Limit {
		items = items[:page.Limit]
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(creditLimitChangeCursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if wantTotal {
		n, err := h.svc.CountCreditLimitChanges(r.Context(), id)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(n))
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

func (h *Handler) HandleWorklist(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "as_of", "limit")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	asOf := parseDateListQuery(r, v, q, "as_of")
	limit := 0
	if raw, ok := single(v, q, "limit"); ok {
		n := 0
		digits := raw != ""
		for i := 0; i < len(raw); i++ {
			if raw[i] < '0' || raw[i] > '9' {
				digits = false
				break
			}
			n = n*10 + int(raw[i]-'0')
		}
		if !digits || n < 1 || n > 500 {
			v.Check(false, "limit", "must be a plain integer between 1 and 500")
		} else {
			limit = n
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if asOf.IsZero() {
		today, terr := h.svc.todayInDefaultBranch(r.Context())
		if terr != nil {
			httpx.WriteError(w, r, terr)
			return
		}
		asOf = today
	}
	out, err := h.svc.Worklist(r.Context(), asOf, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// statementRunBody is the body of POST /api/v1/ar/statements/runs.
type statementRunBody struct {
	PeriodStart string   `json:"period_start"`
	PeriodEnd   string   `json:"period_end"`
	CustomerIDs []string `json:"customer_ids"`
	QueueEmail  bool     `json:"queue_email"`
}

func (h *Handler) HandleCreateStatementRun(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body statementRunBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in := StatementRunIn{QueueEmail: body.QueueEmail, Actor: actor(r), Role: callerRole(r)}
	v := &httpx.Validator{}
	parseDate := func(name, raw string) time.Time {
		if raw == "" {
			return time.Time{}
		}
		t, err := time.Parse("2006-01-02", raw)
		if err != nil || t.Format("2006-01-02") != raw {
			v.Check(false, name, "must be a date, YYYY-MM-DD")
			return time.Time{}
		}
		return t
	}
	in.PeriodStart, in.PeriodEnd = parseDate("period_start", strings.TrimSpace(body.PeriodStart)),
		parseDate("period_end", strings.TrimSpace(body.PeriodEnd))
	seen := map[uuid.UUID]bool{}
	for _, raw := range body.CustomerIDs {
		id, ok := v.UUID("customer_ids", &raw, true)
		if !ok {
			break
		}
		if seen[id] {
			v.Check(false, "customer_ids", "a customer appears once in a request")
			break
		}
		seen[id] = true
		in.CustomerIDs = append(in.CustomerIDs, id)
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	run, err := h.svc.CreateStatementRun(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, run)
}

func (h *Handler) HandleListStatementRuns(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "cursor", "limit", "include"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, statementRunCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := StatementRunFilter{Limit: page.Limit + 1}
	v := &httpx.Validator{}
	wantTotal := includeTotal(v, q)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
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
	items, err := h.svc.ListStatementRuns(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > page.Limit {
		items = items[:page.Limit]
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(statementRunCursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if wantTotal {
		n, err := h.svc.CountStatementRuns(r.Context())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(n))
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

func (h *Handler) HandleGetStatementRun(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "statement run")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	run, err := h.svc.GetStatementRun(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) HandleGetFinanceChargeRules(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rules, err := h.svc.GetFinanceChargeRules(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (h *Handler) HandleUpdateFinanceChargeRules(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body FinanceChargeRules
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rules, err := h.svc.UpdateFinanceChargeRules(r.Context(), &body, actor(r), callerRole(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// financeChargeRunBody is the body of POST /api/v1/ar/finance-charges/runs.
type financeChargeRunBody struct {
	AsOfDate    string   `json:"as_of_date"`
	DryRun      *bool    `json:"dry_run"`
	CustomerIDs []string `json:"customer_ids"`
}

func (h *Handler) HandleRunFinanceCharges(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var body financeChargeRunBody
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in := FinanceChargeRunIn{DryRun: body.DryRun == nil || *body.DryRun, Actor: actor(r), Role: callerRole(r)}
	v := &httpx.Validator{}
	for _, raw := range body.CustomerIDs {
		id, ok := v.UUID("customer_ids", &raw, true)
		if !ok {
			break
		}
		in.CustomerIDs = append(in.CustomerIDs, id)
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if raw := strings.TrimSpace(body.AsOfDate); raw != "" {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil || t.Format("2006-01-02") != raw {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "as_of_date", Message: "must be a date, YYYY-MM-DD"}))
			return
		}
		in.AsOf = t
	}
	run, err := h.svc.RunFinanceCharges(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) HandleListFinanceChargeRuns(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "cursor", "limit", "include"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, financeChargeRunCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := FinanceChargeRunFilter{Limit: page.Limit + 1}
	v := &httpx.Validator{}
	wantTotal := includeTotal(v, q)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
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
	items, err := h.svc.ListFinanceChargeRuns(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > page.Limit {
		items = items[:page.Limit]
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(financeChargeRunCursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if wantTotal {
		n, err := h.svc.CountFinanceChargeRuns(r.Context())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(n))
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

func (h *Handler) HandleGetFinanceChargeRun(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "finance charge run")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	run, err := h.svc.GetFinanceChargeRun(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}
