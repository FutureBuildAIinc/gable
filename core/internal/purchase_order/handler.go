// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
	guard   BranchGuard // optional; nil leaves a payload location and a path id unchecked (unit tests)
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// BranchGuard applies the payload branch rule (ADR 0007 section 2.3) to a
// branch or location a request names, in the body or behind a path id.
// *middleware.BranchGuard satisfies it.
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
	CheckPayloadLocation(ctx context.Context, locationID uuid.UUID) error
}

// WithBranchGuard makes receiving refuse a location whose branch the caller
// may not target, and makes every path id route refuse a purchase order of a
// branch the caller may not target. Without it neither is checked, so serve
// always sets it.
func (h *Handler) WithBranchGuard(g BranchGuard) *Handler {
	h.guard = g
	return h
}

// checkPOBranch holds a purchase order addressed by its path id to the
// caller's branch wall (ADR 0007 section 2.3): the record's branch must be
// one the caller may target, the same rule a branch named in a body is held
// to. A purchase order that does not exist belongs to no branch and passes;
// the service answers for it. A refusal is the contract's 403 error
// envelope. It reports whether the request may proceed.
func (h *Handler) checkPOBranch(w http.ResponseWriter, r *http.Request, id uuid.UUID) bool {
	if h.guard == nil {
		return true
	}
	branch, err := h.service.GetPOBranch(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return false
	}
	if branch == nil {
		return true
	}
	if err := h.guard.CheckPayloadBranch(r.Context(), *branch); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httpx.WriteError(w, r, httpx.Forbidden("purchase order is outside the branches this caller may target"))
			return false
		}
		httpx.WriteError(w, r, err)
		return false
	}
	return true
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

	mux.HandleFunc("GET /api/v1/purchase-orders", guard(h.HandleListPOs))
	mux.HandleFunc("POST /api/v1/purchase-orders", guard(h.HandleCreatePO))
	mux.HandleFunc("GET /api/v1/purchase-orders/recommendations", guard(h.HandleListRecommendations))
	mux.HandleFunc("GET /api/v1/purchase-orders/source-summary", guard(h.HandleSourceSummary))
	mux.HandleFunc("GET /api/v1/purchase-orders/{id}", guard(h.HandleGetPO))
	mux.HandleFunc("POST /api/v1/purchase-orders/{id}/submit", guard(h.HandleSubmitPO))
	mux.HandleFunc("POST /api/v1/purchase-orders/{id}/receive", guard(h.HandleReceivePO))
	mux.HandleFunc("POST /api/v1/purchase-orders/reorder-check", guard(h.HandleCreateReorders))
	mux.HandleFunc("POST /api/v1/purchase-orders/refresh-reorder-targets", guard(h.HandleRefreshReorderTargets))
	mux.HandleFunc("GET /api/v1/purchase-orders/reorder-runs", guard(h.HandleListReorderRuns))
	mux.HandleFunc("POST /api/v1/purchase-orders/{id}/freight", guard(h.HandleUploadFreight))
	mux.HandleFunc("POST /api/v1/purchase-orders/{id}/freight/{freightId}/apply", guard(h.HandleApplyFreight))
	mux.HandleFunc("GET /api/v1/purchase-orders/{id}/freight", guard(h.HandleListFreight))
}

// parseID reads the path id into a 400 that names it.
func parseID(r *http.Request, w http.ResponseWriter) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return uuid.Nil, false
	}
	return id, true
}

// poOrdering is the purchase order list's cursor scope.
const poOrdering = "purchase_orders.created_at_id_desc"

// poQuery is the parsed list request.
type poQuery struct {
	filters    ListFilter
	limit      int
	wantTotal  bool
}

// parsePOQuery reads the list request: the strict guard on the route's
// names (cursor, limit, include and the two filters), the page and the
// cursor's keyset position.
func parsePOQuery(r *http.Request) (poQuery, error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status", "vendor_id")
	if err != nil {
		return poQuery{}, err
	}
	page, err := httpx.ParseListQuery(r, poOrdering)
	if err != nil {
		return poQuery{}, err
	}
	out := poQuery{limit: page.Limit}
	out.filters.Limit = page.Limit
	if page.Key != nil {
		if len(page.Key) != 2 {
			return poQuery{}, httpx.BadRequest("cursor keyset is malformed",
				httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return poQuery{}, httpx.BadRequest("cursor keyset is malformed",
				httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
		}
		out.filters.AfterTime, out.filters.AfterID = &at, &id
	}
	v := &httpx.Validator{}
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			return poQuery{}, httpx.BadRequest("include parameter is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"})
		}
		set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal)
		if ierr != nil {
			return poQuery{}, ierr
		}
		out.wantTotal = set.Has(httpx.IncludeTotal)
	}
	if vals := q["status"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "status", "parameter is repeated")
		} else if status, ok := ParseStatus(vals[0]); ok {
			out.filters.Statuses = []Status{status}
		} else {
			v.Check(false, "status", "must be one of draft, sent, partial, received, cancelled")
		}
	}
	if vals := q["vendor_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "vendor_id", "parameter is repeated")
		} else if id, ok := v.UUID("vendor_id", &vals[0], true); ok {
			out.filters.VendorID = &id
		}
	}
	if err := v.Err(); err != nil {
		return poQuery{}, err
	}
	return out, nil
}

// HandleListPOs serves GET /api/v1/purchase-orders: the list envelope,
// filtered by status and vendor, held to the caller's branch wall by the
// repository's three arm predicate (ADR 0008 section 11).
func (h *Handler) HandleListPOs(w http.ResponseWriter, r *http.Request) {
	query, err := parsePOQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pos, more, err := h.service.ListPOs(r.Context(), query.filters)
	if err != nil {
		slog.Error("ListPOs failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if more && len(pos) > 0 {
		last := pos[len(pos)-1]
		next, err = httpx.MintCursor(poOrdering,
			httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	opts := []httpx.ListOption{}
	if query.wantTotal {
		total, err := h.service.CountPOs(r.Context(), query.filters)
		if err != nil {
			slog.Error("CountPOs failed", "error", err)
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(total))
	}
	httpx.WriteList(w, pos, next, query.limit, opts...)
}

// HandleCreatePO serves POST /api/v1/purchase-orders: the parsed and
// validated body, the payload branch rule, one transaction, 201 with the
// document, its ETag and Location.
func (h *Handler) HandleCreatePO(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := ParseCreate(&req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if h.guard != nil && draft.BranchID != nil {
		if err := h.guard.CheckPayloadBranch(r.Context(), *draft.BranchID); err != nil {
			if errors.Is(err, middleware.ErrPayloadBranchRefused) {
				httpx.WriteError(w, r, httpx.Forbidden("branch_id is outside the branches this caller may target"))
				return
			}
			httpx.WriteError(w, r, err)
			return
		}
	}
	po, err := h.service.CreatePO(r.Context(), draft)
	if err != nil {
		slog.Error("CreatePO failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/purchase-orders/"+po.ID.String())
	httpx.WriteRevisionETag(w, po.Revision)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(po)
}

func (h *Handler) HandleGetPO(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, w)
	if !ok {
		return
	}
	if !h.checkPOBranch(w, r, id) {
		return
	}
	po, err := h.service.GetPO(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, po.Revision)
	json.NewEncoder(w).Encode(po)
}

// HandleSubmitPO serves POST /api/v1/purchase-orders/{id}/submit: the
// revision precondition beside If-Match, the status checked under the lock,
// the sent event in the same transaction, and the document back with its
// new revision and ETag.
func (h *Handler) HandleSubmitPO(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, w)
	if !ok {
		return
	}
	if !h.checkPOBranch(w, r, id) {
		return
	}
	var req SubmitRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	revision, err := ParseRevision(req.Revision)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	po, err := h.service.SubmitPO(r.Context(), id, r.Header.Get("If-Match"), revision)
	if err != nil {
		slog.Error("SubmitPO failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, po.Revision)
	json.NewEncoder(w).Encode(po)
}

// HandleReceivePO serves POST /api/v1/purchase-orders/{id}/receive: the
// purchase order's branch held to the caller's wall by the path id, each
// body location held by the payload rule, the over receipt cap and the
// same branch rule inside the act, and the document back with its new
// revision and ETag.
func (h *Handler) HandleReceivePO(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, w)
	if !ok {
		return
	}
	if !h.checkPOBranch(w, r, id) {
		return
	}
	var req ReceiveRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := ParseReceive(&req)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if h.guard != nil {
		for _, l := range draft.Lines {
			err := h.guard.CheckPayloadLocation(r.Context(), l.LocationID)
			if errors.Is(err, middleware.ErrPayloadBranchRefused) {
				httpx.WriteError(w, r, httpx.Forbidden("lines[].location_id is in a branch this caller may not target"))
				return
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
		}
	}
	po, err := h.service.ReceivePO(r.Context(), id, r.Header.Get("If-Match"), draft.Revision, draft.Lines)
	if err != nil {
		slog.Error("ReceivePO failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, po.Revision)
	json.NewEncoder(w).Encode(po)
}

func (h *Handler) HandleCreateReorders(w http.ResponseWriter, r *http.Request) {
	count, err := h.service.CreateReorders(r.Context())
	if err != nil {
		slog.Error("CreateReorders failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"count":  count,
	})
}

// HandleRefreshReorderTargets manually triggers the reorder-target recompute
// (the same logic the scheduler runs on cron). Body: {"dry_run": bool,
// "lookback_days": int} — both optional. Defaults to dry_run=true so the
// curl-once-and-look workflow can't accidentally rewrite the targets.
//
// The recompute reads every branch's sales and stock per product and branch
// (ADR 0008 10.2), the seam the cron path always was: the handler strips the
// BranchContext the middleware put on the request and marks the resulting
// context a system caller, so a bound user's refresh writes the same
// per branch targets an administrator's would.
func (h *Handler) HandleRefreshReorderTargets(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), branchctx.Key, (*branchctx.Context)(nil))
	ctx = branchctx.WithSystem(ctx)

	var req struct {
		DryRun       *bool `json:"dry_run"`
		LookbackDays int   `json:"lookback_days"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}
	result, err := h.service.RefreshReorderTargets(ctx, dryRun, req.LookbackDays)
	if err != nil {
		slog.Error("RefreshReorderTargets failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// HandleListReorderRuns returns the most recent reorder-cron executions
// (refresh_targets and create_reorders) for the operator dashboard, in the
// list envelope.
func (h *Handler) HandleListReorderRuns(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "cursor", "limit", "include"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	runs, err := h.service.ListReorderRuns(r.Context(), 50)
	if err != nil {
		slog.Error("ListReorderRuns failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	if runs == nil {
		runs = []ReorderRun{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": runs, "next_cursor": nil, "limit": 50})
}

// HandleSourceSummary returns PO counts grouped by source so the purchasing
// dashboard can render the "% replenishments automated" KPI. An aggregate,
// not a collection: it keeps its object shape.
func (h *Handler) HandleSourceSummary(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	counts, err := h.service.GetSourceSummary(r.Context())
	if err != nil {
		slog.Error("GetSourceSummary failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
}

// recOrdering is the stored recommendations list's cursor scope.
const recOrdering = "reorder_recommendations.created_at_id_desc"

// HandleListRecommendations serves GET /api/v1/purchase-orders/recommendations:
// the stored recommendations of the reorder runs in the list envelope (ADR
// 0008 10.3), filtered by branch, vendor and status, held to the caller's
// branch wall.
func (h *Handler) HandleListRecommendations(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "branch_id", "vendor_id", "status")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, recOrdering)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var filters RecommendationFilter
	filters.Limit = page.Limit
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, httpx.BadRequest("cursor keyset is malformed",
				httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"}))
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			httpx.WriteError(w, r, httpx.BadRequest("cursor keyset is malformed",
				httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"}))
			return
		}
		filters.AfterTime, filters.AfterID = &at, &id
	}
	wantTotal := false
	v := &httpx.Validator{}
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
	if vals := q["branch_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "branch_id", "parameter is repeated")
		} else if id, ok := v.UUID("branch_id", &vals[0], true); ok {
			filters.BranchID = &id
		}
	}
	if vals := q["vendor_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "vendor_id", "parameter is repeated")
		} else if id, ok := v.UUID("vendor_id", &vals[0], true); ok {
			filters.VendorID = &id
		}
	}
	if vals := q["status"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "status", "parameter is repeated")
		} else {
			switch vals[0] {
			case "open", "ordered", "dismissed":
				filters.Status = map[string]string{"open": "OPEN", "ordered": "ORDERED", "dismissed": "DISMISSED"}[vals[0]]
			default:
				v.Check(false, "status", "must be one of open, ordered, dismissed")
			}
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	recs, more, err := h.service.repo.ListRecommendationsPage(r.Context(), filters)
	if err != nil {
		slog.Error("ListRecommendationsPage failed", "error", err)
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if more && len(recs) > 0 {
		last := recs[len(recs)-1]
		next, err = httpx.MintCursor(recOrdering,
			httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	opts := []httpx.ListOption{}
	if wantTotal {
		total, err := h.service.repo.CountRecommendations(r.Context(), filters)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		opts = append(opts, httpx.WithTotal(total))
	}
	httpx.WriteList(w, recs, next, page.Limit, opts...)
}

// HandleUploadFreight processes a freight invoice upload for a received PO.
func (h *Handler) HandleUploadFreight(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r, w)
	if !ok {
		return
	}
	if !h.checkPOBranch(w, r, id) {
		return
	}

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("file too large or invalid form data"))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("missing 'file' field in form data"))
		return
	}
	defer file.Close()

	fileBytes, err := io.ReadAll(io.LimitReader(file, 10<<20))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	contentType := http.DetectContentType(fileBytes)

	slog.Info("Freight invoice upload",
		"po_id", id,
		"filename", header.Filename,
		"size_bytes", header.Size,
		"content_type", contentType,
	)

	result, err := h.service.UploadFreightInvoice(r.Context(), id, fileBytes, contentType, header.Filename)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// HandleApplyFreight applies a pending freight charge to product costs.
func (h *Handler) HandleApplyFreight(w http.ResponseWriter, r *http.Request) {
	poID, ok := parseID(r, w)
	if !ok {
		return
	}
	if !h.checkPOBranch(w, r, poID) {
		return
	}

	freightID, err := uuid.Parse(r.PathValue("freightId"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("freightId is not a UUID",
			httpx.FieldError{Field: "freightId", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}

	if err := h.service.ApplyFreightCharge(r.Context(), poID, freightID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "applied"})
}

// HandleListFreight returns all freight charges for a PO, in the list
// envelope.
func (h *Handler) HandleListFreight(w http.ResponseWriter, r *http.Request) {
	poID, ok := parseID(r, w)
	if !ok {
		return
	}
	if !h.checkPOBranch(w, r, poID) {
		return
	}

	charges, err := h.service.GetFreightCharges(r.Context(), poID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	httpx.WriteList(w, charges, "", 0)
}
