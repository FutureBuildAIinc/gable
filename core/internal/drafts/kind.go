// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package drafts is the draft resource of ADR 0007: a proposed document
// that is not yet an entity, shared between an agent tool and a person's
// screen, with a revision for optimistic concurrency, a change feed of
// server sent events, and one transactional promotion per kind that turns
// the draft into its entity.
//
// A draft kind is named by the module whose entity it proposes, spelled as
// that module's path segment under /api/v1 (quotes, orders). Each kind
// registers its own literal routes through this package's handler, so the
// route census lists exactly which kinds exist. The module's code decodes
// and validates the payload; this package never imports a module, and a
// module never imports this one back (the kind implementation and its
// registration live in the module's package and serve.go respectively).
package drafts

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Draft is one row of the drafts table, the store's shape. Payload is the
// module's own request body as JSON, decoded and validated by the kind.
type Draft struct {
	ID              uuid.UUID
	Module          string
	BranchID        uuid.UUID
	SubjectID       *uuid.UUID
	SubjectRevision *int64
	Status          Status
	Revision        int64
	Payload         json.RawMessage
	CreatedBy       Actor
	UpdatedBy       Actor
	PromotedEntity  *uuid.UUID
	PromotedNumber  *string
	PromotedAt      *httpx.Timestamp
	PromotedBy      *Actor
	DiscardedAt     *httpx.Timestamp
	DiscardedBy     *Actor
	CreatedAt       httpx.Timestamp
	UpdatedAt       httpx.Timestamp
}

// Status is the draft lifecycle in its storage vocabulary; the wire spells
// it lowercase (open, promoted, discarded).
type Status string

const (
	StatusOpen      Status = "OPEN"
	StatusPromoted  Status = "PROMOTED"
	StatusDiscarded Status = "DISCARDED"
)

// StatusWire is the status's wire name.
func (s Status) StatusWire() string {
	return map[Status]string{
		StatusOpen: "open", StatusPromoted: "promoted", StatusDiscarded: "discarded",
	}[s]
}

// ParseStatus maps a lowercase wire name to its status. Any other spelling
// is not a status.
func ParseStatus(name string) (Status, bool) {
	for _, s := range []Status{StatusOpen, StatusPromoted, StatusDiscarded} {
		if name == s.StatusWire() {
			return s, true
		}
	}
	return "", false
}

// StatusNames is the wire vocabulary in lifecycle order.
var StatusNames = []string{"open", "promoted", "discarded"}

// Actor is the actor quadruple of section 6 as it is stored on the draft
// and written on the wire: what pkg/actor resolved for the request.
type Actor struct {
	Kind     string
	ID       string
	ActingAs string
	Tool     string
}

// ActorFrom resolves the actor for the context (pkg/actor's precedence,
// unchanged): for an agent acting with a person's session the id is the
// person's subject, for a keyed agent the key's id.
func ActorFrom(ctx context.Context) Actor {
	a := actorFromContext(ctx)
	return Actor{Kind: a.Kind, ID: a.ID, ActingAs: a.ActingAs, Tool: a.Tool}
}

// Promoted is what a promotion made: the entity's id, its document number
// when it has one, and its revision.
type Promoted struct {
	EntityID uuid.UUID
	Number   *string
	Revision int64
}

// Kind is the interface a confirm gated module implements for its draft
// kind (ADR 0007 section 4.3). The drafts package calls it; it never calls
// back.
type Kind interface {
	// Module is the path segment and the scope module ("quotes").
	Module() string
	// Entity is the entity name in events and links ("quote").
	Entity() string
	// Roles are the user roles the module's routes admit.
	Roles() []string
	// Check is the structural decode for draft writes: unknown fields and
	// wrong types are the error; missing required fields come back as
	// problems (a draft is by nature unfinished). The problems are the
	// parser's field errors with their unprefixed paths; the drafts layer
	// prefixes each with payload.
	Check(payload json.RawMessage, edit bool) (problems []httpx.FieldError, err error)
	// SubjectRevision is the subject's current revision, for edit drafts;
	// ErrNotFound when the caller cannot see it.
	SubjectRevision(ctx context.Context, id uuid.UUID) (int64, error)
	// Promote runs inside the caller's transaction and writes no event: it
	// parses the payload with the module's full parser, runs the module's
	// own create or update (references checked, document priced, number
	// minted, rows written, the module's lock order kept) and returns the
	// entity and the event(s) the module would have written.
	Promote(ctx context.Context, d Draft) (Promoted, []outbox.Event, error)
}

// Registry holds the registered kinds by module.
type Registry struct {
	kinds map[string]Kind
}

// NewRegistry builds a registry from the kinds, refusing a duplicate
// module.
func NewRegistry(kinds ...Kind) (*Registry, error) {
	r := &Registry{kinds: make(map[string]Kind, len(kinds))}
	for _, k := range kinds {
		if _, dup := r.kinds[k.Module()]; dup {
			return nil, fmt.Errorf("draft kind %q registered twice", k.Module())
		}
		r.kinds[k.Module()] = k
	}
	return r, nil
}

// Get returns the kind of a module.
func (r *Registry) Get(module string) (Kind, bool) {
	k, ok := r.kinds[module]
	return k, ok
}

// Modules lists the registered kinds' modules, sorted.
func (r *Registry) Modules() []string {
	out := make([]string, 0, len(r.kinds))
	for m := range r.kinds {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
