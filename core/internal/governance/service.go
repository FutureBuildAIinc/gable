// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox, as the
// LAST statement of the mutation's transaction (ADR 0003 section 2).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// AuditSink writes an audit row through the caller's executor.
type AuditSink interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// Precondition is the client's revision for a write (ADR 0001 section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

// Event types the module writes to the outbox.
const (
	EventCreated  = "rfc.created"
	EventUpdated  = "rfc.updated"
	EventReview   = "rfc.review"
	EventApproved = "rfc.approved"
	EventRejected = "rfc.rejected"
	EventReopened = "rfc.reopened"
)

var transitionEvents = map[RFCStatus]string{
	RFCStatusReview:   EventReview,
	RFCStatusApproved: EventApproved,
	RFCStatusRejected: EventRejected,
	RFCStatusDraft:    EventReopened,
}

// Service holds the RFC workflow. Every mutation runs in one transaction
// that carries its audit row and its outbox event.
type Service struct {
	repo     Repository
	ai       AIProvider
	events   EventRecorder
	tx       TxRunner
	auditLog AuditSink
}

func NewService(repo Repository, ai AIProvider) *Service {
	return &Service{repo: repo, ai: ai}
}

// WithOutbox wires the recorder of RFC events.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithTxRunner wires the transaction wrapper every write uses.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

// WithAuditLog wires the audit sink. A nil sink is ignored rather than
// stored.
func (s *Service) WithAuditLog(a AuditSink) *Service {
	if a == nil {
		return s
	}
	s.auditLog = a
	return s
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

func (s *Service) audit(ctx context.Context, action string, id uuid.UUID, changes map[string]any) error {
	if s.auditLog == nil {
		return nil
	}
	return s.auditLog.Log(ctx, audit.Entry{
		Action: action, EntityType: "rfc", EntityID: id, Changes: changes,
	})
}

func (s *Service) record(ctx context.Context, eventType string, rfc *RFC, from string) error {
	if s.events == nil {
		return nil
	}
	data := map[string]any{
		"number": rfc.Number, "status": string(rfc.Status), "revision": rfc.Revision,
	}
	if from != "" {
		data["from_status"] = from
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "rfc", EntityID: rfc.ID, Data: raw,
	})
}

// notFound maps the repository's sentinel to the wire's 404.
func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}

// DraftRFC creates an RFC in draft: the provider writes the content from the
// triple, the number is minted from the sequence, and the create, its audit
// row and rfc.created are one transaction. The provider runs BEFORE the
// transaction opens (it is a call into another system; a refusal creates
// nothing).
func (s *Service) DraftRFC(ctx context.Context, in *ParsedCreate) (*RFC, error) {
	content, err := s.ai.GenerateRFC(ctx, in.Title, in.ProblemStatement, in.ProposedSolution)
	if err != nil {
		return nil, &httpx.Error{Status: 503, Code: httpx.CodeUnavailable,
			Message: "the RFC generator is unavailable: " + err.Error()}
	}
	var out *RFC
	err = s.inTx(ctx, func(ctx context.Context) error {
		rfc := &RFC{
			RFCSummary: RFCSummary{
				Title:    in.Title,
				Status:   RFCStatusDraft,
				AuthorID: in.AuthorID,
			},
			ProblemStatement: in.ProblemStatement,
			ProposedSolution: in.ProposedSolution,
			Content:          &content,
		}
		if rfc.Number, err = s.repo.NextNumber(ctx); err != nil {
			return err
		}
		if err := s.repo.CreateRFC(ctx, rfc); err != nil {
			return err
		}
		if err := s.audit(ctx, "rfc.created", rfc.ID, map[string]any{
			"number": rfc.Number, "title": rfc.Title,
		}); err != nil {
			return err
		}
		if err := s.record(ctx, EventCreated, rfc, ""); err != nil {
			return err
		}
		out = rfc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) GetRFC(ctx context.Context, id uuid.UUID) (*RFC, error) {
	rfc, err := s.repo.GetRFC(ctx, id)
	return rfc, notFound(err)
}

// ListRFCs returns one page of summaries and whether more follow.
func (s *Service) ListRFCs(ctx context.Context, f ListFilter, wantTotal bool) (items []RFCSummary, hasMore bool, total *int64, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListRFCs(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	if wantTotal {
		n, err := s.repo.CountRFCs(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

// UpdateRFC replaces the editable fields on the client's revision. An RFC
// under review may still be edited (a review is a conversation); an approved
// or rejected one may not until it is reopened.
func (s *Service) UpdateRFC(ctx context.Context, id uuid.UUID, in *ParsedUpdate, pre Precondition) (*RFC, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *RFC
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRFC(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetRFC(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		if cur.Status == RFCStatusApproved || cur.Status == RFCStatusRejected {
			return &httpx.Error{Status: 409, Code: httpx.CodeConflict,
				Message: "only draft or review RFCs can be edited",
				Details: []httpx.FieldError{httpx.Blocker("rfc_not_editable",
					"the RFC is "+string(cur.Status)+"; reopen it before editing it")}}
		}
		next := &RFC{RFCSummary: cur.RFCSummary, ProblemStatement: cur.ProblemStatement, ProposedSolution: cur.ProposedSolution, Content: cur.Content}
		if in.Title != nil {
			next.Title = *in.Title
		}
		if in.ProblemStatement != nil {
			next.ProblemStatement = *in.ProblemStatement
		}
		if in.ProposedSolution != nil {
			next.ProposedSolution = *in.ProposedSolution
		}
		if in.Content != nil {
			next.Content = in.Content
		}
		if err := s.repo.UpdateRFC(ctx, next); err != nil {
			return err
		}
		updated, err := s.repo.GetRFC(ctx, id)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, "rfc.updated", id, map[string]any{
			"number": cur.Number, "revision": updated.Revision,
		}); err != nil {
			return err
		}
		if err := s.record(ctx, EventUpdated, updated, ""); err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Transition moves an RFC along its lifecycle on the client's revision:
// draft to review, review to approved or rejected, rejected back to draft
// (a reopen). approved is terminal.
func (s *Service) Transition(ctx context.Context, id uuid.UUID, to RFCStatus, pre Precondition) (*RFC, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *RFC
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRFC(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetRFC(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		if err := validateTransition(cur.Status, to); err != nil {
			return err
		}
		if err := s.repo.SetStatus(ctx, id, to); err != nil {
			return err
		}
		moved, err := s.repo.GetRFC(ctx, id)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, "rfc.transitioned", id, map[string]any{
			"number": cur.Number, "from": string(cur.Status), "to": string(to), "revision": moved.Revision,
		}); err != nil {
			return err
		}
		if err := s.record(ctx, transitionEvents[to], moved, string(cur.Status)); err != nil {
			return err
		}
		out = moved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateTransition(from, to RFCStatus) error {
	allowed := map[RFCStatus][]RFCStatus{
		RFCStatusDraft:    {RFCStatusReview},
		RFCStatusReview:   {RFCStatusApproved, RFCStatusRejected},
		RFCStatusApproved: {},
		RFCStatusRejected: {RFCStatusDraft},
	}
	for _, t := range allowed[from] {
		if t == to {
			return nil
		}
	}
	return httpx.InvalidStateTransition(
		"cannot transition from " + string(from) + " to " + string(to))
}
