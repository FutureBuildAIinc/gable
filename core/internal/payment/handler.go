// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"encoding/json"
	"errors"
	"github.com/gablelbm/gable/internal/account"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The list's ordering: created_at then id, newest first (ADR 0001 section 2).
const cursorScope = "payments.created_at_id_desc"

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts the payment routes (ADR 0005 section 9.4).
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("GET /api/v1/payments", guard(h.HandleList))
	mux.HandleFunc("POST /api/v1/payments", guard(h.HandleCreate))
	mux.HandleFunc("GET /api/v1/payments/{id}", guard(h.HandleGet))
	mux.HandleFunc("POST /api/v1/payments/{id}/applications", guard(h.HandleApply))
	mux.HandleFunc("POST /api/v1/payments/{id}/transitions", guard(h.HandleTransition))
	mux.HandleFunc("POST /api/v1/payments/{id}/refunds", guard(h.HandleRefund))
	mux.HandleFunc("POST /api/v1/credit-memos/{id}/refunds", guard(h.HandleCreditRefund))

	// Run Payments gateway routes
	mux.HandleFunc("POST /api/v1/payments/intent", guard(h.HandleIntent))
	mux.HandleFunc("POST /api/v1/payments/card", guard(h.HandleCard))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid "+what+" id", httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

func caller(r *http.Request) Caller {
	if claims, ok := r.Context().Value(middleware.UserContextKey).(*middleware.UserClaims); ok && claims != nil {
		return Caller{Actor: claims.Subject, Role: account.RoleOf(claims)}
	}
	return Caller{}
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func single(v *httpx.Validator, q map[string][]string, name string) (string, bool) {
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

func uuidParam(v *httpx.Validator, q map[string][]string, name string) *uuid.UUID {
	raw, ok := single(v, q, name)
	if !ok {
		return nil
	}
	if id, ok := v.UUID(name, &raw, true); ok {
		return &id
	}
	return nil
}

// parseListFilter reads the list's filters (ADR 0005 9.4): customer_id, status,
// unapplied, order_id, job_id and method beside the platform's cursor, limit
// and include. Unknown parameters and uppercase values are refused.
func parseListFilter(r *http.Request) (f ListFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "customer_id", "status", "unapplied", "order_id", "job_id", "method")
	if err != nil {
		return f, false, err
	}
	page, err := httpx.ParseListQuery(r, cursorScope)
	if err != nil {
		return f, false, err
	}
	f.Limit = page.Limit
	v := &httpx.Validator{}
	f.CustomerID = uuidParam(v, q, "customer_id")
	f.OrderID = uuidParam(v, q, "order_id")
	f.JobID = uuidParam(v, q, "job_id")
	if raw, ok := single(v, q, "status"); ok {
		for _, name := range strings.Split(raw, ",") {
			st, ok := ParseStatus(name)
			if !ok {
				v.Check(false, "status", "must be a comma separated list of: posted, voided")
				break
			}
			f.Statuses = append(f.Statuses, st)
		}
	}
	if raw, ok := single(v, q, "method"); ok {
		for _, name := range strings.Split(raw, ",") {
			if m, ok := ParseMethod(name); ok {
				f.Methods = append(f.Methods, m)
			} else if name == "card" {
				f.Methods = append(f.Methods, PaymentMethodCard)
			} else if name == "account" {
				f.Methods = append(f.Methods, PaymentMethodAccount)
			} else {
				v.Check(false, "method", "must be a comma separated list of: cash, card, check, ach, other, account")
				break
			}
		}
	}
	if raw, ok := single(v, q, "unapplied"); ok {
		switch raw {
		case "true":
			t := true
			f.Unapplied = &t
		case "false":
			t := false
			f.Unapplied = &t
		default:
			v.Check(false, "unapplied", "must be true or false")
		}
	}
	if raw, ok := single(v, q, "include"); ok {
		set, ierr := httpx.ParseInclude(raw)
		if ierr != nil {
			return f, false, ierr
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if err := v.Err(); err != nil {
		return f, false, err
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return f, false, cursorError()
		}
		t, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return f, false, cursorError()
		}
		f.AfterTime, f.AfterID = &t, id
	}
	return f, wantTotal, nil
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	f, wantTotal, err := parseListFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.List(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next, err = httpx.MintCursor(cursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if total != nil {
		opts = append(opts, httpx.WithTotal(*total))
	}
	httpx.WriteList(w, items, next, f.Limit, opts...)
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "payment")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, p.Revision)
	writeJSON(w, http.StatusOK, p)
}

func writeCreated(w http.ResponseWriter, p *Payment) {
	httpx.WriteRevisionETag(w, p.Revision)
	w.Header().Set("Location", "/api/v1/payments/"+p.ID.String())
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req CreateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.Create(r.Context(), in, caller(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	full, err := h.service.Get(r.Context(), p.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCreated(w, full)
}

// HandleIntent returns the Run Payments public key for Runner.js tokenization.
// The frontend calls this before showing the card input form.
func (h *Handler) HandleIntent(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req struct {
		AmountCents json.RawMessage `json:"amount_cents"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	amount, _ := v.Int("amount_cents", req.AmountCents, true)
	v.Check(amount >= 1 || req.AmountCents == nil, "amount_cents", "must be 1 cent or more")
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	publicKey := h.service.GetPublicKey()
	if publicKey == "" {
		httpx.WriteError(w, r, httpx.Unavailable("payment gateway not configured"))
		return
	}
	writeJSON(w, http.StatusOK, PaymentIntentResponse{PublicKey: publicKey, AmountCents: httpx.Cents(amount)})
}

// HandleCard handles tokenized card payments through Run Payments. Flow:
// frontend tokenizes via Runner.js, sends the token here, we charge via the
// gateway. A charge the system refused to record and could not give back is a
// 502 charge_not_reversed naming the gateway transaction.
func (h *Handler) HandleCard(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req CardRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.CreateCard(r.Context(), in, caller(r))
	if err != nil {
		var notReversed *ChargeNotReversedError
		if errors.As(err, &notReversed) {
			httpx.WriteError(w, r, notReversed.Wire())
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	full, err := h.service.Get(r.Context(), p.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeCreated(w, full)
}

func preconditionOf(r *http.Request, rev *int64) Precondition {
	return Precondition{IfMatch: r.Header.Get("If-Match"), Revision: rev}
}

func (h *Handler) HandleApply(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "payment")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req ApplyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	lines, rev, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.Apply(r.Context(), id, lines, preconditionOf(r, rev), caller(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	full, err := h.service.Get(r.Context(), p.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, full.Revision)
	writeJSON(w, http.StatusOK, full)
}

// HandleTransition runs POST /payments/{id}/transitions: the one transition a
// client asks for is voided.
func (h *Handler) HandleTransition(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "payment")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req TransitionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	to := ""
	if req.To == nil {
		v.Check(false, "to", "is required")
	} else {
		to = *req.To
	}
	var rev *int64
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		rev = &n
	}
	reason := trim(req.Reason)
	v.Check(len(reason) <= 500, "reason", "must be at most 500 characters")
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status, ok := ParseStatus(to)
	if !ok {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "to", Message: "must be one of: posted, voided"}))
		return
	}
	if status != StatusVoided {
		httpx.WriteError(w, r, httpx.InvalidStateTransition("a payment is voided by a transition; "+status.Status()+" is how it starts"))
		return
	}
	if reason == "" {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "reason", Message: "is required to void a payment"}))
		return
	}
	p, err := h.service.Void(r.Context(), id, preconditionOf(r, rev), reason, caller(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	full, err := h.service.Get(r.Context(), p.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, full.Revision)
	writeJSON(w, http.StatusOK, full)
}

func (h *Handler) HandleRefund(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "payment")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req RefundRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ref, err := h.service.Refund(r.Context(), id, in, preconditionOf(r, in.Revision), caller(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ref)
}

func (h *Handler) HandleCreditRefund(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "credit memo")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req RefundRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.ParseCredit()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ref, err := h.service.RefundCredit(r.Context(), id, in, preconditionOf(r, in.Revision), caller(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ref)
}
