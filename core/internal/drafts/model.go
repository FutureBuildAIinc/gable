// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/google/uuid"
)

// timestampOf wraps a database time for the wire.
func timestampOf(t time.Time) httpx.Timestamp { return httpx.TimestampOf(t.UTC()) }

// actorFromContext adapts pkg/actor to the stored quadruple.
func actorFromContext(ctx context.Context) Actor {
	a := actor.FromContext(ctx)
	return Actor{Kind: a.Kind, ID: a.ID, ActingAs: a.ActingAs, Tool: a.Tool}
}

// actorWire is the actor object on the wire (section 6): optional fields
// present as null, never omitted.
type actorWire struct {
	Kind     string  `json:"kind"`
	ID       *string `json:"id"`
	ActingAs *string `json:"acting_as"`
	Tool     *string `json:"tool"`
}

func (a Actor) wire() actorWire {
	w := actorWire{Kind: a.Kind}
	if a.ID != "" {
		w.ID = &a.ID
	}
	if a.ActingAs != "" {
		w.ActingAs = &a.ActingAs
	}
	if a.Tool != "" {
		w.Tool = &a.Tool
	}
	return w
}

// Validation is computed on every read and write from the payload alone, by
// the kind's parser: ready is true when the parser would accept the payload
// as a create (or update) request, and problems is exactly the details that
// parser's 400 would carry, each field prefixed with payload. It tells both
// editors what still blocks a promotion without refusing the save.
type Validation struct {
	Ready    bool               `json:"ready"`
	Problems []httpx.FieldError `json:"problems"`
}

// PromotionInfo is the promoted block of the wire document: null until the
// promotion, filled once.
type PromotionInfo struct {
	EntityID uuid.UUID       `json:"entity_id"`
	Number   *string         `json:"number"`
	At       httpx.Timestamp `json:"at"`
	By       actorWire       `json:"by"`
}

// DiscardInfo is the discarded block: null while open, filled at discard,
// cleared at reopen.
type DiscardInfo struct {
	At httpx.Timestamp `json:"at"`
	By actorWire       `json:"by"`
}

// Document is the draft on the wire (section 2.2): every draft route
// returns one type.
type Document struct {
	ID              uuid.UUID       `json:"id"`
	Module          string          `json:"module"`
	Status          string          `json:"status"`
	Revision        int64           `json:"revision"`
	BranchID        uuid.UUID       `json:"branch_id"`
	SubjectID       *uuid.UUID      `json:"subject_id"`
	SubjectRevision *int64          `json:"subject_revision"`
	Payload         json.RawMessage `json:"payload"`
	Validation      Validation      `json:"validation"`
	CreatedBy       actorWire       `json:"created_by"`
	UpdatedBy       actorWire       `json:"updated_by"`
	Promoted        *PromotionInfo  `json:"promoted"`
	Discarded       *DiscardInfo    `json:"discarded"`
	CreatedAt       httpx.Timestamp `json:"created_at"`
	UpdatedAt       httpx.Timestamp `json:"updated_at"`
}

// Summary is the list item: the same object without the payload and
// without the validation problems (it keeps validation.ready).
type Summary struct {
	ID              uuid.UUID       `json:"id"`
	Module          string          `json:"module"`
	Status          string          `json:"status"`
	Revision        int64           `json:"revision"`
	BranchID        uuid.UUID       `json:"branch_id"`
	SubjectID       *uuid.UUID      `json:"subject_id"`
	SubjectRevision *int64          `json:"subject_revision"`
	Validation      ValidationLite  `json:"validation"`
	CreatedBy       actorWire       `json:"created_by"`
	UpdatedBy       actorWire       `json:"updated_by"`
	Promoted        *PromotionInfo  `json:"promoted"`
	Discarded       *DiscardInfo    `json:"discarded"`
	CreatedAt       httpx.Timestamp `json:"created_at"`
	UpdatedAt       httpx.Timestamp `json:"updated_at"`
}

// ValidationLite keeps validation.ready and drops the problems.
type ValidationLite struct {
	Ready bool `json:"ready"`
}

// toWire builds the full document, computing the validation from the
// payload through the kind's parser.
func toWire(k Kind, d Draft, v Validation) Document {
	doc := Document{
		ID: d.ID, Module: d.Module, Status: d.Status.StatusWire(), Revision: d.Revision,
		BranchID: d.BranchID, SubjectID: d.SubjectID, SubjectRevision: d.SubjectRevision,
		Payload: d.Payload, Validation: v,
		CreatedBy: d.CreatedBy.wire(), UpdatedBy: d.UpdatedBy.wire(),
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.PromotedEntity != nil && d.PromotedAt != nil {
		by := actorWire{Kind: "anonymous"}
		if d.PromotedBy != nil {
			by = d.PromotedBy.wire()
		}
		doc.Promoted = &PromotionInfo{EntityID: *d.PromotedEntity, Number: d.PromotedNumber, At: *d.PromotedAt, By: by}
	}
	if d.DiscardedAt != nil {
		by := actorWire{Kind: "anonymous"}
		if d.DiscardedBy != nil {
			by = d.DiscardedBy.wire()
		}
		doc.Discarded = &DiscardInfo{At: *d.DiscardedAt, By: by}
	}
	return doc
}

// toSummary builds the list item from the document.
func toSummary(doc Document) Summary {
	return Summary{
		ID: doc.ID, Module: doc.Module, Status: doc.Status, Revision: doc.Revision,
		BranchID: doc.BranchID, SubjectID: doc.SubjectID, SubjectRevision: doc.SubjectRevision,
		Validation: ValidationLite{Ready: doc.Validation.Ready},
		CreatedBy:  doc.CreatedBy, UpdatedBy: doc.UpdatedBy,
		Promoted: doc.Promoted, Discarded: doc.Discarded,
		CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt,
	}
}

// DraftEvent is one row of draft_events: what a write did, after the write.
type DraftEvent struct {
	Position       int64
	DraftID        uuid.UUID
	Module         string
	BranchID       uuid.UUID
	SubjectID      *uuid.UUID
	Op             string
	Revision       int64
	Status         Status
	Actor          Actor
	PromotedEntity *uuid.UUID
	PromotedNumber *string
	At             httpx.Timestamp
}

// feedItem is the event data the feed carries: the row's summary, never the
// payload. A subscriber whose revision is behind GETs the draft.
type feedItem struct {
	DraftID   uuid.UUID     `json:"draft_id"`
	Module    string        `json:"module"`
	Op        string        `json:"op"`
	Revision  int64         `json:"revision"`
	Status    string        `json:"status"`
	BranchID  uuid.UUID     `json:"branch_id"`
	SubjectID *uuid.UUID    `json:"subject_id"`
	By        actorWire     `json:"by"`
	Promoted  *feedPromoted `json:"promoted"`
	At        string        `json:"at"`
}

// feedPromoted is the promoted block of a promoted event, null on the
// others.
type feedPromoted struct {
	EntityID uuid.UUID `json:"entity_id"`
	Number   *string   `json:"number"`
}

// ErrNotFound is the repository's sentinel for a row the caller named that
// does not exist or is not visible.
var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (*notFoundError) Error() string { return "no such draft" }
