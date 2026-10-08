// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox. The
// service calls it as the LAST statement of the mutation's transaction
// (ADR 0003 section 2), so the event commits or rolls back with the mutation
// it describes. *outbox.Writer satisfies it.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// AuditLogger writes an audit row for a customer (entity type customer). Called
// with the transaction's context the row joins the act's transaction. The
// customer package cannot import pkg/audit (audit reaches pkg/middleware, which
// imports this package), so customeraudit adapts the platform logger to it.
type AuditLogger interface {
	LogChange(ctx context.Context, action string, customerID uuid.UUID, changes map[string]any) error
}

// Event types the module writes to the outbox. Every state change of a
// customer, its ship-tos, contacts and terms is customer.updated; its data
// names the part (ADR 0005 section 7.4).
const (
	EventCreated = "customer.created"
	EventUpdated = "customer.updated"
)

// Parts a customer.updated event names.
const (
	PartHeader           = "header"
	PartTerms            = "terms"
	PartShipTo           = "ship_to"
	PartContact          = "contact"
	PartEscalationPolicy = "escalation_policy"
)

type Service struct {
	repo   Repository
	events EventRecorder // optional; nil records nothing (unit tests)
	tx     TxRunner      // optional; nil runs each method unwrapped (unit tests)
	audit  AuditLogger   // optional; nil writes no audit rows (unit tests)
	now    func() time.Time
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, now: time.Now}
}

// WithOutbox wires the recorder of customer.created and customer.updated.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithAudit wires the audit rows of the control changes: the credit limit, the
// payment terms, the PO requirement and a contact's order authority. Every
// other customer write keeps its event only.
func (s *Service) WithAudit(a AuditLogger) *Service {
	s.audit = a
	return s
}

// WithTxRunner wires the transaction wrapper every write uses, so the
// mutation, its revision move and its event are one transactional fact.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// Precondition is the client's revision for a write: the If-Match header
// and/or the body's revision (ADR 0001 section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

func needRevision(p Precondition) error {
	if p.missing() {
		return httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	return nil
}

// notFound maps the repository's sentinels to the wire's 404.
func notFound(err error) error {
	for _, s := range []error{ErrNotFound, ErrShipToNotFound, ErrContactNotFound, ErrTermsNotFound} {
		if errors.Is(err, s) {
			return httpx.NotFound(s.Error())
		}
	}
	return err
}

func fieldProblem(field, msg string) error {
	v := &httpx.Validator{}
	v.Check(false, field, msg)
	return v.Err()
}

// ---- customers ----

// GetCustomer reads one customer. It is the read the other modules (orders,
// pricing, the portal, documents) use; a missing one is ErrNotFound.
func (s *Service) GetCustomer(ctx context.Context, id uuid.UUID) (*Customer, error) {
	return s.repo.GetCustomer(ctx, id)
}

// Get is GetCustomer for the handler: a missing customer is the wire's 404.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Customer, error) {
	c, err := s.repo.GetCustomer(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return c, nil
}

// checkCurrency validates a customer's currency override: a supported code
// that the dealer has enabled.
func (s *Service) checkCurrency(ctx context.Context, code *string) error {
	if code == nil {
		return nil
	}
	if !SupportedCurrency(*code) {
		return fieldProblem("currency", "must be an ISO 4217 code with a minor unit of 0 or 2 places")
	}
	enabled, err := s.repo.EnabledCurrencies(ctx)
	if err != nil {
		return err
	}
	for _, e := range enabled {
		if e == *code {
			return nil
		}
	}
	return fieldProblem("currency", "is not an enabled currency: "+strings.Join(enabled, ", "))
}

// checkTerms validates a payment terms reference: it must exist and, unless
// the customer already holds it, be active.
func (s *Service) checkTerms(ctx context.Context, id uuid.UUID, held *uuid.UUID) error {
	t, err := s.repo.GetTerms(ctx, id)
	if errors.Is(err, ErrTermsNotFound) {
		return fieldProblem("payment_terms_id", "no such payment terms")
	}
	if err != nil {
		return err
	}
	if !t.IsActive && (held == nil || *held != id) {
		return fieldProblem("payment_terms_id", "these payment terms are inactive")
	}
	return nil
}

func (s *Service) checkBranch(ctx context.Context, branch *uuid.UUID) error {
	if branch == nil {
		return nil
	}
	if w := branchctx.IDForQuery(ctx); w != nil && *w != *branch {
		return fieldProblem("primary_branch_id", "is outside the branch of this request")
	}
	return nil
}

// Create validates the references, stores the customer and writes
// customer.created as the transaction's last statement. A create that fails
// anywhere leaves no customer and no event.
func (s *Service) Create(ctx context.Context, d *Draft) (*Customer, error) {
	var out *Customer
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.checkBranch(ctx, d.PrimaryBranchID); err != nil {
			return err
		}
		if err := s.checkCurrency(ctx, d.Currency); err != nil {
			return err
		}
		if d.PaymentTermsID != nil {
			if err := s.checkTerms(ctx, *d.PaymentTermsID, nil); err != nil {
				return err
			}
		}
		now := httpx.TimestampOf(s.now().UTC())
		c := &Customer{
			ID: uuid.New(), AccountNumber: d.AccountNumber, Name: d.Name,
			Email: d.Email, Phone: d.Phone, Address: d.Address,
			Tier: d.Tier, IsActive: d.IsActive, PriceLevelID: d.PriceLevelID, SalespersonID: d.SalespersonID,
			CreditLimitCents: d.CreditLimitCents, Currency: d.Currency, POrequired: d.POrequired,
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		if d.PaymentTermsID != nil {
			c.PaymentTermsID = *d.PaymentTermsID
		}
		if d.PrimaryBranchID != nil {
			c.PrimaryBranchID = *d.PrimaryBranchID
		}
		if err := s.repo.InsertCustomer(ctx, c); err != nil {
			return err
		}
		var err error
		if out, err = s.repo.GetCustomer(ctx, c.ID); err != nil {
			return notFound(err)
		}
		return s.record(ctx, out, EventCreated, "", nil)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListCustomers returns one page: up to f.Limit rows, and whether more follow
// (the repository is asked for one extra row to know). total is set only when
// the caller asks, the opt in count of ADR 0001 section 1.
func (s *Service) ListCustomers(ctx context.Context, f ListFilter, wantTotal bool) (items []Customer, hasMore bool, total *int64, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListCustomers(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	if wantTotal {
		n, err := s.repo.CountCustomers(ctx, f)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

// Update replaces a customer's header on the client's revision. The currency
// override cannot change while the customer has an open document.
func (s *Service) Update(ctx context.Context, id uuid.UUID, d *Draft, pre Precondition) (*Customer, error) {
	if err := needRevision(pre); err != nil {
		return nil, err
	}
	var out *Customer
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockCustomer(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetCustomer(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}

		next := *cur
		next.AccountNumber, next.Name = d.AccountNumber, d.Name
		next.Email, next.Phone, next.Address = d.Email, d.Phone, d.Address
		next.Tier, next.IsActive = d.Tier, d.IsActive
		next.PriceLevelID, next.SalespersonID = d.PriceLevelID, d.SalespersonID
		next.CreditLimitCents, next.Currency, next.POrequired = d.CreditLimitCents, d.Currency, d.POrequired
		if d.PaymentTermsID != nil {
			next.PaymentTermsID = *d.PaymentTermsID
		} else {
			def, err := s.defaultTermsID(ctx)
			if err != nil {
				return err
			}
			next.PaymentTermsID = def
		}
		next.UpdatedAt = httpx.TimestampOf(s.now().UTC())

		changed := diffCustomer(cur, &next)
		if contains(changed, "currency") {
			if err := s.checkCurrency(ctx, next.Currency); err != nil {
				return err
			}
			kinds, err := s.repo.OpenDocuments(ctx, id)
			if err != nil {
				return err
			}
			if len(kinds) > 0 {
				return &httpx.Error{Status: 409, Code: httpx.CodeConflict,
					Message: "the currency cannot change while the customer has open documents",
					Details: []httpx.FieldError{httpx.Blocker("open_documents", "open "+strings.Join(kinds, ", "))}}
			}
		}
		if contains(changed, "payment_terms_id") {
			held := cur.PaymentTermsID
			if err := s.checkTerms(ctx, next.PaymentTermsID, &held); err != nil {
				return err
			}
		}
		if err := s.repo.UpdateCustomer(ctx, &next); err != nil {
			return err
		}
		if out, err = s.repo.GetCustomer(ctx, id); err != nil {
			return notFound(err)
		}
		if err := s.auditControls(ctx, cur, &next, changed); err != nil {
			return err
		}
		part := PartHeader
		if len(changed) == 1 && changed[0] == "payment_terms_id" {
			part = PartTerms
		}
		return s.record(ctx, out, EventUpdated, part, map[string]any{"changed": nonNil(changed)})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) defaultTermsID(ctx context.Context) (uuid.UUID, error) {
	id, err := s.repo.DefaultTermsID(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if id == uuid.Nil {
		return uuid.Nil, fieldProblem("payment_terms_id", "is required: the default terms NET30 do not exist")
	}
	return id, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func eqStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eqID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// diffCustomer names the wire fields a PUT changed.
func diffCustomer(a, b *Customer) []string {
	var out []string
	add := func(changed bool, name string) {
		if changed {
			out = append(out, name)
		}
	}
	add(a.AccountNumber != b.AccountNumber, "account_number")
	add(a.Name != b.Name, "name")
	add(!eqStr(a.Email, b.Email), "email")
	add(!eqStr(a.Phone, b.Phone), "phone")
	add(!eqStr(a.Address, b.Address), "address")
	add(a.Tier != b.Tier, "tier")
	add(a.IsActive != b.IsActive, "is_active")
	add(!eqID(a.PriceLevelID, b.PriceLevelID), "price_level_id")
	add(!eqID(a.SalespersonID, b.SalespersonID), "salesperson_id")
	add((a.CreditLimitCents == nil) != (b.CreditLimitCents == nil) ||
		(a.CreditLimitCents != nil && *a.CreditLimitCents != *b.CreditLimitCents), "credit_limit_cents")
	add(!eqStr(a.Currency, b.Currency), "currency")
	add(a.PaymentTermsID != b.PaymentTermsID, "payment_terms_id")
	add(a.POrequired != b.POrequired, "po_required")
	return out
}

// SetSalesperson assigns or clears the customer's salesperson on the
// client's revision.
func (s *Service) SetSalesperson(ctx context.Context, id uuid.UUID, d *SalespersonDraft, pre Precondition) (*Customer, error) {
	if err := needRevision(pre); err != nil {
		return nil, err
	}
	var out *Customer
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockCustomer(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetCustomer(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		if err := s.repo.SetSalesperson(ctx, id, d.SalespersonID); err != nil {
			return err
		}
		if out, err = s.repo.GetCustomer(ctx, id); err != nil {
			return notFound(err)
		}
		return s.record(ctx, out, EventUpdated, PartHeader, map[string]any{"changed": []string{"salesperson_id"}})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- escalation policy ----

func (s *Service) GetEscalationPolicy(ctx context.Context, id uuid.UUID) (*EscalationPolicy, error) {
	p, err := s.repo.GetEscalationPolicy(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return p, nil
}

// SetEscalationPolicy persists the policy on the client's revision.
// AUTO_ESCALATE needs a signed agreement timestamp (also a CHECK constraint
// of migration 081). A reference without a timestamp is stamped at now, so
// the reference is never recorded without a date.
func (s *Service) SetEscalationPolicy(ctx context.Context, id uuid.UUID, d *PolicyDraft, pre Precondition) (*EscalationPolicy, error) {
	if err := needRevision(pre); err != nil {
		return nil, err
	}
	var out *EscalationPolicy
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockCustomer(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetCustomer(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		p := &EscalationPolicy{CustomerID: id, Policy: d.Policy, ThresholdPercent: d.ThresholdPercent,
			AgreementSignedAt: d.AgreementSignedAt, AgreementRef: d.AgreementRef}
		if p.AgreementSignedAt == nil && p.AgreementRef != nil {
			now := httpx.TimestampOf(s.now().UTC())
			p.AgreementSignedAt = &now
		}
		if err := s.repo.SetEscalationPolicy(ctx, p); err != nil {
			return err
		}
		if out, err = s.repo.GetEscalationPolicy(ctx, id); err != nil {
			return notFound(err)
		}
		updated, err := s.repo.GetCustomer(ctx, id)
		if err != nil {
			return notFound(err)
		}
		return s.record(ctx, updated, EventUpdated, PartEscalationPolicy, nil)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListPriceLevels is one page of price levels.
func (s *Service) ListPriceLevels(ctx context.Context, f ChildFilter, wantTotal bool) (items []PriceLevel, hasMore bool, total *int64, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.ListPriceLevels(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	if wantTotal {
		n, err := s.repo.CountPriceLevels(ctx)
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

// record writes the customer's event into the outbox through the
// transaction's executor. part names what changed on a customer.updated.
func (s *Service) record(ctx context.Context, c *Customer, eventType, part string, extra map[string]any) error {
	if s.events == nil {
		return nil
	}
	data := map[string]any{
		"number":             c.AccountNumber,
		"customer_id":        c.ID,
		"revision":           c.Revision,
		"currency":           c.EffectiveCurrency,
		"credit_limit_cents": nil,
		"balance_cents":      int64(c.BalanceCents),
	}
	if c.CreditLimitCents != nil {
		data["credit_limit_cents"] = int64(*c.CreditLimitCents)
	}
	if part != "" {
		data["part"] = part
	}
	for k, v := range extra {
		data[k] = v
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	branch := c.PrimaryBranchID
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "customer", EntityID: c.ID, BranchID: &branch, Data: raw,
	})
}

// auditControls writes one audit row, inside the transaction, when the update
// changed the credit limit, the payment terms or the PO requirement. It names
// each control that changed with its old and new value.
func (s *Service) auditControls(ctx context.Context, cur, next *Customer, changed []string) error {
	if s.audit == nil {
		return nil
	}
	changes := map[string]any{}
	for _, field := range changed {
		switch field {
		case "credit_limit_cents":
			changes[field] = map[string]any{"from": centsPtrValue(cur.CreditLimitCents), "to": centsPtrValue(next.CreditLimitCents)}
		case "payment_terms_id":
			changes[field] = map[string]any{"from": cur.PaymentTermsID, "to": next.PaymentTermsID}
		case "po_required":
			changes[field] = map[string]any{"from": cur.POrequired, "to": next.POrequired}
		}
	}
	if len(changes) == 0 {
		return nil
	}
	changes["account_number"] = next.AccountNumber
	if err := s.audit.LogChange(ctx, "customer.controls_changed", cur.ID, changes); err != nil {
		return fmt.Errorf("failed to write audit log: %w", err)
	}
	return nil
}

func centsPtrValue(c *httpx.Cents) any {
	if c == nil {
		return nil
	}
	return int64(*c)
}
