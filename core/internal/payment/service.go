// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox, as the
// LAST statement of the mutation's transaction (ADR 0003 section 2).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// AuditSink writes an audit row (*audit.Logger satisfies it).
type AuditSink interface {
	Log(ctx context.Context, e audit.Entry) error
}

// BranchGuard applies the payload branch rule (ADR 0007 section 2.3).
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service is the payment module: the acts of ADR 0005 section 9.4. The money
// moves in the AR core (internal/account); this layer parses, holds the branch
// wall and the roles, calls the card gateway outside the transaction, and
// writes the audit rows and the events inside it.
type Service struct {
	db            *database.DB
	tx            TxRunner
	repo          *Repository
	account       *account.Service
	gateway       PaymentGateway // Run Payments (or nil for non-card payments)
	publicKey     string         // Run Payments public key for Runner.js (static fallback)
	keyStore      *KeyStore      // Optional: DB-first key resolution (Tech Admin settable)
	brainNotifier *BrainNotifier // FB Brain financial engine notifier (or nil)
	brainOrgID    string         // Brain org_id for this tenant
	auditLog      AuditSink
	events        EventRecorder
	branches      BranchGuard
	logger        *slog.Logger
	now           func() time.Time
}

func NewService(db *database.DB, repo *Repository, accountService *account.Service) *Service {
	return &Service{db: db, repo: repo, account: accountService, logger: slog.Default(), now: time.Now}
}

// WithGateway sets the payment gateway (Run Payments) and returns the service for chaining.
func (s *Service) WithGateway(gw PaymentGateway, publicKey string) *Service {
	s.gateway = gw
	s.publicKey = publicKey
	return s
}

// WithBrainNotifier sets the FB Brain financial notifier and returns the service for chaining.
// When set, successfully paid invoices will fire an async notification to Brain's 10bps engine.
func (s *Service) WithBrainNotifier(n *BrainNotifier, orgID string) *Service {
	s.brainNotifier = n
	s.brainOrgID = orgID
	return s
}

// WithAuditLog sets the audit logger for financial operation tracking.
func (s *Service) WithAuditLog(l AuditSink) *Service {
	s.auditLog = l
	return s
}

// WithOutbox sets the event writer.
func (s *Service) WithOutbox(e EventRecorder) *Service { s.events = e; return s }

// WithTxRunner replaces the database as the transaction runner (a test gates
// transactions with it; serve leaves the database).
func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

// WithBranchGuard sets the payload branch rule for the write routes.
func (s *Service) WithBranchGuard(g BranchGuard) *Service { s.branches = g; return s }

// WithKeyStore enables DB-first gateway credential resolution so keys set
// at runtime (system_settings via Tech Admin) take effect without restart.
func (s *Service) WithKeyStore(ks *KeyStore) *Service {
	s.keyStore = ks
	return s
}

// GetPublicKey returns the Run Payments public key for frontend Runner.js integration.
func (s *Service) GetPublicKey() string {
	if s.keyStore != nil {
		if pk := s.keyStore.Resolve().PublicKey; pk != "" {
			return pk
		}
	}
	return s.publicKey
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx != nil {
		return s.tx.RunInTx(ctx, fn)
	}
	return s.db.RunInTx(ctx, fn)
}

func (s *Service) record(ctx context.Context, ev outbox.Event) error {
	if s.events == nil {
		return nil
	}
	return s.events.Write(ctx, ev)
}

func (s *Service) recordAll(ctx context.Context, evs []outbox.Event) error {
	for _, ev := range evs {
		if err := s.record(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, e audit.Entry) error {
	if s.auditLog == nil {
		return nil
	}
	if err := s.auditLog.Log(ctx, e); err != nil {
		return fmt.Errorf("failed to write the audit row: %w", err)
	}
	return nil
}

func conflictBlocker(code, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict, Message: message,
		Details: []httpx.FieldError{httpx.Blocker(code, message)}}
}

func fieldError(field, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed, Message: "the request is not valid",
		Details: []httpx.FieldError{{Field: field, Message: message}}}
}

// Precondition is the client's revision for an act on the payment named in the
// path (ADR 0001 section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

// Caller is who is acting: the audit subject and the role the finance rules
// read. An in process caller has neither.
type Caller struct {
	Actor string
	Role  string
}

func (c Caller) finance(what string) error {
	if c.Role != "" && !account.FinanceRole(c.Role) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden, Message: what + " needs the admin, owner or finance role"}
	}
	return nil
}

// checkBranch is the record branch rule for a branch a payload names.
func (s *Service) checkBranch(ctx context.Context, branch uuid.UUID, field string) error {
	if s.branches == nil {
		return nil
	}
	err := s.branches.CheckPayloadBranch(ctx, branch)
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: "the branch is outside the branches this caller may target",
			Details: []httpx.FieldError{{Field: field, Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
	}
	return err
}

// resolved is a payment request checked against the database, ready to record.
type resolved struct {
	in       *Input
	currency string
	branch   uuid.UUID
}

// resolve reads what the request leaves to the database: the customer's
// currency and branch, and holds the order, the job and the invoices it names
// to the customer and the branch wall. Nothing is locked.
func (s *Service) resolve(ctx context.Context, in *Input) (*resolved, error) {
	facts, err := s.repo.CustomerFacts(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	branch := facts.Branch
	if in.BranchID != nil {
		branch = *in.BranchID
	} else if ctxBranch := middleware.BranchIDForQuery(ctx); ctxBranch != nil {
		branch = *ctxBranch
	}
	if err := s.checkBranch(ctx, branch, "branch_id"); err != nil {
		return nil, err
	}
	if in.OrderID != nil {
		exists, matches, err := s.repo.OrderBelongsTo(ctx, *in.OrderID, in.CustomerID)
		if err != nil {
			return nil, err
		}
		if !exists || !matches {
			return nil, fieldError("order_id", "no such order for this customer")
		}
	}
	if in.JobID != nil {
		exists, matches, err := s.repo.JobBelongsTo(ctx, *in.JobID, in.CustomerID)
		if err != nil {
			return nil, err
		}
		if !exists || !matches {
			return nil, fieldError("job_id", "no such job for this customer")
		}
	}
	for i, a := range in.Applications {
		ok, err := s.repo.InvoiceVisible(ctx, a.InvoiceID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fieldError(fmt.Sprintf("applications[%d].invoice_id", i), "no such invoice")
		}
	}
	return &resolved{in: in, currency: facts.Currency, branch: branch}, nil
}

// recordedEvent is payment.recorded: the payment as it stands after the act.
func recordedEvent(p *Payment) outbox.Event {
	branch := p.BranchID
	data := map[string]any{
		"number": p.Number, "customer_id": p.CustomerID, "status": p.Status.Status(), "revision": p.Revision, "currency": p.Currency,
		"amount_cents": int64(p.AmountCents), "unapplied_cents": int64(p.UnappliedCents), "method": string(p.Method),
	}
	if p.OrderID != nil {
		data["order_id"] = p.OrderID
	}
	return outbox.Event{Type: EventRecorded, EntityType: "payment", EntityID: p.ID, BranchID: &branch, Data: mustJSON(data)}
}

// Event types the payment module writes (the AR core writes the application
// level ones).
const (
	EventRecorded = "payment.recorded"
	EventRefunded = "payment.refunded"
	EventVoided   = "payment.voided"
)

// notifyPaid tells FB Brain's financial engine of the invoices an act closed,
// after the commit.
func (s *Service) notifyPaid(fx *account.Effects) {
	if s.brainNotifier == nil || fx == nil {
		return
	}
	for _, inv := range fx.PaidInvoices() {
		s.brainNotifier.notifyInvoicePaid(s.brainOrgID, inv.ID, inv.TotalCents)
	}
}

// Create records a payment (cash, check, ACH or other), applying it to the
// invoices the request names in the same transaction. Without applications it
// is unapplied cash held in 2200.
func (s *Service) Create(ctx context.Context, in *Input, who Caller) (*Payment, error) {
	res, err := s.resolve(ctx, in)
	if err != nil {
		return nil, err
	}
	p, fx, err := s.record1(ctx, res, nil, who)
	if err != nil {
		return nil, err
	}
	s.notifyPaid(fx)
	return p, nil
}

// record1 is the one transaction that records a payment: the core's receipt
// and applications, the audit row, then payment.recorded and the core's events
// last. card is the gateway's result for a card payment.
func (s *Service) record1(ctx context.Context, res *resolved, card *account.CardFacts, who Caller) (*Payment, *account.Effects, error) {
	in := res.in
	var out *Payment
	var effects *account.Effects
	err := s.inTx(ctx, func(ctx context.Context) error {
		on := time.Time{}
		if in.ReceivedOn != nil {
			on = *in.ReceivedOn
		} else {
			d, err := s.account.LocalDate(ctx, res.branch, s.now())
			if err != nil {
				return err
			}
			on = d
		}
		id, fx, err := s.account.RecordPayment(ctx, account.RecordPaymentIn{
			CustomerID: in.CustomerID, BranchID: res.branch, Currency: res.currency, Method: string(in.Method), AmountCents: in.AmountCents,
			Reference: in.Reference, Notes: in.Notes, ReceivedOn: on, OrderID: in.OrderID, ProjectID: in.JobID, Card: card,
			Actor: who.Actor, Applications: in.Applications,
		})
		if err != nil {
			return err
		}
		effects = fx
		if out, err = s.repo.Get(ctx, id); err != nil {
			return err
		}
		changes := map[string]interface{}{
			"customer_id": in.CustomerID, "number": out.Number, "amount_cents": in.AmountCents, "method": string(in.Method),
			"reference": in.Reference, "applications": len(in.Applications),
		}
		if card != nil {
			changes["gateway_tx_id"], changes["card_brand"], changes["card_last4"] = card.GatewayTxID, card.Brand, card.Last4
		}
		if err := s.audit(ctx, audit.Entry{Action: "payment.processed", EntityType: "payment", EntityID: id, UserID: who.Actor, Changes: changes}); err != nil {
			return err
		}
		if err := s.record(ctx, recordedEvent(out)); err != nil {
			return err
		}
		return s.recordAll(ctx, fx.Events())
	})
	if err != nil {
		return nil, nil, err
	}
	return out, effects, nil
}

// lockForWrite takes the payment row (section 11, step 2) held to the branch
// wall, and checks the revision and the branch rule. It answers the payment as
// read under the lock.
func (s *Service) lockForWrite(ctx context.Context, id uuid.UUID, pre Precondition) (*Summary, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var locked uuid.UUID
	if err := s.db.GetExecutor(ctx).QueryRow(ctx, `SELECT id FROM payments p WHERE p.id = $1 AND `+wall("p", 2, 3)+` FOR UPDATE`,
		id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&locked); err != nil {
		return nil, errPaymentNotFound
	}
	p, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.checkBranch(ctx, p.BranchID, "id"); err != nil {
		return nil, err
	}
	if err := pre.check(p.Revision); err != nil {
		return nil, err
	}
	return &p.Summary, nil
}

// Apply applies a payment's unapplied cash to invoices (section 9.2).
func (s *Service) Apply(ctx context.Context, id uuid.UUID, lines []account.ApplyLine, pre Precondition, who Caller) (*Payment, error) {
	for i, l := range lines {
		ok, err := s.repo.InvoiceVisible(ctx, l.InvoiceID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fieldError(fmt.Sprintf("applications[%d].invoice_id", i), "no such invoice")
		}
	}
	var out *Payment
	var effects *account.Effects
	err := s.inTx(ctx, func(ctx context.Context) error {
		head, err := s.lockForWrite(ctx, id, pre)
		if err != nil {
			return err
		}
		on, err := s.account.LocalDate(ctx, head.BranchID, s.now())
		if err != nil {
			return err
		}
		fx, err := s.account.Apply(ctx, account.ApplyIn{PaymentID: id, Lines: lines, On: on, Actor: who.Actor})
		if err != nil {
			return err
		}
		effects = fx
		if err := s.audit(ctx, audit.Entry{Action: "payment.applied", EntityType: "payment", EntityID: id, UserID: who.Actor,
			Changes: map[string]interface{}{"applications": len(lines), "application_ids": fx.ApplicationIDs}}); err != nil {
			return err
		}
		if out, err = s.repo.Get(ctx, id); err != nil {
			return err
		}
		return s.recordAll(ctx, fx.Events())
	})
	if err != nil {
		return nil, err
	}
	s.notifyPaid(effects)
	return out, nil
}

// Void voids a posted payment: every live application is reversed (with its
// discounts), then the unapplied rest is paid back out of 2200. Finance roles
// only; a card payment is refunded through the gateway instead.
func (s *Service) Void(ctx context.Context, id uuid.UUID, pre Precondition, reason string, who Caller) (*Payment, error) {
	if err := who.finance("voiding a payment"); err != nil {
		return nil, err
	}
	var out *Payment
	err := s.inTx(ctx, func(ctx context.Context) error {
		head, err := s.lockForWrite(ctx, id, pre)
		if err != nil {
			return err
		}
		on, err := s.account.LocalDate(ctx, head.BranchID, s.now())
		if err != nil {
			return err
		}
		fx, err := s.account.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: id, Reason: reason, Actor: who.Actor, On: on})
		if err != nil {
			return err
		}
		if err := s.audit(ctx, audit.Entry{Action: "payment.voided", EntityType: "payment", EntityID: id, UserID: who.Actor,
			Changes: map[string]interface{}{"reason": reason, "number": head.Number, "amount_cents": int64(head.AmountCents), "reversed_applications": len(fx.ReversedIDs)}}); err != nil {
			return err
		}
		if out, err = s.repo.Get(ctx, id); err != nil {
			return err
		}
		if err := s.recordAll(ctx, fx.Events()); err != nil {
			return err
		}
		branch := out.BranchID
		return s.record(ctx, outbox.Event{Type: EventVoided, EntityType: "payment", EntityID: id, BranchID: &branch, Data: mustJSON(map[string]any{
			"number": out.Number, "customer_id": out.CustomerID, "status": out.Status.Status(), "from_status": "posted", "revision": out.Revision,
			"currency": out.Currency, "amount_cents": int64(out.AmountCents), "unapplied_cents": 0,
		})})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Refund pays unapplied cash back out. A card payment's refund goes through the
// gateway before the transaction, as every gateway call does; a database
// failure after a successful gateway refund is logged as critical for
// reconciliation.
func (s *Service) Refund(ctx context.Context, id uuid.UUID, in *RefundInput, pre Precondition, who Caller) (*Refund, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	facts, err := s.repo.GatewayFactsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.checkBranch(ctx, facts.BranchID, "id"); err != nil {
		return nil, err
	}
	head, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := pre.check(head.Revision); err != nil {
		return nil, err
	}
	switch {
	case facts.Status != StatusPosted:
		return nil, conflictBlocker("payment_voided", "the payment is voided: it refunds nothing")
	case in.AmountCents > facts.Unapplied:
		return nil, conflictBlocker("exceeds_unapplied", fmt.Sprintf("the payment has %d cents unapplied: %d cents were asked", facts.Unapplied, in.AmountCents))
	}
	gatewayID := ""
	if facts.Method == PaymentMethodCard {
		if s.gateway == nil {
			return nil, httpx.Unavailable("payment gateway not configured")
		}
		if facts.GatewayTxID == "" {
			return nil, conflictBlocker("no_gateway_transaction", "the card payment has no gateway transaction to refund")
		}
		res, gerr := s.gateway.Refund(ctx, facts.GatewayTxID, in.AmountCents)
		if gerr != nil {
			return nil, &httpx.Error{Status: http.StatusBadGateway, Code: httpx.CodeUnavailable, Message: "gateway refund failed: " + gerr.Error()}
		}
		gatewayID = res.TransactionID
	}
	var refundID uuid.UUID
	err = s.inTx(ctx, func(ctx context.Context) error {
		locked, err := s.lockForWrite(ctx, id, pre)
		if err != nil {
			return err
		}
		on, err := s.account.LocalDate(ctx, locked.BranchID, s.now())
		if err != nil {
			return err
		}
		rid, _, err := s.account.RefundPayment(ctx, account.RefundPaymentIn{PaymentID: id, AmountCents: in.AmountCents, Reason: in.Reason,
			GatewayRefundID: gatewayID, Actor: who.Actor, On: on})
		if err != nil {
			return err
		}
		refundID = rid
		if err := s.audit(ctx, audit.Entry{Action: "payment.refunded", EntityType: "refund", EntityID: rid, UserID: who.Actor,
			Changes: map[string]interface{}{"payment_id": id, "amount_cents": in.AmountCents, "reason": in.Reason, "gateway_id": gatewayID}}); err != nil {
			return err
		}
		after, err := s.repo.Get(ctx, id)
		if err != nil {
			return err
		}
		branch := after.BranchID
		return s.record(ctx, outbox.Event{Type: EventRefunded, EntityType: "payment", EntityID: id, BranchID: &branch, Data: mustJSON(map[string]any{
			"number": after.Number, "customer_id": after.CustomerID, "status": after.Status.Status(), "revision": after.Revision,
			"currency": after.Currency, "amount_cents": in.AmountCents, "unapplied_cents": int64(after.UnappliedCents), "refund_id": rid,
		})})
	})
	if err != nil {
		if gatewayID != "" {
			s.logger.Error("CRITICAL: Gateway refunded but DB commit failed",
				"gateway_refund_id", gatewayID, "payment_id", id, "amount_cents", in.AmountCents, "error", err)
		}
		return nil, err
	}
	refunds, err := s.repo.Refunds(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range refunds {
		if refunds[i].ID == refundID {
			return &refunds[i], nil
		}
	}
	return nil, errors.New("payment: the refund was not found after it was recorded")
}

// Get reads one payment with its applications and refunds.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Payment, error) {
	p, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	apps, err := s.account.ListApplications(ctx, account.AppFilter{PaymentID: &id})
	if err != nil {
		return nil, err
	}
	p.Applications = apps
	return p, nil
}

// List answers one page and whether another follows.
func (s *Service) List(ctx context.Context, f ListFilter, wantTotal bool) ([]Summary, bool, *int64, error) {
	asked := f.Limit
	f.Limit = asked + 1
	items, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	more := len(items) > asked
	if more {
		items = items[:asked]
	}
	var total *int64
	if wantTotal {
		n, err := s.repo.Count(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return items, more, total, nil
}

func mustJSON(v map[string]any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}
