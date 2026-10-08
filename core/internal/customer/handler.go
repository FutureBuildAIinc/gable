// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Cursor scopes name each list's ordering: created_at then id, newest first.
// A cursor minted for any other ordering is refused (ADR 0001 section 2).
const (
	customersScope    = "customers.created_at_id_desc"
	shipTosScope      = "ship_tos.created_at_id_desc"
	contactsScope     = "contacts.created_at_id_desc"
	termsScope        = "payment_terms.created_at_id_desc"
	priceLevelsScope  = "price_levels.created_at_id_desc"
	maxSearchTermSize = 100
)

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

	mux.HandleFunc("GET /api/v1/customers", guard(h.HandleListCustomers))
	mux.HandleFunc("POST /api/v1/customers", guard(h.HandleCreateCustomer))
	mux.HandleFunc("GET /api/v1/customers/{id}", guard(h.HandleGetCustomer))
	mux.HandleFunc("PUT /api/v1/customers/{id}", guard(h.HandleUpdateCustomer))
	mux.HandleFunc("PATCH /api/v1/customers/{id}/salesperson", guard(h.HandleUpdateSalesperson))
	mux.HandleFunc("GET /api/v1/customers/{id}/escalation-policy", guard(h.HandleGetEscalationPolicy))
	mux.HandleFunc("PUT /api/v1/customers/{id}/escalation-policy", guard(h.HandleSetEscalationPolicy))
	mux.HandleFunc("GET /api/v1/price_levels", guard(h.HandleListPriceLevels))

	mux.HandleFunc("GET /api/v1/customers/{id}/ship-tos", guard(h.HandleListShipTos))
	mux.HandleFunc("POST /api/v1/customers/{id}/ship-tos", guard(h.HandleCreateShipTo))
	mux.HandleFunc("GET /api/v1/ship-tos/{id}", guard(h.HandleGetShipTo))
	mux.HandleFunc("PUT /api/v1/ship-tos/{id}", guard(h.HandleUpdateShipTo))

	mux.HandleFunc("GET /api/v1/payment-terms", guard(h.HandleListTerms))
	mux.HandleFunc("POST /api/v1/payment-terms", guard(h.HandleCreateTerms))
	mux.HandleFunc("GET /api/v1/payment-terms/{id}", guard(h.HandleGetTerms))
	mux.HandleFunc("PUT /api/v1/payment-terms/{id}", guard(h.HandleUpdateTerms))

	mux.HandleFunc("GET /api/v1/customers/{customerId}/contacts", guard(h.HandleListContacts))
	mux.HandleFunc("POST /api/v1/customers/{customerId}/contacts", guard(h.HandleCreateContact))
	mux.HandleFunc("GET /api/v1/contacts/{id}", guard(h.HandleGetContact))
	mux.HandleFunc("PUT /api/v1/contacts/{id}", guard(h.HandleUpdateContact))
	mux.HandleFunc("DELETE /api/v1/contacts/{id}", guard(h.HandleDeleteContact))
}

// writeJSON writes a 2xx JSON body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// pathID reads a UUID path value; a malformed one is a 400 naming it.
func pathID(r *http.Request, name, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid "+what+" id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

// noQuery refuses every query parameter: these routes declare none.
func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

// precondition reads the client's revision: the If-Match header and the
// body's revision, both optional here; the service refuses a write with
// neither (428) and one whose revision is behind (409).
func precondition(r *http.Request, body *int64) Precondition {
	return Precondition{IfMatch: r.Header.Get("If-Match"), Revision: body}
}

func writeCustomer(w http.ResponseWriter, status int, c *Customer) {
	httpx.WriteRevisionETag(w, c.Revision)
	writeJSON(w, status, c)
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

// listParams reads the platform's list parameters (cursor, limit, include)
// and the keyset position; the caller has already run StrictQuery.
func listParams(r *http.Request, scope string, q map[string][]string) (limit int, afterTime *httpx.Timestamp, afterID uuid.UUID, wantTotal bool, err error) {
	page, err := httpx.ParseListQuery(r, scope)
	if err != nil {
		return 0, nil, uuid.Nil, false, err
	}
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			return 0, nil, uuid.Nil, false, fieldProblem("include", "parameter is repeated")
		}
		set, ierr := httpx.ParseInclude(vals[0])
		if ierr != nil {
			return 0, nil, uuid.Nil, false, ierr
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return 0, nil, uuid.Nil, false, cursorError()
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return 0, nil, uuid.Nil, false, cursorError()
		}
		ts := httpx.TimestampOf(at)
		afterTime, afterID = &ts, id
	}
	return page.Limit, afterTime, afterID, wantTotal, nil
}

// singleValue returns a parameter's one value; a repeated one is recorded as
// a field problem.
func singleValue(v *httpx.Validator, q map[string][]string, name string) (string, bool) {
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

func boolParam(v *httpx.Validator, q map[string][]string, name string) *bool {
	s, ok := singleValue(v, q, name)
	if !ok {
		return nil
	}
	switch s {
	case "true":
		b := true
		return &b
	case "false":
		b := false
		return &b
	}
	v.Check(false, name, "must be true or false")
	return nil
}

func nextCursor(scope string, hasMore bool, created httpx.Timestamp, id uuid.UUID) (string, error) {
	if !hasMore {
		return "", nil
	}
	return httpx.MintCursor(scope, httpx.FormatKeyTime(created.Time), id.String())
}

func listOptions(total *int64) []httpx.ListOption {
	if total == nil {
		return nil
	}
	return []httpx.ListOption{httpx.WithTotal(*total)}
}

// ---- customers ----

func parseCustomerFilter(r *http.Request) (f ListFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "q", "tier", "is_active", "salesperson_id")
	if err != nil {
		return f, false, err
	}
	limit, at, id, wantTotal, err := listParams(r, customersScope, q)
	if err != nil {
		return f, false, err
	}
	f.Limit, f.AfterID = limit, id
	if at != nil {
		t := at.Time
		f.AfterTime = &t
	}

	v := &httpx.Validator{}
	if s, ok := singleValue(v, q, "q"); ok {
		s = strings.TrimSpace(s)
		v.Check(len(s) <= maxSearchTermSize, "q", "must be at most 100 characters")
		f.Query = s
	}
	if s, ok := singleValue(v, q, "tier"); ok {
		if t, ok := ParseTier(s); ok {
			f.Tier = &t
		} else {
			v.Check(false, "tier", "must be one of: "+strings.Join(tierNames, ", "))
		}
	}
	f.IsActive = boolParam(v, q, "is_active")
	if s, ok := singleValue(v, q, "salesperson_id"); ok {
		if sid, ok := v.UUID("salesperson_id", &s, true); ok {
			f.SalespersonID = &sid
		}
	}
	if err := v.Err(); err != nil {
		return f, false, err
	}
	return f, wantTotal, nil
}

func (h *Handler) HandleListCustomers(w http.ResponseWriter, r *http.Request) {
	f, wantTotal, err := parseCustomerFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.ListCustomers(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = nextCursor(customersScope, hasMore, last.CreatedAt, last.ID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, f.Limit, listOptions(total)...)
}

func (h *Handler) HandleGetCustomer(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCustomer(w, http.StatusOK, c)
}

func (h *Handler) HandleCreateCustomer(w http.ResponseWriter, r *http.Request) {
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
	c, err := h.service.Create(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/customers/"+c.ID.String())
	writeCustomer(w, http.StatusCreated, c)
}

func (h *Handler) HandleUpdateCustomer(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "customer")
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
	c, err := h.service.Update(r.Context(), id, draft, precondition(r, draft.Revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCustomer(w, http.StatusOK, c)
}

func (h *Handler) HandleUpdateSalesperson(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req SalespersonRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.SetSalesperson(r.Context(), id, draft, precondition(r, draft.Revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCustomer(w, http.StatusOK, c)
}

// ---- escalation policy ----

func writePolicy(w http.ResponseWriter, status int, p *EscalationPolicy) {
	httpx.WriteRevisionETag(w, p.Revision)
	writeJSON(w, status, p)
}

func (h *Handler) HandleGetEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.GetEscalationPolicy(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writePolicy(w, http.StatusOK, p)
}

func (h *Handler) HandleSetEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req PolicyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.SetEscalationPolicy(r.Context(), id, draft, precondition(r, draft.Revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writePolicy(w, http.StatusOK, p)
}

// ---- price levels ----

func (h *Handler) HandleListPriceLevels(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit, at, id, wantTotal, err := listParams(r, priceLevelsScope, q)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := ChildFilter{Limit: limit, AfterID: id}
	if at != nil {
		t := at.Time
		f.AfterTime = &t
	}
	items, hasMore, total, err := h.service.ListPriceLevels(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = nextCursor(priceLevelsScope, hasMore, last.CreatedAt, last.ID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, limit, listOptions(total)...)
}

// ---- child lists ----

// parseChildFilter reads a child list's parameters: the platform three, the
// is_active filter, and any extra names the route declares.
func parseChildFilter(r *http.Request, scope string, extra ...string) (f ChildFilter, q map[string][]string, wantTotal bool, err error) {
	allowed := append([]string{"cursor", "limit", "include", "is_active"}, extra...)
	q, err = httpx.StrictQuery(r, allowed...)
	if err != nil {
		return f, nil, false, err
	}
	limit, at, id, wantTotal, err := listParams(r, scope, q)
	if err != nil {
		return f, nil, false, err
	}
	f.Limit, f.AfterID = limit, id
	if at != nil {
		t := at.Time
		f.AfterTime = &t
	}
	v := &httpx.Validator{}
	f.IsActive = boolParam(v, q, "is_active")
	if err := v.Err(); err != nil {
		return f, nil, false, err
	}
	return f, q, wantTotal, nil
}

// ---- ship-tos ----

func writeShipTo(w http.ResponseWriter, status int, s *ShipTo) {
	httpx.WriteRevisionETag(w, s.Revision)
	writeJSON(w, status, s)
}

func (h *Handler) HandleListShipTos(w http.ResponseWriter, r *http.Request) {
	customerID, err := pathID(r, "id", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f, _, wantTotal, err := parseChildFilter(r, shipTosScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.ListShipTos(r.Context(), customerID, f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = nextCursor(shipTosScope, hasMore, last.CreatedAt, last.ID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, f.Limit, listOptions(total)...)
}

func (h *Handler) HandleCreateShipTo(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	customerID, err := pathID(r, "id", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ShipToRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s, err := h.service.CreateShipTo(r.Context(), customerID, draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/ship-tos/"+s.ID.String())
	writeShipTo(w, http.StatusCreated, s)
}

func (h *Handler) HandleGetShipTo(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "ship-to")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s, err := h.service.GetShipTo(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeShipTo(w, http.StatusOK, s)
}

func (h *Handler) HandleUpdateShipTo(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "ship-to")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ShipToRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s, err := h.service.UpdateShipTo(r.Context(), id, draft, precondition(r, draft.Revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeShipTo(w, http.StatusOK, s)
}

// ---- contacts ----

func writeContact(w http.ResponseWriter, status int, c *Contact) {
	httpx.WriteRevisionETag(w, c.Revision)
	writeJSON(w, status, c)
}

func (h *Handler) HandleListContacts(w http.ResponseWriter, r *http.Request) {
	customerID, err := pathID(r, "customerId", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f, _, wantTotal, err := parseChildFilter(r, contactsScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.ListContacts(r.Context(), customerID, f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = nextCursor(contactsScope, hasMore, last.CreatedAt, last.ID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, f.Limit, listOptions(total)...)
}

func (h *Handler) HandleCreateContact(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	customerID, err := pathID(r, "customerId", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ContactRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.CreateContact(r.Context(), customerID, draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/contacts/"+c.ID.String())
	writeContact(w, http.StatusCreated, c)
}

func (h *Handler) HandleGetContact(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "contact")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.GetContact(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeContact(w, http.StatusOK, c)
}

func (h *Handler) HandleUpdateContact(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "contact")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ContactRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.UpdateContact(r.Context(), id, draft, precondition(r, draft.Revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeContact(w, http.StatusOK, c)
}

// HandleDeleteContact deletes on the client's revision, carried by If-Match
// (a DELETE has no body).
func (h *Handler) HandleDeleteContact(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "contact")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.DeleteContact(r.Context(), id, precondition(r, nil)); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- payment terms ----

func writeTerms(w http.ResponseWriter, status int, t *PaymentTerms) {
	httpx.WriteRevisionETag(w, t.Revision)
	writeJSON(w, status, t)
}

func (h *Handler) HandleListTerms(w http.ResponseWriter, r *http.Request) {
	f, q, wantTotal, err := parseChildFilter(r, termsScope, "kind")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	if s, ok := singleValue(v, q, "kind"); ok {
		if k, ok := ParseTermsKind(s); ok {
			f.Kind = &k
		} else {
			v.Check(false, "kind", "must be one of: "+strings.Join(termsKindNames, ", "))
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.ListTerms(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = nextCursor(termsScope, hasMore, last.CreatedAt, last.ID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, f.Limit, listOptions(total)...)
}

func (h *Handler) HandleCreateTerms(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req TermsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	t, err := h.service.CreateTerms(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/payment-terms/"+t.ID.String())
	writeTerms(w, http.StatusCreated, t)
}

func (h *Handler) HandleGetTerms(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "payment terms")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	t, err := h.service.GetTerms(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeTerms(w, http.StatusOK, t)
}

func (h *Handler) HandleUpdateTerms(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "payment terms")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req TermsRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	t, err := h.service.UpdateTerms(r.Context(), id, draft, precondition(r, draft.Revision))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeTerms(w, http.StatusOK, t)
}
