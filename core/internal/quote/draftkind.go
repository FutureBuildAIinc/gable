// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// DraftKind is the quotes module's implementation of drafts.Kind (ADR 0007
// section 10): module quotes, entity quote, roles admin/owner/sales. A
// create draft's payload is the quote create request; an edit draft's is
// the update request, with the update's field refusals applying at the
// structural check. Promotion of a create draft runs the create core (the
// quote is born in status draft with its Q- number and quote.created);
// promotion of an edit draft runs the update core with subject_revision as
// the precondition, so a quote already sent refuses it (quote_not_draft)
// and a quote edited since refuses it (stale_revision, which the drafts
// layer marks with the subject_stale blocker).
type DraftKind struct {
	svc *Service
}

// NewDraftKind builds the kind over the module's service.
func NewDraftKind(svc *Service) DraftKind {
	return DraftKind{svc: svc}
}

func (DraftKind) Module() string  { return "quotes" }
func (DraftKind) Entity() string  { return "quote" }
func (DraftKind) Roles() []string { return []string{"admin", "owner", "sales"} }

// Check is the structural decode of a draft payload: unknown fields and
// wrong types are the error; missing required fields and every other field
// problem come back as problems, the exact details the module's 400 would
// carry, so the drafts layer can tell an editor what still blocks a
// promotion without refusing the save.
func (k DraftKind) Check(payload json.RawMessage, edit bool) ([]httpx.FieldError, error) {
	var req Request
	if err := httpx.DecodeStrictJSON(payload, &req); err != nil {
		return nil, err
	}
	var parsed *Draft
	var err error
	if edit {
		parsed, err = req.ParseUpdate()
	} else {
		parsed, err = req.Parse()
	}
	if err != nil {
		var e *httpx.Error
		if errors.As(err, &e) {
			return e.Details, nil
		}
		return nil, err
	}
	_ = parsed
	return nil, nil
}

// SubjectRevision reads the quote's current revision, held to the caller's
// branch wall: a subject the caller cannot see is a 404 the drafts layer
// answers.
func (k DraftKind) SubjectRevision(ctx context.Context, id uuid.UUID) (int64, error) {
	q, err := k.svc.GetQuote(ctx, id)
	if err != nil {
		return 0, err
	}
	return q.Revision, nil
}

// Promote runs inside the promotion's transaction, with the draft's branch
// as the branch context, and writes no event: it returns the quote.created
// event a create promotion would write, for the drafts layer to write as
// its transaction's last statements. An edit draft's promotion writes none
// (the quote's update writes no event today).
func (k DraftKind) Promote(ctx context.Context, d drafts.Draft) (drafts.Promoted, []outbox.Event, error) {
	var req Request
	if err := httpx.DecodeStrictJSON(d.Payload, &req); err != nil {
		return drafts.Promoted{}, nil, err
	}
	if d.SubjectID == nil {
		parsed, err := req.Parse()
		if err != nil {
			return drafts.Promoted{}, nil, err
		}
		q, ev, err := k.svc.createCore(ctx, parsed)
		if err != nil {
			return drafts.Promoted{}, nil, err
		}
		number := q.Number
		out := drafts.Promoted{EntityID: q.ID, Number: &number, Revision: q.Revision}
		if ev != nil {
			return out, []outbox.Event{*ev}, nil
		}
		return out, nil, nil
	}
	parsed, err := req.ParseUpdate()
	if err != nil {
		return drafts.Promoted{}, nil, err
	}
	// The edit's precondition is the draft's subject_revision: a quote
	// edited since the draft was built on it is stale (the drafts layer
	// adds the subject_stale blocker).
	q, err := k.svc.updateCore(ctx, *d.SubjectID, parsed, Precondition{Revision: d.SubjectRevision})
	if err != nil {
		return drafts.Promoted{}, nil, err
	}
	number := q.Number
	return drafts.Promoted{EntityID: q.ID, Number: &number, Revision: q.Revision}, nil, nil
}

// RegisterDraftRoutes registers the quotes kind's seven literal routes
// behind the given guard (ADR 0007 section 2.3): literal, so the route
// census lists exactly the kinds that exist.
func RegisterDraftRoutes(mux *http.ServeMux, h *drafts.Handler, k DraftKind, guard func(http.Handler) http.Handler) {
	g := func(handler http.HandlerFunc) http.HandlerFunc { return drafts.Guarded(guard, handler) }
	mux.HandleFunc("GET /api/v1/drafts/quotes", g(h.List(k)))
	mux.HandleFunc("POST /api/v1/drafts/quotes", g(h.Create(k)))
	mux.HandleFunc("GET /api/v1/drafts/quotes/feed", g(h.Feed(k)))
	mux.HandleFunc("GET /api/v1/drafts/quotes/{id}", g(h.Get(k)))
	mux.HandleFunc("PUT /api/v1/drafts/quotes/{id}", g(h.Update(k)))
	mux.HandleFunc("POST /api/v1/drafts/quotes/{id}/transitions", g(h.Transitions(k)))
	mux.HandleFunc("POST /api/v1/drafts/quotes/{id}/promote", g(h.Promote(k)))
}
