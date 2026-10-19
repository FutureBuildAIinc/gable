// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Handler exposes the GL REST endpoints on the ADR 0001 wire contract:
// integer cents, cursor pagination on the lists, revision plus If-Match on
// the mutable masters, and the branch wall on every read and write that
// names or returns a branch.
type Handler struct {
	svc   *Service
	guard *middleware.BranchGuard
}

// NewHandler creates a new GL Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// WithBranchGuard wires the branch guard that holds a named branch to the
// caller's reach.
func (h *Handler) WithBranchGuard(g *middleware.BranchGuard) *Handler {
	h.guard = g
	return h
}

// RegisterRoutes registers all GL routes on the mux.
// roleGuard protects write endpoints; pass middleware.RequireRole("admin","owner") in production.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	// Accounts
	mux.HandleFunc("GET /api/v1/gl/accounts", guard(h.HandleListAccounts))
	mux.HandleFunc("POST /api/v1/gl/accounts", guard(h.HandleCreateAccount))
	mux.HandleFunc("PUT /api/v1/gl/accounts/{id}", guard(h.HandleUpdateAccount))

	// Journal Entries
	mux.HandleFunc("GET /api/v1/gl/journal-entries", guard(h.HandleListJournalEntries))
	mux.HandleFunc("GET /api/v1/gl/journal-entries/{id}", guard(h.HandleGetJournalEntry))
	mux.HandleFunc("POST /api/v1/gl/journal-entries", guard(h.HandleCreateJournalEntry))
	mux.HandleFunc("POST /api/v1/gl/journal-entries/{id}/post", guard(h.HandlePostJournalEntry))
	mux.HandleFunc("POST /api/v1/gl/journal-entries/{id}/void", guard(h.HandleVoidJournalEntry))
	mux.HandleFunc("POST /api/v1/gl/journal-entries/{id}/reverse", guard(h.HandleReverseJournalEntry))

	// Trial Balance, statements, drill down, tie out
	mux.HandleFunc("GET /api/v1/gl/trial-balance", guard(h.HandleTrialBalance))
	mux.HandleFunc("GET /api/v1/gl/profit-and-loss", guard(h.HandleProfitAndLoss))
	mux.HandleFunc("GET /api/v1/gl/balance-sheet", guard(h.HandleBalanceSheet))
	mux.HandleFunc("GET /api/v1/gl/detail", guard(h.HandleGLDetail))
	mux.HandleFunc("GET /api/v1/gl/tie-out", guard(h.HandleTieOut))

	// Fiscal Periods and the year end close
	mux.HandleFunc("GET /api/v1/gl/fiscal-periods", guard(h.HandleListFiscalPeriods))
	mux.HandleFunc("POST /api/v1/gl/fiscal-periods/{id}/close", guard(h.HandleCloseFiscalPeriod))
	mux.HandleFunc("POST /api/v1/gl/fiscal-periods/{id}/reopen", guard(h.HandleReopenFiscalPeriod))
	mux.HandleFunc("POST /api/v1/gl/years/{year}/close", guard(h.HandleCloseYear))
}

// branchRead resolves a statement read's branch parameter against the
// caller's reach. A named branch is held to it. No branch named reads the
// request's branch header's branch when it has one (the platform's scope
// rule: the header names the yard a request is for), else it is the
// consolidated read, which needs every branch's reach: a caller held to some
// branches is refused rather than handed a silent partial sum.
func (h *Handler) branchRead(w http.ResponseWriter, r *http.Request, raw string) (*uuid.UUID, bool) {
	if raw == "" {
		if b := middleware.BranchIDForQuery(r.Context()); b != nil {
			return b, true
		}
		return nil, h.everyBranch(w, r, "a consolidated read")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("branch_id is not a uuid",
			httpx.FieldError{Field: "branch_id", Message: "must be a branch id"}))
		return nil, false
	}
	if !h.inReach(w, r, id) {
		return nil, false
	}
	return &id, true
}

// inReach holds a named branch to the caller's reach: a caller with a branch
// header may name only that branch (an administrator may name any), and
// without a header the caller's grants decide. A refusal is written.
func (h *Handler) inReach(w http.ResponseWriter, r *http.Request, id uuid.UUID) bool {
	ctx := r.Context()
	if bc := branchctx.FromContext(ctx); bc != nil && bc.BranchID != nil && *bc.BranchID != id && !bc.IsAdmin {
		h.writeGuardError(w, r, middleware.ErrPayloadBranchRefused)
		return false
	}
	if h.guard != nil {
		if err := h.guard.CheckGrantsBranch(ctx, &id); err != nil {
			h.writeGuardError(w, r, err)
			return false
		}
	}
	return true
}

// entryInReach loads the entry the path names and holds it to the caller's
// reach: another branch's entry reads as missing, as the by-id read always
// has.
func (h *Handler) entryInReach(w http.ResponseWriter, r *http.Request) (*JournalEntry, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid journal entry ID", http.StatusBadRequest, err)
		return nil, false
	}
	entry, err := h.svc.GetJournalEntry(r.Context(), id)
	if err != nil {
		h.writeActError(w, r, err)
		return nil, false
	}
	if b := middleware.BranchIDForQuery(r.Context()); b != nil && *b != entry.BranchID {
		httpx.WriteError(w, r, httpx.NotFound("journal entry not found"))
		return nil, false
	}
	if h.guard != nil {
		branch := entry.BranchID
		if err := h.guard.CheckGrantsBranch(r.Context(), &branch); err != nil {
			if errors.Is(err, middleware.ErrPayloadBranchRefused) {
				httpx.WriteError(w, r, httpx.NotFound("journal entry not found"))
			} else {
				httpx.WriteError(w, r, err)
			}
			return nil, false
		}
	}
	return entry, true
}

// everyBranch refuses a caller held to some branches an act that spans the
// company: a consolidated read, a period close, the year end. An
// administrator, an unbound caller and a caller outside the branch middleware
// have every branch's reach; a branch bound key or a user with grants does not.
func (h *Handler) everyBranch(w http.ResponseWriter, r *http.Request, act string) bool {
	bc := branchctx.FromContext(r.Context())
	if bc == nil || bc.IsAdmin || (bc.BranchID == nil && bc.UserSub == "") {
		return true
	}
	httpx.WriteError(w, r, &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden, Message: act + " needs every branch reach",
		Details: []httpx.FieldError{{Field: "branch_id", Message: "this act spans every branch; a caller held to some branches may not make it"}}})
	return false
}

// branchWrite holds a written branch to the caller's reach the same way.
func (h *Handler) branchWrite(w http.ResponseWriter, r *http.Request, id uuid.UUID) bool {
	if h.guard != nil {
		if err := h.guard.CheckPayloadBranch(r.Context(), id); err != nil {
			h.writeGuardError(w, r, err)
			return false
		}
	}
	return true
}

// writeGuardError maps the branch guard's refusal to its 403; a lookup fault
// stays the server's.
func (h *Handler) writeGuardError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		httpx.WriteError(w, r, &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: "the branch is outside your reach",
			Details: []httpx.FieldError{{Field: "branch_id", Message: "is not one of your branches"}}})
		return
	}
	httpx.WriteError(w, r, err)
}

// HandleReverseJournalEntry posts a net-zero reversal of a posted entry.
func (h *Handler) HandleReverseJournalEntry(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.entryInReach(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	revID, err := h.svc.ReverseJournalEntry(r.Context(), entry.ID, body.Reason)
	if err != nil {
		httputil.RespondError(w, r, "failed to reverse journal entry", http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"reversal_entry_id": revID})
}

// --- Account Handlers ---

const accountsCursorScope = "gl_accounts"

func (h *Handler) HandleListAccounts(w http.ResponseWriter, r *http.Request) {
	page, err := httpx.ParseListQuery(r, accountsCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	after := ""
	if len(page.Key) > 0 {
		after = page.Key[0]
	}
	accounts, err := h.svc.ListAccountsPage(r.Context(), page.Limit+1, after)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(accounts) > page.Limit {
		accounts = accounts[:page.Limit]
		next, err = httpx.MintCursor(accountsCursorScope, accounts[len(accounts)-1].Code)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, accounts, next, page.Limit)
}

type createAccountRequest struct {
	Code          string     `json:"code"`
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	Subtype       string     `json:"subtype"`
	ParentID      *uuid.UUID `json:"parent_id,omitempty"`
	NormalBalance string     `json:"normal_balance"`
	Description   string     `json:"description"`
	Revision      *int64     `json:"revision"`
}

func (h *Handler) HandleCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !h.everyBranch(w, r, "creating a GL account") {
		return
	}
	var req createAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "invalid request body", http.StatusBadRequest, err)
		return
	}

	acct := &GLAccount{
		Code:          req.Code,
		Name:          req.Name,
		Type:          req.Type,
		Subtype:       req.Subtype,
		ParentID:      req.ParentID,
		NormalBalance: req.NormalBalance,
		Description:   req.Description,
	}

	if err := h.svc.CreateAccount(r.Context(), acct); err != nil {
		httputil.RespondError(w, r, "failed to create GL account", http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(acct)
}

func (h *Handler) HandleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	if !h.everyBranch(w, r, "updating a GL account") {
		return
	}
	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "invalid account ID", http.StatusBadRequest, err)
		return
	}

	current, err := h.svc.GetAccount(r.Context(), id)
	if err != nil {
		h.writeActError(w, r, err)
		return
	}
	var req createAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "invalid request body", http.StatusBadRequest, err)
		return
	}
	if err := httpx.CheckRevision(current.Revision, r.Header.Get("If-Match"), req.Revision); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	acct := &GLAccount{
		ID:            id,
		Code:          req.Code,
		Name:          req.Name,
		Type:          req.Type,
		Subtype:       req.Subtype,
		ParentID:      req.ParentID,
		NormalBalance: req.NormalBalance,
		Description:   req.Description,
		IsActive:      current.IsActive,
		Balance:       current.Balance,
		Revision:      current.Revision,
		CreatedAt:     current.CreatedAt,
	}

	if err := h.svc.UpdateAccount(r.Context(), acct); err != nil {
		if errors.Is(err, ErrStaleRevision) {
			httpx.WriteError(w, r, httpx.StaleRevision("the account was changed after this revision was read; reload and retry"))
			return
		}
		httputil.RespondError(w, r, "failed to update GL account", http.StatusBadRequest, err)
		return
	}

	httpx.WriteRevisionETag(w, acct.Revision)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(acct)
}

// --- Journal Entry Handlers ---

const entriesCursorScope = "gl_journal_entries"

func (h *Handler) HandleListJournalEntries(w http.ResponseWriter, r *http.Request) {
	page, err := httpx.ParseListQuery(r, entriesCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	branch, ok := h.branchRead(w, r, r.URL.Query().Get("branch_id"))
	if !ok {
		return
	}
	before := 0
	if len(page.Key) > 0 {
		before, err = strconv.Atoi(page.Key[0])
		if err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("cursor is malformed",
				httpx.FieldError{Field: "cursor", Message: "was not minted for this ordering"}))
			return
		}
	}
	entries, err := h.svc.ListJournalEntriesPage(r.Context(), page.Limit+1, before, branch)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(entries) > page.Limit {
		entries = entries[:page.Limit]
		next, err = httpx.MintCursor(entriesCursorScope, strconv.Itoa(entries[len(entries)-1].EntryNumber))
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, entries, next, page.Limit)
}

func (h *Handler) HandleGetJournalEntry(w http.ResponseWriter, r *http.Request) {
	// The by-id read is walled like the list: a caller held to one branch
	// finds another branch's entry missing rather than read.
	entry, ok := h.entryInReach(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry)
}

type createJournalEntryRequest struct {
	EntryDate string           `json:"entry_date"` // YYYY-MM-DD
	Memo      string           `json:"memo"`
	BranchID  string           `json:"branch_id"`
	Lines     []journalLineReq `json:"lines"`
}

type journalLineReq struct {
	AccountID   string `json:"account_id"`
	Description string `json:"description"`
	DebitCents  int64  `json:"debit_cents"`
	CreditCents int64  `json:"credit_cents"`
}

func (h *Handler) HandleCreateJournalEntry(w http.ResponseWriter, r *http.Request) {
	var req createJournalEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "invalid request body", http.StatusBadRequest, err)
		return
	}

	branchID, err := uuid.Parse(req.BranchID)
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("a manual journal entry names its branch",
			httpx.FieldError{Field: "branch_id", Message: "must be a branch id"}))
		return
	}
	if !h.branchWrite(w, r, branchID) {
		return
	}

	entryDate, err := h.svc.BusinessDay(r.Context(), &branchID)
	if err != nil {
		httputil.RespondError(w, r, "failed to read the business day", http.StatusInternalServerError, err)
		return
	}
	if req.EntryDate != "" {
		parsed, err := time.Parse("2006-01-02", req.EntryDate)
		if err != nil {
			httputil.RespondError(w, r, "invalid entry_date format (expected YYYY-MM-DD)", http.StatusBadRequest, err)
			return
		}
		entryDate = parsed
	}

	var lines []JournalLine
	for _, lr := range req.Lines {
		if lr.DebitCents < 0 || lr.CreditCents < 0 {
			httpx.WriteError(w, r, httpx.BadRequest("debit and credit are cents and never negative",
				httpx.FieldError{Field: "lines", Message: "debit_cents and credit_cents must be >= 0"}))
			return
		}
		accountID, err := uuid.Parse(lr.AccountID)
		if err != nil {
			httputil.RespondError(w, r, "invalid account_id: "+lr.AccountID, http.StatusBadRequest, err)
			return
		}
		lines = append(lines, JournalLine{
			AccountID:   accountID,
			Description: lr.Description,
			Debit:       lr.DebitCents,
			Credit:      lr.CreditCents,
		})
	}

	entry := &JournalEntry{
		EntryDate: entryDate,
		Memo:      req.Memo,
		Source:    SourceManual,
		BranchID:  branchID,
		PostedBy:  actor.FromContext(r.Context()).ID,
		Status:    StatusDraft,
		Lines:     lines,
	}

	if err := h.svc.CreateManualJournalEntry(r.Context(), entry); err != nil {
		if errors.Is(err, ErrNoBranch) {
			httpx.WriteError(w, r, httpx.BadRequest("a manual journal entry names its branch",
				httpx.FieldError{Field: "branch_id", Message: "is required"}))
			return
		}
		httputil.RespondError(w, r, "failed to create journal entry", http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(entry)
}

func (h *Handler) HandlePostJournalEntry(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.entryInReach(w, r)
	if !ok {
		return
	}

	if err := h.svc.PostJournalEntry(r.Context(), entry.ID); err != nil {
		httputil.RespondError(w, r, "failed to post journal entry", http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "posted"})
}

func (h *Handler) HandleVoidJournalEntry(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.entryInReach(w, r)
	if !ok {
		return
	}

	if err := h.svc.VoidJournalEntry(r.Context(), entry.ID); err != nil {
		httputil.RespondError(w, r, "failed to void journal entry", http.StatusBadRequest, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "voided"})
}

// includeInterBranchCtx answers the request's context with the
// include_inter_branch arm settled on it: true or 1 keeps the 1095
// Inter-Branch Clearing account on a consolidated statement.
func includeInterBranchCtx(r *http.Request) context.Context {
	if v := r.URL.Query().Get("include_inter_branch"); v == "true" || v == "1" {
		return WithIncludeInterBranch(r.Context(), true)
	}
	return r.Context()
}

// --- Trial Balance ---

func (h *Handler) HandleTrialBalance(w http.ResponseWriter, r *http.Request) {
	branch, ok := h.branchRead(w, r, r.URL.Query().Get("branch_id"))
	if !ok {
		return
	}
	asOf := time.Time{}
	if asOfStr := r.URL.Query().Get("as_of"); asOfStr != "" {
		parsed, err := time.Parse("2006-01-02", asOfStr)
		if err != nil {
			httputil.RespondError(w, r, "invalid as_of date (expected YYYY-MM-DD)", http.StatusBadRequest, err)
			return
		}
		asOf = parsed
	}

	currency, ok := currencyParam(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.GetTrialBalanceFor(includeInterBranchCtx(r), asOf, currency, branch)
	if err != nil {
		httputil.RespondError(w, r, "failed to get trial balance", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rows)
}

// --- Financial Statements ---

// HandleProfitAndLoss serves GET /api/v1/gl/profit-and-loss?start=&end=.
// Both parameters are YYYY-MM-DD and inclusive; start defaults to the first of
// the current month and end to today. compare_start and compare_end carry
// the previous period the row asks to sit beside the current one.
func (h *Handler) HandleProfitAndLoss(w http.ResponseWriter, r *http.Request) {
	branch, ok := h.branchRead(w, r, r.URL.Query().Get("branch_id"))
	if !ok {
		return
	}
	now, err := h.svc.BusinessDay(r.Context(), branch)
	if err != nil {
		httputil.RespondError(w, r, "failed to read the business day", http.StatusInternalServerError, err)
		return
	}
	startStr := r.URL.Query().Get("start")
	if startStr == "" {
		startStr = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	}
	endStr := r.URL.Query().Get("end")
	if endStr == "" {
		endStr = now.Format("2006-01-02")
	}

	// Validate here so that a malformed range answers 400 and anything the
	// service reports afterwards is a genuine server fault (500). The service
	// re-checks both: it is a public method with other potential callers.
	start, err := time.Parse("2006-01-02", startStr)
	if err != nil {
		httputil.RespondError(w, r, "invalid start date (expected YYYY-MM-DD)", http.StatusBadRequest, err)
		return
	}
	end, err := time.Parse("2006-01-02", endStr)
	if err != nil {
		httputil.RespondError(w, r, "invalid end date (expected YYYY-MM-DD)", http.StatusBadRequest, err)
		return
	}
	if end.Before(start) {
		httputil.RespondError(w, r, "end date precedes start date", http.StatusBadRequest, nil)
		return
	}

	currency, ok := currencyParam(w, r)
	if !ok {
		return
	}
	compareStart, compareEnd := r.URL.Query().Get("compare_start"), r.URL.Query().Get("compare_end")
	if compareStart != "" || compareEnd != "" {
		cs, errS := time.Parse("2006-01-02", compareStart)
		ce, errE := time.Parse("2006-01-02", compareEnd)
		if errS != nil || errE != nil || ce.Before(cs) {
			httpx.WriteError(w, r, httpx.BadRequest("the comparison period needs a valid compare_start and compare_end",
				httpx.FieldError{Field: "compare_start", Message: "both bounds are YYYY-MM-DD and the end is not before the start"}))
			return
		}
	}
	report, err := h.svc.GetProfitAndLossFor(includeInterBranchCtx(r), startStr, endStr, currency, branch, compareStart, compareEnd)
	if err != nil {
		httputil.RespondError(w, r, "failed to get profit and loss report", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// HandleBalanceSheet serves GET /api/v1/gl/balance-sheet?as_of=.
// as_of is YYYY-MM-DD and inclusive; it defaults to the branch's business day.
func (h *Handler) HandleBalanceSheet(w http.ResponseWriter, r *http.Request) {
	branch, ok := h.branchRead(w, r, r.URL.Query().Get("branch_id"))
	if !ok {
		return
	}
	asOfStr := r.URL.Query().Get("as_of")
	if asOfStr == "" {
		day, err := h.svc.BusinessDay(r.Context(), branch)
		if err != nil {
			httputil.RespondError(w, r, "failed to read the business day", http.StatusInternalServerError, err)
			return
		}
		asOfStr = day.Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", asOfStr); err != nil {
		httputil.RespondError(w, r, "invalid as_of date (expected YYYY-MM-DD)", http.StatusBadRequest, err)
		return
	}

	currency, ok := currencyParam(w, r)
	if !ok {
		return
	}
	report, err := h.svc.GetBalanceSheetFor(includeInterBranchCtx(r), asOfStr, currency, branch)
	if err != nil {
		httputil.RespondError(w, r, "failed to get balance sheet report", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// HandleGLDetail serves the drill down from a statement row:
// GET /api/v1/gl/detail?account_id=&from=&to=&branch_id=, the posted lines of
// one account over a date window, one branch's or every branch's.
func (h *Handler) HandleGLDetail(w http.ResponseWriter, r *http.Request) {
	branch, ok := h.branchRead(w, r, r.URL.Query().Get("branch_id"))
	if !ok {
		return
	}
	accountID, err := uuid.Parse(r.URL.Query().Get("account_id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("account_id is required",
			httpx.FieldError{Field: "account_id", Message: "must be an account id"}))
		return
	}
	from, err := time.Parse("2006-01-02", r.URL.Query().Get("from"))
	if err != nil {
		httputil.RespondError(w, r, "invalid from date (expected YYYY-MM-DD)", http.StatusBadRequest, err)
		return
	}
	to, err := time.Parse("2006-01-02", r.URL.Query().Get("to"))
	if err != nil {
		httputil.RespondError(w, r, "invalid to date (expected YYYY-MM-DD)", http.StatusBadRequest, err)
		return
	}
	currency, ok := currencyParam(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.GetGLDetail(r.Context(), accountID, from, to, branch, currency)
	if err != nil {
		httputil.RespondError(w, r, "failed to get gl detail", http.StatusInternalServerError, err)
		return
	}
	if rows == nil {
		rows = []GLDetailRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rows)
}

// HandleTieOut serves GET /api/v1/gl/tie-out?as_of=&currency=&branch_id=: the
// AR and AP control accounts against their subledgers, differences in cents.
func (h *Handler) HandleTieOut(w http.ResponseWriter, r *http.Request) {
	branch, ok := h.branchRead(w, r, r.URL.Query().Get("branch_id"))
	if !ok {
		return
	}
	asOf := time.Time{}
	if s := r.URL.Query().Get("as_of"); s != "" {
		parsed, err := time.Parse("2006-01-02", s)
		if err != nil {
			httputil.RespondError(w, r, "invalid as_of date (expected YYYY-MM-DD)", http.StatusBadRequest, err)
			return
		}
		asOf = parsed
	}
	currency, ok := currencyParam(w, r)
	if !ok {
		return
	}
	report, err := h.svc.TieOut(r.Context(), asOf, currency, branch)
	if errors.Is(err, ErrTieOutHistoric) {
		httpx.WriteError(w, r, httpx.BadRequest("the tie out compares current balances",
			httpx.FieldError{Field: "as_of", Message: "must be the business day or later"}))
		return
	}
	if err != nil {
		httputil.RespondError(w, r, "failed to tie out", http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// --- Fiscal Periods ---

const periodsCursorScope = "gl_fiscal_periods"

func (h *Handler) HandleListFiscalPeriods(w http.ResponseWriter, r *http.Request) {
	page, err := httpx.ParseListQuery(r, periodsCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var after time.Time
	if len(page.Key) > 0 {
		after, err = time.Parse("2006-01-02", page.Key[0])
		if err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("cursor is malformed",
				httpx.FieldError{Field: "cursor", Message: "was not minted for this ordering"}))
			return
		}
	}
	periods, err := h.svc.ListFiscalPeriodsPage(r.Context(), page.Limit+1, after)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(periods) > page.Limit {
		periods = periods[:page.Limit]
		next, err = httpx.MintCursor(periodsCursorScope, periods[len(periods)-1].StartDate.Format("2006-01-02"))
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, periods, next, page.Limit)
}

func (h *Handler) HandleCloseFiscalPeriod(w http.ResponseWriter, r *http.Request) {
	if !h.everyBranch(w, r, "closing or reopening a fiscal period") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid fiscal period ID", http.StatusBadRequest, err)
		return
	}
	period, err := h.svc.GetFiscalPeriod(r.Context(), id)
	if err != nil {
		h.writeActError(w, r, err)
		return
	}
	if err := httpx.CheckRevision(period.Revision, r.Header.Get("If-Match"), nil); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	closed, err := h.svc.CloseFiscalPeriodFor(r.Context(), id, period.Revision, actorOf(r))
	if err != nil {
		h.writeActError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, closed.Revision)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(closed)
}

func (h *Handler) HandleReopenFiscalPeriod(w http.ResponseWriter, r *http.Request) {
	if !h.everyBranch(w, r, "closing or reopening a fiscal period") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid fiscal period ID", http.StatusBadRequest, err)
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Reason == "" {
		httpx.WriteError(w, r, httpx.BadRequest("a period is reopened with a reason",
			httpx.FieldError{Field: "reason", Message: "is required to reopen a fiscal period"}))
		return
	}
	period, err := h.svc.GetFiscalPeriod(r.Context(), id)
	if err != nil {
		h.writeActError(w, r, err)
		return
	}
	if err := httpx.CheckRevision(period.Revision, r.Header.Get("If-Match"), nil); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	reopened, err := h.svc.ReopenFiscalPeriodFor(r.Context(), id, period.Revision, body.Reason, actorOf(r))
	if err != nil {
		h.writeActError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, reopened.Revision)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(reopened)
}

// HandleCloseYear zeroes the year's revenue and expense accounts into
// Retained Earnings, per branch.
func (h *Handler) HandleCloseYear(w http.ResponseWriter, r *http.Request) {
	if !h.everyBranch(w, r, "the year end close") {
		return
	}
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil {
		httputil.RespondError(w, r, "invalid year", http.StatusBadRequest, err)
		return
	}
	out, err := h.svc.CloseYear(r.Context(), year, r.URL.Query().Get("currency"), actorOf(r))
	if err != nil {
		h.writeActError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// writeActError maps the service refusals of the period and year end acts to
// their wire shapes; anything else is the server's fault.
func (h *Handler) writeActError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrStaleRevision):
		httpx.WriteError(w, r, httpx.StaleRevision("the period was changed after this revision was read; reload and retry"))
	case errors.Is(err, ErrReopenReason):
		httpx.WriteError(w, r, httpx.BadRequest("a period is reopened with a reason",
			httpx.FieldError{Field: "reason", Message: "is required to reopen a fiscal period"}))
	case errors.Is(err, ErrPeriodNotFound):
		httpx.WriteError(w, r, httpx.NotFound("fiscal period not found"))
	case errors.Is(err, ErrEntryNotFound):
		httpx.WriteError(w, r, httpx.NotFound("journal entry not found"))
	case errors.Is(err, ErrAccountNotFound):
		httpx.WriteError(w, r, httpx.NotFound("account not found"))
	case errors.Is(err, ErrPeriodState):
		httpx.WriteError(w, r, httpx.InvalidStateTransition("the fiscal period is not in the state this act needs",
			httpx.Blocker("period_state", "close acts on an open period and reopen on a closed one")))
	case errors.Is(err, ErrPeriodClosed):
		httpx.WriteError(w, r, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
			Message: "the year end close would post into a closed fiscal period",
			Details: []httpx.FieldError{httpx.Blocker("period_closed", "reopen the period with a reason, close the year, then close it again")}})
	default:
		httputil.RespondError(w, r, "the act failed", http.StatusInternalServerError, err)
	}
}

// actorOf answers the caller's actor for the audit rows.
func actorOf(r *http.Request) string {
	if id := actor.FromContext(r.Context()).ID; id != "" {
		return id
	}
	return "system"
}

// currencyParam reads the optional currency parameter of the reports: three
// capital letters, else a 400. Empty means the report's default (every
// currency on the trial balance, the dealer default on a statement).
func currencyParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	c := r.URL.Query().Get("currency")
	if c == "" {
		return "", true
	}
	if len(c) != 3 || strings.ToUpper(c) != c || strings.Trim(c, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		httputil.RespondError(w, r, "invalid currency (expected three capital letters)", http.StatusBadRequest, nil)
		return "", false
	}
	return c, true
}
