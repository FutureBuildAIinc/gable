// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The terminal intent routes (CP-05, IX-01): the desk's waiting state and
// its honest cancel, the sweep the scheduler or a manager runs, and the
// manager's lost answer queue with its resolve acts. The money routes of the
// queue are the manager's alone (finance and owner, the same resolution mount
// as refund claims; a machine key is refused, bound or not), and every route
// runs behind the branch middleware like every money route of the counter.

// GetTerminalIntentHandler is GET /pos/terminal-intents/{id}: the desk's
// waiting state of one card leg.
func (h *Handler) GetTerminalIntentHandler(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	view, err := h.service.GetTerminalIntent(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// CancelTerminalIntentHandler is POST /pos/terminal-intents/{id}/cancel:
// the desk's cancel, honest about the device (it never claims money back).
func (h *Handler) CancelTerminalIntentHandler(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a card leg is cancelled by a person, never a machine key") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	view, err := h.service.CancelTerminalIntent(r.Context(), id, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// SweepTerminalIntentsHandler is POST /pos/terminal-intents/sweep: the
// sweep act, run by the scheduler or a manager. Manager roles only.
func (h *Handler) SweepTerminalIntentsHandler(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "the terminal sweep is run by a person, never a machine key") {
		return
	}
	res, err := h.service.SweepTerminalIntents(r.Context(), actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ListTerminalLostItemsHandler is GET /pos/terminal-lost-items: the open
// manager items of the lost answer queue. Manager roles only.
func (h *Handler) ListTerminalLostItemsHandler(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	list, err := h.service.ListTerminalLostItems(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, list, "", 200)
}

// resolveLostItemRequest is the resolve route's body.
type resolveLostItemRequest struct {
	Outcome   *string `json:"outcome"`
	Reference *string `json:"reference"`
	Reason    *string `json:"reason"`
}

func (req *resolveLostItemRequest) parse() (*ResolveTerminalLostItemIn, error) {
	in := &ResolveTerminalLostItemIn{}
	if req.Outcome != nil {
		in.Outcome = *req.Outcome
	}
	if req.Reference != nil {
		in.Reference = *req.Reference
	}
	if req.Reason != nil {
		in.Reason = *req.Reason
	}
	if in.Outcome == "" {
		return nil, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "outcome", Message: "is required"})
	}
	return in, nil
}

// ResolveTerminalLostItemHandler is POST /pos/terminal-lost-items/{id}/resolve:
// the manager's settlement of one lost answer, the terminal's own record the
// authority. Manager roles only; a machine key is refused, bound or not.
func (h *Handler) ResolveTerminalLostItemHandler(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a lost terminal answer is resolved by a person, never a machine key") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req resolveLostItemRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	view, err := h.service.ResolveTerminalLostItem(r.Context(), id, *in, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
