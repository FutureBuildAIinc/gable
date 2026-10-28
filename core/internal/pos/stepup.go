// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The manager step up (CP-04 slice 3, with CP-08's pickup and sign off and
// CP-11's override way forward): a cashier at the terminal asks a manager,
// the manager picks themselves from the roster and types their own counter
// PIN, and the server hands back one single use approval the act consumes.
// The PIN is the authority, so the step up works under any auth mode; a call
// with neither claims nor a token keeps the old dev mode answers.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/staff"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// pinVerifySem bounds concurrent Argon2id checks so a burst of step up calls
// cannot saturate CPU cores while waiting on row locks.
var pinVerifySem = make(chan struct{}, 4)

// verifyStepUpPIN is the staff package's constant time check: the hash and
// the PIN never meet anywhere else.
func verifyStepUpPIN(hash, pin string) bool {
	pinVerifySem <- struct{}{}
	defer func() { <-pinVerifySem }()
	return staff.VerifyCounterPIN(hash, pin)
}

// The acts a step up can approve, lowercase on the wire (ADR 0001).
const (
	StepUpActOverride = "override"
	StepUpActPickup   = "pickup"
	StepUpActSignoff  = "signoff"
)

// stepUpExpiry is how long a minted approval lives: long enough for the
// cashier's screen to spend it, short enough that a written down token is
// worthless within minutes.
const stepUpExpiry = 5 * time.Minute

// stepUpMaxFailures is the consecutive wrong PINs that lock a manager's PIN
// until an admin unlocks it on the staff screen.
const stepUpMaxFailures = 5

// stepUpRoles are the roles each act's approver needs, the same roles the
// act's own route demands of a signed in caller: an override is approved by
// an admin or owner (overrideRoles), a sign off by finance or owner (the
// resolve mount), a pickup is taken by any of the movement route's own
// roles.
func stepUpRoles(act string) []string {
	switch act {
	case StepUpActOverride:
		return overrideRoles
	case StepUpActSignoff:
		return []string{"finance", "owner"}
	case StepUpActPickup:
		return []string{"admin", "owner", "cashier"}
	}
	return nil
}

// stepUpRefusalWords are the refusals the counter screens say plainly.
func stepUpRefused() error {
	return &httpx.Error{Status: http.StatusForbidden, Code: "step_up_refused",
		Message: "That PIN is not right"}
}

func stepUpLocked() error {
	return &httpx.Error{Status: http.StatusForbidden, Code: "step_up_locked",
		Message: "This manager's PIN is locked: an admin can unlock it"}
}

func stepUpRole(act string) error {
	return &httpx.Error{Status: http.StatusForbidden, Code: "step_up_role",
		Message: "This manager's role cannot approve this"}
}

func stepUpOwn(act string) error {
	if act == StepUpActOverride {
		return &httpx.Error{Status: http.StatusForbidden, Code: "step_up_own_sale",
			Message: "A manager cannot approve their own sale"}
	}
	return &httpx.Error{Status: http.StatusForbidden, Code: "step_up_own_drawer",
		Message: "A manager cannot approve their own drawer"}
}

// StepUpManager is a roster row the step up reads.
type StepUpManager struct {
	ID       uuid.UUID
	FullName string
	StaffNo  string
	Email    string
	Role     string
	Active   bool
	HasPIN   bool
	Locked   bool
	Hash     string
}

// managerIdentifiers are the strings a signed in subject may hold for this
// manager: the staff id, the staff number, the email. The existing rules
// compare the approver with the cashier's subject, so every identifier is
// checked before the step up says the manager is the cashier.
func (m *StepUpManager) identifiers() []string {
	return []string{m.ID.String(), m.StaffNo, m.Email}
}

// isSubject reports whether the signed in subject names this manager.
func (m *StepUpManager) isSubject(sub string) bool {
	if sub == "" {
		return false
	}
	for _, id := range m.identifiers() {
		if id != "" && strings.EqualFold(id, sub) {
			return true
		}
	}
	return false
}

// StepUpIn is a parsed step up request. The manager approves exactly what it
// names: an override's kind and cap, a pickup's amount. The act refuses a
// request whose terms differ.
type StepUpIn struct {
	Act         string
	Kind        string
	Manager     string
	PIN         string
	TargetID    uuid.UUID
	AmountCents *int64
}

// StepUpCaller is the signed in person asking, from their claims: at the
// terminal the caller is the act's own cashier (the sale they are ringing,
// the drawer they are working), so a manager who is the caller is the one
// person the act's rules refuse.
type StepUpCaller struct {
	Subject string
	Email   string
}

// StepUpApproval is the wire answer of a verified step up: the token the one
// act spends, and the manager's name for the screen to say.
type StepUpApproval struct {
	Token       string          `json:"token"`
	ManagerName string          `json:"manager_name"`
	ExpiresAt   httpx.Timestamp `json:"expires_at"`
}

// StepUpManagerSummary is one row of the picker's list: a manager the act's
// roles admit, with a PIN set.
type StepUpManagerSummary struct {
	ID       uuid.UUID `json:"id"`
	FullName string    `json:"full_name"`
	StaffNo  string    `json:"staff_no"`
	Role     string    `json:"role"`
}

// stepUpRequest is POST /api/v1/pos/step-up.
type stepUpRequest struct {
	Act         *string         `json:"act"`
	Kind        *string         `json:"kind"`
	Manager     *string         `json:"manager"`
	PIN         *string         `json:"pin"`
	TargetID    string          `json:"target_id"`
	AmountCents json.RawMessage `json:"amount_cents"`
}

func (r *stepUpRequest) parse() (*StepUpIn, error) {
	v := &httpx.Validator{}
	out := &StepUpIn{}
	if r.Act != nil {
		out.Act = *r.Act
	}
	v.Check(out.Act == StepUpActOverride || out.Act == StepUpActPickup || out.Act == StepUpActSignoff,
		"act", "must be override, pickup or signoff")
	if r.Manager != nil {
		out.Manager = strings.TrimSpace(*r.Manager)
	}
	v.Required("manager", out.Manager)
	pin := ""
	if r.PIN != nil {
		pin = *r.PIN
	}
	v.Required("pin", pin)
	out.PIN = pin
	id, err := uuid.Parse(r.TargetID)
	if err != nil {
		v.Check(false, "target_id", "must be a UUID")
	} else {
		out.TargetID = id
	}
	if r.Kind != nil {
		out.Kind = *r.Kind
	}
	if n, ok := v.Int("amount_cents", r.AmountCents, false); ok {
		v.Check(n > 0, "amount_cents", "must be greater than zero")
		out.AmountCents = &n
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	// the manager approves the terms the act will spend: an override names
	// its kind and its cap, a pickup its amount, and no other act carries an
	// amount (a minted approval must always be spendable)
	switch out.Act {
	case StepUpActOverride:
		if out.Kind != OverrideAccount && out.Kind != OverrideCheck && out.Kind != OverrideRefundMethod {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "kind", Message: "is required when the act is an override: account, check or refund_method"})
		}
		if out.AmountCents == nil {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "amount_cents", Message: "is required when the act is an override: the cap the manager approves"})
		}
	case StepUpActPickup:
		if out.Kind != "" {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "kind", Message: "only an override names a kind"})
		}
		if out.AmountCents == nil {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "amount_cents", Message: "is required when the act is a pickup"})
		}
	default:
		if out.Kind != "" {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "kind", Message: "only an override names a kind"})
		}
		if out.AmountCents != nil {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "amount_cents", Message: "only a pickup or an override carries an amount"})
		}
	}
	return out, nil
}

// StepUp verifies a manager's counter PIN and mints the single use approval
// for one act, one target and the terms the manager saw. The PIN's failure
// count and lock live on the staff row; the token is stored only as its
// SHA-256 hash. The caller is the person at the terminal; a manager who is
// the caller is the one person the act's rules refuse.
func (s *Service) StepUp(ctx context.Context, in *StepUpIn, caller *StepUpCaller) (*StepUpApproval, error) {
	var callerSub, callerEmail string
	if caller != nil {
		callerSub, callerEmail = caller.Subject, caller.Email
	}
	var approval *StepUpApproval
	var stepUpErr error
	err := s.inTx(ctx, func(ctx context.Context) error {
		// lock the manager's roster row first so concurrent wrong tries against
		// the same manager queue on the row lock and stop at the fifth try
		m, ok, err := s.repo.FindStepUpManagerForUpdate(ctx, in.Manager)
		if err != nil {
			return err
		}
		// an unknown manager, an inactive one and a missing PIN all answer the
		// same plain refusal: the answer never says which part was wrong
		if !ok || !m.Active || !m.HasPIN {
			stepUpErr = stepUpRefused()
			return nil
		}
		if m.Locked {
			stepUpErr = stepUpLocked()
			return nil
		}
		if !verifyStepUpPIN(m.Hash, in.PIN) {
			if err := s.repo.RecordStepUpFailure(ctx, m.ID, stepUpMaxFailures); err != nil {
				return err
			}
			stepUpErr = stepUpRefused()
			return nil
		}
		if !roleAllowed(stepUpRoles(in.Act), m.Role) {
			stepUpErr = stepUpRole(in.Act)
			return nil
		}
		// under real sign in the cashier's subject is not the roster row's id:
		// the caller's own claims (subject and email) name the person the act's
		// rules refuse, whatever the subject's shape
		if m.isSubject(callerSub) || (callerEmail != "" && strings.EqualFold(m.Email, callerEmail)) {
			stepUpErr = stepUpOwn(in.Act)
			return nil
		}
		if err := s.checkStepUpTarget(ctx, in, m); err != nil {
			stepUpErr = err
			return nil
		}
		token, err := newStepUpToken()
		if err != nil {
			return err
		}
		hash := stepUpTokenHash(token)
		expires := s.now().Add(stepUpExpiry)
		unlocked, err := s.repo.ResetStepUpFailuresUnlocked(ctx, m.ID)
		if err != nil {
			return err
		}
		if !unlocked {
			stepUpErr = stepUpLocked()
			return nil
		}
		if err := s.repo.InsertStepUpApproval(ctx, in.Act, in.Kind, in.TargetID, in.AmountCents, hash, m.ID, m.FullName, expires); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.step_up.approved", EntityType: "pos_step_up", EntityID: in.TargetID, UserID: m.ID.String(),
				Changes: map[string]any{"act": in.Act, "manager_staff_id": m.ID, "manager_name": m.FullName,
					"target_id": in.TargetID}}); err != nil {
				return err
			}
		}
		approval = &StepUpApproval{Token: token, ManagerName: m.FullName, ExpiresAt: httpx.TimestampOf(expires)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if stepUpErr != nil {
		return nil, stepUpErr
	}
	return approval, nil
}

// StaffIDByEmail resolves a signed in user's email to their roster row's id
// when one exists, so the counter's cashier and actor fields name the same
// staff id the step up picker and person rules use.
func (s *Service) StaffIDByEmail(ctx context.Context, email string) uuid.UUID {
	email = strings.TrimSpace(email)
	if email == "" {
		return uuid.Nil
	}
	m, ok, err := s.repo.FindStepUpManager(ctx, email)
	if err != nil || !ok {
		return uuid.Nil
	}
	return m.ID
}

// checkStepUpTarget holds the acts' own person rules at the step up: the
// manager is not the sale's cashier (the override rule), not the drawer's
// cashier (the pickup rule), not the closer or the cashier (the sign off
// rule). The targets must be in the state the act waits for.
func (s *Service) checkStepUpTarget(ctx context.Context, in *StepUpIn, m *StepUpManager) error {
	switch in.Act {
	case StepUpActOverride:
		sale, err := s.repo.GetSale(ctx, in.TargetID)
		if err != nil {
			return err
		}
		if m.isSubject(sale.CashierID.String()) {
			return stepUpOwn(in.Act)
		}
	case StepUpActPickup:
		session, err := s.repo.GetTillSession(ctx, in.TargetID)
		if err != nil {
			return err
		}
		if m.isSubject(session.CashierID.String()) {
			return stepUpOwn(in.Act)
		}
	case StepUpActSignoff:
		session, err := s.repo.GetTillSession(ctx, in.TargetID)
		if err != nil {
			return err
		}
		if session.Status != TillPendingSignoff {
			return httpx.InvalidStateTransition("the till session is not waiting for sign off")
		}
		if m.isSubject(session.CashierID.String()) {
			return stepUpOwn(in.Act)
		}
		if session.ClosedBy != nil && m.isSubject(*session.ClosedBy) {
			return stepUpOwn(in.Act)
		}
	}
	return nil
}

// StepUpManagers lists the roster the picker shows: active staff with a PIN
// whose role the act admits.
func (s *Service) StepUpManagers(ctx context.Context, act string) ([]StepUpManagerSummary, error) {
	return s.repo.ListStepUpManagers(ctx, stepUpRoles(act))
}

// StepUpManagerRef names the manager an approval carried: the name the act's
// own columns record, and the staff id its audit row and its person rules
// use.
type StepUpManagerRef struct {
	Name    string
	StaffID string
}

// consumeStepUpApproval spends one approval inside the act's own
// transaction: the right act, the right target, the terms the manager saw
// (an override's kind and cap, a pickup's amount), unexpired and
// unconsumed. The answer names the manager the act records.
func (s *Service) consumeStepUpApproval(ctx context.Context, act, kind string, target uuid.UUID, amountCents *int64, token string) (StepUpManagerRef, error) {
	ref, ok, err := s.repo.ConsumeStepUpApproval(ctx, act, kind, target, amountCents, stepUpTokenHash(strings.TrimSpace(token)))
	if err != nil {
		return StepUpManagerRef{}, err
	}
	if !ok {
		return StepUpManagerRef{}, &httpx.Error{Status: http.StatusConflict, Code: "step_up_spent",
			Message: "This approval does not cover this, is used up or expired: ask the manager again"}
	}
	return ref, nil
}

// roleAllowed reports whether the staff role is one the act admits,
// case-insensitively: the roster's role is a label an admin types.
func roleAllowed(allowed []string, role string) bool {
	for _, a := range allowed {
		if strings.EqualFold(a, role) {
			return true
		}
	}
	return false
}

// newStepUpToken mints the opaque token one act spends.
func newStepUpToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// stepUpTokenHash is the only shape of the token the server stores.
func stepUpTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// StepUpHandler is POST /api/v1/pos/step-up: any signed in person at the
// counter may ask; the manager's PIN is the authority, so the caller's own
// role is never checked here.
func (h *Handler) StepUpHandler(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a step up is asked by a person, never a machine key") {
		return
	}
	var req stepUpRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	caller := &StepUpCaller{}
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		caller.Subject, caller.Email = claims.Subject, claims.Email
	}
	approval, err := h.service.StepUp(r.Context(), in, caller)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, approval)
}

// StepUpManagersHandler is GET /api/v1/pos/step-up/managers?act=: the picker's
// list. It names managers and their staff numbers only, never a hint of the
// PIN beyond the fact one is set.
func (h *Handler) StepUpManagersHandler(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a step up is asked by a person, never a machine key") {
		return
	}
	q, err := httpx.StrictQuery(r, "act")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	act := q.Get("act")
	if stepUpRoles(act) == nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "act", Message: "must be override, pickup or signoff"}))
		return
	}
	items, err := h.service.StepUpManagers(r.Context(), act)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, items, "", len(items))
}
