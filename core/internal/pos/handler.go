// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"encoding/json"
	"log/slog"
	"net/http"
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
}

// NewHandler creates the counter's handler.
func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterRoutes registers the counter's routes. roleGuard composes with
// the branch middleware in serve.go.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	// The sale lifecycle
	mux.HandleFunc("POST /api/v1/pos/transactions", guard(h.StartTransaction))
	mux.HandleFunc("GET /api/v1/pos/transactions", guard(h.ListTransactions))
	mux.HandleFunc("GET /api/v1/pos/transactions/{id}", guard(h.GetTransaction))
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/items", guard(h.AddItem))
	mux.HandleFunc("DELETE /api/v1/pos/transactions/{id}/items/{itemId}", guard(h.RemoveItem))
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/complete", guard(h.CompleteTransaction))
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/void", guard(h.VoidTransaction))

	// History and search
	mux.HandleFunc("GET /api/v1/pos/products/search", guard(h.SearchProducts))

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

	// Returns
	mux.HandleFunc("POST /api/v1/pos/returns", guard(h.CreateReturn))
	mux.HandleFunc("GET /api/v1/pos/returns", guard(h.ListReturns))
	mux.HandleFunc("GET /api/v1/pos/returns/{id}", guard(h.GetReturn))
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

// refuseMachineKeyCashier answers the request with 403 when the caller
// authenticated with a machine key, reporting whether it answered. The
// cashier on a counter mutation is a human user whose JWT names them.
func refuseMachineKeyCashier(w http.ResponseWriter, r *http.Request) bool {
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
	body.Error.Message = "a cashier must be a user"
	body.Meta.RequestID = reqID
	slog.Warn("machine key refused on a POS cashier route",
		"key_id", keyID, "status", http.StatusForbidden, "method", r.Method, "path", r.URL.Path, "request_id", reqID)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(body)
	return true
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
	cashierID, ok := cashierOf(w, r)
	if !ok {
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
	sale, err := h.service.AddLine(r.Context(), id, parsed, cashierID.String())
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
	cashierID, ok := cashierOf(w, r)
	if !ok {
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
	report, err := h.service.CloseTill(r.Context(), id, counted, notes, cashierID.String())
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
