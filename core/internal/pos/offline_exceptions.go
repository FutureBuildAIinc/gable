// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// The offline exception queue and its resolutions (ADR 0011 3.4, slice O4).
// A manager (an owner or admin other than the sale's cashier, never a
// machine key) resolves each exception in one transaction under the
// register's device row (lock 0a) and then the exception row (lock 0b); a
// resolution that posts runs the completion's own body, as the sync does.

// The resolutions a manager may take.
const (
	ResolveRetry        = "retry"
	ResolveAccept       = "accept"
	ResolveLatePost     = "late_post"
	ResolveWriteOff     = "write_off"
	ResolvePostSeparate = "post_separate"
	ResolveRestate      = "restate"
	ResolveReview       = "review"
)

// reasonUnknownTail is the item a re-enrolment over a missing or rolled back
// store records (ADR 0011 section 2; migration 146 names it exactly).
const reasonUnknownTail = "unknown_tail"

// offlineResolverRoles may resolve an exception: the rule override.go
// applies to a manager's override.
var offlineResolverRoles = overrideRoles

// OfflineException is one row of the manager's queue as the list and the
// resolution answer it. Actions are what a manager may do with it now.
type OfflineException struct {
	ID               uuid.UUID        `json:"id"`
	SaleID           uuid.UUID        `json:"sale_id"`
	RegisterID       string           `json:"register_id"`
	BranchID         uuid.UUID        `json:"branch_id"`
	DeviceEpoch      int64            `json:"device_epoch"`
	DeviceSeq        int64            `json:"device_seq"`
	GapFromSeq       *int64           `json:"gap_from_seq"`
	GapToSeq         *int64           `json:"gap_to_seq"`
	OfflineRef       string           `json:"offline_ref"`
	TillSessionID    *uuid.UUID       `json:"till_session_id"`
	BusinessDate     string           `json:"business_date"`
	CustomerID       *uuid.UUID       `json:"customer_id"`
	CashierID        *uuid.UUID       `json:"cashier_id"`
	WarningAlertIDs  []uuid.UUID      `json:"warning_alert_ids"`
	ReasonCode       string           `json:"reason_code"`
	ReasonMessage    string           `json:"reason_message"`
	CashCents        httpx.Cents      `json:"cash_cents"`
	CheckCents       httpx.Cents      `json:"check_cents"`
	AccountCents     httpx.Cents      `json:"account_cents"`
	TotalCents       httpx.Cents      `json:"total_cents"`
	Status           string           `json:"status"`
	Actions          []string         `json:"actions"`
	ResolutionAction *string          `json:"resolution_action"`
	ResolutionReason *string          `json:"resolution_reason"`
	ResolvedBy       *string          `json:"resolved_by"`
	ResolvedAt       *httpx.Timestamp `json:"resolved_at"`
	PostedSaleID     *uuid.UUID       `json:"posted_sale_id"`
	SignedSale       json.RawMessage  `json:"signed_sale"`
	Revision         int64            `json:"revision"`
	CreatedAt        httpx.Timestamp  `json:"created_at"`

	signatureDigest string
}

// finish derives the cashier and the actions from the stored row.
func (e *OfflineException) finish() {
	if len(e.SignedSale) == 0 || !json.Valid(e.SignedSale) {
		e.SignedSale = json.RawMessage(`{}`)
	}
	var sale struct {
		CashierID string `json:"cashier_id"`
	}
	if json.Unmarshal(e.SignedSale, &sale) == nil {
		if id, err := uuid.Parse(sale.CashierID); err == nil {
			e.CashierID = &id
		}
	}
	e.Actions = offlineActions(e.ReasonCode, e.Status)
}

type offlineExceptionHead struct {
	BranchID    uuid.UUID
	RegisterID  string
	DeviceEpoch int64
}

// offlineActions are the resolutions ADR 0011 3.4 gives each reason, for a
// row still open (or held). A row resolved, relinked, superseded, found or
// reviewed takes none.
func offlineActions(reason, status string) []string {
	if status == "held" {
		return []string{ResolveAccept, ResolveWriteOff}
	}
	if status != "open" {
		return []string{}
	}
	switch reason {
	case reasonSoldPastWarn:
		return []string{ResolveReview}
	case reasonUnknownTail, reasonSequenceGap:
		return []string{ResolveAccept}
	case reasonSequenceReused:
		return []string{ResolveAccept, ResolveWriteOff}
	case reasonBadSignature, reasonReplayMismatch, "device_revoked", reasonBrokenChain:
		return []string{ResolveWriteOff}
	case "price_unverified", "tax_unverified", "tax_flag_unverified", "cap_exceeded", "snapshot_stale":
		return []string{ResolveAccept, ResolveRetry, ResolveWriteOff}
	case "stock_unit_changed":
		return []string{ResolveRestate, ResolveWriteOff}
	case "session_closed", "day_closed", "period_closed", reasonLateAfterGap:
		return []string{ResolveLatePost, ResolveRetry, ResolveWriteOff}
	case "replaced_sale_settled":
		return []string{ResolveRetry, ResolvePostSeparate, ResolveWriteOff}
	case "replaced_sale_mismatch":
		return []string{ResolvePostSeparate, ResolveWriteOff}
	default:
		return []string{ResolveRetry, ResolveWriteOff}
	}
}

// OfflineResolveIn is a parsed resolution.
type OfflineResolveIn struct {
	Action   string
	Reason   string
	Revision *int64
}

type offlineResolveRequest struct {
	Action   *string         `json:"action"`
	Reason   *string         `json:"reason"`
	Revision json.RawMessage `json:"revision"`
}

func (r *offlineResolveRequest) parse() (*OfflineResolveIn, error) {
	v := &httpx.Validator{}
	out := &OfflineResolveIn{}
	if r.Action != nil {
		out.Action = strings.TrimSpace(*r.Action)
	}
	v.Required("action", out.Action)
	if out.Action != "" {
		v.Enum("action", out.Action, ResolveRetry, ResolveAccept, ResolveLatePost, ResolveWriteOff, ResolvePostSeparate,
			ResolveRestate, ResolveReview)
	}
	if r.Reason != nil {
		out.Reason = strings.TrimSpace(*r.Reason)
	}
	v.Check(out.Reason != "", "reason", "is required: the manager says why")
	if n, ok := v.Int("revision", r.Revision, false); ok {
		out.Revision = &n
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// offlineLatePostKey marks a late post's verification: the day the sale was
// made in may be closed (that is why it posts late).
type offlineLatePostKey struct{}

func offlineLatePostFrom(ctx context.Context) bool {
	v, _ := ctx.Value(offlineLatePostKey{}).(bool)
	return v
}

// ListOfflineExceptions is the manager's queue of a branch (ADR 0011 3.4):
// open and held rows by default, oldest first.
func (s *Service) ListOfflineExceptions(ctx context.Context, branch uuid.UUID, statuses []string,
	after *time.Time, afterID *uuid.UUID, limit int) ([]OfflineException, bool, error) {
	if err := s.grants.CheckPayloadBranch(ctx, branch); err != nil {
		return nil, false, branchRefusal(err, "branch_id")
	}
	return s.repo.ListOfflineExceptions(ctx, branch, statuses, after, afterID, limit)
}

// GetOfflineException reads one exception inside the caller's wall.
func (s *Service) GetOfflineException(ctx context.Context, id uuid.UUID) (*OfflineException, error) {
	if _, err := s.holdOfflineException(ctx, id); err != nil {
		return nil, err
	}
	e, err := s.repo.GetOfflineException(ctx, id)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, httpx.NotFound("offline exception not found")
	}
	return e, nil
}

// holdOfflineException refuses by name an exception of a branch outside the
// caller's grants.
func (s *Service) holdOfflineException(ctx context.Context, id uuid.UUID) (*offlineExceptionHead, error) {
	h, err := s.repo.OfflineExceptionHead(ctx, id)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, httpx.NotFound("offline exception not found")
	}
	b := h.BranchID
	return h, s.holdRecordBranch(ctx, "offline exception", &b, id)
}

// ResolveOfflineException runs one manager's resolution (ADR 0011 3.4): lock
// 0a, then 0b, the act, and the row resolved with the sale it posted, the
// audit row and the event, in one transaction. A posting the live checks
// refuse again rolls back whole and the exception stays open.
func (s *Service) ResolveOfflineException(ctx context.Context, id uuid.UUID, in *OfflineResolveIn, ifMatch, actor string) (*OfflineException, error) {
	if s.ar == nil || s.invoices == nil || s.inventory == nil {
		return nil, errNotConfigured
	}
	if actor == "" {
		return nil, httpx.Forbidden("an offline exception is resolved by a signed in manager")
	}
	head, err := s.holdOfflineException(ctx, id)
	if err != nil {
		return nil, err
	}
	reoffer := false
	err = s.inTx(ctx, func(ctx context.Context) error {
		// 0a, the register's device row of the item's epoch (nil when no
		// enrolment holds it: a forged item), then 0b, the exception row
		dev, err := s.repo.LockRegisterDevice(ctx, head.RegisterID, head.DeviceEpoch)
		if err != nil {
			return err
		}
		e, err := s.repo.LockOfflineException(ctx, id)
		if err != nil {
			return err
		}
		if e == nil {
			return httpx.NotFound("offline exception not found")
		}
		if err := httpx.CheckRevision(e.Revision, ifMatch, in.Revision); err != nil {
			return err
		}
		if !slices.Contains(e.Actions, in.Action) {
			if len(e.Actions) == 0 {
				return httpx.InvalidStateTransition(fmt.Sprintf("the exception is %s: nothing is left to resolve", e.Status))
			}
			return conflict("resolution_not_allowed", fmt.Sprintf("a %s exception is resolved by %s, not %s",
				e.ReasonCode, strings.Join(e.Actions, ", "), in.Action))
		}
		// separation of duties: never the sale's own cashier, compared as
		// people (the roster row the actor names), not as strings
		if e.CashierID != nil && s.sameStaffAs(ctx, actor, *e.CashierID) {
			return httpx.Forbidden("a manager other than the sale's own cashier resolves its offline exception")
		}
		var posted *Sale
		switch {
		case e.Status == "held":
			if dev == nil {
				return conflict("resolution_not_allowed", "no enrolment holds the held item's epoch")
			}
			if err := s.resolveHeld(ctx, dev, e, in, actor); err != nil {
				return err
			}
			reoffer = in.Action == ResolveAccept
		case in.Action == ResolveWriteOff, in.Action == ResolveReview, e.ReasonCode == reasonUnknownTail,
			e.ReasonCode == reasonSequenceGap:
			status := "resolved"
			if in.Action == ResolveReview {
				status = "reviewed"
			}
			if err := s.repo.ResolveOfflineExceptionRow(ctx, e.ID, status, in.Action, in.Reason, actor, nil); err != nil {
				return err
			}
		default:
			if dev == nil {
				return conflict("resolution_not_allowed", "no enrolment holds the sale's epoch: write it off")
			}
			// the row leaves the open queue first, so the account room
			// the sale is judged against does not count it twice
			if err := s.repo.ResolveOfflineExceptionRow(ctx, e.ID, "resolved", in.Action, in.Reason, actor, nil); err != nil {
				return err
			}
			posted, err = s.postResolution(ctx, dev, e, in, actor)
			if err != nil {
				return err
			}
			if err := s.repo.SetOfflineExceptionPosted(ctx, e.ID, posted.ID); err != nil {
				return err
			}
		}
		return s.offlineResolvedTrail(ctx, e, in, posted, actor)
	})
	if err != nil {
		return nil, err
	}
	if reoffer {
		// the accepted gap restarts the chain: the held item at the mark
		// plus one is re-offered now, in its own transaction (ADR 0011 3.4)
		if _, err := s.reofferHeld(ctx, "resolve:"+id.String(), head.RegisterID, head.DeviceEpoch, actor); err != nil {
			return nil, err
		}
	}
	return s.repo.GetOfflineException(ctx, id)
}

// resolveHeld accepts or writes off a held broken_chain item whose link has
// not arrived (ADR 0011 3.4). Both advance the mark over the missing numbers,
// leaving a sequence_gap item naming them, and restart the chain. The accept
// leaves the held item at the mark plus one for the re-offer (posted, or
// opened for its own reason); the write off also advances over the item
// itself, whose verified digest becomes the head, and posts nothing.
func (s *Service) resolveHeld(ctx context.Context, dev *registerDevice, e *OfflineException, in *OfflineResolveIn, actor string) error {
	if e.DeviceSeq <= dev.HighSeq {
		return conflict("resolution_not_allowed", "the held item is at or below the register's mark")
	}
	below, err := s.repo.OfflineHeldBelow(ctx, e.RegisterID, e.DeviceEpoch, e.DeviceSeq)
	if err != nil {
		return err
	}
	if below {
		return conflict("offline_gap_order", "a held item of this register waits at a lower sequence: resolve that one first")
	}
	from, to := dev.HighSeq+1, e.DeviceSeq-1
	if from <= to {
		if _, err := s.repo.InsertOfflineGap(ctx, e, from, to, in.Action, in.Reason, actor); err != nil {
			return err
		}
	}
	if in.Action == ResolveAccept {
		return s.repo.AdvanceDeviceMark(ctx, dev.ID, to, offlineChainRestart)
	}
	if err := s.repo.AdvanceDeviceMark(ctx, dev.ID, e.DeviceSeq, e.signatureDigest); err != nil {
		return err
	}
	return s.repo.ResolveOfflineExceptionRow(ctx, e.ID, "resolved", in.Action, in.Reason, actor, nil)
}

// postResolution posts the exception's sale as the resolution says: retry
// through the verification again, accept at the recorded figures, late post
// on the earliest open date with no session, post as a separate sale with the
// replaced cart's link cleared, or the stock figures restated.
func (s *Service) postResolution(ctx context.Context, dev *registerDevice, e *OfflineException, in *OfflineResolveIn, actor string) (*Sale, error) {
	it, err := parseOfflineItem(e.SignedSale)
	if err != nil {
		return nil, conflict("resolution_not_allowed", "the stored sale cannot be read: write it off")
	}
	// the digest the sale was recorded under, not one recomputed from the
	// stored copy, so a later replay of the item answers duplicate
	it.Digest = e.signatureDigest
	ins := resolvedInsert{seq: &it.Sale.DeviceSeq}
	var v *offlineVerdict
	switch in.Action {
	case ResolveAccept:
		v, err = s.acceptedVerdict(ctx, e.BranchID, it)
		if e.ReasonCode == reasonSequenceReused {
			// the number stays on the exception row; the sale row holds
			// none, so the posting never collides with the index
			ins.seq = nil
		}
	case ResolveLatePost:
		v, err = s.verifyOfflineSale(context.WithValue(ctx, offlineLatePostKey{}, true), e.BranchID, dev, it)
		if err == nil {
			err = s.latePostDate(ctx, e.BranchID, v)
		}
		session := uuid.MustParse(it.Sale.TillSessionID)
		ins.offlineSession = &session
	case ResolvePostSeparate:
		if it.Sale.ReplacesSaleID == nil {
			return nil, conflict("resolution_not_allowed", "the sale replaces no cart: retry it instead")
		}
		ins.clearedLink = it.Sale.ReplacesSaleID
		it.Sale.ReplacesSaleID = nil
		v, err = s.verifyOfflineSale(ctx, e.BranchID, dev, it)
	default:
		v, err = s.verifyOfflineSale(ctx, e.BranchID, dev, it)
	}
	if err != nil {
		return nil, resolutionRefusal(err)
	}
	if in.Action == ResolveRestate {
		ins.restated, err = s.restateStockFigures(ctx, it)
		if err != nil {
			return nil, err
		}
	}
	v.batchID = "resolve:" + e.ID.String()
	v.latePost = in.Action == ResolveLatePost
	done, err := s.postResolvedSale(ctx, it, v, ins, actor)
	if err != nil {
		return nil, resolutionRefusal(err)
	}
	if v.latePost {
		if err := s.latePostReclass(ctx, e, done.ID, v.businessDate, actor); err != nil {
			return nil, err
		}
	}
	if s.auditLog != nil {
		if ins.clearedLink != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.offline_sale.link_cleared", EntityType: "pos_transaction", EntityID: done.ID, UserID: actor,
				Changes: map[string]any{"replaces_sale_id": *ins.clearedLink, "reason": in.Reason, "exception_id": e.ID,
					"offline_ref": it.Sale.OfflineRef}}); err != nil {
				return nil, fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		for _, r := range ins.restated {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.offline_sale.stock_restated", EntityType: "pos_transaction", EntityID: done.ID, UserID: actor,
				Changes: map[string]any{"line": r.line, "product_id": r.product, "old_stock_uom": r.oldUOM,
					"old_stock_quantity": r.oldQty, "new_stock_uom": r.newUOM, "new_stock_quantity": r.newQty,
					"reason": in.Reason, "exception_id": e.ID}}); err != nil {
				return nil, fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
	}
	return done, nil
}

// acceptedVerdict is an accept's verdict (ADR 0011 3.4): the sale posts at
// its recorded figures, the price, tax or cap the manager checked not judged
// again. The session must still be the register's own and the cashier its
// cashier (section 6.3); every live check of the body still runs.
func (s *Service) acceptedVerdict(ctx context.Context, branch uuid.UUID, it *offlineItem) (*offlineVerdict, error) {
	sale := &it.Sale
	snapID, serr := uuid.Parse(sale.SnapshotID)
	sessionID, perr := uuid.Parse(sale.TillSessionID)
	if serr != nil || perr != nil {
		return nil, refuse("out_of_wall", "the sale names an id the server does not know")
	}
	if _, err := uuid.Parse(sale.CashierID); err != nil {
		return nil, refuse("out_of_wall", "the sale's cashier is not an id the server knows")
	}
	session, err := s.repo.OfflineSessionFacts(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil || session.RegisterID != sale.RegisterID {
		return nil, refuse("out_of_wall", "the session the sale names belongs to another register")
	}
	if !strings.EqualFold(session.CashierID.String(), sale.CashierID) {
		return nil, refuse("cashier_mismatch", "the sale's cashier is not the session's own cashier")
	}
	reg, err := s.repo.GetRegisterBranch(ctx, sale.RegisterID)
	if err != nil {
		return nil, err
	}
	if reg == nil || *reg != branch {
		return nil, refuse("out_of_wall", "the sale's register is not of the exception's branch")
	}
	return &offlineVerdict{snapshotID: snapID, taxCents: sale.TaxCents, taxRate: sale.TaxRate, exempt: sale.TaxExempt,
		businessDate: session.BusinessDate, item: it}, nil
}

// latePostDate takes the branch's day lock after 0b (the lock a day close
// takes, so no close freezes the date under the post) and sets the verdict's
// date to the earliest open business date in an open period, from the sale's
// own day on (ADR 0011 3.4).
func (s *Service) latePostDate(ctx context.Context, branch uuid.UUID, v *offlineVerdict) error {
	if err := s.repo.LockBranchDay(ctx, branch); err != nil {
		return err
	}
	date, ok, err := s.repo.EarliestOpenBusinessDate(ctx, branch, v.businessDate)
	if err != nil {
		return err
	}
	if !ok {
		return conflict("no_open_date", "no business date from the sale's day to today is open in an open period: reopen one, then late post")
	}
	v.businessDate = date
	return nil
}

// latePostReclass moves the cash and check the counted drawer booked as an
// over back to cash (ADR 0011 3.4): debit over/short, credit cash, the short
// pair the close uses, because the late post debits cash again. An account
// tender put nothing in the drawer. The entry is sourced on the posted sale.
func (s *Service) latePostReclass(ctx context.Context, e *OfflineException, sale uuid.UUID, date time.Time, actor string) error {
	amount := int64(e.CashCents) + int64(e.CheckCents)
	if s.ledger == nil || amount <= 0 {
		return nil
	}
	_, err := s.ledger.PostEntry(ctx, gl.PostingInput{
		EntryDate:   date,
		Memo:        fmt.Sprintf("Offline late post %s: drawer over moved to cash", e.OfflineRef),
		Source:      gl.SourceAdjustment,
		SourceRefID: &sale,
		BranchID:    e.BranchID,
		PostedBy:    actorOrSystem(actor),
		Legs: []gl.Leg{
			{AccountCode: gl.AccountCodeOverShort, Description: "Offline late post (drawer over)", Debit: amount},
			{AccountCode: gl.AccountCodeCash, Description: "Offline late post (cash and check)", Credit: amount},
		},
	})
	if err != nil {
		return ledgerRefusal(err, "failed to post the late post's drawer adjustment")
	}
	return nil
}

// resolvedInsert is how a resolution's sale row differs from the sync's.
type resolvedInsert struct {
	seq            *int64
	offlineSession *uuid.UUID
	clearedLink    *string
	restated       []restatedLine
}

// postResolvedSale writes the sale row with the client's id, its provenance
// and its lines as recorded (or restated), settles the replaced cart in id
// order (ADR 0011 3.2 step 1), then runs the completion's body with the
// verdict.
func (s *Service) postResolvedSale(ctx context.Context, it *offlineItem, v *offlineVerdict, ins resolvedInsert, actor string) (*Sale, error) {
	id := uuid.MustParse(it.Sale.ClientID)
	walkInID, defaultCurrency, err := s.repo.WalkInCustomer(ctx)
	if err != nil {
		return nil, err
	}
	session := uuid.MustParse(it.Sale.TillSessionID)
	sale := &Sale{ID: id, RegisterID: it.Sale.RegisterID, CashierID: uuid.MustParse(it.Sale.CashierID),
		Status: StatusOpen, Currency: defaultCurrency, WalkInID: walkInID, TillSessionID: &session}
	if it.Sale.CustomerID != nil {
		c := uuid.MustParse(*it.Sale.CustomerID)
		sale.CustomerID = &c
		_, facts, err := s.namedCustomer(ctx, c)
		if err != nil {
			return nil, err
		}
		sale.Currency = facts.Currency
	}
	var replaced *uuid.UUID
	if it.Sale.ReplacesSaleID != nil {
		r := uuid.MustParse(*it.Sale.ReplacesSaleID)
		replaced = &r
	}
	if replaced != nil && replaced.String() < id.String() {
		if err := s.settleReplacedCart(ctx, it, *replaced); err != nil {
			return nil, err
		}
	}
	if err := s.repo.CreateSale(ctx, sale); err != nil {
		return nil, err
	}
	if err := s.repo.MarkResolvedOfflineSale(ctx, id, it, v.snapshotID, ins.seq, ins.offlineSession); err != nil {
		return nil, err
	}
	if replaced != nil && replaced.String() >= id.String() {
		if err := s.settleReplacedCart(ctx, it, *replaced); err != nil {
			return nil, err
		}
	}
	lines, err := s.offlineLines(ctx, it)
	if err != nil {
		return nil, err
	}
	if err := s.repo.AddLines(ctx, id, lines); err != nil {
		return nil, err
	}
	pre := &Sale{ID: id, CustomerID: sale.CustomerID, WalkInID: sale.WalkInID, BranchID: sale.BranchID}
	realCustomer := sale.WalkInID
	if sale.CustomerID != nil {
		realCustomer = *sale.CustomerID
	}
	tenders := offlineTenders(it.Sale.Tenders)
	rev := sale.Revision
	return s.completeSaleTx(ctx, id, "", &rev, tenders, derefString(it.Sale.PickedUpBy), actor, pre, realCustomer,
		make([]*chargeResult, len(tenders)), nil, nil, nil, nil, false, false, v)
}

// restatedLine is one line whose stock figures a restatement changed.
type restatedLine struct {
	line           int
	product        string
	oldUOM, newUOM string
	oldQty, newQty string
}

// restateStockFigures restates each line whose stocking unit changed after
// the snapshot the way the engine's own rule does (ADR 0006 3.4,
// salesdoc.ResolveStockMove): the recorded stock quantity, never the sale
// quantity, converted from the old unit through the product's current unit
// set into the current stocking unit. Price, extension, tax and tenders are
// untouched. A unit no longer in the set, or a conversion not exact at scale
// 4, is the engine's 409 stock_unit_changed and the item stays open.
func (s *Service) restateStockFigures(ctx context.Context, it *offlineItem) ([]restatedLine, error) {
	ids := make([]uuid.UUID, 0, len(it.Sale.Lines))
	for _, l := range it.Sale.Lines {
		ids = append(ids, uuid.MustParse(l.ProductID))
	}
	refs, err := s.repo.LookupProducts(ctx, ids)
	if err != nil {
		return nil, err
	}
	sets, err := s.repo.LookupProductUnits(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []restatedLine
	for i := range it.Sale.Lines {
		l := &it.Sale.Lines[i]
		ref, ok := refs[l.ProductID]
		if !ok || ref.UOMPrimary == "" || l.StockUOM == ref.UOMPrimary {
			continue
		}
		oldUOM, oldQty := l.StockUOM, l.StockQuantity
		sq := mustQty(oldQty)
		probe := salesdoc.Line{StockUOM: &oldUOM, StockQuantity: &sq}
		var set *salesdoc.ProductUnitSet
		if us, ok := sets[uuid.MustParse(l.ProductID)]; ok {
			set = &us
		}
		uom, qty, err := salesdoc.ResolveStockMove(i, &probe, ref.UOMPrimary, set)
		if err != nil {
			return nil, err
		}
		l.StockUOM, l.StockQuantity = uom, qty.DecimalString()
		out = append(out, restatedLine{line: i, product: l.ProductID, oldUOM: oldUOM, oldQty: oldQty, newUOM: uom,
			newQty: l.StockQuantity})
	}
	if len(out) == 0 {
		return nil, conflict("resolution_not_allowed", "no line's stocking unit changed: retry the sale instead")
	}
	return out, nil
}

// resolutionRefusal answers a posting a resolution could not make: a coded
// refusal as the 409 naming it (the exception stays open), anything else as
// it is.
func resolutionRefusal(err error) error {
	var r *offlineRefusal
	if errors.As(err, &r) {
		return conflict(r.code, r.message)
	}
	return err
}

// offlineResolvedTrail writes the resolution's audit row and its event, last
// (ADR 0011 6.4: naming the manager, the resolution and the reason).
func (s *Service) offlineResolvedTrail(ctx context.Context, e *OfflineException, in *OfflineResolveIn, posted *Sale, actor string) error {
	var postedID *uuid.UUID
	if posted != nil {
		postedID = &posted.ID
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "pos.offline_exception.resolved", EntityType: "pos_offline_exception", EntityID: e.ID, UserID: actor,
			Changes: map[string]any{"resolution": in.Action, "reason": in.Reason, "resolved_by": actor,
				"reason_code": e.ReasonCode, "sale_id": e.SaleID, "posted_sale_id": postedID, "register_id": e.RegisterID,
				"device_epoch": e.DeviceEpoch, "device_seq": e.DeviceSeq, "cash_cents": int64(e.CashCents),
				"check_cents": int64(e.CheckCents), "account_cents": int64(e.AccountCents)}}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	b := e.BranchID
	return s.recordEvent(ctx, outbox.Event{Type: "pos_offline.exception_resolved", EntityType: "pos_offline_exception",
		EntityID: e.ID, BranchID: &b, Data: eventJSON(map[string]any{
			"sale_id": e.SaleID, "register_id": e.RegisterID, "reason_code": e.ReasonCode, "resolution": in.Action,
			"resolved_by": actor, "posted_sale_id": postedID, "offline_ref": e.OfflineRef,
		})})
}

// sameStaffAs reports whether actor names the staff member id: the same id,
// or the roster row the actor's id, staff number or email reads to.
func (s *Service) sameStaffAs(ctx context.Context, actor string, staff uuid.UUID) bool {
	if actor == "" || actor == devApprover {
		return false
	}
	if strings.EqualFold(actor, staff.String()) {
		return true
	}
	m, ok, err := s.repo.FindStepUpManager(ctx, actor)
	if err != nil || !ok {
		return false
	}
	return m.ID == staff
}

// offlineHoldsClose is the close's look at the exception rows (ADR 0011 3.2
// and 3.4): a plain read, after the close holds the session FOR UPDATE, so
// it never waits for a resolution's 0b; an open exception naming the
// session refuses the close, and so does a device whose own reported mark
// is past the mark the server recorded (section 2: a sale it assigned has
// not arrived).
func (s *Service) offlineHoldsClose(ctx context.Context, session uuid.UUID, register string) error {
	refs, err := s.repo.OpenOfflineExceptionsOfSession(ctx, session)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return conflict("offline_exception_open", fmt.Sprintf(
			"an offline sale of this session is with a manager (%s): resolve it on the offline exceptions list before closing the drawer",
			strings.Join(refs, ", ")))
	}
	epoch, reported, high, found, err := s.repo.OfflineUndrained(ctx, register)
	if err != nil {
		return err
	}
	if found {
		return conflict("queue_not_drained", fmt.Sprintf(
			"the register's device assigned sale %d of epoch %d and the server has recorded up to %d: sync the register's queue before closing the drawer",
			reported, epoch, high))
	}
	return nil
}

// offlineHoldsDay refuses a day close while an open exception names a
// session of the day or earlier (ADR 0011 3.4), as a plain read under the
// day lock.
func (s *Service) offlineHoldsDay(ctx context.Context, branch uuid.UUID, date string) error {
	refs, err := s.repo.OpenOfflineExceptionsOfDay(ctx, branch, date)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return conflict("offline_exception_open", fmt.Sprintf(
			"an offline sale of this day or earlier is with a manager (%s): resolve it on the offline exceptions list before closing the day",
			strings.Join(refs, ", ")))
	}
	return nil
}

// The routes.

const offlineExceptionsOrdering = "pos.offline_exceptions.created_at_id_asc"

// offlineExceptionStatuses are the list's status filter values.
var offlineExceptionStatuses = map[string][]string{
	"open":     {"open", "held"},
	"held":     {"held"},
	"resolved": {"resolved", "relinked", "superseded", "found", "reviewed"},
	"all":      {"open", "held", "resolved", "relinked", "superseded", "found", "reviewed"},
}

// ListOfflineExceptions is GET /pos/offline/exceptions?branch_id=: the
// branch's offline exceptions, open and held by default.
func (h *Handler) ListOfflineExceptions(w http.ResponseWriter, r *http.Request) {
	after, afterID, limit, err := keysetPage(r, offlineExceptionsOrdering, "branch_id", "status")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	q := r.URL.Query()
	branch, perr := uuid.Parse(q.Get("branch_id"))
	if perr != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "branch_id", Message: "is required and must be a UUID"}))
		return
	}
	status := q.Get("status")
	if status == "" {
		status = "open"
	}
	statuses, ok := offlineExceptionStatuses[status]
	if !ok {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "status", Message: "must be one of: open, held, resolved, all"}))
		return
	}
	items, more, err := h.service.ListOfflineExceptions(r.Context(), branch, statuses, after, afterID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeKeysetList(w, r, items, more, offlineExceptionsOrdering, func(e OfflineException) (time.Time, uuid.UUID) {
		return e.CreatedAt.Time, e.ID
	}, limit)
}

// GetOfflineException is GET /pos/offline/exceptions/{id}.
func (h *Handler) GetOfflineException(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	e, err := h.service.GetOfflineException(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, e.Revision)
	writeJSON(w, http.StatusOK, e)
}

// ResolveOfflineException is POST /pos/offline/exceptions/{id}/resolve: a
// manager's resolution, never a machine key's (ADR 0011 3.4).
func (h *Handler) ResolveOfflineException(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "an offline exception is resolved by a person, never a machine key") {
		return
	}
	var req offlineResolveRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// A configured deployment always carries claims; none is the dev mode
	// bypass, which resolves as "dev-mode", a person other than any cashier.
	actor := devApprover
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		if claims.Subject == "" || !middleware.ClaimsHaveAnyRole(claims, offlineResolverRoles...) {
			httpx.WriteError(w, r, httpx.Forbidden("an offline exception is resolved by a signed in manager (owner or admin)"))
			return
		}
		actor = h.actorFrom(r)
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	e, err := h.service.ResolveOfflineException(r.Context(), id, in, r.Header.Get("If-Match"), actor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, e.Revision)
	writeJSON(w, http.StatusOK, e)
}
