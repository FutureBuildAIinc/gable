// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// A register's device (ADR 0011 section 6.1, slice O2): a manager enrols the
// shell's Ed25519 key online, a new enrolment raises the epoch and revokes
// the previous key, and a revocation sends the old key's later sales to the
// exception queue as device_revoked (the sync's own check). Enrolment and
// revocation are owner or admin acts, never a machine key, and the manager
// proves presence at the register with the counter PIN; both are audited.
//
// The shell side of the enrolment (the key pair generated in the keychain,
// the access token it calls this route with) is ADR 0012 slice 5's keychain
// token; until it lands the tests stand in for the shell. The revocation's
// end of the web sessions signed in on the register's shell is the
// RegisterSessionEnder seam: nothing on the server records a session to end
// yet (ADR 0012's staff_sessions), so with no ender wired the answer says so.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// RegisterDevice is a register's enrolment of one epoch on the wire.
type RegisterDevice struct {
	RegisterID   string           `json:"register_id"`
	DeviceEpoch  int64            `json:"device_epoch"`
	PublicKey    string           `json:"public_key"`
	EnrolledBy   string           `json:"enrolled_by"`
	EnrolledAt   httpx.Timestamp  `json:"enrolled_at"`
	RevokedBy    *string          `json:"revoked_by"`
	RevokedAt    *httpx.Timestamp `json:"revoked_at"`
	RevokeReason *string          `json:"revoke_reason"`
	HighSeq      int64            `json:"high_seq"`
	ChainHead    string           `json:"chain_head"`
	ConfirmedSeq int64            `json:"confirmed_seq"`
	ReportedSeq  int64            `json:"reported_seq"`
}

// RegisterSessionsEnded says what the revocation did to the web sessions
// signed in on the register's shell (ADR 0011 section 6.1).
type RegisterSessionsEnded struct {
	Supported bool   `json:"supported"`
	Ended     int    `json:"ended"`
	Message   string `json:"message"`
}

// RegisterEnrolment is the register's enrolment as the manager screen and
// the shell read it: the active device, the last revoked one and the epoch
// the next enrolment must name. An enrolment answer names the unknown tail
// item it left, a revocation answer the sessions it ended.
type RegisterEnrolment struct {
	RegisterID             string                 `json:"register_id"`
	BranchID               uuid.UUID              `json:"branch_id"`
	NextEpoch              int64                  `json:"next_epoch"`
	Device                 *RegisterDevice        `json:"device"`
	Previous               *RegisterDevice        `json:"previous"`
	UnknownTailExceptionID *uuid.UUID             `json:"unknown_tail_exception_id"`
	Sessions               *RegisterSessionsEnded `json:"sessions"`
}

// RegisterSessionEnder ends the web sessions signed in on a register's
// shell, where the sign in method can revoke them (ADR 0011 section 6.1).
// It is the seam ADR 0012's sessions fill; it answers how many it ended.
type RegisterSessionEnder interface {
	EndRegisterSessions(ctx context.Context, registerID string, epoch int64, actor string) (int, error)
}

// WithRegisterSessionEnder wires the session end a revocation calls.
func (s *Service) WithRegisterSessionEnder(e RegisterSessionEnder) *Service {
	s.sessionEnder = e
	return s
}

// offlineOpenSession is a register's open till session as the snapshot and
// the unknown tail item read it.
type offlineOpenSession struct {
	ID                uuid.UUID
	CashierID         uuid.UUID
	OpenedAt          time.Time
	OpeningFloatCents int64
	BusinessDate      time.Time
}

// EnrolDeviceIn is a parsed enrolment: the epoch the shell made its key for
// (the register's next), the key, the manager and the PIN, and the mark the
// shell's keychain kept for the epoch it replaces.
type EnrolDeviceIn struct {
	DeviceEpoch  int64
	PublicKey    string
	Manager      string
	PIN          string
	PriorEpoch   *int64
	PriorLastSeq *int64
}

// RevokeDeviceIn is a parsed revocation.
type RevokeDeviceIn struct {
	DeviceEpoch int64
	Reason      string
	Manager     string
	PIN         string
}

type enrolDeviceRequest struct {
	DeviceEpoch json.RawMessage `json:"device_epoch"`
	PublicKey   *string         `json:"public_key"`
	Manager     *string         `json:"manager"`
	PIN         *string         `json:"pin"`
	Prior       *struct {
		DeviceEpoch json.RawMessage `json:"device_epoch"`
		LastSeq     json.RawMessage `json:"last_seq"`
	} `json:"previous_epoch_mark"`
}

type revokeDeviceRequest struct {
	DeviceEpoch json.RawMessage `json:"device_epoch"`
	Reason      *string         `json:"reason"`
	Manager     *string         `json:"manager"`
	PIN         *string         `json:"pin"`
}

func trimmed(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func (r *enrolDeviceRequest) parse() (*EnrolDeviceIn, error) {
	v := &httpx.Validator{}
	out := &EnrolDeviceIn{Manager: trimmed(r.Manager)}
	if n, ok := v.Int("device_epoch", r.DeviceEpoch, true); ok {
		v.Check(n >= 1, "device_epoch", "must be 1 or more")
		out.DeviceEpoch = n
	}
	out.PublicKey = strings.ToLower(trimmed(r.PublicKey))
	if raw, err := hex.DecodeString(out.PublicKey); err != nil || len(raw) != ed25519.PublicKeySize {
		v.Check(false, "public_key", "must be the device's Ed25519 public key, 64 hex characters")
	}
	v.Required("manager", out.Manager)
	if r.PIN != nil {
		out.PIN = *r.PIN
	}
	v.Required("pin", out.PIN)
	if r.Prior != nil {
		if n, ok := v.Int("previous_epoch_mark.device_epoch", r.Prior.DeviceEpoch, true); ok {
			v.Check(n >= 1, "previous_epoch_mark.device_epoch", "must be 1 or more")
			out.PriorEpoch = &n
		}
		if n, ok := v.Int("previous_epoch_mark.last_seq", r.Prior.LastSeq, true); ok {
			v.Check(n >= 0, "previous_epoch_mark.last_seq", "must be zero or more")
			out.PriorLastSeq = &n
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *revokeDeviceRequest) parse() (*RevokeDeviceIn, error) {
	v := &httpx.Validator{}
	out := &RevokeDeviceIn{Manager: trimmed(r.Manager), Reason: trimmed(r.Reason)}
	if n, ok := v.Int("device_epoch", r.DeviceEpoch, true); ok {
		v.Check(n >= 1, "device_epoch", "must be 1 or more")
		out.DeviceEpoch = n
	}
	v.Required("reason", out.Reason)
	v.Check(len(out.Reason) <= 500, "reason", "must be at most 500 characters")
	v.Required("manager", out.Manager)
	if r.PIN != nil {
		out.PIN = *r.PIN
	}
	v.Required("pin", out.PIN)
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// keyFingerprint names a public key in an audit row without repeating it.
func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(key)))
	return hex.EncodeToString(sum[:8])
}

// registerOfCaller resolves the register's branch and holds it to the
// caller's wall: a register of another branch is refused, never read.
func (s *Service) registerOfCaller(ctx context.Context, register string) (uuid.UUID, error) {
	branch, err := s.repo.GetRegisterBranch(ctx, register)
	if err != nil {
		return uuid.Nil, err
	}
	if branch == nil {
		return uuid.Nil, conflict("register_has_no_branch", "the register belongs to no branch: give it a branch before enrolling it")
	}
	if err := s.grants.CheckPayloadBranch(ctx, *branch); err != nil {
		return uuid.Nil, branchRefusal(err, "register_id")
	}
	return *branch, nil
}

// verifyEnrolmentManager is the counter PIN proof of an enrolment or a
// revocation: the step up's own check (the roster row locked, the failure
// count, the lock after five), for an owner or admin. A refusal comes back
// as refused with the transaction left to commit the failure count.
func (s *Service) verifyEnrolmentManager(ctx context.Context, manager, pin string) (m *StepUpManager, refused error, err error) {
	m, ok, err := s.repo.FindStepUpManagerForUpdate(ctx, manager)
	if err != nil {
		return nil, nil, err
	}
	if !ok || !m.Active || !m.HasPIN {
		return nil, stepUpRefused(), nil
	}
	if m.Locked {
		return nil, stepUpLocked(), nil
	}
	if !verifyStepUpPIN(m.Hash, pin) {
		if err := s.repo.RecordStepUpFailure(ctx, m.ID, stepUpMaxFailures); err != nil {
			return nil, nil, err
		}
		return nil, stepUpRefused(), nil
	}
	if !roleAllowed(overrideRoles, m.Role) {
		return nil, &httpx.Error{Status: http.StatusForbidden, Code: "step_up_role",
			Message: "Only an owner or an admin can enrol or revoke a register's device"}, nil
	}
	unlocked, err := s.repo.ResetStepUpFailuresUnlocked(ctx, m.ID)
	if err != nil {
		return nil, nil, err
	}
	if !unlocked {
		return nil, stepUpLocked(), nil
	}
	return m, nil, nil
}

// RegisterEnrolment reads the register's enrolment.
func (s *Service) RegisterEnrolment(ctx context.Context, register string) (*RegisterEnrolment, error) {
	branch, err := s.registerOfCaller(ctx, register)
	if err != nil {
		return nil, err
	}
	active, previous, maxEpoch, err := s.repo.RegisterDevices(ctx, register)
	if err != nil {
		return nil, err
	}
	return &RegisterEnrolment{RegisterID: register, BranchID: branch, NextEpoch: maxEpoch + 1,
		Device: active, Previous: previous}, nil
}

// EnrolDevice enrols the shell's key for the register's next epoch, revoking
// the previous key, in one transaction: the manager's PIN, the register's
// row (FOR NO KEY UPDATE, so two enrolments serialize), the previous
// enrolment's row (lock 0a, so a sync of it in flight finishes first), the
// unknown tail item, the new row, the audit rows and the events last.
func (s *Service) EnrolDevice(ctx context.Context, register string, in *EnrolDeviceIn, actor string) (*RegisterEnrolment, error) {
	branch, err := s.registerOfCaller(ctx, register)
	if err != nil {
		return nil, err
	}
	var out *RegisterEnrolment
	var refused error
	err = s.inTx(ctx, func(ctx context.Context) error {
		m, why, err := s.verifyEnrolmentManager(ctx, in.Manager, in.PIN)
		if err != nil {
			return err
		}
		if why != nil {
			refused = why
			return nil
		}
		if ok, err := s.repo.LockRegisterForEnrolment(ctx, register); err != nil {
			return err
		} else if !ok {
			return httpx.NotFound("no such register")
		}
		active, _, maxEpoch, err := s.repo.RegisterDevices(ctx, register)
		if err != nil {
			return err
		}
		if in.DeviceEpoch != maxEpoch+1 {
			return conflict("epoch_moved",
				fmt.Sprintf("the register's next epoch is %d: make the key for that epoch and enrol again", maxEpoch+1))
		}
		if used, err := s.repo.PublicKeyEnrolled(ctx, in.PublicKey); err != nil {
			return err
		} else if used {
			return conflict("public_key_reused", "that key was enrolled before: the shell makes a new key for every enrolment")
		}
		by := m.ID.String()
		var replaced *RegisterDevice
		var tail *uuid.UUID
		if active != nil {
			// lock 0a on the epoch being replaced: a sync of it in flight
			// finishes before the revocation, every later one reads it
			if _, err := s.repo.LockRegisterDevice(ctx, register, active.DeviceEpoch); err != nil {
				return err
			}
			if in.PriorEpoch != nil && *in.PriorEpoch != active.DeviceEpoch {
				return invalid("previous_epoch_mark.device_epoch",
					fmt.Sprintf("must be the register's enrolled epoch, %d", active.DeviceEpoch))
			}
			if in.PriorLastSeq != nil {
				if tail, err = s.unknownTail(ctx, register, branch, active.DeviceEpoch, *in.PriorLastSeq); err != nil {
					return err
				}
			}
			if replaced, err = s.repo.RevokeRegisterDevice(ctx, register, active.DeviceEpoch, by, "re-enrolled"); err != nil {
				return err
			}
		} else if in.PriorEpoch != nil {
			return invalid("previous_epoch_mark", "the register holds no enrolment to replace")
		}
		dev, err := s.repo.InsertRegisterDevice(ctx, register, branch, in.DeviceEpoch, in.PublicKey, by)
		if err != nil {
			return err
		}
		if replaced != nil {
			if err := s.deviceTrail(ctx, "pos.register_device.revoked", "pos_register_device.revoked", branch, replaced, m, actor,
				map[string]any{"reason": "re-enrolled", "replaced_by_epoch": dev.DeviceEpoch}); err != nil {
				return err
			}
		}
		if err := s.deviceTrail(ctx, "pos.register_device.enrolled", "pos_register_device.enrolled", branch, dev, m, actor,
			map[string]any{"replaced_epoch": epochOf(replaced), "unknown_tail_exception_id": tail}); err != nil {
			return err
		}
		out = &RegisterEnrolment{RegisterID: register, BranchID: branch, NextEpoch: dev.DeviceEpoch + 1,
			Device: dev, Previous: replaced, UnknownTailExceptionID: tail}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if refused != nil {
		return nil, refused
	}
	return out, nil
}

func epochOf(d *RegisterDevice) *int64 {
	if d == nil {
		return nil
	}
	e := d.DeviceEpoch
	return &e
}

// unknownTail records the unknown tail item of a re-enrolment over a lost or
// rolled back store (ADR 0011 section 5.1): the numbers the shell's keychain
// says it assigned under the old epoch that the server never recorded. It
// names the register's open session, so it holds the till close until a
// manager accepts it (slice O4).
func (s *Service) unknownTail(ctx context.Context, register string, branch uuid.UUID, epoch, lastSeq int64) (*uuid.UUID, error) {
	mark, err := s.repo.RecordedMark(ctx, register, epoch)
	if err != nil {
		return nil, err
	}
	if lastSeq <= mark {
		return nil, nil
	}
	session, err := s.repo.RegisterOpenSession(ctx, register)
	if err != nil {
		return nil, err
	}
	var sessionID *uuid.UUID
	var day time.Time
	if session != nil {
		sessionID, day = &session.ID, session.BusinessDate
	} else if day, err = s.repo.BranchLocalDate(ctx, branch, s.now()); err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("the register was enrolled again over a lost or rolled back store: sales %d to %d of epoch %d never reached the server",
		mark+1, lastSeq, epoch)
	id, err := s.repo.InsertUnknownTail(ctx, register, branch, epoch, mark+1, lastSeq, sessionID, day, msg)
	if err != nil {
		return nil, err
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.offline_sale.refused", EntityType: "pos_offline_exception",
			EntityID: id, Changes: map[string]any{"register_id": register, "device_epoch": epoch, "reason_code": "unknown_tail",
				"gap_from_seq": mark + 1, "gap_to_seq": lastSeq, "status": "open", "till_session_id": sessionID}}); err != nil {
			return nil, fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	if err := s.recordEvent(ctx, outbox.Event{Type: "pos_offline.exception_opened", EntityType: "pos_offline_exception",
		EntityID: id, BranchID: &branch, Data: eventJSON(map[string]any{"register_id": register, "reason_code": "unknown_tail",
			"status": "open", "device_epoch": epoch, "gap_from_seq": mark + 1, "gap_to_seq": lastSeq})}); err != nil {
		return nil, err
	}
	return &id, nil
}

// deviceTrail writes an enrolment's or a revocation's audit row and event
// (ADR 0011 section 6.4), naming the manager whose PIN proved the act and
// the signed in caller who asked.
func (s *Service) deviceTrail(ctx context.Context, action, event string, branch uuid.UUID, d *RegisterDevice, m *StepUpManager,
	actor string, extra map[string]any) error {
	changes := map[string]any{"register_id": d.RegisterID, "device_epoch": d.DeviceEpoch,
		"key_fingerprint": keyFingerprint(d.PublicKey), "manager_staff_id": m.ID, "manager_name": m.FullName,
		"caller": actor, "high_seq": d.HighSeq}
	for k, v := range extra {
		changes[k] = v
	}
	entityID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("pos_register_device:"+d.RegisterID))
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{Action: action, EntityType: "pos_register", EntityID: entityID,
			UserID: m.ID.String(), Changes: changes}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return s.recordEvent(ctx, outbox.Event{Type: event, EntityType: "pos_register", EntityID: entityID, BranchID: &branch,
		Data: eventJSON(map[string]any{"register_id": d.RegisterID, "device_epoch": d.DeviceEpoch})})
}

// RevokeDevice revokes the register's enrolment of an epoch: the manager's
// PIN, the register's row, lock 0a on the device row (a sync of it in
// flight finishes first, every later one refuses its sales as
// device_revoked), the audit row and event. After the commit the web
// sessions signed in on the register's shell are ended where the sign in
// method can end them.
func (s *Service) RevokeDevice(ctx context.Context, register string, in *RevokeDeviceIn, actor string) (*RegisterEnrolment, error) {
	branch, err := s.registerOfCaller(ctx, register)
	if err != nil {
		return nil, err
	}
	var out *RegisterEnrolment
	var refused error
	var manager string
	err = s.inTx(ctx, func(ctx context.Context) error {
		m, why, err := s.verifyEnrolmentManager(ctx, in.Manager, in.PIN)
		if err != nil {
			return err
		}
		if why != nil {
			refused = why
			return nil
		}
		manager = m.ID.String()
		if ok, err := s.repo.LockRegisterForEnrolment(ctx, register); err != nil {
			return err
		} else if !ok {
			return httpx.NotFound("no such register")
		}
		dev, err := s.repo.LockRegisterDevice(ctx, register, in.DeviceEpoch)
		if err != nil {
			return err
		}
		if dev == nil || dev.RevokedAt != nil {
			return conflict("device_not_enrolled", fmt.Sprintf("the register holds no live enrolment of epoch %d", in.DeviceEpoch))
		}
		revoked, err := s.repo.RevokeRegisterDevice(ctx, register, in.DeviceEpoch, m.ID.String(), in.Reason)
		if err != nil {
			return err
		}
		if err := s.deviceTrail(ctx, "pos.register_device.revoked", "pos_register_device.revoked", branch, revoked, m, actor,
			map[string]any{"reason": in.Reason}); err != nil {
			return err
		}
		_, _, maxEpoch, err := s.repo.RegisterDevices(ctx, register)
		if err != nil {
			return err
		}
		out = &RegisterEnrolment{RegisterID: register, BranchID: branch, NextEpoch: maxEpoch + 1, Previous: revoked}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if refused != nil {
		return nil, refused
	}
	out.Sessions = s.endRegisterSessions(ctx, register, in.DeviceEpoch, manager)
	return out, nil
}

// endRegisterSessions calls the session end seam after the revocation
// committed: the revocation stands whatever the seam answers.
func (s *Service) endRegisterSessions(ctx context.Context, register string, epoch int64, actor string) *RegisterSessionsEnded {
	if s.sessionEnder == nil {
		return &RegisterSessionsEnded{Supported: false,
			Message: "This sign in method cannot end the register's web sessions from here: disable the user if the device is lost."}
	}
	n, err := s.sessionEnder.EndRegisterSessions(ctx, register, epoch, actor)
	if err != nil {
		s.logger.Error("ending the register's sessions failed after its device was revoked",
			"register_id", register, "device_epoch", epoch, "error", err)
		return &RegisterSessionsEnded{Supported: true, Ended: n,
			Message: "The device is revoked, but its web sessions could not all be ended: disable the user if the device is lost."}
	}
	return &RegisterSessionsEnded{Supported: true, Ended: n, Message: fmt.Sprintf("Ended %d web sessions on this register.", n)}
}

// GetRegisterDevice is GET /pos/offline/registers/{id}/device.
func (h *Handler) GetRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.RegisterEnrolment(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// EnrolRegisterDevice is POST /pos/offline/registers/{id}/device: an owner
// or admin enrols the shell's key with the counter PIN.
func (h *Handler) EnrolRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a register's device is enrolled by a person, never a machine key") {
		return
	}
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req enrolDeviceRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.EnrolDevice(r.Context(), r.PathValue("id"), in, h.actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// RevokeRegisterDevice is POST /pos/offline/registers/{id}/device/revoke.
func (h *Handler) RevokeRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "a register's device is revoked by a person, never a machine key") {
		return
	}
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req revokeDeviceRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.RevokeDevice(r.Context(), r.PathValue("id"), in, h.actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
