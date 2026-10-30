// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Worklist is GET /api/v1/large-cash/worklist: the manager's queue of the
// crossings the dealer owes a filing on, in the order the manager should
// work them.
func (h *Handler) Worklist(w http.ResponseWriter, r *http.Request) {
	list, err := h.service.Worklist(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// filingRequest is the manager's record of a filing on the wire.
type filingRequest struct {
	FiledDate         string `json:"filed_date"`
	AcknowledgementNo string `json:"acknowledgement_no"`
	Voluntary         bool   `json:"voluntary"`
}

// RecordFiling is POST /api/v1/large-cash/events/{id}/filing: the manager's
// record of the Form 8300 they filed for this crossing, with the day they
// filed it and the BSA E-Filing acknowledgement number they typed.
func (h *Handler) RecordFiling(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a filing is recorded by a manager in person: it is not a machine key's to write") {
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.holdEvent(w, r, id) {
		return
	}
	var req filingRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in := RecordFilingIn{
		AcknowledgementNo: req.AcknowledgementNo,
		Voluntary:         req.Voluntary,
		Actor:             actorOf(r),
	}
	if req.FiledDate != "" {
		parsed, err := time.Parse("2006-01-02", req.FiledDate)
		if err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("the filed date is not a date",
				httpx.FieldError{Field: "filed_date", Message: "must be a date as 2006-01-02"}))
			return
		}
		in.FiledDate = parsed
	}
	filing, err := h.service.RecordFiling(r.Context(), id, in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, filing)
}

// refuseMachineKey answers the request with 403 when the caller
// authenticated with a machine key, reporting whether it answered. The
// filing and the statement are a manager's act, recorded against the person
// who did it, and the Form 8300 export carries the payer's taxpayer
// identification number: a key is not an owner or an admin, and none of the
// three is a key's to do.
func refuseMachineKey(w http.ResponseWriter, r *http.Request, message string) bool {
	if _, isKey := middleware.KeyIDFromContext(r.Context()); !isKey {
		return false
	}
	httpx.WriteError(w, r, httpx.Forbidden(message))
	return true
}

// Form8300 is GET /api/v1/large-cash/events/{id}/form-8300: the data set the
// filer types into BSA E-Filing. It carries the payer's taxpayer
// identification number, so the route is the dealer's own and admin's only,
// and it is audited without the number in the audit body.
func (h *Handler) Form8300(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "the Form 8300 export is the dealer's own and admin's only: it is not a machine key's to read") {
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.holdEvent(w, r, id) {
		return
	}
	form, err := h.service.Form8300(r.Context(), id, actorOf(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, form)
}

// Statements is GET /api/v1/large-cash/statements: the customer statements
// for a year, one per payer who had reportable cash in it.
func (h *Handler) Statements(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "year"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	year := 0
	if raw := r.URL.Query().Get("year"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 2000 || parsed > 2100 {
			httpx.WriteError(w, r, httpx.BadRequest("the year is not a year",
				httpx.FieldError{Field: "year", Message: "must be a year between 2000 and 2100"}))
			return
		}
		year = parsed
	}
	list, err := h.service.Statements(r.Context(), year)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// statementRequest is the manager's ask on the wire.
type statementRequest struct {
	// IncludeSuspicion asks the generator to put the dealer's suspicion of
	// the transaction on the customer's statement. The generator refuses.
	IncludeSuspicion bool `json:"include_suspicion"`
}

// IssueStatement is POST /api/v1/large-cash/events/{id}/statement: the
// customer statement for the crossing's payer and the year the cash was
// received.
func (h *Handler) IssueStatement(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a customer statement is issued by a manager in person: it is not a machine key's to write") {
		return
	}
	id, err := pathUUID(r, "id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.holdEvent(w, r, id) {
		return
	}
	var req statementRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	statement, err := h.service.IssueStatement(r.Context(), IssueStatementIn{
		EventID:          id,
		IncludeSuspicion: req.IncludeSuspicion,
		Actor:            actorOf(r),
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, statement)
}

// holdEvent holds the crossing to the caller's wall: a branch scoped key
// sees a crossing its branch received cash for or contributed cash to, and
// a crossing outside the wall answers what a missing crossing answers.
func (h *Handler) holdEvent(w http.ResponseWriter, r *http.Request, id uuid.UUID) bool {
	if h.guard == nil {
		httpx.WriteError(w, r, httpx.Unavailable(
			"the branch wall is not wired on this handler: a crossing is reached only inside the caller's branch"))
		return false
	}
	facts, err := h.service.EventWall(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return false
	}
	branches := append([]uuid.UUID{facts.BranchID}, facts.ContributedBranchIDs...)
	for _, branch := range branches {
		err := h.guard.CheckPayloadBranch(r.Context(), branch)
		if err == nil {
			return true
		}
		if !errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httpx.WriteError(w, r, fmt.Errorf("branch grant lookup: %w", err))
			return false
		}
	}
	// A crossing outside the caller's wall answers what a missing one does.
	httpx.WriteError(w, r, httpx.NotFound("crossing not found"))
	return false
}

// pathUUID reads the id the path names.
func pathUUID(r *http.Request, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid "+what+" id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}
