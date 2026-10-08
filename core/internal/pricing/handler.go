// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"encoding/json"
	"net/http"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/google/uuid"
)

type Handler struct {
	service     *Service
	customerSvc *customer.Service
	productSvc  *product.Service
}

func NewHandler(s *Service, c *customer.Service, p *product.Service) *Handler {
	return &Handler{service: s, customerSvc: c, productSvc: p}
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

	// These reads expose per-customer negotiated pricing and the rule set, so
	// they are guarded at the same level as the writes (in dev mode the guard
	// is nil and passes through). Server-side order/quote/POS pricing uses the
	// Go service directly and is unaffected.
	mux.HandleFunc("GET /api/v1/pricing/calculate", guard(h.HandleCalculatePrice))
	mux.HandleFunc("POST /api/v1/pricing/rules", guard(h.HandleCreateRule))
	mux.HandleFunc("GET /api/v1/pricing/rules", guard(h.HandleListRules))
}

// CalculatedPriceView is the price read's answer on the wire (ADR 0006 7.3):
// the scaled price with its unit, the pair (1 and 1 until the unit sets of
// C3-2A: every price is in the product's stocking unit), the extension by
// Extend, and the source lowercased as price_basis.
type CalculatedPriceView struct {
	CustomerID uuid.UUID      `json:"customer_id"`
	ProductID  uuid.UUID      `json:"product_id"`
	Quantity   httpx.Quantity `json:"quantity"`
	UOM        product.UOM    `json:"uom"`

	UnitPrice   httpx.Price    `json:"unit_price_ten_thousandths"`
	PriceUOM    string         `json:"price_uom"`
	UOMQty      httpx.Quantity `json:"uom_qty"`
	PriceUOMQty httpx.Quantity `json:"price_uom_qty"`
	LineTotal   httpx.Cents    `json:"line_total_cents"`

	PriceBasis PricingSource `json:"price_basis"`
	Details    string        `json:"details"`
}

// checkPrice refuses a unit price the NUMERIC(12,4) columns cannot hold or
// that is negative: a price past the bound is a 400, never a database fault.
func checkPrice(v *httpx.Validator, field string, p httpx.Price) {
	v.Check(p >= 0, field, "a unit price is never negative")
	v.Check(int64(p) <= int64(httpx.QuantityMax), field, "is larger than a price can be (99999999.9999)")
}

// checkPercent refuses a percentage that is negative or, for a discount or a
// floor, past 100 (a price below zero).
func checkPercent(v *httpx.Validator, field string, q httpx.Quantity, atMost100 bool) {
	v.Check(q >= 0, field, "a percentage is never negative")
	if atMost100 {
		v.Check(q <= 1_000_000, field, "a percentage is at most 100")
	}
}

func writePricingJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// HandleCalculatePrice answers the engine's price for one line (ADR 0006
// 7.3): quantity and job_id that do not parse are 400s naming them, where
// the base commit ignored them (ADR 0006 section 10).
func (h *Handler) HandleCalculatePrice(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "customer_id", "product_id", "quantity", "job_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	var customerID, productID uuid.UUID
	if vals := q["customer_id"]; len(vals) > 0 {
		customerID, _ = v.UUID("customer_id", &vals[0], true)
	}
	if vals := q["product_id"]; len(vals) > 0 {
		productID, _ = v.UUID("product_id", &vals[0], true)
	}

	quantity := httpx.Quantity(10_000) // 1.0000
	if vals := q["quantity"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "quantity", "parameter is repeated")
		} else if p, perr := httpx.ParseQuantity(vals[0]); perr != nil {
			v.Check(false, "quantity", "must be a plain decimal string with at most 4 fraction digits")
		} else {
			quantity = p
			v.Check(quantity > 0, "quantity", "must be positive")
		}
	}

	var jobID *uuid.UUID
	if vals := q["job_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "job_id", "parameter is repeated")
		} else if id, ok := v.UUID("job_id", &vals[0], true); ok {
			jobID = &id
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	cust, err := h.customerSvc.GetCustomer(r.Context(), customerID)
	if err != nil {
		httpx.WriteError(w, r, httpx.NotFound("no such customer"))
		return
	}
	prod, err := h.productSvc.GetProduct(r.Context(), productID)
	if err != nil {
		httpx.WriteError(w, r, httpx.NotFound("no such product"))
		return
	}

	scaled, err := h.service.CalculateScaled(r.Context(), cust, productID, prod.BasePrice, qtyFloat(quantity), jobID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	// Until the unit sets of C3-2A, every price is in the product's stocking
	// unit: the pair is 1 and 1 and the extension is the one rounding.
	pair := httpx.Quantity(10_000)
	total, err := httpx.Extend(quantity, pair, pair, scaled.Price)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writePricingJSON(w, http.StatusOK, CalculatedPriceView{
		CustomerID:  customerID,
		ProductID:   productID,
		Quantity:    quantity,
		UOM:         prod.UOMPrimary,
		UnitPrice:   scaled.Price,
		PriceUOM:    string(prod.UOMPrimary),
		UOMQty:      pair,
		PriceUOMQty: pair,
		LineTotal:   total,
		PriceBasis:  scaled.Source,
		Details:     scaled.Details,
	})
}

// ruleCreateRequest is the POST /pricing/rules body.
type ruleCreateRequest struct {
	Name           string           `json:"name"`
	RuleType       string           `json:"rule_type"`
	ProductID      *string          `json:"product_id"`
	CustomerID     *string          `json:"customer_id"`
	JobID          *string          `json:"job_id"`
	Category       *string          `json:"category"`
	FixedPrice     *json.RawMessage `json:"fixed_price_ten_thousandths"`
	DiscountPct    *json.RawMessage `json:"discount_pct"`
	MarkupPct      *json.RawMessage `json:"markup_pct"`
	MinQuantity    *json.RawMessage `json:"min_quantity"`
	MaxQuantity    *json.RawMessage `json:"max_quantity"`
	MarginFloorPct *json.RawMessage `json:"margin_floor_pct"`
	StartsAt       *json.RawMessage `json:"starts_at"`
	ExpiresAt      *json.RawMessage `json:"expires_at"`
	IsActive       *bool            `json:"is_active"`
	Priority       *int             `json:"priority"`
}

func (req *ruleCreateRequest) parse() (*PricingRule, error) {
	v := &httpx.Validator{}
	rule := &PricingRule{Name: req.Name}
	v.Required("name", req.Name)
	if t, ok := ParseRuleType(req.RuleType); ok {
		rule.RuleType = t
	} else {
		v.Check(false, "rule_type", "must be one of: quantity_break, job_override, promotional")
	}
	parseOptUUID := func(field string, s *string) *uuid.UUID {
		if s == nil || *s == "" {
			return nil
		}
		if id, ok := v.UUID(field, s, true); ok {
			return &id
		}
		return nil
	}
	rule.ProductID = parseOptUUID("product_id", req.ProductID)
	rule.CustomerID = parseOptUUID("customer_id", req.CustomerID)
	rule.JobID = parseOptUUID("job_id", req.JobID)
	if req.Category != nil {
		rule.Category = *req.Category
	}
	if req.FixedPrice != nil {
		if n, ok := v.Int("fixed_price_ten_thousandths", *req.FixedPrice, true); ok {
			p := httpx.Price(n)
			rule.FixedPrice = &p
			checkPrice(v, "fixed_price_ten_thousandths", p)
		}
	}
	if req.DiscountPct != nil {
		if q, ok := v.Quantity("discount_pct", *req.DiscountPct, true); ok {
			rule.DiscountPct = &q
			checkPercent(v, "discount_pct", q, true)
		}
	}
	if req.MarkupPct != nil {
		if q, ok := v.Quantity("markup_pct", *req.MarkupPct, true); ok {
			rule.MarkupPct = &q
			checkPercent(v, "markup_pct", q, false)
		}
	}
	if req.MinQuantity != nil {
		if q, ok := v.Quantity("min_quantity", *req.MinQuantity, true); ok {
			rule.MinQuantity = q
			v.Check(q >= 0, "min_quantity", "must be zero or positive")
		}
	}
	if req.MaxQuantity != nil {
		if q, ok := v.Quantity("max_quantity", *req.MaxQuantity, true); ok {
			rule.MaxQuantity = &q
			v.Check(q >= 0, "max_quantity", "must be zero or positive")
		}
	}
	if req.MarginFloorPct != nil {
		if q, ok := v.Quantity("margin_floor_pct", *req.MarginFloorPct, true); ok {
			rule.MarginFloorPct = &q
			checkPercent(v, "margin_floor_pct", q, true)
		}
	}
	if req.StartsAt != nil {
		if ts, ok := v.Timestamp("starts_at", *req.StartsAt, false); ok && ts != nil {
			rule.StartsAt = ts
		}
	}
	if req.ExpiresAt != nil {
		if ts, ok := v.Timestamp("expires_at", *req.ExpiresAt, false); ok && ts != nil {
			rule.ExpiresAt = ts
		}
	}
	rule.IsActive = true
	if req.IsActive != nil {
		rule.IsActive = *req.IsActive
	}
	if req.Priority != nil {
		rule.Priority = *req.Priority
	}
	if rule.FixedPrice == nil && rule.DiscountPct == nil && rule.MarkupPct == nil {
		v.Check(false, "fixed_price_ten_thousandths", "a rule carries a fixed price, a discount or a markup")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return rule, nil
}

func (h *Handler) HandleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req ruleCreateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rule, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.CreateRule(r.Context(), rule); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pricing/rules/"+rule.ID.String())
	httpx.WriteRevisionETag(w, rule.Revision)
	writePricingJSON(w, http.StatusCreated, rule)
}

// rulesOrdering is the rules list's ordering scope.
const rulesOrdering = "pricing_rules.created_at_id_desc"

func (h *Handler) HandleListRules(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, rulesOrdering)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	wantTotal := false
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			httpx.WriteError(w, r, httpx.BadRequest("include parameter is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"}))
			return
		}
		set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal)
		if ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}

	var after *RuleCursor
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, cursorErr())
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			httpx.WriteError(w, r, cursorErr())
			return
		}
		after = &RuleCursor{CreatedAt: at, ID: id}
	}

	rules, err := h.service.ListRulesPage(r.Context(), after, page.Limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	opts := []httpx.ListOption{}
	if wantTotal {
		total, terr := h.service.CountRules(r.Context())
		if terr != nil {
			httpx.WriteError(w, r, terr)
			return
		}
		opts = append(opts, httpx.WithTotal(total))
	}
	more := len(rules) > page.Limit
	if more {
		rules = rules[:page.Limit]
	}
	next := ""
	if more && len(rules) > 0 {
		last := rules[len(rules)-1]
		next, err = httpx.MintCursor(rulesOrdering, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, rules, next, page.Limit, opts...)
}

func cursorErr() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}
