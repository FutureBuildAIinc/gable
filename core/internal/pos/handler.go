// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Handler handles the counter's routes on the wire contract: strict query
// parsing, DecodeJSON bodies, the error envelope, revision preconditions
// and list envelopes (the recipe's handler step).
type Handler struct {
	service *Service
	guard   BranchGuard // optional; see WithBranchGuard
}

// NewHandler creates the counter's handler.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// BranchGuard holds the record the resolution acts on to the caller's
// branch wall (ADR 0007 section 2.3). *middleware.BranchGuard satisfies it.
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// WithBranchGuard makes the resolution route hold the claim's sale to the
// caller's wall. The sale read's repository filter stops a caller whose
// context names a branch; the record check is what stops a bound caller
// with no context branch (default_branch_required=false, no header), whose
// grants the filter never sees. Without it that caller settles any branch's
// claim, so serve always sets it, and a handler mounted without it refuses
// (fail closed, the fifteenth review's P3-2) rather than settle a claim
// nothing holds.
func (h *Handler) WithBranchGuard(g BranchGuard) *Handler {
	h.guard = g
	return h
}

// saleOfIntentHeld holds the claim's sale to the caller's wall (r12 P1-2):
// a refusal is the same missing row the branch scoped read answers (404,
// nothing moved), so a caller outside the sale's branch learns nothing of
// the claim. It reports whether the request may proceed, and a handler
// wired with no guard at all refuses: the claim routes fail closed (the
// fifteenth review's P3-2).
func (h *Handler) saleOfIntentHeld(w http.ResponseWriter, r *http.Request, intentID uuid.UUID) bool {
	if h.guard == nil {
		httpx.WriteError(w, r, httpx.Unavailable(
			"the branch wall is not wired on this handler: a refund claim is settled only inside the caller's branch, so the route refuses rather than settle a claim nothing holds"))
		return false
	}
	branch, err := h.service.IntentSaleBranch(r.Context(), intentID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return false
	}
	if branch == uuid.Nil {
		return true
	}
	if err := h.guard.CheckPayloadBranch(r.Context(), branch); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httpx.WriteError(w, r, httpx.NotFound("refund claim not found"))
			return false
		}
		httpx.WriteError(w, r, fmt.Errorf("branch grant lookup: %w", err))
		return false
	}
	return true
}

// RegisterRoutes registers the counter's routes. roleGuard composes with
// the branch middleware in serve.go; resolveGuard, when passed as the second
// guard, is the resolution route's own (the finance and owner roles behind
// the branch middleware), and when absent the route's own finance and owner
// role check stands alone, as it does in tests that mount the branch
// middleware around the whole mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}
	resolveMount := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 1 && roleGuard[1] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[1](handler).ServeHTTP(w, r)
			}
		}
		return resolveGuard(handler)
	}

	// The sale lifecycle
	mux.HandleFunc("POST /api/v1/pos/transactions", guard(h.StartTransaction))
	mux.HandleFunc("GET /api/v1/pos/transactions", guard(h.ListTransactions))
	mux.HandleFunc("GET /api/v1/pos/transactions/{id}", guard(h.GetTransaction))
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/items", guard(h.AddItem))
	mux.HandleFunc("PATCH /api/v1/pos/transactions/{id}/items/{itemId}", guard(h.ChangeItemQuantity))
	mux.HandleFunc("DELETE /api/v1/pos/transactions/{id}/items/{itemId}", guard(h.RemoveItem))
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/complete", guard(h.CompleteTransaction))
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/void", guard(h.VoidTransaction))

	// History and search
	mux.HandleFunc("GET /api/v1/pos/products/search", guard(h.SearchProducts))
	mux.HandleFunc("GET /api/v1/pos/tiles", guard(h.ListTiles))

	// The counter's customer: the lookup with the account's standing, and
	// naming the customer (and the account's purchase order and job) on an
	// open sale.
	mux.HandleFunc("GET /api/v1/pos/customers", guard(h.FindCustomers))
	mux.HandleFunc("PUT /api/v1/pos/transactions/{id}/customer", guard(h.AttachCustomer))

	// The registers the caller may use (the counter opens on its yard's)
	mux.HandleFunc("GET /api/v1/pos/registers", guard(h.ListRegisters))

	// Offline sync
	mux.HandleFunc("POST /api/v1/pos/sync", guard(h.SyncOffline))
	mux.HandleFunc("GET /api/v1/pos/catalog", guard(h.GetCatalog))

	// Till sessions (drawer lifecycle)
	mux.HandleFunc("POST /api/v1/pos/till/open", guard(h.OpenTill))
	mux.HandleFunc("GET /api/v1/pos/till/current", guard(h.CurrentTill))
	mux.HandleFunc("GET /api/v1/pos/till/{id}/report", guard(h.TillReportHandler))
	mux.HandleFunc("POST /api/v1/pos/till/{id}/close", guard(h.CloseTill))
	mux.HandleFunc("GET /api/v1/pos/till/{id}/zreport", guard(h.GetZReport))
	mux.HandleFunc("GET /api/v1/pos/zreports", guard(h.ListZReports))
	mux.HandleFunc("POST /api/v1/pos/till/{id}/movements", guard(h.CreateMovement))
	mux.HandleFunc("GET /api/v1/pos/till/{id}/movements", guard(h.ListMovements))
	// The variance sign off (CP-08): the queue and the release are the
	// managers' (finance and owner, as the resolve route), the policy read is
	// the counter's own and its write the managers'.
	mux.HandleFunc("GET /api/v1/pos/till/signoffs", resolveMount(h.ListPendingSignoffs))
	mux.HandleFunc("POST /api/v1/pos/till/{id}/signoff", resolveMount(h.SignOffTill))
	mux.HandleFunc("GET /api/v1/pos/till/policy", guard(h.GetVariancePolicy))
	mux.HandleFunc("PUT /api/v1/pos/till/policy", resolveMount(h.PutVariancePolicy))

	// The end of day by branch (CP-09): the summary is the counter's to read,
	// the close and the deposit match are the managers' (finance and owner).
	mux.HandleFunc("GET /api/v1/pos/end-of-day", guard(h.GetBranchDay))
	mux.HandleFunc("POST /api/v1/pos/end-of-day/close", resolveMount(h.CloseBranchDay))
	mux.HandleFunc("GET /api/v1/pos/end-of-day/{id}/deposit-candidates", resolveMount(h.ListDepositCandidates))
	mux.HandleFunc("POST /api/v1/pos/end-of-day/{id}/deposit-match", resolveMount(h.MatchDeposit))

	// Returns
	mux.HandleFunc("POST /api/v1/pos/returns", guard(h.CreateReturn))
	mux.HandleFunc("GET /api/v1/pos/returns", guard(h.ListReturns))
	mux.HandleFunc("GET /api/v1/pos/returns/{id}", guard(h.GetReturn))

	// The operator resolution of a stranded refund claim (FROM-PRINCIPAL 41):
	// finance and owner only, and never a machine key, bound or not, refused
	// in the route itself. It carries no cashier identity and no client
	// revision: the operator's record is its own authority. It runs behind
	// the branch middleware like every money route of the counter (ninth
	// review P1-3): the claim's sale is read through the caller's branch
	// context, so another branch's claim is a missing row.
	mux.HandleFunc("POST /api/v1/pos/refund-intents/{id}/resolve", resolveMount(h.ResolveRefundIntent))

	// The payback of a declined void tender's credit (FROM-PRINCIPAL 60b):
	// the same mount, the same roles and the same wall as the resolve route,
	// a small sibling of it keyed on the void's own released claim.
	mux.HandleFunc("POST /api/v1/pos/refund-intents/{id}/payback", resolveMount(h.PaybackRefundIntent))
}

// resolveGuard wraps the resolution route: the finance and owner roles only
// (a machine key has no role and is refused before it).
func resolveGuard(handler http.HandlerFunc) http.HandlerFunc {
	return middleware.RequireRole("finance", "owner")(handler).ServeHTTP
}

// cashierRefusalBody is the wire error envelope for the one refusal this
// package writes itself; the shared writer handles every other.
type cashierRefusalBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

// refuseMachineKey answers the request with 403 when the caller
// authenticated with a machine key, reporting whether it answered: the key,
// bound or not, whatever scopes it holds, is refused on routes whose act
// belongs to a person.
func refuseMachineKey(w http.ResponseWriter, r *http.Request, message string) bool {
	keyID, isKey := middleware.KeyIDFromContext(r.Context())
	if !isKey {
		return false
	}
	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = r.Header.Get("X-Request-ID")
	}
	var body cashierRefusalBody
	body.Error.Code = "forbidden"
	body.Error.Message = message
	body.Meta.RequestID = reqID
	slog.Warn("machine key refused on a POS person-only route",
		"key_id", keyID, "status", http.StatusForbidden, "method", r.Method, "path", r.URL.Path, "request_id", reqID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(body)
	return true
}

// refuseMachineKeyCashier answers the request with 403 when the caller
// authenticated with a machine key, reporting whether it answered. The
// cashier on a counter mutation is a human user whose JWT names them.
func refuseMachineKeyCashier(w http.ResponseWriter, r *http.Request) bool {
	return refuseMachineKey(w, r, "a cashier must be a user")
}

// cashierOf resolves the acting cashier from the JWT, refusing a machine
// key.
func cashierOf(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if refuseMachineKeyCashier(w, r) {
		return uuid.Nil, false
	}
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil && claims.Subject != "" {
		if id, err := uuid.Parse(claims.Subject); err == nil {
			return id, true
		}
	}
	return uuid.Nil, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// StartTransaction is POST /pos/transactions.
func (h *Handler) StartTransaction(w http.ResponseWriter, r *http.Request) {
	cashierID, ok := cashierOf(w, r)
	if !ok {
		return
	}
	var req startSaleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if in.CashierID == uuid.Nil {
		in.CashierID = cashierID
	}
	sale, err := h.service.StartSale(r.Context(), in, in.CashierID.String())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pos/transactions/"+sale.ID.String())
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusCreated, sale)
}

// GetTransaction is GET /pos/transactions/{id}.
func (h *Handler) GetTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	sale, err := h.service.GetSale(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// AddItem is POST /pos/transactions/{id}/items.
func (h *Handler) AddItem(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req addLineRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	parsed, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// An item change carries no cashier identity of its own (ADR 0002
	// section 4): a key holding pos scopes works it; the acting subject, if
	// any, is the audit attribution.
	sale, err := h.service.AddLine(r.Context(), id, parsed, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// ChangeItemQuantity is PATCH /pos/transactions/{id}/items/{itemId}: the
// line's quantity on the sale's revision, repriced as the add prices it.
func (h *Handler) ChangeItemQuantity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	itemID, err := uuid.Parse(r.PathValue("itemId"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "itemId", Message: "must be a UUID"}))
		return
	}
	var req changeQuantityRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	qty, revision, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sale, err := h.service.ChangeLineQuantity(r.Context(), id, itemID, qty, r.Header.Get("If-Match"), revision, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// RemoveItem is DELETE /pos/transactions/{id}/items/{itemId}.
func (h *Handler) RemoveItem(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	itemID, err := uuid.Parse(r.PathValue("itemId"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "itemId", Message: "must be a UUID"}))
		return
	}
	sale, err := h.service.RemoveLine(r.Context(), id, itemID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// CompleteTransaction is POST /pos/transactions/{id}/complete: the tenders
// in cents, one transaction, everything booked or nothing.
func (h *Handler) CompleteTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req completeSaleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	tenders, pickedUpBy, revision, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sale, err := h.service.CompleteSale(r.Context(), id, r.Header.Get("If-Match"), revision, tenders, pickedUpBy, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// VoidTransaction is POST /pos/transactions/{id}/void.
func (h *Handler) VoidTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req voidSaleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	reason, revision, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sale, err := h.service.VoidSale(r.Context(), id, r.Header.Get("If-Match"), revision, reason, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// ListTransactions is GET /pos/transactions: the list envelope.
func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "register_id", "date", "status", "limit", "cursor", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := SaleFilter{RegisterID: q.Get("register_id"), Date: time.Now().Format("2006-01-02")}
	if d := q.Get("date"); d != "" {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "date", Message: "must be a date as YYYY-MM-DD"}))
			return
		}
		f.Date = d
	}
	if s := q.Get("status"); s != "" {
		switch s {
		case "open", "held", "completed", "voided":
			f.Status = uppercase(s)
		default:
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "status", Message: "must be one of: open, held, completed, voided"}))
			return
		}
	}
	limit := 50
	items, err := h.service.repo.ListSales(r.Context(), f, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, items, "", limit)
}

// SearchProducts is GET /pos/products/search: the counter's typeahead (the
// product module's data; a bare array, never null).
func (h *Handler) SearchProducts(w http.ResponseWriter, r *http.Request) {
	_, err := httpx.StrictQuery(r, "q", "limit")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusOK, []QuickSearchResult{})
		return
	}
	limit := 20
	results, err := h.service.repo.SearchProducts(r.Context(), query, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if results == nil {
		results = []QuickSearchResult{}
	}
	writeJSON(w, http.StatusOK, results)
}

// ListTiles is GET /pos/tiles: the counter's catalogue for the caller's yard,
// the category chips and the product tiles in top seller order (never null
// arrays). q is two characters or more; limit is 1 to 48, default 24.
func (h *Handler) ListTiles(w http.ResponseWriter, r *http.Request) {
	vals, err := httpx.StrictQuery(r, "category", "q", "limit")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := TileQuery{Category: vals.Get("category"), Q: vals.Get("q"), Limit: 24}
	var bad []httpx.FieldError
	if vals.Has("q") && len([]rune(q.Q)) < 2 {
		bad = append(bad, httpx.FieldError{Field: "q", Message: "must be two characters or more"})
	}
	if raw := vals.Get("limit"); vals.Has("limit") {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 1 || n > 48 {
			bad = append(bad, httpx.FieldError{Field: "limit", Message: "must be a whole number from 1 to 48"})
		} else {
			q.Limit = n
		}
	}
	if len(bad) > 0 {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", bad...))
		return
	}
	set, err := h.service.repo.CounterTiles(r.Context(), q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

// FindCustomers is GET /pos/customers: the counter's lookup by name or
// account number, each account's standing beside it. A bare array, never
// null; q under two characters answers [].
func (h *Handler) FindCustomers(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "q"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	found, err := h.service.FindCustomers(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, found)
}

// AttachCustomer is PUT /pos/transactions/{id}/customer: the sale's customer,
// purchase order and job. A null customer_id makes the sale a walk-in again.
func (h *Handler) AttachCustomer(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req attachCustomerRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sale, err := h.service.AttachCustomer(r.Context(), id, *in, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// SyncOffline is POST /pos/sync.
func (h *Handler) SyncOffline(w http.ResponseWriter, r *http.Request) {
	var req offlineSyncRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	batch, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resp, err := h.service.SyncOfflineTransactions(r.Context(), batch, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetCatalog is GET /pos/catalog: the offline cache (a bare array, never
// null).
func (h *Handler) GetCatalog(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	catalog, err := h.service.repo.GetProductCatalog(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if catalog == nil {
		catalog = []CatalogProduct{}
	}
	writeJSON(w, http.StatusOK, catalog)
}

// OpenTill is POST /pos/till/open.
func (h *Handler) OpenTill(w http.ResponseWriter, r *http.Request) {
	cashierID, ok := cashierOf(w, r)
	if !ok {
		return
	}
	var req openTillRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	register, cents, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	session, err := h.service.OpenTill(r.Context(), register, cashierID, cents, cashierID.String())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pos/till/"+session.ID.String()+"/report")
	writeJSON(w, http.StatusCreated, session)
}

// CurrentTill is GET /pos/till/current.
func (h *Handler) CurrentTill(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "register_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	register := q.Get("register_id")
	if register == "" {
		register = "REG-01"
	}
	session, err := h.service.CurrentTill(r.Context(), register)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if session == nil {
		httpx.WriteError(w, r, httpx.NotFound("no open till session for this register"))
		return
	}
	writeJSON(w, http.StatusOK, session)
}

// TillReportHandler is GET /pos/till/{id}/report.
func (h *Handler) TillReportHandler(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	report, err := h.service.TillReport(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// CloseTill is POST /pos/till/{id}/close.
func (h *Handler) CloseTill(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req closeTillRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	counted, notes, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// The close carries no cashier identity of its own either (ADR 0002
	// section 4): the counted drawer is posted by the acting subject.
	report, err := h.service.CloseTill(r.Context(), id, counted, notes, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// GetZReport is GET /pos/till/{id}/zreport.
func (h *Handler) GetZReport(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	z, err := h.service.GetZReport(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, z)
}

// ListZReports is GET /pos/zreports.
func (h *Handler) ListZReports(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "register_id", "date")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	date := time.Now()
	if d := q.Get("date"); d != "" {
		parsed, err := time.Parse("2006-01-02", d)
		if err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "date", Message: "must be a date as YYYY-MM-DD"}))
			return
		}
		date = parsed
	}
	list, err := h.service.ListZReports(r.Context(), q.Get("register_id"), date)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// CreateReturn is POST /pos/returns.
func (h *Handler) CreateReturn(w http.ResponseWriter, r *http.Request) {
	cashierID, ok := cashierOf(w, r)
	if !ok {
		return
	}
	var req returnRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ret, err := h.service.ReturnSale(r.Context(), cashierID, in, cashierID.String())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pos/returns/"+ret.ID.String())
	writeJSON(w, http.StatusCreated, ret)
}

// GetReturn is GET /pos/returns/{id}.
func (h *Handler) GetReturn(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	ret, err := h.service.GetReturn(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ret)
}

// ListReturns is GET /pos/returns.
func (h *Handler) ListReturns(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "register_id", "date")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := ReturnFilter{RegisterID: q.Get("register_id"), Date: time.Now().Format("2006-01-02")}
	if d := q.Get("date"); d != "" {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "date", Message: "must be a date as YYYY-MM-DD"}))
			return
		}
		f.Date = d
	}
	list, err := h.service.ListReturns(r.Context(), f)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// ResolveRefundIntent is POST /pos/refund-intents/{id}/resolve: the guarded
// admin route that records a stranded claim's actual outcome at the terminal
// and settles it. Finance and owner roles only; a machine key is refused
// here, bound or not, whatever scopes it holds. The claim's sale is held to
// the caller's branch wall before anything of it is read (r12 P1-2), so a
// caller outside the branch, header or not, finds a missing row.
func (h *Handler) ResolveRefundIntent(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a refund claim is resolved by a person, never a machine key") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	if !h.saleOfIntentHeld(w, r, id) {
		return
	}
	var req resolveIntentRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	resolved, err := h.service.ResolveRefundIntent(r.Context(), id, *in, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resolved)
}

// PaybackRefundIntent is POST /pos/refund-intents/{id}/payback: the guarded
// admin route that pays a declined void tender's credit back to its card
// through the counter terminal rail, the credit's one way back. Finance and
// owner roles only; a machine key is refused here, bound or not. The
// claim's sale is held to the caller's branch wall exactly as the resolve
// route holds it, so a caller outside the branch, header or not, finds a
// missing row and the terminal is never asked.
func (h *Handler) PaybackRefundIntent(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a held credit is paid back by a person, never a machine key") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	if !h.saleOfIntentHeld(w, r, id) {
		return
	}
	var req paybackIntentRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	paid, err := h.service.PaybackRefundIntent(r.Context(), id, *in, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, paid)
}

// actorFrom reads the acting user's subject for audit rows.
func actorFrom(r *http.Request) string {
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		return claims.Subject
	}
	return ""
}

func uppercase(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 'a' - 'A'
		}
	}
	return string(out)
}

// ListRegisters is GET /pos/registers: the registers the caller may use,
// each with its branch, ordered by branch then name, in the list envelope.
func (h *Handler) ListRegisters(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	found, err := h.service.ListRegisters(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, found, "", 200)
}
