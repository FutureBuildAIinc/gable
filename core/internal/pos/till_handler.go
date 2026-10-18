// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

const (
	movementsOrdering = "pos.till_movements.created_at_id_asc"
)

// pathUUID reads a UUID path value, or answers the 400 naming it.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: name, Message: "must be a UUID"}))
		return uuid.Nil, false
	}
	return id, true
}

// keysetPage reads the shared list parameters of a (time, id) keyset list: the
// strict guard on the route's names, the page and the keyset position when a
// cursor was sent.
func keysetPage(r *http.Request, scope string, allowed ...string) (after *time.Time, afterID *uuid.UUID, limit int, err error) {
	names := append([]string{"cursor", "limit"}, allowed...)
	if _, err := httpx.StrictQuery(r, names...); err != nil {
		return nil, nil, 0, err
	}
	page, err := httpx.ParseListQuery(r, scope)
	if err != nil {
		return nil, nil, 0, err
	}
	if page.Key != nil {
		bad := httpx.BadRequest("cursor keyset is malformed", httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
		if len(page.Key) != 2 {
			return nil, nil, 0, bad
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return nil, nil, 0, bad
		}
		after, afterID = &at, &id
	}
	return after, afterID, page.Limit, nil
}

// writeKeysetList answers a page: the next cursor is minted from the last
// row when a row follows.
func writeKeysetList[T any](w http.ResponseWriter, r *http.Request, items []T, more bool, scope string, last func(T) (time.Time, uuid.UUID), limit int) {
	next := ""
	if more && len(items) > 0 {
		at, id := last(items[len(items)-1])
		var err error
		next, err = httpx.MintCursor(scope, httpx.FormatKeyTime(at), id.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, limit)
}

// CreateMovement is POST /pos/till/{id}/movements.
func (h *Handler) CreateMovement(w http.ResponseWriter, r *http.Request) {
	// a movement is a person's act, as the open and the close are
	if _, ok := cashierOf(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var req movementRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m, err := h.service.RecordMovement(r.Context(), id, in, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pos/till/"+id.String()+"/movements")
	writeJSON(w, http.StatusCreated, m)
}

// ListMovements is GET /pos/till/{id}/movements.
func (h *Handler) ListMovements(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	after, afterID, limit, err := keysetPage(r, movementsOrdering)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, more, err := h.service.ListMovements(r.Context(), id, after, afterID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeKeysetList(w, r, items, more, movementsOrdering, func(m TillMovement) (time.Time, uuid.UUID) {
		return m.CreatedAt.Time, m.ID
	}, limit)
}

// SignOffTill is POST /pos/till/{id}/signoff: a manager's release of a
// counted drawer whose variance passed the threshold.
func (h *Handler) SignOffTill(w http.ResponseWriter, r *http.Request) {
	// a sign off is a person's act, never a machine key's
	if _, ok := cashierOf(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var req signOffRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	report, err := h.service.SignOffTill(r.Context(), id, reason, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

const signoffsOrdering = "pos.till_signoffs.closed_at_id_asc"

// ListPendingSignoffs is GET /pos/till/signoffs: the counted drawers waiting
// for a manager.
func (h *Handler) ListPendingSignoffs(w http.ResponseWriter, r *http.Request) {
	after, afterID, limit, err := keysetPage(r, signoffsOrdering, "branch_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var branch *uuid.UUID
	if raw := r.URL.Query().Get("branch_id"); raw != "" {
		b, perr := uuid.Parse(raw)
		if perr != nil {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "branch_id", Message: "must be a UUID"}))
			return
		}
		branch = &b
	}
	items, more, err := h.service.PendingSignoffs(r.Context(), branch, after, afterID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeKeysetList(w, r, items, more, signoffsOrdering, func(s TillSession) (time.Time, uuid.UUID) {
		closed := time.Time{}
		if s.ClosedAt != nil {
			closed = s.ClosedAt.Time
		}
		return closed, s.ID
	}, limit)
}

// GetVariancePolicy is GET /pos/till/policy?branch_id=.
func (h *Handler) GetVariancePolicy(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "branch_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	branch, perr := uuid.Parse(q.Get("branch_id"))
	if perr != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "branch_id", Message: "is required and must be a UUID"}))
		return
	}
	p, err := h.service.GetVariancePolicy(r.Context(), branch)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// PutVariancePolicy is PUT /pos/till/policy: a branch's own threshold.
func (h *Handler) PutVariancePolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := cashierOf(w, r); !ok {
		return
	}
	var req variancePolicyRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	branch, cents, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	p, err := h.service.SetVariancePolicy(r.Context(), branch, cents, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// GetBranchDay is GET /pos/end-of-day?branch_id=&date=: the branch's end of
// day summary for a business date (the branch's today when no date is sent).
func (h *Handler) GetBranchDay(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "branch_id", "date")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	branch, perr := uuid.Parse(q.Get("branch_id"))
	if perr != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "branch_id", Message: "is required and must be a UUID"}))
		return
	}
	day, err := h.service.BranchDay(r.Context(), branch, q.Get("date"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, day)
}

// CloseBranchDay is POST /pos/end-of-day/close.
func (h *Handler) CloseBranchDay(w http.ResponseWriter, r *http.Request) {
	// the close is a person's act, as the sign off is
	if _, ok := cashierOf(w, r); !ok {
		return
	}
	var req dayCloseRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	branch, date, slip, bank, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	day, err := h.service.CloseBranchDay(r.Context(), branch, date, slip, bank, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pos/end-of-day?branch_id="+branch.String()+"&date="+date)
	writeJSON(w, http.StatusCreated, day)
}

// ListDepositCandidates is GET /pos/end-of-day/{id}/deposit-candidates.
func (h *Handler) ListDepositCandidates(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	lines, err := h.service.DepositCandidates(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	type candidate struct {
		BankTransactionID uuid.UUID   `json:"bank_transaction_id"`
		TransactionDate   string      `json:"transaction_date"`
		AmountCents       httpx.Cents `json:"amount_cents"`
		Description       string      `json:"description"`
		Reference         string      `json:"reference"`
	}
	items := make([]candidate, 0, len(lines))
	for _, l := range lines {
		items = append(items, candidate{BankTransactionID: l.ID, TransactionDate: l.TransactionDate.Format("2006-01-02"),
			AmountCents: httpx.Cents(l.Amount), Description: l.Description, Reference: l.Reference})
	}
	httpx.WriteList(w, items, "", dayCandidateLimit)
}

// MatchDeposit is POST /pos/end-of-day/{id}/deposit-match.
func (h *Handler) MatchDeposit(w http.ResponseWriter, r *http.Request) {
	if _, ok := cashierOf(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var req depositMatchRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	line, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.MatchDeposit(r.Context(), id, line, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// ReopenBranchDay is POST /pos/end-of-day/{id}/reopen.
func (h *Handler) ReopenBranchDay(w http.ResponseWriter, r *http.Request) {
	// a reopen is a person's act, as the close is
	if _, ok := cashierOf(w, r); !ok {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var req dayReopenRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	day, err := h.service.ReopenBranchDay(r.Context(), id, reason, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, day)
}
