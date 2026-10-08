// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// CategoryHandler handles HTTP requests for category pricing administration.
type CategoryHandler struct {
	service     *CategoryPricingService
	customerSvc *customer.Service
}

// NewCategoryHandler creates a new CategoryHandler.
func NewCategoryHandler(svc *CategoryPricingService, custSvc *customer.Service) *CategoryHandler {
	return &CategoryHandler{service: svc, customerSvc: custSvc}
}

// RegisterCategoryRoutes registers all category pricing routes.
// roleGuard is applied to write endpoints; pass nil in dev mode.
func (h *CategoryHandler) RegisterCategoryRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	// Helper to wrap a handler with the role guard if provided
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	// Category tree management. The tree itself is structural reference data
	// (category names/hierarchy) and stays open, but everything that exposes
	// markups/margins/negotiated pricing below is guarded at the same level as
	// the writes (the guard is nil/pass-through in dev mode).
	mux.HandleFunc("GET /api/v1/pricing/categories", h.HandleListCategories)
	mux.HandleFunc("POST /api/v1/pricing/categories", guard(h.HandleCreateCategory))
	mux.HandleFunc("PUT /api/v1/pricing/categories/{id}", guard(h.HandleUpdateCategory))

	// Category pricing rules CRUD
	mux.HandleFunc("GET /api/v1/pricing/category-rules", guard(h.HandleListCategoryRules))
	mux.HandleFunc("POST /api/v1/pricing/category-rules", guard(h.HandleCreateCategoryRule))
	mux.HandleFunc("PUT /api/v1/pricing/category-rules/{id}", guard(h.HandleUpdateCategoryRule))
	mux.HandleFunc("DELETE /api/v1/pricing/category-rules/{id}", guard(h.HandleDeleteCategoryRule))

	// Bulk operations
	mux.HandleFunc("POST /api/v1/pricing/category-rules/bulk", guard(h.HandleBulkUpsertRules))
	mux.HandleFunc("DELETE /api/v1/pricing/category-rules/bulk", guard(h.HandleBulkDeleteRules))

	// Audit trail
	mux.HandleFunc("GET /api/v1/pricing/category-rules/{id}/audit", guard(h.HandleGetRuleAudit))

	// Matrix view (admin grid)
	mux.HandleFunc("GET /api/v1/pricing/matrix", guard(h.HandleGetMatrix))

	// Resolution preview
	mux.HandleFunc("GET /api/v1/pricing/resolve", guard(h.HandleResolvePreview))
}

func writeCatJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// --- Category Endpoints ---

func (h *CategoryHandler) HandleListCategories(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "view")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	view := "tree"
	if vals := q["view"]; len(vals) > 0 {
		if len(vals) > 1 {
			httpx.WriteError(w, r, httpx.BadRequest("view parameter is repeated",
				httpx.FieldError{Field: "view", Message: "parameter is repeated"}))
			return
		}
		if vals[0] != "flat" && vals[0] != "tree" {
			httpx.WriteError(w, r, httpx.BadRequest("view is not one of the route's values",
				httpx.FieldError{Field: "view", Message: "must be flat or tree"}))
			return
		}
		view = vals[0]
	}

	var result []ProductCategory
	if view == "flat" {
		result, err = h.service.ListCategories(r.Context())
	} else {
		result, err = h.service.ListCategoriesTree(r.Context())
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, result, "", 0)
}

// categoryWriteRequest is the categories create and update body.
type categoryWriteRequest struct {
	Name      string  `json:"name"`
	Slug      string  `json:"slug"`
	Path      string  `json:"path"`
	ParentID  *string `json:"parent_id"`
	SortOrder *int    `json:"sort_order"`
	IsActive  *bool   `json:"is_active"`
}

func (req *categoryWriteRequest) parse(v *httpx.Validator) ProductCategory {
	cat := ProductCategory{Name: req.Name, Slug: req.Slug, Path: req.Path}
	v.Required("name", req.Name)
	v.Required("slug", req.Slug)
	v.Required("path", req.Path)
	if req.ParentID != nil && *req.ParentID != "" {
		if id, ok := v.UUID("parent_id", req.ParentID, true); ok {
			cat.ParentID = &id
		}
	}
	if req.SortOrder != nil {
		cat.SortOrder = *req.SortOrder
	}
	cat.IsActive = true
	if req.IsActive != nil {
		cat.IsActive = *req.IsActive
	}
	return cat
}

func (h *CategoryHandler) HandleCreateCategory(w http.ResponseWriter, r *http.Request) {
	var req categoryWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	cat := req.parse(v)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.CreateCategory(r.Context(), &cat); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pricing/categories/"+cat.ID.String())
	writeCatJSON(w, http.StatusCreated, cat)
}

func (h *CategoryHandler) HandleUpdateCategory(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	var req categoryWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	cat := req.parse(v)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cat.ID = id
	if err := h.service.UpdateCategory(r.Context(), &cat); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCatJSON(w, http.StatusOK, cat)
}

// --- Category Pricing Rules Endpoints ---

// categoryRuleOrdering is the category rules list's ordering scope.
const categoryRuleOrdering = "category_pricing_rules.created_at_id_desc"

func (h *CategoryHandler) HandleListCategoryRules(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include",
		"target_type", "tier", "customer_id", "category_id", "is_active")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, categoryRuleOrdering)
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

	v := &httpx.Validator{}
	filter := CategoryRuleFilter{}
	if vals := q["target_type"]; len(vals) > 0 {
		if t, ok := ParseTargetType(vals[0]); ok {
			filter.TargetType = &t
		} else {
			v.Check(false, "target_type", "must be one of: account, tier")
		}
	}
	if vals := q["tier"]; len(vals) > 0 {
		filter.Tier = vals[0]
	}
	if vals := q["customer_id"]; len(vals) > 0 {
		if id, ok := v.UUID("customer_id", &vals[0], true); ok {
			filter.CustomerID = &id
		}
	}
	if vals := q["category_id"]; len(vals) > 0 {
		if id, ok := v.UUID("category_id", &vals[0], true); ok {
			filter.CategoryID = &id
		}
	}
	if vals := q["is_active"]; len(vals) > 0 {
		b := vals[0] == "true"
		if vals[0] != "true" && vals[0] != "false" {
			v.Check(false, "is_active", "must be true or false")
		}
		filter.IsActive = &b
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	var after *time.Time
	var afterID *uuid.UUID
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
		after, afterID = &at, &id
	}

	rules, more, total, err := h.service.ListCategoryRulesPage(r.Context(), filter, after, afterID, page.Limit, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if more && len(rules) > 0 {
		last := rules[len(rules)-1]
		next, err = httpx.MintCursor(categoryRuleOrdering, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	opts := []httpx.ListOption{}
	if wantTotal {
		opts = append(opts, httpx.WithTotal(total))
	}
	httpx.WriteList(w, rules, next, page.Limit, opts...)
}

// categoryRuleValues is what a category rule write sets after the rule's
// target is fixed: the rule's value in the one reading its type gives it (a
// scale 4 price for a fixed rule, value_ten_thousandths, a percentage
// otherwise, value_pct), its window and its standing.
type categoryRuleValues struct {
	RuleType       string           `json:"rule_type"`
	ValuePrice     *json.RawMessage `json:"value_ten_thousandths"`
	ValuePct       *json.RawMessage `json:"value_pct"`
	MarginFloorPct *json.RawMessage `json:"margin_floor_pct"`
	StartsAt       *json.RawMessage `json:"starts_at"`
	ExpiresAt      *json.RawMessage `json:"expires_at"`
	IsActive       *bool            `json:"is_active"`
	Priority       *int             `json:"priority"`
}

// categoryRuleWriteRequest is the create body of a category rule (and one
// element of the bulk body, which may also carry the id of the rule it
// replaces).
type categoryRuleWriteRequest struct {
	ID         *string `json:"id,omitempty"`
	TargetType string  `json:"target_type"`
	CustomerID *string `json:"customer_id"`
	Tier       *string `json:"tier"`
	CategoryID string  `json:"category_id"`
	categoryRuleValues
}

// categoryRuleBulkRequest is one element of the bulk body: a create body that
// may carry the id of the rule it replaces and then the revision it read.
type categoryRuleBulkRequest struct {
	categoryRuleWriteRequest
	Revision *int64 `json:"revision"`
}

// categoryRuleUpdateRequest is the update body: the values and the
// revision. The target (account or tier, customer, category) is fixed at
// create, and a body that names one is a 400 naming it.
type categoryRuleUpdateRequest struct {
	categoryRuleValues
	Revision *int64 `json:"revision"`
}

func (req *categoryRuleValues) parseValues(v *httpx.Validator, rule *CategoryPricingRule) {
	if t, ok := ParseCategoryRuleType(req.RuleType); ok {
		rule.RuleType = t
	} else {
		v.Check(false, "rule_type", "must be one of: markup, markdown, fixed, margin")
	}
	if req.ValuePrice != nil {
		if n, ok := v.Int("value_ten_thousandths", *req.ValuePrice, true); ok {
			p := httpx.Price(n)
			rule.ValuePrice = &p
			checkPrice(v, "value_ten_thousandths", p)
		}
	}
	if req.ValuePct != nil {
		if q, ok := v.Quantity("value_pct", *req.ValuePct, true); ok {
			rule.ValuePct = &q
			checkPercent(v, "value_pct", q, rule.RuleType == CategoryRuleMarkdown)
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
	v.Check(rule.ValuePrice == nil || rule.ValuePct == nil, "value_ten_thousandths",
		"a rule carries either value_ten_thousandths or value_pct, never both")
	if rule.RuleType == CategoryRuleFixed {
		v.Check(rule.ValuePrice != nil, "value_ten_thousandths", "is required on a fixed rule")
	} else if rule.RuleType != "" {
		v.Check(rule.ValuePct != nil, "value_pct", "is required on a percent rule")
	}
}

func (req *categoryRuleWriteRequest) parse(v *httpx.Validator) CategoryPricingRule {
	rule := CategoryPricingRule{}
	if t, ok := ParseTargetType(req.TargetType); ok {
		rule.TargetType = t
	} else {
		v.Check(false, "target_type", "must be one of: account, tier")
	}
	if req.CustomerID != nil && *req.CustomerID != "" {
		if id, ok := v.UUID("customer_id", req.CustomerID, true); ok {
			rule.CustomerID = &id
		}
	}
	if req.Tier != nil {
		rule.Tier = *req.Tier
	}
	if req.CategoryID != "" {
		if id, ok := v.UUID("category_id", &req.CategoryID, true); ok {
			rule.CategoryID = id
		}
	} else {
		v.Check(false, "category_id", "is required")
	}
	if req.ID != nil && *req.ID != "" {
		if id, ok := v.UUID("id", req.ID, true); ok {
			rule.ID = id
		}
	}
	req.categoryRuleValues.parseValues(v, &rule)
	if rule.TargetType == TargetTypeAccount {
		v.Check(rule.CustomerID != nil, "customer_id", "is required on an account rule")
	} else {
		v.Check(rule.Tier != "", "tier", "is required on a tier rule")
	}
	return rule
}

func (h *CategoryHandler) HandleCreateCategoryRule(w http.ResponseWriter, r *http.Request) {
	var req categoryRuleWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	rule := req.parse(v)
	v.Check(req.ID == nil, "id", "the server assigns the id of a new rule")
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.CreateCategoryRule(r.Context(), &rule); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pricing/category-rules/"+rule.ID.String())
	httpx.WriteRevisionETag(w, rule.Revision)
	writeCatJSON(w, http.StatusCreated, rule)
}

func (h *CategoryHandler) HandleUpdateCategoryRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	var req categoryRuleUpdateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	rule := CategoryPricingRule{}
	req.parseValues(v, &rule)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rule.ID = id
	if err := h.service.UpdateCategoryRule(r.Context(), &rule, Precondition{IfMatch: r.Header.Get("If-Match"), Revision: req.Revision}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	updated, err := h.service.GetCategoryRule(r.Context(), id)
	if err != nil || updated == nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, updated.Revision)
	writeCatJSON(w, http.StatusOK, updated)
}

func (h *CategoryHandler) HandleDeleteCategoryRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	var bodyRevision *int64
	if raw, rerr := readBody(r); rerr == nil && len(raw) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct {
			Revision *int64 `json:"revision"`
		}
		if err := httpx.DecodeJSON(r, &body); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		bodyRevision = body.Revision
	}
	if err := h.service.DeleteCategoryRule(r.Context(), id, Precondition{IfMatch: r.Header.Get("If-Match"), Revision: bodyRevision}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Matrix ---

func (h *CategoryHandler) HandleGetMatrix(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	matrix, err := h.service.GetMatrix(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCatJSON(w, http.StatusOK, matrix)
}

// --- Resolution Preview ---

func (h *CategoryHandler) HandleResolvePreview(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "product_id", "customer_id", "tier")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	var productID uuid.UUID
	if vals := q["product_id"]; len(vals) > 0 {
		if id, ok := v.UUID("product_id", &vals[0], true); ok {
			productID = id
		}
	} else {
		v.Check(false, "product_id", "is required")
	}
	var customerID uuid.UUID
	if vals := q["customer_id"]; len(vals) > 0 {
		if id, ok := v.UUID("customer_id", &vals[0], true); ok {
			customerID = id
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	tier := ""
	if vals := q["tier"]; len(vals) > 0 {
		tier = vals[0]
	}
	if tier == "" && customerID != uuid.Nil && h.customerSvc != nil {
		if cust, err := h.customerSvc.GetCustomer(r.Context(), customerID); err == nil && cust != nil {
			tier = string(cust.Tier)
		}
	}
	if tier == "" {
		tier = "RETAIL"
	}

	resolved, err := h.service.ResolveEffectivePrice(r.Context(), customerID, tier, productID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCatJSON(w, http.StatusOK, resolved)
}

// --- Audit ---

func (h *CategoryHandler) HandleGetRuleAudit(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	entries, err := h.service.ListAuditEntries(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, entries, "", 0)
}

// --- Bulk Operations ---

func (h *CategoryHandler) HandleBulkUpsertRules(w http.ResponseWriter, r *http.Request) {
	var reqs []categoryRuleBulkRequest
	if err := httpx.DecodeJSON(r, &reqs); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(reqs) == 0 {
		httpx.WriteError(w, r, httpx.BadRequest("at least one rule is required",
			httpx.FieldError{Field: "", Message: "at least one rule is required"}))
		return
	}
	if len(reqs) > 500 {
		httpx.WriteError(w, r, httpx.BadRequest("max 500 rules per batch",
			httpx.FieldError{Field: "", Message: "max 500 rules per batch"}))
		return
	}
	v := &httpx.Validator{}
	rules := make([]CategoryPricingRule, len(reqs))
	pres := make([]Precondition, len(reqs))
	for i := range reqs {
		rules[i] = reqs[i].parse(v)
		pres[i] = Precondition{Revision: reqs[i].Revision}
		v.Check(reqs[i].Revision == nil || rules[i].ID != uuid.Nil, "revision", "a revision belongs to an element that names the id of the rule it replaces")
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.BulkUpsertRules(r.Context(), rules, pres); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCatJSON(w, http.StatusOK, map[string]int{"count": len(rules)})
}

func (h *CategoryHandler) HandleBulkDeleteRules(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(body.IDs) == 0 {
		httpx.WriteError(w, r, httpx.BadRequest("at least one id is required",
			httpx.FieldError{Field: "ids", Message: "at least one id is required"}))
		return
	}
	v := &httpx.Validator{}
	ids := make([]uuid.UUID, 0, len(body.IDs))
	for i := range body.IDs {
		if id, ok := v.UUID("ids", &body.IDs[i], true); ok {
			ids = append(ids, id)
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.BulkDeleteRules(r.Context(), ids); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readBody drains the request body for an optional body revision, leaving an
// empty body alone.
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(r.Body)
}
