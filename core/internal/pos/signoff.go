// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventTillPendingSignoff is the event of a count held for a manager.
const EventTillPendingSignoff = "till.pending_signoff"

// recordHeldClose writes the audit row and the event of a count whose
// variance passed the threshold, inside the close's transaction. The session
// row already says PENDING_SIGNOFF; nothing is posted until the sign off.
func (s *Service) recordHeldClose(ctx context.Context, report *TillReport, overShort, threshold int64, actor string) error {
	session := report.Session
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "pos.till.close_held", EntityType: "till_session", EntityID: session.ID, UserID: actor,
			Changes: map[string]any{
				"register_id": session.RegisterID, "over_short_cents": overShort,
				"variance_threshold_cents": threshold, "sale_count": report.SaleCount,
			}}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	if session.BranchID != nil {
		branch := *session.BranchID
		return s.recordEvent(ctx, outbox.Event{Type: EventTillPendingSignoff, EntityType: "till_session",
			EntityID: session.ID, BranchID: &branch, Data: eventJSON(map[string]any{
				"register_id": session.RegisterID, "status": TillPendingSignoff.Status(),
				"from_status": TillOpen.Status(), "over_short_cents": overShort,
				"variance_threshold_cents": threshold,
			})})
	}
	return nil
}

// SignOffTill releases a counted drawer that waits for a manager: the over or
// short posts in this transaction, the session ends CLOSED with who signed
// off and why, and the Z snapshot freezes. The signer is never the person who
// closed the drawer nor its cashier. A call with no identity at all (a dev
// mode caller) carries nothing to compare, so the rule cannot hold it. A
// step up approval token, when sent, is spent in this transaction and its
// manager is the signer the session records.
func (s *Service) SignOffTill(ctx context.Context, sessionID uuid.UUID, reason, actor, approvalToken string) (*TillReport, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalid("reason", "is required: why the variance is accepted")
	}
	if err := s.holdTillBranch(ctx, sessionID); err != nil {
		return nil, err
	}
	var out *TillReport
	err := s.inTx(ctx, func(ctx context.Context) error {
		if approvalToken != "" {
			// the manager's PIN already spoke: one use, this act, this session
			manager, err := s.consumeStepUpApproval(ctx, StepUpActSignoff, sessionID, nil, approvalToken)
			if err != nil {
				return err
			}
			actor = manager
		}
		if err := s.repo.LockTillSession(ctx, sessionID, true); err != nil {
			return err
		}
		session, err := s.repo.GetTillSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if session.Status != TillPendingSignoff {
			return httpx.InvalidStateTransition("the till session is not waiting for sign off")
		}
		// the variance the manager releases, method by method (CP-08): the
		// books keep the cash shortage beside the check overage, never the
		// clean net an overage would give
		session.OverShortByMethod = overShortByMethod(session.ExpectedByMethod, session.CountedByMethod)
		if actor != "" {
			closer := ""
			if session.ClosedBy != nil {
				closer = *session.ClosedBy
			}
			if strings.EqualFold(actor, closer) || strings.EqualFold(actor, session.CashierID.String()) {
				return conflict("signoff_same_person",
					"the person who closed the drawer, or its cashier, cannot sign off its variance: a different manager must")
			}
		}
		var overShort int64
		if session.OverShort != nil {
			overShort = int64(*session.OverShort)
		}
		// The manager's release posts each method's variance as its own pair
		// of lines, so an offsetting count (a cash shortage beside a check
		// overage that nets to zero) still posts both sides and the ledger
		// equals the money the drawer holds (CP-08).
		if s.ledger != nil && hasDrawerVariance(session.OverShortByMethod) {
			if session.BranchID == nil {
				return fmt.Errorf("the till session names no branch: its over/short cannot post")
			}
			glID, err := s.postTillOverShortByMethod(ctx, *session.BranchID, sessionID, session.OverShortByMethod, actor)
			if err != nil {
				return ledgerRefusal(err, "failed to post the drawer variance")
			}
			if glID != uuid.Nil {
				session.GLEntryID = &glID
			}
		}
		session.SignOff = &TillSignOff{SignedOffBy: actor, Reason: reason}
		if err := s.repo.SignOffTillSession(ctx, session); err != nil {
			return err
		}
		session.Status = TillClosed
		// The report as the count froze it: the expected figures the count
		// compared with, not a fresh sum, so the snapshot cannot move after.
		report, err := s.TillReport(ctx, sessionID)
		if err != nil {
			return err
		}
		report.ExpectedByMethod = session.ExpectedByMethod
		report.Session = *session
		if err := s.generateZReport(ctx, report); err != nil {
			return err
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.till.signed_off", EntityType: "till_session", EntityID: sessionID, UserID: actor,
				Changes: map[string]any{"register_id": session.RegisterID, "over_short_cents": overShort,
					"reason": reason, "closed_by": derefString(session.ClosedBy)}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if session.BranchID != nil {
			branch := *session.BranchID
			if err := s.recordEvent(ctx, outbox.Event{Type: EventTillClosed, EntityType: "till_session",
				EntityID: sessionID, BranchID: &branch, Data: eventJSON(map[string]any{
					"register_id": session.RegisterID, "status": TillClosed.Status(),
					"from_status": TillPendingSignoff.Status(), "over_short_cents": overShort,
					"sale_count": report.SaleCount, "signed_off": true,
				})}); err != nil {
				return err
			}
		}
		out = report
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// PendingSignoffs pages the counted drawers waiting for a manager inside the
// caller's wall, oldest close first; branch, when set, narrows to one branch
// the caller may target.
func (s *Service) PendingSignoffs(ctx context.Context, branch *uuid.UUID, after *time.Time, afterID *uuid.UUID, limit int) ([]TillSession, bool, error) {
	if branch != nil {
		if err := s.grants.CheckPayloadBranch(ctx, *branch); err != nil {
			return nil, false, branchRefusal(err, "branch_id")
		}
	}
	return s.repo.ListPendingSignoffs(ctx, branch, after, afterID, limit)
}

// GetVariancePolicy reads a branch's threshold with its sources.
func (s *Service) GetVariancePolicy(ctx context.Context, branch uuid.UUID) (*VariancePolicy, error) {
	if err := s.grants.CheckPayloadBranch(ctx, branch); err != nil {
		return nil, branchRefusal(err, "branch_id")
	}
	if _, ok, err := s.repo.BranchZone(ctx, branch); err != nil {
		return nil, err
	} else if !ok {
		return nil, httpx.NotFound("branch not found")
	}
	return s.repo.GetVariancePolicy(ctx, branch)
}

// SetVariancePolicy sets (or, with a nil threshold, clears) a branch's own
// threshold, with its audit row in the same transaction.
func (s *Service) SetVariancePolicy(ctx context.Context, branch uuid.UUID, cents *int64, actor string) (*VariancePolicy, error) {
	if err := s.grants.CheckPayloadBranch(ctx, branch); err != nil {
		return nil, branchRefusal(err, "branch_id")
	}
	var out *VariancePolicy
	err := s.inTx(ctx, func(ctx context.Context) error {
		if _, ok, err := s.repo.BranchZone(ctx, branch); err != nil {
			return err
		} else if !ok {
			return httpx.NotFound("branch not found")
		}
		if err := s.repo.SetVariancePolicy(ctx, branch, cents, actor); err != nil {
			return err
		}
		if s.auditLog != nil {
			changes := map[string]any{"branch_id": branch}
			if cents != nil {
				changes["variance_threshold_cents"] = *cents
			}
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "pos.till.variance_policy", EntityType: "location",
				EntityID: branch, UserID: actor, Changes: changes}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		p, err := s.repo.GetVariancePolicy(ctx, branch)
		out = p
		return err
	})
	return out, err
}

// branchRefusal maps the branch guard's refusal of a branch the request names
// to the 403 naming the field (ADR 0007 section 2.3); any other error passes.
func branchRefusal(err error, field string) error {
	if errors.Is(err, middleware.ErrPayloadBranchRefused) {
		return &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
			Message: "the branch is outside the branches this caller may target",
			Details: []httpx.FieldError{{Field: field, Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}}
	}
	return err
}
