// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

// The catalogue routes under /api/v1/pricing/ (ADR 0006 section 5.6): the
// levels, the profiles, the product price lists, the vendor levels and
// costs, the contracts and the cost read. Every write carrying a branch_id
// passes the payload branch rule, and the every branch writes pass
// CheckEveryBranch; the reads apply the branch wall.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// catalogOrdering is the catalogue lists' ordering scope: created_at then
// id, newest first.
const catalogOrdering = "pricing_catalog.created_at_id_desc"

func jsonMarshal(m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// pgconnCommandTag is the command tag a write answers, for the affected-rows
// check the revision writes make.
type pgconnCommandTag = pgconn.CommandTag

// catalogWriteErr maps a catalogue write's database refusals to the
// contract's answers: a foreign key is a 400 naming the field, a unique
// conflict a 409 duplicate, a check on the price unit the hold's own words.
func catalogWriteErr(err error, what string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return httpx.Duplicate("a " + what + " with this key already exists")
		case "23503":
			field := "id"
			for _, f := range []string{"price_level_id", "price_profile_id", "vendor_id", "vendor_price_level_id", "customer_id", "product_id", "branch_id"} {
				if strings.Contains(pgErr.ConstraintName, f) {
					field = f
					break
				}
			}
			return httpx.BadRequest(field+" does not name an existing row",
				httpx.FieldError{Field: field, Message: "no such row"})
		case "23514":
			if strings.Contains(pgErr.Message, "stocking unit") || strings.Contains(pgErr.ConstraintName, "price_uom") {
				return httpx.BadRequest("price_uom: a price unit other than the stocking unit arrives with C3-2B",
					httpx.FieldError{Field: "price_uom", Message: "is not the product's stocking unit"})
			}
			return httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: pgErr.ConstraintName, Message: pgErr.Message})
		case "23508":
			if strings.Contains(pgErr.Message, "price_uom") {
				return httpx.BadRequest("price_uom: a price unit other than the stocking unit arrives with C3-2B",
					httpx.FieldError{Field: "price_uom", Message: pgErr.Message})
			}
		}
	}
	if errors.Is(err, ErrStaleRevision) {
		return httpx.StaleRevision("the row was changed after this revision was read; reload and retry")
	}
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("no such " + what)
	}
	if errors.Is(err, ErrCatalogDuplicate) {
		return httpx.Duplicate("a " + what + " with this key already exists")
	}
	return err
}

// catalogHandler carries the catalogue's service and the guards.
type CatalogHandler struct {
	service *CatalogService
	engine  *Service
	guard   *middleware.BranchGuard
}

func NewCatalogHandler(service *CatalogService) *CatalogHandler {
	return &CatalogHandler{service: service}
}

// WithEngine wires the engine the cost read runs through.
func (h *CatalogHandler) WithEngine(s *Service) *CatalogHandler {
	h.engine = s
	return h
}

// WithBranchGuard wires the payload branch rule the writes pass.
func (h *CatalogHandler) WithBranchGuard(g *middleware.BranchGuard) *CatalogHandler {
	h.guard = g
	return h
}

// checkPayloadBranch holds a branch the write names to the caller's grants.
func (h *CatalogHandler) checkPayloadBranch(ctx interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(any) any
}, branch *uuid.UUID) error {
	if h.guard == nil {
		return nil
	}
	if branch == nil {
		return h.guard.CheckEveryBranch(ctx)
	}
	return h.guard.CheckPayloadBranch(ctx, *branch)
}

// checkRowBranch holds a loaded row's branch to the caller's wall: a context
// branch names it, the row names another, the row reads as missing (404); a
// user with no context branch holds the row's branch to their grants
// (CheckGrantsBranch), and an every-branch row is visible to everyone.
func (h *CatalogHandler) checkRowBranch(ctx context.Context, rowBranch *uuid.UUID) error {
	if h.guard == nil {
		return nil
	}
	if bc := middleware.BranchFromContext(ctx); bc != nil && bc.BranchID != nil &&
		rowBranch != nil && *bc.BranchID != *rowBranch {
		return httpx.NotFound("no such row")
	}
	if rowBranch == nil {
		return nil
	}
	return h.guard.CheckGrantsBranch(ctx, rowBranch)
}

// checkQueryBranch holds the branch a read filters on to the same rule.
func (h *CatalogHandler) checkQueryBranch(ctx context.Context, branch *uuid.UUID) error {
	if h.guard == nil || branch == nil {
		return nil
	}
	return h.guard.CheckPayloadBranch(ctx, *branch)
}

// writeCatalogError writes a catalogue error, mapping the branch guards'
// refusals to the 403 the payload branch rule answers with.
func writeCatalogError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		httpx.WriteError(w, r, httpx.Forbidden("branch is outside the branches this caller may target"))
		return
	}
	httpx.WriteError(w, r, err)
}

func writeCatalogJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func catalogPathID(r *http.Request, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid "+field,
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

// catalogPage parses the list envelope's cursor and limit.
func catalogPage(r *http.Request, limit int) (*CatalogCursor, int, error) {
	page, err := httpx.ParseListQuery(r, catalogOrdering)
	if err != nil {
		return nil, 0, err
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return nil, 0, httpx.BadRequest("cursor keyset is malformed",
				httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return nil, 0, httpx.BadRequest("cursor keyset is malformed",
				httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
		}
		return &CatalogCursor{CreatedAt: at, ID: id}, page.Limit, nil
	}
	return nil, page.Limit, nil
}

// --- Levels ---

func (h *CatalogHandler) HandleListLevels(w http.ResponseWriter, r *http.Request) {
	after, limit, err := catalogPage(r, 50)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.service.repo.ListLevels(r.Context(), after, limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var next string
	if more && len(items) > 0 {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(catalogOrdering, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, levelViews(items), next, limit)
}

func levelViews(items []PriceLevel) []PriceLevel { return items }

func (h *CatalogHandler) HandleGetLevel(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "level id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	l, err := h.service.repo.GetLevel(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if l == nil {
		httpx.WriteError(w, r, httpx.NotFound("no such price level"))
		return
	}
	writeCatalogJSON(w, http.StatusOK, l)
}

type levelWriteRequest struct {
	Code          *string `json:"code"`
	Name          *string `json:"name"`
	Basis         *string `json:"basis"`
	AdjustPercent *string `json:"adjust_percent"`
	Position      *int    `json:"position"`
	IsActive      *bool   `json:"is_active"`
	Revision      *int64  `json:"revision"`
}

var levelBases = map[string]bool{"BASE": true, "AVERAGE_COST": true, "REPLACEMENT_COST": true}

func (h *CatalogHandler) HandleCreateLevel(w http.ResponseWriter, r *http.Request) {
	var req levelWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	l := &PriceLevel{Basis: "BASE", IsActive: true, AdjustPercent: 0}
	if req.Code != nil {
		l.Code = strings.ToUpper(strings.TrimSpace(*req.Code))
	}
	v.Check(len(l.Code) > 0, "code", "is required")
	v.Check(len(l.Code) <= 16, "code", "is longer than 16 characters")
	if req.Name != nil {
		l.Name = *req.Name
	} else {
		v.Check(false, "name", "is required")
	}
	if req.Basis != nil {
		basis := strings.ToUpper(strings.TrimSpace(*req.Basis))
		if !levelBases[basis] {
			v.Check(false, "basis", "must be one of: base, average_cost, replacement_cost")
		}
		l.Basis = basis
	}
	if req.AdjustPercent != nil {
		if q, ok := v.Quantity("adjust_percent", []byte(*req.AdjustPercent), false); ok {
			l.AdjustPercent = q
		}
	}
	if l.AdjustPercent < -1_000_000 {
		v.Check(false, "adjust_percent", "is below -100")
	}
	if req.Position != nil {
		l.Position = *req.Position
	}
	if req.IsActive != nil {
		l.IsActive = *req.IsActive
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.CreateLevel(r.Context(), l); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pricing/levels/"+l.ID.String())
	httpx.WriteRevisionETag(w, l.Revision)
	writeCatalogJSON(w, http.StatusCreated, l)
}

func (h *CatalogHandler) HandleUpdateLevel(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "level id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req levelWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	if req.Revision == nil {
		v.Check(false, "revision", "is required on a PUT")
	}
	l := &PriceLevel{ID: id}
	if req.Name != nil {
		l.Name = *req.Name
	} else {
		v.Check(false, "name", "is required on a PUT: a left out field would reset the level")
	}
	if req.Basis != nil {
		basis := strings.ToUpper(strings.TrimSpace(*req.Basis))
		if !levelBases[basis] {
			v.Check(false, "basis", "must be one of: base, average_cost, replacement_cost")
		}
		l.Basis = basis
	} else {
		v.Check(false, "basis", "is required on a PUT")
	}
	if req.AdjustPercent != nil {
		if q, ok := v.Quantity("adjust_percent", []byte(*req.AdjustPercent), false); ok {
			l.AdjustPercent = q
		}
	} else {
		v.Check(false, "adjust_percent", "is required on a PUT")
	}
	if req.IsActive != nil {
		l.IsActive = *req.IsActive
	} else {
		v.Check(false, "is_active", "is required on a PUT")
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.UpdateLevel(r.Context(), l, *req.Revision); err != nil {
		httpx.WriteError(w, r, catalogWriteErr(err, "price level"))
		return
	}
	stored, err := h.service.repo.GetLevel(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, stored.Revision)
	writeCatalogJSON(w, http.StatusOK, stored)
}

// --- Profiles ---

func (h *CatalogHandler) HandleListProfiles(w http.ResponseWriter, r *http.Request) {
	after, limit, err := catalogPage(r, 50)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.service.repo.ListProfiles(r.Context(), after, limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var next string
	if more && len(items) > 0 {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(catalogOrdering, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, limit)
}

func (h *CatalogHandler) HandleGetProfile(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "profile id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.repo.GetProfile(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if p == nil {
		httpx.WriteError(w, r, httpx.NotFound("no such price profile"))
		return
	}
	writeCatalogJSON(w, http.StatusOK, p)
}

type profileWriteRequest struct {
	Code         *string `json:"code"`
	Name         *string `json:"name"`
	PriceLevelID *string `json:"price_level_id"`
	IsActive     *bool   `json:"is_active"`
	Revision     *int64  `json:"revision"`
}

func (h *CatalogHandler) HandleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var req profileWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	p := &PriceProfile{IsActive: true}
	if req.Code != nil {
		p.Code = strings.ToUpper(strings.TrimSpace(*req.Code))
	}
	v.Check(len(p.Code) > 0, "code", "is required")
	v.Check(len(p.Code) <= 16, "code", "is longer than 16 characters")
	if req.Name != nil {
		p.Name = *req.Name
	} else {
		v.Check(false, "name", "is required")
	}
	if req.PriceLevelID != nil {
		if id, ok := v.UUID("price_level_id", req.PriceLevelID, true); ok {
			p.PriceLevelID = id
		}
	} else {
		v.Check(false, "price_level_id", "is required")
	}
	if req.IsActive != nil {
		p.IsActive = *req.IsActive
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.CreateProfile(r.Context(), p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pricing/profiles/"+p.ID.String())
	httpx.WriteRevisionETag(w, p.Revision)
	writeCatalogJSON(w, http.StatusCreated, p)
}

func (h *CatalogHandler) HandleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "profile id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req profileWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	if req.Revision == nil {
		v.Check(false, "revision", "is required on a PUT")
	}
	p := &PriceProfile{ID: id}
	if req.Name != nil {
		p.Name = *req.Name
	} else {
		v.Check(false, "name", "is required on a PUT")
	}
	if req.PriceLevelID != nil {
		if lvl, ok := v.UUID("price_level_id", req.PriceLevelID, true); ok {
			p.PriceLevelID = lvl
		}
	} else {
		v.Check(false, "price_level_id", "is required on a PUT")
	}
	if req.IsActive != nil {
		p.IsActive = *req.IsActive
	} else {
		v.Check(false, "is_active", "is required on a PUT")
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.UpdateProfile(r.Context(), p, *req.Revision); err != nil {
		httpx.WriteError(w, r, catalogWriteErr(err, "price profile"))
		return
	}
	stored, err := h.service.repo.GetProfile(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, stored.Revision)
	writeCatalogJSON(w, http.StatusOK, stored)
}

// --- Product prices ---

func (h *CatalogHandler) HandleListProductPrices(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "branch_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	productID, err := catalogPathID(r, "product id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var branch *uuid.UUID
	if vals := q["branch_id"]; len(vals) > 0 {
		v := &httpx.Validator{}
		if id, ok := v.UUID("branch_id", &vals[0], true); ok {
			branch = &id
		} else if err := v.Err(); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := h.checkQueryBranch(r.Context(), branch); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	items, err := h.service.repo.ProductPrices(r.Context(), productID, branch, middleware.BranchIDForQuery(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCatalogJSON(w, http.StatusOK, map[string]any{"items": priceViews(items)})
}

func priceViews(items []ProductPrice) []ProductPrice { return items }

type priceRowRequest struct {
	PriceLevelID            *string          `json:"price_level_id"`
	BranchID                *string          `json:"branch_id"`
	PriceUOM                *string          `json:"price_uom"`
	UnitPriceTenThousandths *json.RawMessage `json:"unit_price_ten_thousandths"`
}

type priceListPutRequest struct {
	Revision json.RawMessage   `json:"revision"`
	Prices   []priceRowRequest `json:"prices"`
}

// HandleReplaceProductPrices replaces one branch's slice of the product's
// price list at the product's revision. The rows carry no branch_id: the
// slice is the query's branch, and the every branch slice (no branch_id) is
// admitted only by CheckEveryBranch.
func (h *CatalogHandler) HandleReplaceProductPrices(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "branch_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	productID, err := catalogPathID(r, "product id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var branch *uuid.UUID
	if vals := q["branch_id"]; len(vals) > 0 {
		v := &httpx.Validator{}
		if id, ok := v.UUID("branch_id", &vals[0], true); ok {
			branch = &id
		} else if verr := v.Err(); verr != nil {
			httpx.WriteError(w, r, verr)
			return
		}
	}
	if err := h.checkPayloadBranch(r.Context(), branch); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	var req priceListPutRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	var productRevision int64
	if err := json.Unmarshal(req.Revision, &productRevision); err != nil {
		v.Check(false, "revision", "must be the product revision the list was read at")
	}
	facts, ferr := h.service.repo.ProductPricingFacts(r.Context(), productID)
	if ferr != nil {
		httpx.WriteError(w, r, ferr)
		return
	}
	rows := make([]ProductPrice, 0, len(req.Prices))
	for i, row := range req.Prices {
		if row.BranchID != nil {
			v.Check(false, "prices["+string(rune('0'+i))+"].branch_id", "a row carries no branch: the slice is the query's branch")
			continue
		}
		p := ProductPrice{ProductID: productID, BranchID: branch, PriceUOM: facts.PriceUOM}
		if row.PriceLevelID != nil {
			if id, ok := v.UUID("prices["+itoa(i)+"].price_level_id", row.PriceLevelID, false); ok {
				lid := id
				p.PriceLevelID = &lid
			}
		}
		if row.PriceUOM != nil {
			p.PriceUOM = strings.ToUpper(strings.TrimSpace(*row.PriceUOM))
		}
		if row.UnitPriceTenThousandths != nil {
			if n, ok := v.Int("prices["+itoa(i)+"].unit_price_ten_thousandths", *row.UnitPriceTenThousandths, true); ok {
				p.UnitPrice = httpx.Price(n)
				checkPrice(v, "prices["+itoa(i)+"].unit_price_ten_thousandths", p.UnitPrice)
			}
		} else {
			v.Check(false, "prices["+itoa(i)+"].unit_price_ten_thousandths", "is required")
		}
		// The stocking unit hold: a price in another unit arrives with C3-2B.
		if p.PriceUOM != facts.StockUOM {
			v.Check(false, "prices["+itoa(i)+"].price_uom", "a price unit other than the stocking unit arrives with C3-2B")
		}
		rows = append(rows, p)
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.ReplaceProductPrices(r.Context(), productID, branch, productRevision, rows); err != nil {
		httpx.WriteError(w, r, catalogWriteErr(err, "product price"))
		return
	}
	stored, err := h.service.repo.ProductPrices(r.Context(), productID, branch, middleware.BranchIDForQuery(r.Context()))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCatalogJSON(w, http.StatusOK, map[string]any{"items": priceViews(stored)})
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

// --- Vendor levels and costs ---

func (h *CatalogHandler) HandleListVendorLevels(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "vendor_id", "cursor", "limit")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var vendor *uuid.UUID
	if vals := q["vendor_id"]; len(vals) > 0 {
		v := &httpx.Validator{}
		if id, ok := v.UUID("vendor_id", &vals[0], true); ok {
			vendor = &id
		} else if verr := v.Err(); verr != nil {
			httpx.WriteError(w, r, verr)
			return
		}
	}
	after, limit, err := catalogPage(r, 50)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.service.repo.ListVendorLevels(r.Context(), vendor, after, limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var next string
	if more && len(items) > 0 {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(catalogOrdering, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, limit)
}

func (h *CatalogHandler) HandleGetVendorLevel(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "vendor level id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	vl, err := h.service.repo.GetVendorLevel(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if vl == nil {
		httpx.WriteError(w, r, httpx.NotFound("no such vendor level"))
		return
	}
	writeCatalogJSON(w, http.StatusOK, vl)
}

type vendorLevelWriteRequest struct {
	VendorID  *string `json:"vendor_id"`
	Code      *string `json:"code"`
	Name      *string `json:"name"`
	IsDefault *bool   `json:"is_default"`
	IsActive  *bool   `json:"is_active"`
	Revision  *int64  `json:"revision"`
}

func (h *CatalogHandler) HandleCreateVendorLevel(w http.ResponseWriter, r *http.Request) {
	var req vendorLevelWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	vl := &VendorLevel{IsActive: true}
	if req.VendorID != nil {
		if id, ok := v.UUID("vendor_id", req.VendorID, true); ok {
			vl.VendorID = id
		}
	} else {
		v.Check(false, "vendor_id", "is required")
	}
	if req.Code != nil {
		vl.Code = strings.ToUpper(strings.TrimSpace(*req.Code))
	}
	v.Check(len(vl.Code) > 0, "code", "is required")
	if req.Name != nil {
		vl.Name = *req.Name
	} else {
		v.Check(false, "name", "is required")
	}
	if req.IsDefault != nil {
		vl.IsDefault = *req.IsDefault
	}
	if req.IsActive != nil {
		vl.IsActive = *req.IsActive
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.CreateVendorLevel(r.Context(), vl); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pricing/vendor-levels/"+vl.ID.String())
	httpx.WriteRevisionETag(w, vl.Revision)
	writeCatalogJSON(w, http.StatusCreated, vl)
}

func (h *CatalogHandler) HandleUpdateVendorLevel(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "vendor level id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req vendorLevelWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	if req.Revision == nil {
		v.Check(false, "revision", "is required on a PUT")
	}
	vl := &VendorLevel{ID: id}
	if req.Name != nil {
		vl.Name = *req.Name
	} else {
		v.Check(false, "name", "is required on a PUT")
	}
	if req.IsDefault != nil {
		vl.IsDefault = *req.IsDefault
	} else {
		v.Check(false, "is_default", "is required on a PUT")
	}
	if req.IsActive != nil {
		vl.IsActive = *req.IsActive
	} else {
		v.Check(false, "is_active", "is required on a PUT")
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.UpdateVendorLevel(r.Context(), vl, *req.Revision); err != nil {
		httpx.WriteError(w, r, catalogWriteErr(err, "vendor level"))
		return
	}
	stored, err := h.service.repo.GetVendorLevel(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, stored.Revision)
	writeCatalogJSON(w, http.StatusOK, stored)
}

func (h *CatalogHandler) HandleListVendorCosts(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "vendor_id", "product_id", "branch_id", "cursor", "limit")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	parse := func(field string) *uuid.UUID {
		if vals := q[field]; len(vals) > 0 {
			if id, ok := v.UUID(field, &vals[0], true); ok {
				return &id
			}
		}
		return nil
	}
	vendor, product, branch := parse("vendor_id"), parse("product_id"), parse("branch_id")
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.checkQueryBranch(r.Context(), branch); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	after, limit, err := catalogPage(r, 50)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.service.repo.ListVendorCosts(r.Context(), vendor, product, branch, middleware.BranchIDForQuery(r.Context()), after, limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var next string
	if more && len(items) > 0 {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(catalogOrdering, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, limit)
}

func (h *CatalogHandler) HandleGetVendorCost(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "vendor cost id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.repo.GetVendorCost(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if c == nil {
		httpx.WriteError(w, r, httpx.NotFound("no such vendor cost"))
		return
	}
	if err := h.checkRowBranch(r.Context(), c.BranchID); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	writeCatalogJSON(w, http.StatusOK, c)
}

type vendorCostWriteRequest struct {
	VendorID               *string          `json:"vendor_id"`
	VendorLevelID          *string          `json:"vendor_price_level_id"`
	ProductID              *string          `json:"product_id"`
	BranchID               *string          `json:"branch_id"`
	PurchaseUOM            *string          `json:"purchase_uom"`
	UnitCostTenThousandths *json.RawMessage `json:"unit_cost_ten_thousandths"`
	VendorSKU              *string          `json:"vendor_sku"`
	EffectiveFrom          *string          `json:"effective_from"`
	EffectiveTo            *string          `json:"effective_to"`
	Revision               *int64           `json:"revision"`
}

func (h *CatalogHandler) HandleCreateVendorCost(w http.ResponseWriter, r *http.Request) {
	var req vendorCostWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.parseVendorCost(&req, &httpx.Validator{}, nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.checkPayloadBranch(r.Context(), c.BranchID); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	if err := h.service.CreateVendorCost(r.Context(), c); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pricing/vendor-costs/"+c.ID.String())
	httpx.WriteRevisionETag(w, c.Revision)
	writeCatalogJSON(w, http.StatusCreated, c)
}

func (h *CatalogHandler) parseVendorCost(req *vendorCostWriteRequest, v *httpx.Validator, id *uuid.UUID) (*VendorCost, error) {
	c := &VendorCost{EffectiveFrom: time.Now().UTC().Format("2006-01-02")}
	if id != nil {
		c.ID = *id
	}
	if req.VendorID != nil {
		if vid, ok := v.UUID("vendor_id", req.VendorID, true); ok {
			c.VendorID = vid
		}
	} else {
		v.Check(false, "vendor_id", "is required")
	}
	if req.VendorLevelID != nil {
		if lid, ok := v.UUID("vendor_price_level_id", req.VendorLevelID, true); ok {
			c.VendorLevelID = lid
		}
	} else {
		v.Check(false, "vendor_price_level_id", "is required")
	}
	if req.ProductID != nil {
		if pid, ok := v.UUID("product_id", req.ProductID, true); ok {
			c.ProductID = pid
		}
	} else {
		v.Check(false, "product_id", "is required")
	}
	if req.BranchID != nil {
		if bid, ok := v.UUID("branch_id", req.BranchID, false); ok {
			c.BranchID = &bid
		}
	}
	if req.PurchaseUOM != nil {
		c.PurchaseUOM = strings.ToUpper(strings.TrimSpace(*req.PurchaseUOM))
	}
	v.Check(len(c.PurchaseUOM) > 0, "purchase_uom", "is required")
	if req.UnitCostTenThousandths != nil {
		if n, ok := v.Int("unit_cost_ten_thousandths", *req.UnitCostTenThousandths, true); ok {
			c.UnitCost = httpx.Price(n)
			checkPrice(v, "unit_cost_ten_thousandths", c.UnitCost)
		}
	} else {
		v.Check(false, "unit_cost_ten_thousandths", "is required")
	}
	if req.VendorSKU != nil {
		sku := *req.VendorSKU
		c.VendorSKU = &sku
	}
	if req.EffectiveFrom != nil {
		c.EffectiveFrom = *req.EffectiveFrom
		v.Check(isDate(c.EffectiveFrom), "effective_from", "must be a calendar date (YYYY-MM-DD)")
	}
	if req.EffectiveTo != nil {
		to := *req.EffectiveTo
		c.EffectiveTo = &to
		v.Check(isDate(to), "effective_to", "must be a calendar date (YYYY-MM-DD)")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return c, nil
}

func isDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func (h *CatalogHandler) HandleUpdateVendorCost(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "vendor cost id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req vendorCostWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	if req.Revision == nil {
		v.Check(false, "revision", "is required on a PUT")
	}
	// The update checks the rule on the row's stored branch and on the new
	// one: a move out of the caller's reach is refused.
	stored, serr := h.service.repo.GetVendorCost(r.Context(), id)
	if serr != nil {
		httpx.WriteError(w, r, serr)
		return
	}
	if stored == nil {
		httpx.WriteError(w, r, httpx.NotFound("no such vendor cost"))
		return
	}
	c, err := h.parseVendorCost(&req, v, &id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.checkBothBranches(r.Context(), stored.BranchID, c.BranchID); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	if err := h.service.UpdateVendorCost(r.Context(), c, *req.Revision); err != nil {
		httpx.WriteError(w, r, catalogWriteErr(err, "vendor cost"))
		return
	}
	out, err := h.service.repo.GetVendorCost(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, out.Revision)
	writeCatalogJSON(w, http.StatusOK, out)
}

// checkBothBranches applies the payload rule to the old branch and the new:
// a null on either side is the every branch reach.
func (h *CatalogHandler) checkBothBranches(ctx context.Context, oldBranch, newBranch *uuid.UUID) error {
	if h.guard == nil {
		return nil
	}
	if oldBranch != nil {
		if err := h.guard.CheckPayloadBranch(ctx, *oldBranch); err != nil {
			return err
		}
	} else if err := h.guard.CheckEveryBranch(ctx); err != nil {
		return err
	}
	return h.checkPayloadBranch(ctx, newBranch)
}

// --- Contracts ---

func (h *CatalogHandler) HandleListContracts(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "customer_id", "product_id", "branch_id", "cursor", "limit")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	parse := func(field string) *uuid.UUID {
		if vals := q[field]; len(vals) > 0 {
			if id, ok := v.UUID(field, &vals[0], true); ok {
				return &id
			}
		}
		return nil
	}
	customer, product, branch := parse("customer_id"), parse("product_id"), parse("branch_id")
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.checkQueryBranch(r.Context(), branch); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	after, limit, err := catalogPage(r, 50)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.service.repo.ListContracts(r.Context(), customer, product, branch, middleware.BranchIDForQuery(r.Context()), after, limit+1)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	var next string
	if more && len(items) > 0 {
		last := items[len(items)-1]
		if next, err = httpx.MintCursor(catalogOrdering, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String()); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, limit)
}

func (h *CatalogHandler) HandleGetContract(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "contract id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.repo.GetContractRow(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if c == nil {
		httpx.WriteError(w, r, httpx.NotFound("no such contract"))
		return
	}
	if err := h.checkRowBranch(r.Context(), c.BranchID); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	writeCatalogJSON(w, http.StatusOK, c)
}

type contractWriteRequest struct {
	CustomerID    *string          `json:"customer_id"`
	ProductID     *string          `json:"product_id"`
	BranchID      *string          `json:"branch_id"`
	ContractPrice *json.RawMessage `json:"contract_price_ten_thousandths"`
	PriceUOM      *string          `json:"price_uom"`
	Revision      *int64           `json:"revision"`
}

func (h *CatalogHandler) HandleCreateContract(w http.ResponseWriter, r *http.Request) {
	var req contractWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	c := &CustomerContract{}
	if req.CustomerID != nil {
		if id, ok := v.UUID("customer_id", req.CustomerID, true); ok {
			c.CustomerID = id
		}
	} else {
		v.Check(false, "customer_id", "is required")
	}
	if req.ProductID != nil {
		if id, ok := v.UUID("product_id", req.ProductID, true); ok {
			c.ProductID = id
		}
	} else {
		v.Check(false, "product_id", "is required")
	}
	if req.BranchID != nil {
		if id, ok := v.UUID("branch_id", req.BranchID, false); ok {
			c.BranchID = &id
		}
	}
	if req.ContractPrice != nil {
		if n, ok := v.Int("contract_price_ten_thousandths", *req.ContractPrice, true); ok {
			c.ContractPrice = httpx.Price(n)
			checkPrice(v, "contract_price_ten_thousandths", c.ContractPrice)
		}
	} else {
		v.Check(false, "contract_price_ten_thousandths", "is required")
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// The price unit is the product's stocking unit (the hold): a body that
	// names another is a 400 naming price_uom.
	facts, err := h.service.repo.ProductPricingFacts(r.Context(), c.ProductID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c.PriceUOM = facts.StockUOM
	if req.PriceUOM != nil {
		if strings.ToUpper(strings.TrimSpace(*req.PriceUOM)) != facts.StockUOM {
			httpx.WriteError(w, r, httpx.BadRequest("price_uom: a price unit other than the stocking unit arrives with C3-2B",
				httpx.FieldError{Field: "price_uom", Message: "is not the product's stocking unit"}))
			return
		}
	}
	if err := h.checkPayloadBranch(r.Context(), c.BranchID); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	if err := h.service.CreateContract(r.Context(), c); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pricing/contracts/"+c.ID.String())
	httpx.WriteRevisionETag(w, c.Revision)
	writeCatalogJSON(w, http.StatusCreated, c)
}

func (h *CatalogHandler) HandleUpdateContract(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "contract id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req contractWriteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	if req.Revision == nil {
		v.Check(false, "revision", "is required on a PUT")
	}
	if req.ContractPrice == nil {
		v.Check(false, "contract_price_ten_thousandths", "is required on a PUT")
	}
	var price int64
	if req.ContractPrice != nil {
		if n, ok := v.Int("contract_price_ten_thousandths", *req.ContractPrice, true); ok {
			price = n
			checkPrice(v, "contract_price_ten_thousandths", httpx.Price(n))
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	stored, err := h.service.repo.GetContractRow(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if stored == nil {
		httpx.WriteError(w, r, httpx.NotFound("no such contract"))
		return
	}
	if err := h.checkBothBranches(r.Context(), stored.BranchID, stored.BranchID); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	updated := &CustomerContract{ID: id, CustomerID: stored.CustomerID, ProductID: stored.ProductID,
		BranchID: stored.BranchID, PriceUOM: stored.PriceUOM, ContractPrice: httpx.Price(price)}
	if err := h.service.UpdateContract(r.Context(), updated, *req.Revision); err != nil {
		httpx.WriteError(w, r, catalogWriteErr(err, "contract"))
		return
	}
	out, err := h.service.repo.GetContractRow(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, out.Revision)
	writeCatalogJSON(w, http.StatusOK, out)
}

func (h *CatalogHandler) HandleDeleteContract(w http.ResponseWriter, r *http.Request) {
	id, err := catalogPathID(r, "contract id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body := map[string]json.RawMessage{}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var revision int64
	if raw, ok := body["revision"]; ok {
		if err := json.Unmarshal(raw, &revision); err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("revision must be an integer",
				httpx.FieldError{Field: "revision", Message: "must be an integer"}))
			return
		}
	} else {
		httpx.WriteError(w, r, httpx.BadRequest("revision: the delete names the revision it read",
			httpx.FieldError{Field: "revision", Message: "is required"}))
		return
	}
	stored, err := h.service.repo.GetContractRow(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if stored == nil {
		httpx.WriteError(w, r, httpx.NotFound("no such contract"))
		return
	}
	if err := h.checkBothBranches(r.Context(), stored.BranchID, stored.BranchID); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	if err := h.service.DeleteContract(r.Context(), id, revision); err != nil {
		httpx.WriteError(w, r, catalogWriteErr(err, "contract"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- The cost read ---

func (h *CatalogHandler) HandleCost(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "vendor_id", "product_id", "branch_id", "vendor_level_id", "on", "uom")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	var vendorID, productID uuid.UUID
	if vals := q["vendor_id"]; len(vals) > 0 {
		vendorID, _ = v.UUID("vendor_id", &vals[0], true)
	} else {
		v.Check(false, "vendor_id", "is required")
	}
	if vals := q["product_id"]; len(vals) > 0 {
		productID, _ = v.UUID("product_id", &vals[0], true)
	} else {
		v.Check(false, "product_id", "is required")
	}
	var branch, level *uuid.UUID
	if vals := q["branch_id"]; len(vals) > 0 {
		if id, ok := v.UUID("branch_id", &vals[0], true); ok {
			branch = &id
		}
	}
	if vals := q["vendor_level_id"]; len(vals) > 0 {
		if id, ok := v.UUID("vendor_level_id", &vals[0], false); ok {
			level = &id
		}
	}
	on := time.Now()
	if vals := q["on"]; len(vals) > 0 {
		d, derr := time.Parse("2006-01-02", vals[0])
		if derr != nil {
			v.Check(false, "on", "must be a calendar date (YYYY-MM-DD)")
		} else {
			on = d
		}
	}
	uom := ""
	if vals := q["uom"]; len(vals) > 0 {
		uom = strings.ToUpper(strings.TrimSpace(vals[0]))
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.checkQueryBranch(r.Context(), branch); err != nil {
		writeCatalogError(w, r, err)
		return
	}
	cost, err := h.engine.ResolveCost(r.Context(), vendorID, productID, branch, level, on, uom)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if cost == nil {
		httpx.WriteError(w, r, httpx.NotFound("no vendor cost in effect for this product on that date"))
		return
	}
	writeCatalogJSON(w, http.StatusOK, cost)
}

// RegisterCatalogRoutes mounts the catalogue routes. roleGuard is applied to
// every route; pass nil in dev mode.
func (h *CatalogHandler) RegisterCatalogRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			g := roleGuard[0]
			return func(w http.ResponseWriter, r *http.Request) {
				g(handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/pricing/levels", guard(h.HandleListLevels))
	mux.HandleFunc("POST /api/v1/pricing/levels", guard(h.HandleCreateLevel))
	mux.HandleFunc("GET /api/v1/pricing/levels/{id}", guard(h.HandleGetLevel))
	mux.HandleFunc("PUT /api/v1/pricing/levels/{id}", guard(h.HandleUpdateLevel))

	mux.HandleFunc("GET /api/v1/pricing/profiles", guard(h.HandleListProfiles))
	mux.HandleFunc("POST /api/v1/pricing/profiles", guard(h.HandleCreateProfile))
	mux.HandleFunc("GET /api/v1/pricing/profiles/{id}", guard(h.HandleGetProfile))
	mux.HandleFunc("PUT /api/v1/pricing/profiles/{id}", guard(h.HandleUpdateProfile))

	mux.HandleFunc("GET /api/v1/pricing/products/{id}/prices", guard(h.HandleListProductPrices))
	mux.HandleFunc("PUT /api/v1/pricing/products/{id}/prices", guard(h.HandleReplaceProductPrices))

	mux.HandleFunc("GET /api/v1/pricing/vendor-levels", guard(h.HandleListVendorLevels))
	mux.HandleFunc("POST /api/v1/pricing/vendor-levels", guard(h.HandleCreateVendorLevel))
	mux.HandleFunc("GET /api/v1/pricing/vendor-levels/{id}", guard(h.HandleGetVendorLevel))
	mux.HandleFunc("PUT /api/v1/pricing/vendor-levels/{id}", guard(h.HandleUpdateVendorLevel))

	mux.HandleFunc("GET /api/v1/pricing/vendor-costs", guard(h.HandleListVendorCosts))
	mux.HandleFunc("POST /api/v1/pricing/vendor-costs", guard(h.HandleCreateVendorCost))
	mux.HandleFunc("GET /api/v1/pricing/vendor-costs/{id}", guard(h.HandleGetVendorCost))
	mux.HandleFunc("PUT /api/v1/pricing/vendor-costs/{id}", guard(h.HandleUpdateVendorCost))

	mux.HandleFunc("GET /api/v1/pricing/contracts", guard(h.HandleListContracts))
	mux.HandleFunc("POST /api/v1/pricing/contracts", guard(h.HandleCreateContract))
	mux.HandleFunc("GET /api/v1/pricing/contracts/{id}", guard(h.HandleGetContract))
	mux.HandleFunc("PUT /api/v1/pricing/contracts/{id}", guard(h.HandleUpdateContract))
	mux.HandleFunc("DELETE /api/v1/pricing/contracts/{id}", guard(h.HandleDeleteContract))

	mux.HandleFunc("GET /api/v1/pricing/cost", guard(h.HandleCost))
}
