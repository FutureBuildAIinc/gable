// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package links is the link resolver of ADR 0007 section 8: one route per
// entity that answers every frontend's record URL for a record the caller
// can see, so an agent never embeds a route table. The declarative table
// (entity, number pattern, per frontend path patterns) is the only copy; a
// generator writes it to core/api/links.json, which the desk, the door and
// the shell drift tests read.
package links

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Row is one entity's declarative link table row: the module (the path
// segment and the scope module), the entity name, the document number
// pattern (empty when the entity has no number and keeps UUID URLs), and
// the desk record path pattern, with {segment} where the record's segment
// goes. A draft row resolves a draft of a confirm gated kind, whose segment
// is always the draft's UUID.
type Row struct {
	Module    string `json:"module"`
	Entity    string `json:"entity"`
	Number    string `json:"number_pattern,omitempty"`
	Desk      string `json:"desk"`
	DraftKind bool   `json:"draft_kind,omitempty"`
}

// Table is the declarative copy the generator and the registration share
// (ADR 0007 section 8): quotes, orders, invoices, customers (the desk's
// accounts area), products (its inventory area), and the draft kinds. Each
// later module adds its row when it converts.
func Table() []Row {
	return []Row{
		{Module: "quotes", Entity: "quote", Number: `^Q-[0-9]{6,}$`, Desk: "quotes/{segment}"},
		{Module: "orders", Entity: "order", Number: `^SO-[0-9]{6,}$`, Desk: "orders/{segment}"},
		{Module: "invoices", Entity: "invoice", Number: `^IN-[0-9]{6,}$`, Desk: "invoices/{segment}"},
		{Module: "customers", Entity: "customer", Desk: "accounts/{segment}"},
		{Module: "products", Entity: "product", Desk: "inventory/{segment}"},
		{Module: "quotes", Entity: "draft", Desk: "quotes/drafts/{segment}", DraftKind: true},
		{Module: "orders", Entity: "draft", Desk: "orders/drafts/{segment}", DraftKind: true},
	}
}

// RowFor finds a module's row in the declarative table, for registration:
// draft names a draft kind's row. Serve binds the row to its resolver; the
// table stays the only copy.
func RowFor(module string, draft bool) (Row, bool) {
	for _, r := range Table() {
		if r.Module == module && r.DraftKind == draft {
			return r, true
		}
	}
	return Row{}, false
}

// Resolver checks that a record exists and is visible to the caller
// (branch walls, roles, scopes) and answers its id and its document number
// (empty when it has none). The module's own read backs it, so a record the
// caller cannot see is the resolver's 404: a resolver that answered for
// invisible records would confirm their existence.
type Resolver func(ctx context.Context, raw string) (uuid.UUID, string, error)

// Entity is a table row bound to its resolver, as serve registers it.
type Entity struct {
	Row
	Resolve Resolver
}

// Settings are the resolver's URL settings (ADR 0007 section 8): the
// deployment's public URL (set: absolute links; unset: paths relative to
// the deployment's origin, the desk, the door and the API sharing one) and
// the agent UI's URL template, with {module}, {entity}, {id} and {number}
// placeholders (unset: the agent slot is null, every deployment today).
type Settings struct {
	PublicURL        string
	AgentURLTemplate string
}

// Handler serves GET /api/v1/links/<module>/{id} and
// GET /api/v1/links/drafts/<module>/{id}, one registration per entity.
type Handler struct {
	entities []Entity
	settings Settings
}

// NewHandler builds the resolver over the bound entities.
func NewHandler(settings Settings, entities ...Entity) *Handler {
	return &Handler{entities: entities, settings: settings}
}

// body is the resolver's answer (ADR 0007 section 8): who the record is,
// and every frontend's URL for it. Optional slots are present as null.
type body struct {
	Entity string  `json:"entity"`
	Module string  `json:"module"`
	ID     string  `json:"id"`
	Number *string `json:"number"`
	Links  linkSet `json:"links"`
}

type linkSet struct {
	Desk      string  `json:"desk"`
	FrontDoor string  `json:"front_door"`
	Portal    *string `json:"portal"`
	App       string  `json:"app"`
	Agent     *string `json:"agent"`
}

// Guards are the role-plus-branch guards the link routes sit behind, one
// per entity and per draft kind, each the module's own read guard: a link
// route admits exactly the callers the module's entity reads admit.
type Guards struct {
	Quotes      func(http.Handler) http.Handler
	Orders      func(http.Handler) http.Handler
	Invoices    func(http.Handler) http.Handler
	Customers   func(http.Handler) http.Handler
	Products    func(http.Handler) http.Handler
	DraftQuotes func(http.Handler) http.Handler
	DraftOrders func(http.Handler) http.Handler
}

// RegisterAll registers every entity's literal link route, one pattern per
// row of the declarative table, so the route census lists exactly which
// link routes exist (the same reasoning as the drafts kinds' literal
// routes). Each route sits behind its module's own read guard.
func RegisterAll(mux *http.ServeMux, h *Handler, g Guards) {
	RegisterOne(mux, h, "quotes", false, g.Quotes)
	RegisterOne(mux, h, "orders", false, g.Orders)
	RegisterOne(mux, h, "invoices", false, g.Invoices)
	RegisterOne(mux, h, "customers", false, g.Customers)
	RegisterOne(mux, h, "products", false, g.Products)
	RegisterOne(mux, h, "quotes", true, g.DraftQuotes)
	RegisterOne(mux, h, "orders", true, g.DraftOrders)
}

// RegisterOne registers one module's link route behind the given guard:
// /api/v1/links/<module>/{id} for an entity, and
// /api/v1/links/drafts/<module>/{id} for a draft kind. The patterns are
// literal, one per module, so the census resolves them wherever this is
// called from.
func RegisterOne(mux *http.ServeMux, h *Handler, module string, draft bool, guard func(http.Handler) http.Handler) {
	switch module + "/" + strconv.FormatBool(draft) {
	case "quotes/false":
		mux.HandleFunc("GET /api/v1/links/quotes/{id}", Guarded(guard, h.resolveFor("quotes", false)))
	case "orders/false":
		mux.HandleFunc("GET /api/v1/links/orders/{id}", Guarded(guard, h.resolveFor("orders", false)))
	case "invoices/false":
		mux.HandleFunc("GET /api/v1/links/invoices/{id}", Guarded(guard, h.resolveFor("invoices", false)))
	case "customers/false":
		mux.HandleFunc("GET /api/v1/links/customers/{id}", Guarded(guard, h.resolveFor("customers", false)))
	case "products/false":
		mux.HandleFunc("GET /api/v1/links/products/{id}", Guarded(guard, h.resolveFor("products", false)))
	case "quotes/true":
		mux.HandleFunc("GET /api/v1/links/drafts/quotes/{id}", Guarded(guard, h.resolveFor("quotes", true)))
	case "orders/true":
		mux.HandleFunc("GET /api/v1/links/drafts/orders/{id}", Guarded(guard, h.resolveFor("orders", true)))
	default:
		panic("links: no link route for " + module)
	}
}

// resolveFor answers the named entity's link route, from the bound list.
func (h *Handler) resolveFor(module string, draft bool) http.HandlerFunc {
	e, ok := h.entity(module, draft)
	if !ok {
		panic("links: no entity bound for " + module)
	}
	return h.resolve(e)
}

// Guarded composes a guard with a HandlerFunc, the drafts routes' shape.
func Guarded(guard func(http.Handler) http.Handler, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guard(handler).ServeHTTP(w, r)
	}
}

// entity finds the bound entity a registration names.
func (h *Handler) entity(module string, draft bool) (Entity, bool) {
	for _, e := range h.entities {
		if e.Module == module && e.DraftKind == draft {
			return e, true
		}
	}
	return Entity{}, false
}

// resolve answers one entity's link route.
func (h *Handler) resolve(e Entity) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.PathValue("id")
		if _, _, err := parseSlot(e.Row, raw); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		id, number, err := e.Resolve(r.Context(), raw)
		if err != nil {
			httpx.WriteError(w, r, hideMissing(err))
			return
		}
		segment := id.String()
		var numberPtr *string
		if number != "" {
			numberPtr = &number
			// The canonical record segment is the number when the entity
			// has one, otherwise the UUID; draft links use the draft's UUID.
			segment = number
		}
		record := strings.Replace(e.Desk, "{segment}", segment, 1)
		base := h.settings.PublicURL
		agent := (*string)(nil)
		if h.settings.AgentURLTemplate != "" {
			filled := h.settings.AgentURLTemplate
			for ph, val := range map[string]string{
				"{module}": e.Module, "{entity}": e.Entity,
				"{id}": id.String(), "{number}": number,
			} {
				filled = strings.ReplaceAll(filled, ph, val)
			}
			agent = &filled
		}
		writeJSON(w, http.StatusOK, body{
			Entity: e.Entity, Module: e.Module, ID: id.String(), Number: numberPtr,
			Links: linkSet{
				Desk:      base + "/" + record,
				FrontDoor: base + "/?open=" + record,
				Portal:    nil,
				App:       "gable://" + record,
				Agent:     agent,
			},
		})
	}
}

// parseSlot parses the {id} slot the way the entity's own reads do (ADR
// 0007 section 7): a UUID first, then the entity's number pattern when it
// has one. A well formed number of another entity, or anything else, is a
// 400 naming id.
func parseSlot(e Row, raw string) (uuid.UUID, string, error) {
	if id, err := uuid.Parse(raw); err == nil {
		return id, "", nil
	}
	if e.Number != "" {
		if regexp.MustCompile(e.Number).MatchString(raw) {
			return uuid.Nil, raw, nil
		}
	}
	return uuid.Nil, "", httpx.BadRequest("invalid "+e.Entity+" id",
		httpx.FieldError{Field: "id", Message: "must be a UUID" + numberHint(e)})
}

func numberHint(e Row) string {
	if e.Number == "" {
		return ""
	}
	return " or a " + e.Module + " number such as " + sampleNumber(e.Number)
}

// sampleNumber renders one number the pattern accepts, for the 400's hint.
func sampleNumber(pattern string) string {
	prefix := pattern[1:strings.Index(pattern, "-")]
	return prefix + "-000123"
}

// hideMissing maps the resolver's invisibility sentinel to the resolver's
// one answer for a record the caller cannot see (404, never confirming the
// record exists); the modules' own 404 errors and every other error pass
// through unchanged.
func hideMissing(err error) error {
	if errors.Is(err, ErrInvisible) {
		return httpx.NotFound("record not found")
	}
	return err
}

// writeJSON writes a 2xx JSON body, the modules' local helper shape.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// errInvisible is the sentinel a resolver returns for a record the caller
// cannot see; the resolver answers it as the same 404 a missing record
// gets, never confirming the record exists.
var errInvisible = &notVisibleError{}

type notVisibleError struct{}

func (*notVisibleError) Error() string { return "record not visible" }

// ErrInvisible is the sentinel resolvers return for a record the caller
// cannot see.
var ErrInvisible error = errInvisible
