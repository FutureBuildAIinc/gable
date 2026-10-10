// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// maxPayloadBytes bounds the payload: 256 KiB of JSON (ADR 0007 section
// 2.3). A draft never carries a file; every autosave re-sends the payload
// and every feed subscriber re-reads it, and a quote's original file may be
// several MiB decoded.
const maxPayloadBytes = 256 << 10

// forbiddenPayloadFields never ride inside a payload: revision because the
// draft's own revision is the precondition, and the original file triplet
// because a draft never carries a file (it attaches to the quote after
// promotion, through the quote file route).
var forbiddenPayloadFields = []string{"revision", "original_file", "original_filename", "original_content_type"}

// BranchGuard applies the payload branch rule (ADR 0007 section 2.3):
// *middleware.BranchGuard satisfies it.
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// AuditSink writes the draft act's audit row through the caller's executor.
// *audit.Logger satisfies it.
type AuditSink interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// EventRecorder writes the promotion's outbox events, last.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// FeedNudge is the hub's wake signal: the service nudges it after each
// local commit, so streams on the same replica learn at once.
type FeedNudge interface {
	Nudge()
}

// Service is the drafts core. The registry carries the kinds; the sinks are
// optional so unit tests need no database, and serve wires them all.
type Service struct {
	repo     *PostgresRepository
	registry *Registry
	events   EventRecorder
	tx       TxRunner
	audit    AuditSink
	branches BranchGuard
	feed     FeedNudge
	logger   *slog.Logger
	now      func() time.Time
}

// NewService builds the core over the store and the kind registry.
func NewService(repo *PostgresRepository, registry *Registry) *Service {
	return &Service{repo: repo, registry: registry, logger: slog.Default(), now: time.Now}
}

// WithOutbox wires the recorder of the promotion's events.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithTxRunner wires the transaction wrapper every write uses.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

// WithAudit wires the audit sink.
func (s *Service) WithAudit(a AuditSink) *Service {
	if a != nil {
		s.audit = a
	}
	return s
}

// WithBranchGuard wires the payload branch rule.
func (s *Service) WithBranchGuard(g BranchGuard) *Service {
	s.branches = g
	return s
}

// WithFeed wires the hub's wake signal.
func (s *Service) WithFeed(f FeedNudge) *Service {
	s.feed = f
	return s
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// kind resolves the kind, refusing an unregistered module (the router's 404
// normally answers this first).
func (s *Service) kind(module string) (Kind, error) {
	k, ok := s.registry.Get(module)
	if !ok {
		return nil, httpx.NotFound("no such draft kind")
	}
	return k, nil
}

// Precondition is the client's revision: the If-Match header and/or the
// body's revision (ADR 0001 section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

// notFound maps the repository's sentinel to the wire's 404.
func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}

// payloadMap decodes the payload's top level as a JSON object, refusing a
// non object payload.
func payloadMap(payload json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, httpx.BadRequest("payload must be a JSON object",
			httpx.FieldError{Field: "payload", Message: "must be a JSON object"})
	}
	return m, nil
}

// checkPayloadShape applies the payload rules every draft write carries
// (section 2.3): the size bound, the forbidden fields, and the branch
// fixed at create. It answers the payload's named branch, if any.
func checkPayloadShape(payload json.RawMessage) (map[string]json.RawMessage, *uuid.UUID, error) {
	if len(payload) > maxPayloadBytes {
		return nil, nil, httpx.PayloadTooLarge("payload is past the 256 KiB bound")
	}
	m, err := payloadMap(payload)
	if err != nil {
		return nil, nil, err
	}
	var problems []httpx.FieldError
	for _, f := range forbiddenPayloadFields {
		if raw, present := m[f]; present && !isJSONNull(raw) {
			problems = append(problems, httpx.FieldError{Field: "payload." + f, Message: "never rides inside a draft payload"})
		}
	}
	var branch *uuid.UUID
	if raw, present := m["branch_id"]; present && !isJSONNull(raw) {
		var id uuid.UUID
		if err := json.Unmarshal(raw, &id); err != nil {
			problems = append(problems, httpx.FieldError{Field: "payload.branch_id", Message: "must be a UUID"})
		} else {
			branch = &id
		}
	}
	if len(problems) > 0 {
		return nil, nil, &httpx.Error{Status: 400, Code: httpx.CodeValidationFailed,
			Message: "payload carries a field a draft never accepts", Details: problems}
	}
	return m, branch, nil
}

func isJSONNull(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}

// validation runs the kind's parser on the payload and collects its
// problems, each field prefixed with payload. A structural error (unknown
// field, wrong type) is returned as the error; the problems are the
// unfinished document's, never a refusal.
func validation(k Kind, d *Draft) (Validation, error) {
	problems, err := k.Check(d.Payload, d.SubjectID != nil)
	if err != nil {
		return Validation{}, prefixPayloadError(err)
	}
	v := Validation{Problems: []httpx.FieldError{}}
	for _, p := range problems {
		if p.Field != "" {
			p.Field = "payload." + p.Field
		}
		v.Problems = append(v.Problems, p)
	}
	v.Ready = len(v.Problems) == 0
	return v, nil
}

// prefixPayloadError rewrites a structural error's field paths with the
// payload. prefix.
func prefixPayloadError(err error) error {
	var e *httpx.Error
	if !errors.As(err, &e) {
		return err
	}
	out := &httpx.Error{Status: e.Status, Code: e.Code, Message: e.Message}
	for _, d := range e.Details {
		if d.Field != "" && !strings.HasPrefix(d.Field, "payload.") {
			d.Field = "payload." + d.Field
		}
		out.Details = append(out.Details, d)
	}
	return out
}

// payloadSHA256 hashes the stored payload's canonical bytes, so an auditor
// ties the committed entity to the exact revision that was confirmed.
func payloadSHA256(payload json.RawMessage) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// checkRecordBranch is the record branch rule for a draft a path id
// addresses: the draft's branch must be one the caller may target, else
// 404 (ADR 0007 section 4.2 step 1: a branch the caller cannot see).
func (s *Service) checkRecordBranch(ctx context.Context, branchID uuid.UUID) error {
	if s.branches == nil {
		return nil
	}
	if err := s.branches.CheckPayloadBranch(ctx, branchID); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			return httpx.NotFound(ErrNotFound.Error())
		}
		return err
	}
	return nil
}

// resolveBranchForCreate settles the draft's fixed branch: a payload branch
// the caller may target (the guard's 403 naming payload.branch_id), else
// the context branch, else ResolveBranchForWrite's default.
func (s *Service) resolveBranchForCreate(ctx context.Context, payloadBranch *uuid.UUID) (uuid.UUID, error) {
	if payloadBranch != nil {
		if s.branches != nil {
			if err := s.branches.CheckPayloadBranch(ctx, *payloadBranch); errors.Is(err, middleware.ErrPayloadBranchRefused) {
				return uuid.Nil, &httpx.Error{Status: 403, Code: httpx.CodeForbidden,
					Message: "branch_id is outside the branches this caller may target",
					Details: []httpx.FieldError{{Field: "payload.branch_id", Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
			} else if err != nil {
				return uuid.Nil, err
			}
		}
		return *payloadBranch, nil
	}
	if bc := branchctx.FromContext(ctx); bc != nil && bc.BranchID != nil {
		return *bc.BranchID, nil
	}
	id, err := middleware.ResolveBranchForWrite(ctx, s.repo.db)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve the draft's branch: %w", err)
	}
	return id, nil
}

// CreateRequest is the body of POST /api/v1/drafts/{kind}.
type CreateRequest struct {
	Payload         json.RawMessage `json:"payload"`
	SubjectID       *string         `json:"subject_id"`
	SubjectRevision json.RawMessage `json:"subject_revision"`
}

// Create stores a new draft at revision 1 (section 2.3, 2.4): the payload
// rules, the branch rule, the subject rule, the kind's structural check,
// then the row, its audit row and its created event in one transaction.
func (s *Service) Create(ctx context.Context, module string, req CreateRequest) (*Document, error) {
	k, err := s.kind(module)
	if err != nil {
		return nil, err
	}
	if req.Payload == nil || isJSONNull(req.Payload) {
		return nil, httpx.BadRequest("payload is required",
			httpx.FieldError{Field: "payload", Message: "is required"})
	}
	_, payloadBranch, err := checkPayloadShape(req.Payload)
	if err != nil {
		return nil, err
	}

	var subjectID *uuid.UUID
	var subjectRevision *int64
	if req.SubjectID != nil {
		id, err := uuid.Parse(*req.SubjectID)
		if err != nil {
			return nil, httpx.BadRequest("subject_id must be a UUID",
				httpx.FieldError{Field: "subject_id", Message: "must be a UUID"})
		}
		subjectID = &id
		// The server reads the subject's current revision: the client may
		// assert it (a mismatch is 409 stale_revision with a subject_stale
		// blocker), or omit it and take the current one. A subject the
		// caller cannot see is a 404, the same as reading it.
		current, err := k.SubjectRevision(ctx, id)
		if err != nil {
			return nil, subjectLookupError(err)
		}
		subjectRevision = &current
		if raw := req.SubjectRevision; !isJSONNull(raw) {
			var asserted int64
			if err := json.Unmarshal(raw, &asserted); err != nil {
				return nil, httpx.BadRequest("subject_revision must be an integer",
					httpx.FieldError{Field: "subject_revision", Message: "must be an integer"})
			}
			if asserted != current {
				return nil, subjectStale(id, asserted, current)
			}
		}
	}

	branch, err := s.resolveBranchForCreate(ctx, payloadBranch)
	if err != nil {
		return nil, err
	}

	d := &Draft{
		ID: uuid.New(), Module: module, BranchID: branch,
		SubjectID: subjectID, SubjectRevision: subjectRevision,
		Status: StatusOpen, Revision: 1, Payload: req.Payload,
		CreatedBy: ActorFrom(ctx), UpdatedBy: ActorFrom(ctx),
		CreatedAt: httpx.TimestampOf(s.now().UTC()), UpdatedAt: httpx.TimestampOf(s.now().UTC()),
	}
	if _, err := k.Check(d.Payload, d.SubjectID != nil); err != nil {
		return nil, prefixPayloadError(err)
	}

	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.InsertDraft(ctx, d); err != nil {
			return err
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action: "draft.created", EntityType: "draft", EntityID: d.ID,
				Changes: map[string]any{
					"module": module, "revision": d.Revision, "subject_id": d.SubjectID,
					"payload_sha256": payloadSHA256(d.Payload),
				},
			}); err != nil {
				return err
			}
		}
		return s.repo.InsertEvent(ctx, &DraftEvent{
			DraftID: d.ID, Module: module, BranchID: d.BranchID, SubjectID: d.SubjectID,
			Op: "created", Revision: d.Revision, Status: d.Status, Actor: d.CreatedBy,
			At: d.CreatedAt,
		})
	})
	if err != nil {
		return nil, err
	}
	s.nudgeFeed()
	v, err := validation(k, d)
	if err != nil {
		return nil, err
	}
	doc := toWire(k, *d, v)
	return &doc, nil
}

// subjectLookupError maps the kind's subject read to the wire: not found or
// a visibility refusal are both a 404 (the caller cannot see it, the same
// as reading it).
func subjectLookupError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("no such subject")
	}
	var e *httpx.Error
	if errors.As(err, &e) && (e.Status == 404 || e.Status == 403) {
		return httpx.NotFound("no such subject")
	}
	return err
}

// subjectStale builds the 409 for an asserted subject revision the subject
// has moved past, with the subject_stale blocker naming it.
func subjectStale(id uuid.UUID, asserted, current int64) error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeStaleRevision,
		Message: fmt.Sprintf("the subject %s was changed after revision %d; it is at %d", id, asserted, current),
		Details: []httpx.FieldError{httpx.Blocker("subject_stale",
			fmt.Sprintf("the subject %s moved past the asserted revision", id))}}
}

// Get reads one draft, with its computed validation.
func (s *Service) Get(ctx context.Context, module string, id uuid.UUID) (*Document, error) {
	k, err := s.kind(module)
	if err != nil {
		return nil, err
	}
	d, err := s.repo.GetDraft(ctx, id, module)
	if err != nil {
		return nil, notFound(err)
	}
	if err := s.checkRecordBranch(ctx, d.BranchID); err != nil {
		return nil, err
	}
	v, err := validation(k, d)
	if err != nil {
		return nil, err
	}
	doc := toWire(k, *d, v)
	return &doc, nil
}

// ListFilterDTO is the list's query on the wire.
type ListFilterDTO struct {
	Statuses      []Status
	SubjectID     *uuid.UUID
	CreatedByKind string
	AfterTime     *time.Time
	AfterID       uuid.UUID
	Limit         int
	WantTotal     bool
}

// List returns one page of the kind's drafts, newest first, behind the
// branch wall.
func (s *Service) List(ctx context.Context, module string, f ListFilterDTO) (items []Summary, hasMore bool, total *int64, err error) {
	if _, err := s.kind(module); err != nil {
		return nil, false, nil, err
	}
	filter := ListFilter{
		Module: module, Statuses: f.Statuses, SubjectID: f.SubjectID, CreatedByKind: f.CreatedByKind,
		AfterTime: f.AfterTime, AfterID: f.AfterID, Limit: f.Limit + 1,
		BranchID: middleware.BranchIDForQuery(ctx), GrantsSub: middleware.GrantsSubForQuery(ctx),
	}
	rows, err := s.repo.ListDrafts(ctx, filter)
	if err != nil {
		return nil, false, nil, err
	}
	if len(rows) > f.Limit {
		rows, hasMore = rows[:f.Limit], true
	}
	if f.WantTotal {
		n, err := s.repo.CountDrafts(ctx, filter)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	k, _ := s.kind(module)
	items = make([]Summary, 0, len(rows))
	for i := range rows {
		v, err := validation(k, &rows[i])
		if err != nil {
			return nil, false, nil, err
		}
		items = append(items, toSummary(toWire(k, rows[i], v)))
	}
	return items, hasMore, total, nil
}

// UpdateRequest is the body of PUT /api/v1/drafts/{kind}/{id}.
type UpdateRequest struct {
	Payload         json.RawMessage `json:"payload"`
	Revision        json.RawMessage `json:"revision"`
	SubjectRevision json.RawMessage `json:"subject_revision"`
}

// Update replaces the payload of an open draft on the client's revision
// (section 2.5): the draft row is locked FOR UPDATE inside the transaction
// and the revision checked there, so the check and the write are one
// database act.
func (s *Service) Update(ctx context.Context, module string, id uuid.UUID, req UpdateRequest, ifMatch string) (*Document, error) {
	k, err := s.kind(module)
	if err != nil {
		return nil, err
	}
	if req.Payload == nil || isJSONNull(req.Payload) {
		return nil, httpx.BadRequest("payload is required",
			httpx.FieldError{Field: "payload", Message: "is required"})
	}
	_, payloadBranch, err := checkPayloadShape(req.Payload)
	if err != nil {
		return nil, err
	}
	var bodyRevision *int64
	if raw := req.Revision; !isJSONNull(raw) {
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, httpx.BadRequest("revision must be an integer",
				httpx.FieldError{Field: "revision", Message: "must be a revision number, 1 or more"})
		}
		bodyRevision = &n
	}
	pre := Precondition{IfMatch: ifMatch, Revision: bodyRevision}
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var rebase *int64
	if raw := req.SubjectRevision; !isJSONNull(raw) {
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, httpx.BadRequest("subject_revision must be an integer",
				httpx.FieldError{Field: "subject_revision", Message: "must be an integer"})
		}
		rebase = &n
	}

	var out *Draft
	err = s.inTx(ctx, func(ctx context.Context) error {
		d, err := s.repo.LockDraft(ctx, id, module)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkRecordBranch(ctx, d.BranchID); err != nil {
			return err
		}
		if d.Status != StatusOpen {
			return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
				Message: "only an open draft can be edited",
				Details: []httpx.FieldError{httpx.Blocker("draft_not_open", "the draft is "+d.Status.StatusWire())}}
		}
		if err := pre.check(d.Revision); err != nil {
			return err
		}
		// The branch is fixed at create: a later payload naming another
		// branch is a 400 naming payload.branch_id.
		if payloadBranch != nil && *payloadBranch != d.BranchID {
			return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
				Message: "the draft's branch is fixed at create",
				Details: []httpx.FieldError{{Field: "payload.branch_id", Message: "the draft's branch was fixed when it was created"}}}
		}
		// The subject_revision rebase, bounded: the stored value must be at
		// most the new value, and the new value at most the subject's
		// current revision.
		subjectRevision := d.SubjectRevision
		if rebase != nil {
			if d.SubjectID == nil {
				return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
					Message: "subject_revision rides only on an edit draft",
					Details: []httpx.FieldError{{Field: "subject_revision", Message: "a create draft has no subject"}}}
			}
			current, err := k.SubjectRevision(ctx, *d.SubjectID)
			if err != nil {
				return subjectLookupError(err)
			}
			if *rebase < *d.SubjectRevision || *rebase > current {
				return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
					Message: "subject_revision can only move forward, up to the subject's current revision",
					Details: []httpx.FieldError{{Field: "subject_revision",
						Message: fmt.Sprintf("must be between %d (stored) and %d (the subject's current revision)", *d.SubjectRevision, current)}}}
			}
			subjectRevision = rebase
		}
		if _, err := k.Check(req.Payload, d.SubjectID != nil); err != nil {
			return prefixPayloadError(err)
		}
		by := ActorFrom(ctx)
		next := d.Revision + 1
		if err := s.repo.ReplacePayload(ctx, id, req.Payload, next, subjectRevision, by); err != nil {
			return err
		}
		out, err = s.repo.GetDraft(ctx, id, module)
		if err != nil {
			return err
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action: "draft.updated", EntityType: "draft", EntityID: id,
				Changes: map[string]any{
					"module": module, "revision": next, "payload_sha256": payloadSHA256(req.Payload),
				},
			}); err != nil {
				return err
			}
		}
		return s.repo.InsertEvent(ctx, &DraftEvent{
			DraftID: id, Module: module, BranchID: d.BranchID, SubjectID: d.SubjectID,
			Op: "updated", Revision: next, Status: d.Status, Actor: by,
			At: httpx.TimestampOf(s.now().UTC()),
		})
	})
	if err != nil {
		return nil, err
	}
	s.nudgeFeed()
	v, err := validation(k, out)
	if err != nil {
		return nil, err
	}
	doc := toWire(k, *out, v)
	return &doc, nil
}

// Transition moves a draft along its lifecycle on the client's revision:
// open to discarded, discarded to open; promoted is terminal.
func (s *Service) Transition(ctx context.Context, module string, id uuid.UUID, to Status, pre Precondition) (*Document, error) {
	k, err := s.kind(module)
	if err != nil {
		return nil, err
	}
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Draft
	err = s.inTx(ctx, func(ctx context.Context) error {
		d, err := s.repo.LockDraft(ctx, id, module)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkRecordBranch(ctx, d.BranchID); err != nil {
			return err
		}
		if err := pre.check(d.Revision); err != nil {
			return err
		}
		allowed := map[Status][]Status{
			StatusOpen:      {StatusDiscarded},
			StatusDiscarded: {StatusOpen},
			StatusPromoted:  {},
		}
		okState := false
		for _, t := range allowed[d.Status] {
			if t == to {
				okState = true
			}
		}
		if !okState {
			return httpx.InvalidStateTransition(
				fmt.Sprintf("cannot transition from %s to %s", d.Status.StatusWire(), to.StatusWire()))
		}
		by := ActorFrom(ctx)
		next := d.Revision + 1
		switch to {
		case StatusDiscarded:
			if err := s.repo.DiscardDraft(ctx, id, next, s.now().UTC(), by); err != nil {
				return err
			}
		case StatusOpen:
			if err := s.repo.ReopenDraft(ctx, id, next, by); err != nil {
				return err
			}
		}
		out, err = s.repo.GetDraft(ctx, id, module)
		if err != nil {
			return err
		}
		action, op := "draft.discarded", "discarded"
		if to == StatusOpen {
			action, op = "draft.reopened", "reopened"
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{
				Action: action, EntityType: "draft", EntityID: id,
				Changes: map[string]any{"module": module, "revision": next},
			}); err != nil {
				return err
			}
		}
		return s.repo.InsertEvent(ctx, &DraftEvent{
			DraftID: id, Module: module, BranchID: d.BranchID, SubjectID: d.SubjectID,
			Op: op, Revision: next, Status: to, Actor: by,
			At: httpx.TimestampOf(s.now().UTC()),
		})
	})
	if err != nil {
		return nil, err
	}
	s.nudgeFeed()
	v, err := validation(k, out)
	if err != nil {
		return nil, err
	}
	doc := toWire(k, *out, v)
	return &doc, nil
}

// Promote turns the draft into its entity in one transaction (section 4.2,
// in that order): lock, status, revision, parse, the kind's promoter with
// the draft's branch as the branch context, the draft row, the audit row,
// the draft event, and the outbox events last. Every failure rolls the
// whole transaction back; after the rollback the drafts service writes one
// draft.promotion_refused row, best effort.
func (s *Service) Promote(ctx context.Context, module string, id uuid.UUID, pre Precondition) (*Document, bool, error) {
	k, err := s.kind(module)
	if err != nil {
		return nil, false, err
	}
	if pre.missing() {
		return nil, false, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Draft
	created := false
	err = s.inTx(ctx, func(ctx context.Context) error {
		// 1. Lock the draft row.
		d, err := s.repo.LockDraft(ctx, id, module)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkRecordBranch(ctx, d.BranchID); err != nil {
			return err
		}
		// 2. Status before the revision, so a retry of a promotion that
		// already happened is told so, whatever revision it sends.
		switch d.Status {
		case StatusPromoted:
			num := ""
			if d.PromotedNumber != nil {
				num = *d.PromotedNumber
			}
			entity := uuid.Nil
			if d.PromotedEntity != nil {
				entity = *d.PromotedEntity
			}
			return httpx.InvalidStateTransition(
				fmt.Sprintf("this draft was already promoted to %s (%s)", entity, num),
				httpx.Blocker("already_promoted", fmt.Sprintf("the draft became %s (%s) at revision %d", entity, num, d.Revision)))
		case StatusDiscarded:
			return httpx.InvalidStateTransition("a discarded draft cannot be promoted",
				httpx.Blocker("draft_discarded", "the draft is discarded; reopen it first"))
		}
		// 3. The revision: what is committed is exactly the revision the
		// committer read.
		if err := pre.check(d.Revision); err != nil {
			return err
		}
		// 5. The kind's promoter, inside this transaction, with the draft's
		// branch as the branch context whatever the committer's header
		// named: the draft's branch was checked against its writers at
		// every save, and step 1 checked the committer can see it.
		branch := d.BranchID
		promoteCtx := branchctx.With(ctx, &branchctx.Context{BranchID: &branch})
		promoted, evs, err := k.Promote(promoteCtx, *d)
		if err != nil {
			return mapPromoteError(err, d)
		}
		created = d.SubjectID == nil
		// 6. Update the draft.
		by := ActorFrom(ctx)
		next := d.Revision + 1
		at := s.now().UTC()
		if err := s.repo.MarkPromoted(ctx, id, next, promoted.EntityID, promoted.Number, at, by); err != nil {
			return err
		}
		// 7. The audit row, with every proposer.
		proposers, err := s.proposers(ctx, id)
		if err != nil {
			return err
		}
		if s.audit != nil {
			changes := map[string]any{
				"module": module, "revision": next, "payload_sha256": payloadSHA256(d.Payload),
				"entity_type": k.Entity(), "entity_id": promoted.EntityID, "proposers": proposers,
			}
			changes["number"] = nil
			if promoted.Number != nil {
				changes["number"] = *promoted.Number
			}
			if err := s.audit.Log(ctx, audit.Entry{
				Action: "draft.promoted", EntityType: "draft", EntityID: id, Changes: changes,
			}); err != nil {
				return err
			}
		}
		// 8. The draft_events row (op promoted).
		if err := s.repo.InsertEvent(ctx, &DraftEvent{
			DraftID: id, Module: module, BranchID: d.BranchID, SubjectID: d.SubjectID,
			Op: "promoted", Revision: next, Status: StatusPromoted, Actor: by,
			PromotedEntity: &promoted.EntityID, PromotedNumber: promoted.Number,
			At: httpx.TimestampOf(at),
		}); err != nil {
			return err
		}
		// 9. The outbox events, last: the module's own first, then
		// draft.promoted.
		if s.events != nil {
			for _, ev := range evs {
				if err := s.events.Write(ctx, ev); err != nil {
					return err
				}
			}
			proposedBy := d.CreatedBy.wire()
			committedBy := by.wire()
			data, err := json.Marshal(map[string]any{
				"module": module, "entity": k.Entity(),
				"entity_id": promoted.EntityID, "number": promoted.Number,
				"revision":    promoted.Revision,
				"proposed_by": proposedBy, "committed_by": committedBy,
			})
			if err != nil {
				return err
			}
			if err := s.events.Write(ctx, outbox.Event{
				Type: "draft.promoted", EntityType: "draft", EntityID: id,
				BranchID: &branch, Data: data,
			}); err != nil {
				return err
			}
		}
		out, err = s.repo.GetDraft(ctx, id, module)
		return err
	})
	if err != nil {
		// 4.5: nothing about the failure is written into the draft; after
		// the rollback, outside it, one best effort audit row shows who
		// tried and why it did not happen.
		s.auditRefusedPromotion(ctx, module, id, err)
		return nil, false, err
	}
	s.nudgeFeed()
	v, err := validation(k, out)
	if err != nil {
		return nil, false, err
	}
	doc := toWire(k, *out, v)
	return &doc, created, nil
}

// mapPromoteError carries the module's own refusal to the wire: a 400 keeps
// its fields with the payload. prefix, and a subject the entity moved past
// keeps stale_revision and gains the subject_stale blocker, so a client
// tells a stale subject (rebase the draft) from a stale draft (re-read it,
// which carries no blocker).
func mapPromoteError(err error, d *Draft) error {
	var e *httpx.Error
	if !errors.As(err, &e) {
		return err
	}
	if e.Code == httpx.CodeStaleRevision && d.SubjectID != nil && !hasBlocker(e, "subject_stale") {
		e.Details = append(e.Details, httpx.Blocker("subject_stale",
			fmt.Sprintf("the subject %s moved past the draft's subject_revision %d", *d.SubjectID, derefInt(d.SubjectRevision))))
		return e
	}
	if e.Status == http.StatusBadRequest {
		return prefixPayloadError(e)
	}
	return e
}

func hasBlocker(e *httpx.Error, code string) bool {
	for _, d := range e.Details {
		if d.Code == code {
			return true
		}
	}
	return false
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// proposers reads the distinct actors of the draft's draft.created and
// draft.updated rows from the audit log, inside the promotion's
// transaction: with the committer on the row, "who proposed and who
// committed" is one audit row.
func (s *Service) proposers(ctx context.Context, id uuid.UUID) ([]actorWire, error) {
	rows, err := s.repo.db.GetExecutor(ctx).Query(ctx,
		`SELECT DISTINCT actor_kind, actor_id, acting_as, tool FROM audit_log
		  WHERE entity_type = 'draft' AND entity_id = $1
		    AND action IN ('draft.created', 'draft.updated')`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []actorWire
	seen := map[string]bool{}
	for rows.Next() {
		var kind string
		var idp, actp, toolp *string
		if err := rows.Scan(&kind, &idp, &actp, &toolp); err != nil {
			return nil, err
		}
		key := kind + "|" + strOf(idp) + "|" + strOf(actp) + "|" + strOf(toolp)
		if seen[key] {
			continue
		}
		seen[key] = true
		a := Actor{Kind: kind, ID: strOf(idp), ActingAs: strOf(actp), Tool: strOf(toolp)}
		out = append(out, a.wire())
	}
	return out, rows.Err()
}

// auditRefusedPromotion writes the draft.promotion_refused row after the
// rollback, best effort (the ADR 0002 refusal pattern).
func (s *Service) auditRefusedPromotion(ctx context.Context, module string, id uuid.UUID, err error) {
	if s.audit == nil {
		return
	}
	code := "internal_error"
	var e *httpx.Error
	if errors.As(err, &e) {
		code = e.Code
	}
	if d, derr := s.repo.GetDraft(context.WithoutCancel(ctx), id, module); derr == nil {
		if aerr := s.audit.Log(context.WithoutCancel(ctx), audit.Entry{
			Action: "draft.promotion_refused", EntityType: "draft", EntityID: id,
			Changes: map[string]any{"module": module, "revision": d.Revision, "status": d.Status.StatusWire(), "code": code},
		}); aerr != nil {
			s.logger.Warn("drafts: failed to write the promotion_refused row", "draft_id", id, "error", aerr)
		}
	}
}

// nudgeFeed wakes the hub after a local commit.
func (s *Service) nudgeFeed() {
	if s.feed != nil {
		s.feed.Nudge()
	}
}
