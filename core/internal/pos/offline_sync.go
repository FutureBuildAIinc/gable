// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gablelbm/gable/internal/taxzone"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// The offline sync rebuilt (ADR 0011 sections 2, 3 and 6, slice O3). Each
// signed sale of a batch is processed in device sequence order, in one
// transaction under the register's device row (lock 0a): the duplicate check
// across both tables, the chain over every verified item, the snapshot
// verification, then the posting through the completion's own body with an
// offline verdict. What the server cannot post or cannot verify is written to
// pos_offline_exceptions, never dropped. Nothing here prices a sale again.

// Outcomes of one item.
const (
	OfflinePosted    = "posted"
	OfflineDuplicate = "duplicate"
	OfflineRefused   = "refused"
	OfflineHeld      = "held"
	// OfflineResolved: a manager resolved the item's exception with nothing
	// posted (a write off); the device settles it
	OfflineResolved = "resolved"
	// offlineFault: the server could not judge the item; retried next sync
	offlineFault = "error"
)

// The reasons an exception row carries that the sync writes (ADR 0011 3.4).
const (
	reasonBadSignature   = "bad_signature"
	reasonReplayMismatch = "replay_mismatch"
	reasonSequenceReused = "sequence_reused"
	reasonBrokenChain    = "broken_chain"
	reasonLateAfterGap   = "late_after_gap"
	reasonSequenceGap    = "sequence_gap"
	reasonSoldPastWarn   = "sold_past_warning"
)

// The device row's chain head before its first verified item, and the mark
// a manager's accept of a sequence gap leaves (ADR 0011 3.4: the chain
// restarts, and the next item's own link is adopted as the head). O4's accept
// writes offlineChainRestart.
const (
	offlineChainGenesis = "GENESIS"
	offlineChainRestart = "RESTART"
)

// OfflineBatch is one sync request: the register, the device's own mark (the
// last sequence it assigned under its epoch, ADR 0011 5.1) and the signed
// sales as received.
type OfflineBatch struct {
	BatchID     string
	RegisterID  string
	DeviceEpoch int64
	ReportedSeq int64
	Items       []json.RawMessage
}

// OfflineItemOutcome is the server's answer for one signed sale.
type OfflineItemOutcome struct {
	ClientID      string     `json:"client_id"`
	DeviceEpoch   int64      `json:"device_epoch"`
	DeviceSeq     int64      `json:"device_seq"`
	Outcome       string     `json:"outcome"`
	SaleID        *uuid.UUID `json:"sale_id"`
	Number        *string    `json:"number"`
	InvoiceID     *uuid.UUID `json:"invoice_id"`
	InvoiceNumber *string    `json:"invoice_number"`
	ExceptionID   *uuid.UUID `json:"exception_id"`
	ReasonCode    *string    `json:"reason_code"`
	ReasonMessage *string    `json:"reason_message"`
}

// OfflineSyncResult is the batch's answer: every item's outcome and the mark
// the server confirms to the device (ADR 0011 6.2: every sync answer carries
// it back).
type OfflineSyncResult struct {
	BatchID      string               `json:"batch_id"`
	RegisterID   string               `json:"register_id"`
	DeviceEpoch  int64                `json:"device_epoch"`
	ConfirmedSeq int64                `json:"confirmed_seq"`
	Items        []OfflineItemOutcome `json:"items"`
	// OpenExceptionCount is the register's exception rows that wait on a
	// manager with nothing posted, the counter's "needs a manager" count.
	OpenExceptionCount int64 `json:"open_exception_count"`
}

// offlineVerdict is what the snapshot verification hands the completion's
// body (ADR 0011 3.1): the recorded tax and business date, the snapshot and
// the item for the audit row, and back from the body the warnings the sale
// was sold past.
type offlineVerdict struct {
	snapshotID uuid.UUID
	batchID    string
	taxCents   int64
	taxRate    string
	exempt     bool
	// zone is a Canadian sale's parts with the cents the device signed
	// (CP-14 A1), frozen onto its invoice; nil on a US sale.
	zone         *taxzone.Snapshot
	businessDate time.Time
	item         *offlineItem
	warnings     []uuid.UUID
	// latePost is a manager's late post (ADR 0011 3.4, slice O4): the sale
	// attaches to no till session, so the body skips the session check and
	// the drawer attach, and books on businessDate, the earliest open date
	latePost bool
}

// offlineRefusal is a coded reason an item goes to the exception queue.
type offlineRefusal struct {
	code, message string
}

func (r *offlineRefusal) Error() string { return r.code + ": " + r.message }

func refuse(code, format string, args ...any) *offlineRefusal {
	return &offlineRefusal{code: code, message: fmt.Sprintf(format, args...)}
}

// SyncOffline runs one batch (ADR 0011 section 3). The caller is a person
// holding the counter's write grant whose wall reaches the register's branch
// (section 6.3; the handler refuses a machine key). A branch whose offline
// policy is off answers 409 money_never_queues and nothing is written.
func (s *Service) SyncOffline(ctx context.Context, batch *OfflineBatch, actor string) (*OfflineSyncResult, error) {
	if s.ar == nil || s.invoices == nil || s.inventory == nil {
		return nil, errNotConfigured
	}
	if strings.TrimSpace(batch.RegisterID) == "" {
		return nil, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "register_id", Message: "the batch names no register"})
	}
	branch, err := s.repo.GetRegisterBranch(ctx, batch.RegisterID)
	if err != nil {
		var hx *httpx.Error
		if errors.As(err, &hx) && hx.Status == http.StatusNotFound {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "register_id", Message: "the batch names no register the counter knows"})
		}
		return nil, err
	}
	if branch == nil {
		return nil, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "register_id", Message: "the register belongs to no branch"})
	}
	policy, err := s.repo.GetOfflinePolicy(ctx, *branch)
	if err != nil {
		return nil, err
	}
	if policy == nil || !policy.Enabled {
		return nil, &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeMoneyNeverQueues,
			Message: "money never queues offline at this branch: complete the sale online"}
	}
	items := make([]*offlineItem, 0, len(batch.Items))
	for i, raw := range batch.Items {
		it, err := parseOfflineItem(raw)
		if err != nil {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: fmt.Sprintf("items[%d]", i), Message: err.Error()})
		}
		if _, err := uuid.Parse(it.Sale.ClientID); err != nil {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: fmt.Sprintf("items[%d].client_id", i), Message: "must be a UUID"})
		}
		if it.Sale.RegisterID != batch.RegisterID {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: fmt.Sprintf("items[%d].register_id", i), Message: "every item names the batch's register"})
		}
		if it.Sale.DeviceEpoch < 1 || it.Sale.DeviceSeq < 1 {
			return nil, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: fmt.Sprintf("items[%d].device_seq", i), Message: "the epoch and the sequence are positive"})
		}
		items = append(items, it)
	}
	// in device sequence order, epoch first (ADR 0011 section 3)
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Sale, items[j].Sale
		if a.DeviceEpoch != b.DeviceEpoch {
			return a.DeviceEpoch < b.DeviceEpoch
		}
		return a.DeviceSeq < b.DeviceSeq
	})

	res := &OfflineSyncResult{BatchID: batch.BatchID, RegisterID: batch.RegisterID, DeviceEpoch: batch.DeviceEpoch,
		Items: []OfflineItemOutcome{}}
	log := LogBatch{BatchID: batch.BatchID, RegisterID: batch.RegisterID}
	epochs := map[int64]bool{}
	// a re-offer that faults is answered as an error item, like a fault in
	// syncOfflineItem, and that epoch is not re-offered again in this sync,
	// so the confirmed mark and the log are still written
	faulted := map[int64]bool{}
	reoffer := func(epoch int64) error {
		if faulted[epoch] {
			return nil
		}
		reoffered, fault, err := s.reofferHeld(ctx, batch.BatchID, batch.RegisterID, epoch, actor)
		if err != nil {
			return err
		}
		faulted[epoch] = fault
		for i := range reoffered {
			res.Items = append(res.Items, reoffered[i])
			s.countOutcome(&log, &reoffered[i])
		}
		return nil
	}
	for _, it := range items {
		out, err := s.syncOfflineItem(ctx, *branch, batch.BatchID, it, actor)
		if err != nil {
			// an item the server could not judge (a fault, not a refusal)
			// is answered with the fault and retried at the next sync; it
			// advanced nothing
			e := SyncItemResult{ClientID: it.Sale.ClientID, Reason: err.Error()}
			log.Errors = append(log.Errors, e)
			log.ErrorCount++
			msg := err.Error()
			res.Items = append(res.Items, OfflineItemOutcome{ClientID: it.Sale.ClientID, DeviceEpoch: it.Sale.DeviceEpoch,
				DeviceSeq: it.Sale.DeviceSeq, Outcome: offlineFault, ReasonMessage: &msg})
			continue
		}
		res.Items = append(res.Items, *out)
		epochs[it.Sale.DeviceEpoch] = true
		s.countOutcome(&log, out)
		// a held item the mark now reaches is re-offered at once
		if err := reoffer(it.Sale.DeviceEpoch); err != nil {
			return nil, err
		}
	}
	// and at the end of every sync of the register (ADR 0011 3.4)
	if batch.DeviceEpoch > 0 {
		epochs[batch.DeviceEpoch] = true
	}
	for epoch := range epochs {
		if err := reoffer(epoch); err != nil {
			return nil, err
		}
	}
	// the mark the server confirms, and the device's own mark it reported
	// (the till close compares the two, ADR 0011 5.1)
	for epoch := range epochs {
		reported := int64(0)
		if epoch == batch.DeviceEpoch {
			reported = batch.ReportedSeq
		}
		confirmed, err := s.repo.ConfirmDeviceMark(ctx, batch.RegisterID, epoch, reported)
		if err != nil {
			return nil, err
		}
		if epoch == batch.DeviceEpoch || res.DeviceEpoch == 0 {
			res.ConfirmedSeq = confirmed
			res.DeviceEpoch = epoch
		}
	}
	if err := s.repo.LogSyncBatch(ctx, log); err != nil {
		return nil, err
	}
	open, err := s.repo.OpenOfflineExceptionCount(ctx, batch.RegisterID)
	if err != nil {
		return nil, err
	}
	res.OpenExceptionCount = open
	return res, nil
}

func (s *Service) countOutcome(log *LogBatch, out *OfflineItemOutcome) {
	switch out.Outcome {
	case OfflinePosted:
		log.Synced++
	case OfflineDuplicate:
		log.Duplicates++
	case OfflineRefused:
		log.Refused++
		log.Details = append(log.Details, SyncItemResult{ClientID: out.ClientID, Reason: "refused: " + derefString(out.ReasonCode)})
	case OfflineHeld:
		log.Gaps++
		log.Details = append(log.Details, SyncItemResult{ClientID: out.ClientID, Reason: "held: " + derefString(out.ReasonCode)})
	case offlineFault:
		log.ErrorCount++
		log.Errors = append(log.Errors, SyncItemResult{ClientID: out.ClientID, Reason: derefString(out.ReasonMessage)})
	}
}

// itemClass is where the duplicate and chain checks place an item.
type itemClass struct {
	// answered: the item was seen before; out is the answer and nothing is
	// written
	answered *OfflineItemOutcome
	// record: the item goes to the queue as code without a posting attempt
	record *offlineRefusal
	status string
	// gap: the accepted gap a late item closes
	gap *uuid.UUID
	// inOrder: the item is the mark plus one and its link meets the head
	inOrder bool
	// advance: recording the item moves the mark and the head over it
	advance bool
	// supersede: bad copies of the item's id that its true sale closes
	supersede bool
	// held: the held broken_chain row a re-offered item already has; a
	// warning the posting was sold past goes onto that row (one row per
	// sale id), and heldWarned says it did
	held       *uuid.UUID
	heldWarned bool
}

// classifyOfflineItem is the duplicate check of ADR 0011 section 2 and the
// chain rule of section 3.4, run under the register's device row (dev, nil
// when no enrolment of the item's epoch exists).
func (s *Service) classifyOfflineItem(ctx context.Context, dev *registerDevice, it *offlineItem) (*itemClass, error) {
	id := uuid.MustParse(it.Sale.ClientID)
	sigOK := dev != nil && verifySaleSignature(dev.PublicKey, it)
	posted, err := s.repo.OfflinePostedRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if posted != nil {
		if sigOK && posted.Digest == it.Digest {
			return &itemClass{answered: posted.outcome(it)}, nil
		}
		return &itemClass{record: refuse(reasonReplayMismatch,
			"this sale's id is already posted with other figures"), status: "open"}, nil
	}
	rows, err := s.repo.OfflineExceptionsOf(ctx, id)
	if err != nil {
		return nil, err
	}
	var verified []offlineExceptionRow
	badOnly := false
	for _, r := range rows {
		switch r.ReasonCode {
		case reasonBadSignature:
			badOnly = true
		case reasonReplayMismatch:
		default:
			verified = append(verified, r)
		}
	}
	if len(verified) > 0 {
		for _, r := range verified {
			if sigOK && r.RegisterID == it.Sale.RegisterID && r.DeviceEpoch == it.Sale.DeviceEpoch &&
				r.DeviceSeq == it.Sale.DeviceSeq && r.SignatureDigest == it.Digest {
				return &itemClass{answered: r.outcome(it)}, nil
			}
		}
		return &itemClass{record: refuse(reasonReplayMismatch,
			"this sale's id is already recorded with other figures"), status: "open"}, nil
	}
	if !sigOK {
		msg := "the sale's signature does not verify under the register's enrolled key"
		if dev == nil {
			msg = "no enrolment of this register holds the sale's epoch"
		}
		return &itemClass{record: refuse(reasonBadSignature, "%s", msg), status: "open"}, nil
	}
	holder, err := s.repo.OfflineSequenceHolder(ctx, it.Sale.RegisterID, it.Sale.DeviceEpoch, it.Sale.DeviceSeq, id)
	if err != nil {
		return nil, err
	}
	if holder {
		return &itemClass{record: refuse(reasonSequenceReused,
			"sequence %d of epoch %d is already used by another sale", it.Sale.DeviceSeq, it.Sale.DeviceEpoch),
			status: "open", supersede: badOnly}, nil
	}
	head := dev.ChainHead
	if head == "" {
		head = offlineChainGenesis
	}
	switch {
	case it.Sale.DeviceSeq <= dev.HighSeq:
		gap, err := s.repo.OfflineGapCovering(ctx, it.Sale.RegisterID, it.Sale.DeviceEpoch, it.Sale.DeviceSeq)
		if err != nil {
			return nil, err
		}
		if gap != nil {
			return &itemClass{record: refuse(reasonLateAfterGap,
				"sequence %d arrived after a manager accepted its gap", it.Sale.DeviceSeq),
				status: "open", gap: gap, supersede: badOnly}, nil
		}
		return &itemClass{record: refuse(reasonSequenceReused,
			"sequence %d of epoch %d is at or below the register's mark", it.Sale.DeviceSeq, it.Sale.DeviceEpoch),
			status: "open", supersede: badOnly}, nil
	case it.Sale.DeviceSeq == dev.HighSeq+1 && (it.Sale.PrevDigest == head || head == offlineChainRestart):
		return &itemClass{inOrder: true, advance: true, supersede: badOnly}, nil
	default:
		return &itemClass{record: refuse(reasonBrokenChain,
			"the sale's link does not meet the register's chain at sequence %d: it waits for its predecessor", dev.HighSeq+1),
			status: "held", supersede: badOnly}, nil
	}
}

// syncOfflineItem processes one item: the posting attempt in one
// transaction under 0a and, when it is refused, the exception write in its
// own transaction, which takes 0a again and repeats the duplicate check
// (ADR 0011 section 3.4).
func (s *Service) syncOfflineItem(ctx context.Context, branch uuid.UUID, batchID string, it *offlineItem, actor string) (*OfflineItemOutcome, error) {
	var out *OfflineItemOutcome
	var refused *offlineRefusal
	err := s.inTx(ctx, func(ctx context.Context) error {
		dev, err := s.repo.LockRegisterDevice(ctx, it.Sale.RegisterID, it.Sale.DeviceEpoch)
		if err != nil {
			return err
		}
		cls, err := s.classifyOfflineItem(ctx, dev, it)
		if err != nil {
			return err
		}
		switch {
		case cls.answered != nil:
			out = cls.answered
			return nil
		case cls.record != nil:
			out, err = s.writeOfflineException(ctx, branch, dev, it, cls, cls.record, actor)
			return err
		}
		posted, err := s.postOfflineSale(ctx, branch, batchID, dev, it, cls, actor)
		var r *offlineRefusal
		if errors.As(err, &r) {
			refused = r
			return r
		}
		out = posted
		return err
	})
	if refused == nil {
		return out, err
	}
	// The posting rolled back and released 0a: the exception write takes it
	// again and repeats the check, so a concurrent sync of the same batch
	// that posted or recorded the item meanwhile is answered as it stands.
	err = s.inTx(ctx, func(ctx context.Context) error {
		dev, err := s.repo.LockRegisterDevice(ctx, it.Sale.RegisterID, it.Sale.DeviceEpoch)
		if err != nil {
			return err
		}
		cls, err := s.classifyOfflineItem(ctx, dev, it)
		if err != nil {
			return err
		}
		switch {
		case cls.answered != nil:
			out = cls.answered
			return nil
		case cls.record != nil:
			out, err = s.writeOfflineException(ctx, branch, dev, it, cls, cls.record, actor)
			return err
		}
		out, err = s.writeOfflineException(ctx, branch, dev, it, cls, refused, actor)
		return err
	})
	return out, err
}

// postOfflineSale posts one in order, verified item (ADR 0011 3.1 and 3.2):
// the snapshot verification, the sale row inserted with its client id and
// its lines as recorded, the replaced cart, then the completion's own body
// with the offline verdict, and the mark and the head over the item. A coded
// refusal comes back as *offlineRefusal and the caller's transaction rolls
// back.
func (s *Service) postOfflineSale(ctx context.Context, branch uuid.UUID, batchID string, dev *registerDevice,
	it *offlineItem, cls *itemClass, actor string) (*OfflineItemOutcome, error) {
	v, err := s.verifyOfflineSale(ctx, branch, dev, it)
	if err != nil {
		return nil, err
	}
	v.batchID = batchID
	id := uuid.MustParse(it.Sale.ClientID)
	sale, err := s.insertOfflineSale(ctx, it, v)
	if err != nil {
		return nil, err
	}
	tenders := offlineTenders(it.Sale.Tenders)
	pre := &Sale{ID: id, CustomerID: sale.CustomerID, WalkInID: sale.WalkInID, BranchID: sale.BranchID}
	realCustomer := sale.WalkInID
	if sale.CustomerID != nil {
		realCustomer = *sale.CustomerID
	}
	rev := sale.Revision
	done, err := s.completeSaleTx(ctx, id, "", &rev, tenders, derefString(it.Sale.PickedUpBy), actor, pre, realCustomer,
		make([]*chargeResult, len(tenders)), nil, nil, nil, nil, false, false, v)
	if err != nil {
		if r := offlineRefusalOf(err); r != nil {
			return nil, r
		}
		return nil, err
	}
	if len(v.warnings) > 0 {
		if err := s.recordSoldPastWarning(ctx, branch, it, v, done, cls, actor); err != nil {
			return nil, err
		}
	}
	if cls.supersede {
		if err := s.repo.SupersedeBadCopies(ctx, id); err != nil {
			return nil, err
		}
	}
	if cls.advance {
		if err := s.repo.AdvanceDeviceMark(ctx, dev.ID, it.Sale.DeviceSeq, it.Digest); err != nil {
			return nil, err
		}
	}
	out := &OfflineItemOutcome{ClientID: it.Sale.ClientID, DeviceEpoch: it.Sale.DeviceEpoch, DeviceSeq: it.Sale.DeviceSeq,
		Outcome: OfflinePosted, SaleID: &done.ID}
	number := done.Number
	out.Number = &number
	out.InvoiceID = done.InvoiceID
	if done.InvoiceID != nil {
		if n, err := s.repo.OfflineInvoiceNumber(ctx, *done.InvoiceID); err == nil {
			out.InvoiceNumber = &n
		}
	}
	return out, nil
}

// insertOfflineSale writes the sale row with the client's id and the offline
// provenance, its lines as recorded and their tally rows, and settles the
// replaced cart, in the order of ADR 0011 section 3.2 step 1 and ADR 0005
// section 11 (the replaced cart, the session and step 1a before the insert).
func (s *Service) insertOfflineSale(ctx context.Context, it *offlineItem, v *offlineVerdict) (*Sale, error) {
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
			return nil, offlineRefusalOf(err)
		}
		sale.Currency = facts.Currency
	}
	var replaced *uuid.UUID
	if it.Sale.ReplacesSaleID != nil {
		r := uuid.MustParse(*it.Sale.ReplacesSaleID)
		replaced = &r
	}
	if err := s.lockBeforeOfflineInsert(ctx, it, replaced, &session, sale.CustomerID); err != nil {
		return nil, err
	}
	if err := s.repo.CreateSale(ctx, sale); err != nil {
		return nil, err
	}
	if err := s.repo.MarkOfflineSale(ctx, id, it, v.snapshotID); err != nil {
		return nil, err
	}
	lines, err := s.offlineLines(ctx, it)
	if err != nil {
		return nil, err
	}
	if err := s.repo.AddLines(ctx, id, lines); err != nil {
		return nil, err
	}
	return sale, nil
}

// lockBeforeOfflineInsert takes ADR 0005 section 11's locks before an
// offline sale's row is written: the replaced cart's row (step 1, whatever
// its id: the new sale row is not yet visible, so no other act can wait on
// it), the session FOR SHARE (1b, when there is one) and the customer's
// credit lock (1a), all before the sale row's insert, whose customer foreign
// key takes the customer row FOR KEY SHARE; a live completion of the cart or
// the customer holds the cart and 1a, then the customer row FOR UPDATE (step
// 7). The sync and every resolution that posts share it.
func (s *Service) lockBeforeOfflineInsert(ctx context.Context, it *offlineItem, replaced, session, customer *uuid.UUID) error {
	if replaced != nil {
		if err := s.settleReplacedCart(ctx, it, *replaced); err != nil {
			return err
		}
	}
	if session != nil {
		if err := s.repo.LockTillSession(ctx, *session, false); err != nil {
			return err
		}
	}
	if customer != nil {
		return s.repo.LockCustomerCredit(ctx, *customer)
	}
	return nil
}

// offlineLines builds the sale lines as the device recorded them: the price,
// the extension, the units and the stock figures are stored as they are
// (verified, never priced again), with the product's resolved cost as the
// live line carries it.
func (s *Service) offlineLines(ctx context.Context, it *offlineItem) ([]salesdoc.Line, error) {
	ids := make([]uuid.UUID, 0, len(it.Sale.Lines))
	for _, l := range it.Sale.Lines {
		ids = append(ids, uuid.MustParse(l.ProductID))
	}
	refs, err := s.repo.LookupProducts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]salesdoc.Line, 0, len(it.Sale.Lines))
	for i, l := range it.Sale.Lines {
		lineID := uuid.New()
		if parsed, err := uuid.Parse(l.ID); err == nil {
			lineID = parsed
		}
		pid := uuid.MustParse(l.ProductID)
		q := mustQty(l.Quantity)
		uq, pq, sq := mustQty(l.UOMQty), mustQty(l.PriceUOMQty), mustQty(l.StockQuantity)
		uom, priceUOM, stockUOM := l.UOM, l.PriceUOM, l.StockUOM
		price := httpx.Price(l.UnitPriceTenThousandths)
		total := httpx.Cents(l.LineTotalCents)
		sku, desc := l.SKU, l.Description
		line := salesdoc.Line{ID: lineID, Position: i, LineType: salesdoc.LineProduct, ProductID: &pid,
			SKU: &sku, Description: desc, Quantity: &q, UOM: &uom, PriceUOM: &priceUOM, UOMQty: &uq, PriceUOMQty: &pq,
			StockUOM: &stockUOM, StockQuantity: &sq, UnitPrice: &price, PricedUnitPrice: &price,
			PriceSource: salesdoc.PriceSourceList, PriceBasis: offlineBasis(l), LineTotal: &total, Taxable: l.Taxable}
		if ref, ok := refs[pid.String()]; ok && ref.AverageCost > 0 {
			source := "AVERAGE_COST"
			cost := ref.AverageCost
			line.CostSource, line.StockingUnitCost = &source, &cost
		}
		if l.Tally != nil {
			t := &salesdoc.Tally{}
			if l.Tally.ThicknessIn != nil {
				th := mustQty(*l.Tally.ThicknessIn)
				t.ThicknessIn = &th
			}
			if l.Tally.WidthIn != nil {
				w := mustQty(*l.Tally.WidthIn)
				t.WidthIn = &w
			}
			for _, r := range l.Tally.Rows {
				t.Rows = append(t.Rows, salesdoc.TallyRow{Pieces: r.Pieces, LengthFT: mustQty(r.LengthFT)})
			}
			lf := mustQty(l.Tally.LinearFeet)
			t.LinearFeet = lf
			if l.Tally.BoardFeet != nil {
				bf := mustQty(*l.Tally.BoardFeet)
				t.BoardFeet = &bf
			}
			line.Tally = t
		}
		out = append(out, line)
	}
	return out, nil
}

// settleReplacedCart takes the cart an offline sale replaces (ADR 0011 3.2):
// it must be the offline sale's own register's and session's, or the sale is
// refused replaced_sale_mismatch and nothing is abandoned; if it is no longer
// open or held, or a terminal intent of it is still open, the sale is refused
// replaced_sale_settled and nothing is voided; otherwise it moves to
// ABANDONED with an audit row naming the offline sale. The cart row is
// locked FOR NO KEY UPDATE.
func (s *Service) settleReplacedCart(ctx context.Context, it *offlineItem, cartID uuid.UUID) error {
	cart, err := s.repo.LockReplacedCart(ctx, cartID)
	if err != nil {
		return err
	}
	if cart == nil || cart.RegisterID != it.Sale.RegisterID || cart.TillSessionID == nil ||
		cart.TillSessionID.String() != it.Sale.TillSessionID {
		return refuse("replaced_sale_mismatch",
			"the cart this sale replaces is not an open cart of this register's session: nothing was abandoned")
	}
	if cart.Status != StatusOpen && cart.Status != StatusHeld {
		return refuse("replaced_sale_settled",
			"the cart this sale replaces is %s: a manager compares the two, nothing was voided", strings.ToLower(string(cart.Status)))
	}
	open, err := s.repo.SaleHasOpenTerminalIntent(ctx, cartID)
	if err != nil {
		return err
	}
	if open {
		return refuse("replaced_sale_settled",
			"the cart this sale replaces has a card terminal answer still open: a manager compares the two, nothing was voided")
	}
	if err := s.repo.AbandonSale(ctx, cartID); err != nil {
		return err
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "pos.transaction.abandoned", EntityType: "pos_transaction", EntityID: cartID, UserID: it.Sale.CashierID,
			Changes: map[string]any{"from_status": cart.Status.Status(), "replaced_by": it.Sale.ClientID,
				"offline_ref": it.Sale.OfflineRef}}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return nil
}

// recordSoldPastWarning keeps, for a manager, every acknowledge required
// warning an offline sale was sold past (the CP-13 OW exception: reason
// sold_past_warning, the oldest id in warning_alert_id and every id in
// warning_alert_ids), with one audit row per warning, in the posting's own
// transaction. The sale itself posted; the warning stays open. A re-offered
// held item already has its row (one row per sale id): the warning is
// written onto that row instead, which then reads sold_past_warning and open
// for the manager in place of relinked.
func (s *Service) recordSoldPastWarning(ctx context.Context, branch uuid.UUID, it *offlineItem, v *offlineVerdict, done *Sale,
	cls *itemClass, actor string) error {
	const message = "sold offline past an open counter warning"
	if cls.held != nil {
		if err := s.repo.MarkHeldSoldPastWarning(ctx, *cls.held, done.ID, v.warnings, reasonSoldPastWarn, message); err != nil {
			return err
		}
		cls.heldWarned = true
	} else {
		row := s.exceptionRowOf(branch, it, &offlineRefusal{code: reasonSoldPastWarn, message: message}, "open")
		row.WarningAlertIDs = v.warnings
		row.PostedSaleID = &done.ID
		session := uuid.MustParse(it.Sale.TillSessionID)
		row.TillSessionID = &session
		row.BusinessDate = v.businessDate
		snap := v.snapshotID
		row.SnapshotID = &snap
		if _, err := s.repo.InsertOfflineException(ctx, row); err != nil {
			return err
		}
	}
	if s.auditLog != nil {
		for _, a := range v.warnings {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.offline_sale.sold_past_warning", EntityType: "pos_transaction", EntityID: done.ID, UserID: actor,
				Changes: map[string]any{"alert_id": a, "customer_id": done.CustomerID, "offline_ref": it.Sale.OfflineRef}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
	}
	return nil
}

// writeOfflineException records an item the server could not post or could
// not verify (ADR 0011 3.4): the named session FOR SHARE (as the completion
// takes it, so a close holding it FOR UPDATE either sees the row or waits for
// it), the row with the money taken by tender, the bad copies its true sale
// supersedes, the closed gap of a late item, the mark and the head over a
// verified in order item, the audit row and the event last.
func (s *Service) writeOfflineException(ctx context.Context, branch uuid.UUID, dev *registerDevice, it *offlineItem,
	cls *itemClass, why *offlineRefusal, actor string) (*OfflineItemOutcome, error) {
	status := cls.status
	if status == "" {
		status = "open"
	}
	row := s.exceptionRowOf(branch, it, why, status)
	refs, err := s.repo.OfflineExceptionRefs(ctx, it)
	if err != nil {
		return nil, err
	}
	row.TillSessionID, row.SnapshotID, row.CustomerID = refs.session, refs.snapshot, refs.customer
	row.BusinessDate = refs.businessDate
	if refs.session != nil {
		if err := s.repo.LockTillSession(ctx, *refs.session, false); err != nil {
			return nil, err
		}
	}
	exID, err := s.repo.InsertOfflineException(ctx, row)
	if err != nil {
		return nil, err
	}
	id := uuid.MustParse(it.Sale.ClientID)
	if cls.supersede {
		if err := s.repo.SupersedeBadCopies(ctx, id); err != nil {
			return nil, err
		}
	}
	if cls.gap != nil {
		if err := s.repo.CloseGapFound(ctx, *cls.gap); err != nil {
			return nil, err
		}
	}
	if cls.advance && dev != nil {
		if err := s.repo.AdvanceDeviceMark(ctx, dev.ID, it.Sale.DeviceSeq, it.Digest); err != nil {
			return nil, err
		}
	}
	if err := s.offlineRefusedTrail(ctx, exID, row, actor); err != nil {
		return nil, err
	}
	code, msg := why.code, why.message
	outcome := OfflineRefused
	if status == "held" {
		outcome = OfflineHeld
	}
	return &OfflineItemOutcome{ClientID: it.Sale.ClientID, DeviceEpoch: it.Sale.DeviceEpoch, DeviceSeq: it.Sale.DeviceSeq,
		Outcome: outcome, ExceptionID: &exID, ReasonCode: &code, ReasonMessage: &msg}, nil
}

// offlineRefusedTrail writes the refusal's audit row and its event, last
// (ADR 0011 6.4).
func (s *Service) offlineRefusedTrail(ctx context.Context, exID uuid.UUID, row *offlineExceptionRow, actor string) error {
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{
			Action: "pos.offline_sale.refused", EntityType: "pos_offline_exception", EntityID: exID, UserID: actor,
			Changes: map[string]any{
				"sale_id": row.SaleID, "register_id": row.RegisterID, "device_epoch": row.DeviceEpoch,
				"device_seq": row.DeviceSeq, "reason_code": row.ReasonCode, "status": row.Status,
				"cash_cents": row.CashCents, "check_cents": row.CheckCents, "account_cents": row.AccountCents,
				"till_session_id": row.TillSessionID,
			}}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return s.recordEvent(ctx, outbox.Event{Type: "pos_offline.exception_opened", EntityType: "pos_offline_exception",
		EntityID: exID, BranchID: &row.BranchID, Data: eventJSON(map[string]any{
			"sale_id": row.SaleID, "register_id": row.RegisterID, "reason_code": row.ReasonCode, "status": row.Status,
			"offline_ref": row.OfflineRef, "total_cents": row.TotalCents,
		})})
}

// exceptionRowOf builds the row an item writes, the figures as signed.
func (s *Service) exceptionRowOf(branch uuid.UUID, it *offlineItem, why *offlineRefusal, status string) *offlineExceptionRow {
	row := &offlineExceptionRow{SaleID: uuid.MustParse(it.Sale.ClientID), RegisterID: it.Sale.RegisterID, BranchID: branch,
		DeviceEpoch: it.Sale.DeviceEpoch, DeviceSeq: it.Sale.DeviceSeq, OfflineRef: it.Sale.OfflineRef,
		SignedSale: it.Raw, Signature: it.Sale.DeviceSig, SignatureDigest: it.Digest, PrevDigest: it.Sale.PrevDigest,
		ReasonCode: why.code, ReasonMessage: why.message, Status: status, TotalCents: it.Sale.TotalCents}
	// the money taken by tender: what the drawer keeps, the change out of
	// the cash
	change := it.Sale.ChangeCents
	for _, t := range it.Sale.Tenders {
		switch strings.ToLower(strings.TrimSpace(t.Method)) {
		case "cash":
			kept := t.AmountCents
			if change > 0 {
				give := min64(kept, change)
				kept -= give
				change -= give
			}
			row.CashCents += kept
		case "check":
			row.CheckCents += t.AmountCents
		case "account":
			row.AccountCents += t.AmountCents
		}
	}
	return row
}

// reofferHeld takes the held broken_chain item at the register's mark plus
// one, if its link now meets the head, and processes it as if it had just
// arrived, one transaction per item under 0a and 0b, the posting inside a
// savepoint (ADR 0011 3.4): posted, the held row reads relinked; refused for
// a reason of its own, the held row takes that reason and opens for a
// manager. Either way the mark advances over it, and the next is tried. A
// fault in a held item's own transaction rolls it back and is answered as an
// error item (retried at the next sync), with faulted set so the caller
// stops re-offering the epoch; a fault before any held item is read is
// returned.
func (s *Service) reofferHeld(ctx context.Context, batchID, register string, epoch int64, actor string) (
	outs []OfflineItemOutcome, faulted bool, err error) {
	for {
		var out *OfflineItemOutcome
		var cur *offlineItem
		err := s.inTx(ctx, func(ctx context.Context) error {
			dev, err := s.repo.LockRegisterDevice(ctx, register, epoch)
			if err != nil || dev == nil {
				return err
			}
			held, err := s.repo.LockHeldAt(ctx, register, epoch, dev.HighSeq+1)
			if err != nil || held == nil {
				return err
			}
			it, err := parseOfflineItem(held.SignedSale)
			if err != nil {
				return err
			}
			cur = it
			head := dev.ChainHead
			if head == "" {
				head = offlineChainGenesis
			}
			if it.Sale.PrevDigest != head && head != offlineChainRestart {
				return nil
			}
			cls := &itemClass{inOrder: true, held: &held.ID}
			var posted *OfflineItemOutcome
			var refused *offlineRefusal
			perr := s.db.RunInSavepoint(ctx, func(ctx context.Context) error {
				var err error
				posted, err = s.postOfflineSale(ctx, held.BranchID, batchID, dev, it, cls, actor)
				if errors.As(err, &refused) {
					return refused
				}
				return err
			})
			if refused == nil && perr != nil {
				return perr
			}
			if refused != nil {
				if err := s.repo.ReopenHeldAs(ctx, held.ID, refused.code, refused.message); err != nil {
					return err
				}
				row := s.exceptionRowOf(held.BranchID, it, refused, "open")
				row.TillSessionID = held.TillSessionID
				if err := s.offlineRefusedTrail(ctx, held.ID, row, actor); err != nil {
					return err
				}
				code, msg := refused.code, refused.message
				exID := held.ID
				out = &OfflineItemOutcome{ClientID: it.Sale.ClientID, DeviceEpoch: epoch, DeviceSeq: it.Sale.DeviceSeq,
					Outcome: OfflineRefused, ExceptionID: &exID, ReasonCode: &code, ReasonMessage: &msg}
			} else {
				if !cls.heldWarned {
					if err := s.repo.MarkHeldRelinked(ctx, held.ID, *posted.SaleID); err != nil {
						return err
					}
				}
				out = posted
			}
			return s.repo.AdvanceDeviceMark(ctx, dev.ID, it.Sale.DeviceSeq, it.Digest)
		})
		if err != nil {
			if cur == nil {
				return nil, false, err
			}
			msg := err.Error()
			outs = append(outs, OfflineItemOutcome{ClientID: cur.Sale.ClientID, DeviceEpoch: epoch,
				DeviceSeq: cur.Sale.DeviceSeq, Outcome: offlineFault, ReasonMessage: &msg})
			return outs, true, nil
		}
		if out == nil {
			return outs, false, nil
		}
		outs = append(outs, *out)
	}
}

// offlineTenders turns the signed tenders into the completion's tenders.
func offlineTenders(in []SignedSaleTender) []TenderIn {
	out := make([]TenderIn, 0, len(in))
	for _, t := range in {
		ti := TenderIn{Method: TenderMethod(strings.ToUpper(strings.TrimSpace(t.Method))), AmountCents: httpx.Cents(t.AmountCents),
			IDChecked: t.IDChecked}
		if t.Reference != nil {
			ti.Reference = *t.Reference
		}
		if t.IDType != nil {
			ti.IDType = *t.IDType
		}
		out = append(out, ti)
	}
	return out
}

// offlineLiveRefusals are the live checks of the completion's body whose
// refusal sends an offline sale to the queue (ADR 0011 3.1 and 3.4), with
// the code the queue carries. Anything else is a fault, not a refusal.
var offlineLiveRefusals = map[string]string{
	"insufficient_stock":     "insufficient_stock",
	"session_closed":         "session_closed",
	"period_closed":          "period_closed",
	"stock_unit_changed":     "stock_unit_changed",
	"walk_in_account":        "walk_in_account",
	"insufficient_tender":    "tender_mismatch",
	"change_from_cash":       "tender_mismatch",
	"legs_do_not_sum":        "tender_mismatch",
	"no_tender_due":          "tender_mismatch",
	"payer_details_required": "payer_details_required",
}

// offlineRefusalOf reads a coded refusal out of an error of the body: a 409
// whose blocker is one of offlineLiveRefusals, or a wall's 404 or 403 (the
// customer, the session or the sale outside the syncing user's wall), which
// is out_of_wall. Nil when the error is no refusal.
func offlineRefusalOf(err error) *offlineRefusal {
	var r *offlineRefusal
	if errors.As(err, &r) {
		return r
	}
	var hx *httpx.Error
	if !errors.As(err, &hx) {
		return nil
	}
	if hx.Status == http.StatusNotFound || hx.Status == http.StatusForbidden {
		return refuse("out_of_wall", "%s", hx.Message)
	}
	if hx.Status != http.StatusConflict {
		return nil
	}
	for _, d := range hx.Details {
		if code, ok := offlineLiveRefusals[d.Code]; ok {
			return refuse(code, "%s", d.Message)
		}
	}
	return nil
}

// offlineBasis is a recorded line's basis: the sheet row is the engine's
// answer (stored PRICE_LIST, as a live line the engine priced), its basis the
// row's own, else the rung that won (contract, level, list), so the line
// still says where its price came from.
func offlineBasis(l SignedSaleLine) *string {
	if l.PriceBasis != nil && *l.PriceBasis != "" {
		return l.PriceBasis
	}
	rung := l.PriceSource
	if l.LevelFallback {
		rung = "level_fallback"
	}
	return &rung
}

func mustQty(s string) httpx.Quantity {
	q, err := httpx.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q
}
