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
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/scale"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Handler handles the counter's routes on the wire contract: strict query
// parsing, DecodeJSON bodies, the error envelope, revision preconditions
// and list envelopes (the recipe's handler step).
type Handler struct {
	service *Service
	guard   BranchGuard // optional; see WithBranchGuard
	// scaleSim, when set, is the simulated scale (CP-19): its control route
	// mounts only with it.
	scaleSim *scale.Simulator
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
	// The variance pair's own guard: every role the claims path allowed
	// (finance and owner, the resolve mount's) plus the counter's own, so a
	// cashier can reach the queue and spend a manager's step up approval
	// (CP-04 slice 3) while a signed in finance manager loses nothing.
	signoffMount := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 2 && roleGuard[2] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[2](handler).ServeHTTP(w, r)
			}
		}
		return guard(handler)
	}

	// The offline policy (CP-13 O1, ADR 0011 section 4): writing it is an
	// admin act, so the write mounts on admin and owner; the read adds
	// finance. Fourth and fifth guards, else the roles' own check stands.
	policyWrite := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 3 && roleGuard[3] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[3](handler).ServeHTTP(w, r)
			}
		}
		return middleware.RequireRole("admin", "owner")(handler).ServeHTTP
	}
	policyRead := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 4 && roleGuard[4] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[4](handler).ServeHTTP(w, r)
			}
		}
		return middleware.RequireRole("admin", "owner", "finance")(handler).ServeHTTP
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
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/overrides", guard(h.ApproveOverrideHandler))
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/signature", guard(h.CaptureSignatureHandler))

	// History and search
	mux.HandleFunc("GET /api/v1/pos/products/search", guard(h.SearchProducts))
	mux.HandleFunc("GET /api/v1/pos/tiles", guard(h.ListTiles))

	// The counter's customer: the lookup with the account's standing, and
	// naming the customer (and the account's purchase order and job) on an
	// open sale.
	mux.HandleFunc("GET /api/v1/pos/customers", guard(h.FindCustomers))
	mux.HandleFunc("GET /api/v1/pos/customers/{id}/standing", guard(h.GetStanding))
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
	// The variance sign off (CP-08): the queue and the release sit behind the
	// variance pair's own guard (the counter's roles plus finance, the claims
	// path's own), because a manager may step up with their counter PIN at
	// the terminal (CP-04 slice 3); the claims path keeps the finance and
	// owner rule in the handler itself, and the policy routes keep the
	// resolve mount's.
	mux.HandleFunc("GET /api/v1/pos/till/signoffs", signoffMount(h.ListPendingSignoffs))
	mux.HandleFunc("POST /api/v1/pos/till/{id}/signoff", signoffMount(h.SignOffTill))
	mux.HandleFunc("GET /api/v1/pos/till/policy", resolveMount(h.GetVariancePolicy))
	mux.HandleFunc("PUT /api/v1/pos/till/policy", resolveMount(h.PutVariancePolicy))
	mux.HandleFunc("GET /api/v1/pos/offline/policy", policyRead(h.GetOfflinePolicy))
	mux.HandleFunc("PUT /api/v1/pos/offline/policy", policyWrite(h.PutOfflinePolicy))
	// A register's device (CP-13 O2, ADR 0011 section 6.1): enrolment and
	// revocation are owner or admin acts with the counter PIN, on the policy
	// write's mount; the read adds finance, on the policy read's.
	mux.HandleFunc("GET /api/v1/pos/offline/registers/{id}/device", policyRead(h.GetRegisterDevice))
	mux.HandleFunc("POST /api/v1/pos/offline/registers/{id}/device", policyWrite(h.EnrolRegisterDevice))
	mux.HandleFunc("POST /api/v1/pos/offline/registers/{id}/device/revoke", policyWrite(h.RevokeRegisterDevice))

	// The manager step up (CP-04 slice 3): the roster the picker shows, and
	// the PIN verified approval one act spends. Any person at the counter may
	// ask; the manager's PIN is the authority.
	mux.HandleFunc("POST /api/v1/pos/step-up", guard(h.StepUpHandler))
	mux.HandleFunc("GET /api/v1/pos/step-up/managers", guard(h.StepUpManagersHandler))

	// The end of day by branch (CP-09) is the managers' (finance and owner): the
	// summary, the close and the deposit match.
	mux.HandleFunc("GET /api/v1/pos/end-of-day", resolveMount(h.GetBranchDay))
	mux.HandleFunc("POST /api/v1/pos/end-of-day/close", resolveMount(h.CloseBranchDay))
	mux.HandleFunc("GET /api/v1/pos/end-of-day/{id}/deposit-candidates", resolveMount(h.ListDepositCandidates))
	mux.HandleFunc("POST /api/v1/pos/end-of-day/{id}/deposit-match", resolveMount(h.MatchDeposit))
	mux.HandleFunc("POST /api/v1/pos/end-of-day/{id}/reopen", resolveMount(h.ReopenBranchDay))

	// The scale (CP-19): the reading, the weigh in and weigh out, the
	// tickets with their void, the manual entry's closed reasons, and the
	// manual weight on a line.
	mux.HandleFunc("GET /api/v1/pos/scale", guard(h.GetScaleStatus))
	mux.HandleFunc("POST /api/v1/pos/scale/weigh-in", guard(h.WeighIn))
	mux.HandleFunc("POST /api/v1/pos/scale/weigh-out", guard(h.WeighOut))
	mux.HandleFunc("GET /api/v1/pos/scale/tickets", guard(h.ListScaleTickets))
	mux.HandleFunc("GET /api/v1/pos/scale/tickets/{id}", guard(h.GetScaleTicket))
	mux.HandleFunc("POST /api/v1/pos/scale/tickets/{id}/void", guard(h.VoidScaleTicket))
	mux.HandleFunc("GET /api/v1/pos/scale/manual-reasons", guard(h.ListManualWeightReasons))
	mux.HandleFunc("POST /api/v1/pos/transactions/{id}/items/{itemId}/weight", guard(h.EnterLineWeight))
	if h.scaleSim != nil {
		mux.HandleFunc("PUT /api/v1/pos/scale/simulator", guard(h.DriveScaleSimulator))
	}

	// Returns
	mux.HandleFunc("POST /api/v1/pos/returns", guard(h.CreateReturn))
	mux.HandleFunc("POST /api/v1/pos/returns/preview", guard(h.PreviewReturn))
	mux.HandleFunc("GET /api/v1/pos/return-reasons", guard(h.ListReturnReasons))
	// The counter's dealer settings (CP-04): the read is the counter's own
	// (the terminal asks for the ID before Pay), the write the admin and
	// owner roles', held in the handler as the override's roles are.
	mux.HandleFunc("GET /api/v1/pos/settings", guard(h.GetPosSettingsHandler))
	mux.HandleFunc("PUT /api/v1/pos/settings", guard(h.PutPosSettingsHandler))
	mux.HandleFunc("POST /api/v1/pos/exchanges", guard(h.CreateExchange))
	mux.HandleFunc("POST /api/v1/pos/exchanges/preview", guard(h.PreviewExchange))
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
	// The manager's list of the claims still waiting on a person (CP-06):
	// the resolve routes' own mount, roles and wall.
	mux.HandleFunc("GET /api/v1/pos/refund-intents", resolveMount(h.ListRefundIntents))
	// The card terminal's intent routes (CP-05, IX-01): the desk's waiting
	// state and its honest cancel behind the counter's own guard, and the
	// sweep and the manager's lost answer queue behind the finance and owner
	// resolution mount (a machine key is refused in the route itself),
	// every one behind the branch middleware like every money route here.
	mux.HandleFunc("GET /api/v1/pos/terminal-intents/{id}", guard(h.GetTerminalIntentHandler))
	mux.HandleFunc("POST /api/v1/pos/terminal-intents/{id}/cancel", guard(h.CancelTerminalIntentHandler))
	mux.HandleFunc("POST /api/v1/pos/terminal-intents/sweep", resolveMount(h.SweepTerminalIntentsHandler))
	mux.HandleFunc("GET /api/v1/pos/terminal-lost-items", resolveMount(h.ListTerminalLostItemsHandler))
	mux.HandleFunc("POST /api/v1/pos/terminal-lost-items/{id}/resolve", resolveMount(h.ResolveTerminalLostItemHandler))

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
// key. When the caller's email matches a roster row, the roster row's id is
// the cashier id so the counter's person rules compare like with like.
func (h *Handler) cashierOf(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if refuseMachineKeyCashier(w, r) {
		return uuid.Nil, false
	}
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		if id := h.service.StaffIDByEmail(r.Context(), claims.Email); id != uuid.Nil {
			return id, true
		}
		if claims.Subject != "" {
			if id, err := uuid.Parse(claims.Subject); err == nil {
				return id, true
			}
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
	cashierID, ok := h.cashierOf(w, r)
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
	// The cashier is the authenticated caller: a body cashier_id the caller
	// named for another is not recorded, so the separation of duties the
	// override holds (the caller who started the sale cannot approve it) rests
	// on a subject the caller cannot choose.
	if cashierID != uuid.Nil {
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
	// A completed sale carries what of each line earlier returns brought
	// back, so the return dialog can hold a fully returned line back (CP-06).
	if sale.Status == StatusCompleted {
		returned, err := h.service.repo.ReturnedQtyByLine(r.Context(), id)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		if len(returned) > 0 {
			sale.ReturnedQuantities = map[string]httpx.Quantity{}
			for lineID, qty := range returned {
				sale.ReturnedQuantities[lineID.String()] = qty
			}
		}
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
	sale, err := h.service.AddLine(r.Context(), id, parsed, h.actorFrom(r))
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
	sale, err := h.service.ChangeLineQuantity(r.Context(), id, itemID, qty, r.Header.Get("If-Match"), revision, h.actorFrom(r))
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
	tenders, pickedUpBy, revision, acknowledged, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sale, err := h.service.CompleteSale(r.Context(), id, r.Header.Get("If-Match"), revision, tenders, pickedUpBy, h.actorFrom(r), acknowledged, req.payer(), req.suspectedAvoidance(), req.payerDeclined())
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
	sale, err := h.service.VoidSale(r.Context(), id, r.Header.Get("If-Match"), revision, reason, h.actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// ListTransactions is GET /pos/transactions: the list envelope. q is the
// counter's sale search (CP-06): the sale number (the receipt's barcode
// text), the invoice number or the customer name; a search names no day
// bound unless it carries a date, while a plain list stays the register's
// day.
func (h *Handler) ListTransactions(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "register_id", "date", "status", "q", "limit", "cursor", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := SaleFilter{RegisterID: q.Get("register_id"), Query: strings.TrimSpace(q.Get("q"))}
	if d := q.Get("date"); d != "" {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "date", Message: "must be a date as YYYY-MM-DD"}))
			return
		}
		f.Date = d
	}
	if f.Query == "" {
		// The plain list's own day is the branch's today, computed in the
		// branch's zone in SQL, never the server's clock.
		f.Today = true
	}
	if s := q.Get("status"); s != "" {
		switch s {
		case "open", "held", "completed", "voided", "abandoned":
			f.Status = uppercase(s)
		default:
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "status", Message: "must be one of: open, held, completed, voided, abandoned"}))
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

// ListRefundIntents is GET /pos/refund-intents: the manager's list of the
// refund claims still waiting on a person (CP-06), under the resolve
// routes' own guard.
func (h *Handler) ListRefundIntents(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "limit", "cursor", "include"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	limit := 50
	items, err := h.service.repo.ListOpenRefundIntents(r.Context(), limit)
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

// GetStanding is GET /pos/customers/{id}/standing: can a sale of amount_cents
// go on this account, and what are the ways forward when it cannot. The
// optional sale_id makes the verdict count the sale's live manager override.
func (h *Handler) GetStanding(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	q, err := httpx.StrictQuery(r, "amount_cents", "sale_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var bad []httpx.FieldError
	var amount int64
	if q.Has("amount_cents") {
		n, convErr := strconv.ParseInt(q.Get("amount_cents"), 10, 64)
		if convErr != nil || n < 0 {
			bad = append(bad, httpx.FieldError{Field: "amount_cents", Message: "must be a whole number of cents, zero or more"})
		} else {
			amount = n
		}
	}
	var saleID *uuid.UUID
	if q.Has("sale_id") {
		sid, convErr := uuid.Parse(q.Get("sale_id"))
		if convErr != nil {
			bad = append(bad, httpx.FieldError{Field: "sale_id", Message: "must be a UUID"})
		} else {
			saleID = &sid
		}
	}
	if len(bad) > 0 {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", bad...))
		return
	}
	out, err := h.service.Standing(r.Context(), id, amount, saleID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	sale, err := h.service.AttachCustomer(r.Context(), id, *in, h.actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// SyncOffline is POST /pos/sync: refused by rule. Money never queues
// offline, so a batch that parses answers 409 money_never_queues and the
// service never runs: no sale, invoice, payment or sync log row of it is
// written. The malformed shapes keep their 400 and the branch and customer
// wall middlewares ahead of this handler keep their refusals.
func (h *Handler) SyncOffline(w http.ResponseWriter, r *http.Request) {
	var req offlineSyncRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if _, err := req.parse(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteError(w, r, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeMoneyNeverQueues,
		Message: "money never queues offline: complete the sale online"})
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
	cashierID, ok := h.cashierOf(w, r)
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
	// The open session's expected drawer is blind to a cashier (CP-08): the
	// float and the expected figures wait for the close, when the count is
	// already recorded.
	if session.Status == TillOpen && !canViewOpenTillExpectation(r.Context()) {
		session.ExpectedByMethod = map[string]int64{}
		session.OpeningFloat = 0
	}
	writeJSON(w, http.StatusOK, session)
}

// canViewOpenTillExpectation reports whether the caller may read the money
// figures an open drawer still keeps blind (CP-08): the roles that can sign
// a held count off. No claims at all (an unauthenticated dev mode harness,
// or a test request that named a subject but no role) carries nothing to
// hide from and reads them.
func canViewOpenTillExpectation(ctx context.Context) bool {
	claims := middleware.ClaimsFromContext(ctx)
	if claims == nil {
		return true
	}
	if claims.Role == "" && len(claims.Roles) == 0 {
		return true
	}
	return middleware.ClaimsHaveAnyRole(claims, "finance", "owner", "admin")
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
	// The close is blind (CP-08): while the drawer is open a cashier reads the
	// live report without the figures the expected drawer is built from (the
	// target itself, the float, the tendered cash, the movement net), so the
	// count cannot be written to match them. A manager (or any caller past
	// the close, when the count is already recorded) reads the figures in
	// full.
	if report.Session.Status == TillOpen && !canViewOpenTillExpectation(r.Context()) {
		report.ExpectedByMethod = map[string]int64{}
		report.Session.ExpectedByMethod = map[string]int64{}
		report.Session.OpeningFloat = 0
		report.TenderedByMethod = map[string]int64{}
		report.SalesTotalCents = 0
		report.ChangeCents = 0
		report.RefundsByMethod = map[string]int64{}
		report.MovementTotalsByKind = map[string]int64{}
		report.Movements = []TillMovement{}
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
	report, err := h.service.CloseTill(r.Context(), id, counted, notes, h.actorFrom(r))
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
	// no date is each report's branch's today, read in the branch's own zone
	date := q.Get("date")
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "date", Message: "must be a date as YYYY-MM-DD"}))
			return
		}
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
	cashierID, ok := h.cashierOf(w, r)
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

// PreviewReturn is POST /pos/returns/preview: a read-only calculation of the
// same request's figures (the restocking fee and the money back), so the
// counter states them before the return is taken; nothing is written.
func (h *Handler) PreviewReturn(w http.ResponseWriter, r *http.Request) {
	cashierID, ok := h.cashierOf(w, r)
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
	fig, err := h.service.PreviewReturn(r.Context(), cashierID, in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, fig)
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
	resolved, err := h.service.ResolveRefundIntent(r.Context(), id, *in, h.actorFrom(r))
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
	paid, err := h.service.PaybackRefundIntent(r.Context(), id, *in, h.actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, paid)
}

// actorFrom reads the acting user's identity for audit rows and person rules:
// the roster row's id when the signed in email matches one, or the JWT subject.
func (h *Handler) actorFrom(r *http.Request) string {
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		if id := h.service.StaffIDByEmail(r.Context(), claims.Email); id != uuid.Nil {
			return id.String()
		}
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
