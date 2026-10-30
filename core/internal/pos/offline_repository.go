// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// registerDevice is a register's enrolment of one epoch (ADR 0011 6.1): its
// key, the mark and head the server holds, the mark it confirmed.
type registerDevice struct {
	ID           uuid.UUID
	PublicKey    string
	HighSeq      int64
	ChainHead    string
	ConfirmedSeq int64
	RevokedAt    *time.Time
}

// offlinePosted is a posted sale as the duplicate check reads it.
type offlinePosted struct {
	SaleID        uuid.UUID
	Number        string
	Digest        string
	InvoiceID     *uuid.UUID
	InvoiceNumber *string
}

func (p *offlinePosted) outcome(it *offlineItem) *OfflineItemOutcome {
	id, number := p.SaleID, p.Number
	return &OfflineItemOutcome{ClientID: it.Sale.ClientID, DeviceEpoch: it.Sale.DeviceEpoch, DeviceSeq: it.Sale.DeviceSeq,
		Outcome: OfflineDuplicate, SaleID: &id, Number: &number, InvoiceID: p.InvoiceID, InvoiceNumber: p.InvoiceNumber}
}

// offlineExceptionRow is one row of pos_offline_exceptions.
type offlineExceptionRow struct {
	ID              uuid.UUID
	SaleID          uuid.UUID
	RegisterID      string
	BranchID        uuid.UUID
	DeviceEpoch     int64
	DeviceSeq       int64
	OfflineRef      string
	TillSessionID   *uuid.UUID
	BusinessDate    time.Time
	SnapshotID      *uuid.UUID
	CustomerID      *uuid.UUID
	WarningAlertIDs []uuid.UUID
	SignedSale      json.RawMessage
	Signature       string
	SignatureDigest string
	PrevDigest      string
	ReasonCode      string
	ReasonMessage   string
	CashCents       int64
	CheckCents      int64
	AccountCents    int64
	TotalCents      int64
	Status          string
	PostedSaleID    *uuid.UUID
}

func (r *offlineExceptionRow) outcome(it *offlineItem) *OfflineItemOutcome {
	id, code, msg := r.ID, r.ReasonCode, r.ReasonMessage
	out := &OfflineItemOutcome{ClientID: it.Sale.ClientID, DeviceEpoch: it.Sale.DeviceEpoch, DeviceSeq: it.Sale.DeviceSeq,
		Outcome: OfflineRefused, ExceptionID: &id, ReasonCode: &code, ReasonMessage: &msg}
	if r.Status == "held" {
		out.Outcome = OfflineHeld
	}
	return out
}

// offlineSnapshotRow is a stored snapshot.
type offlineSnapshotRow struct {
	RegisterID  string
	DeviceEpoch int64
	Content     json.RawMessage
	CreatedAt   time.Time
}

// offlineSessionFacts is what the sync reads of a till session.
type offlineSessionFacts struct {
	RegisterID   string
	CashierID    uuid.UUID
	OpenedAt     time.Time
	BusinessDate time.Time
}

// offlineRefs are the references an exception row may carry: each one only
// when it names a row that exists (a tampered item may name anything), the
// session only when it is the register's own.
type offlineRefs struct {
	session, snapshot, customer *uuid.UUID
	businessDate                time.Time
}

// offlineSessionDate is a session row's business date (alias t): the day
// it opened in its own branch's zone, UTC when the branch has none.
const offlineSessionDate = `(t.opened_at AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
	WHERE l.id = t.branch_id), 'UTC'))::date`

// LockRegisterDevice takes the register's device row of an epoch FOR UPDATE
// (lock 0a, ADR 0011 3.2). Nil when no enrolment holds that epoch.
func (r *PostgresRepository) LockRegisterDevice(ctx context.Context, register string, epoch int64) (*registerDevice, error) {
	d := &registerDevice{}
	err := r.ex(ctx).QueryRow(ctx, `SELECT id, public_key, high_seq, chain_head, confirmed_seq, revoked_at
		FROM pos_register_devices WHERE register_id = $1 AND epoch = $2 FOR UPDATE`, register, epoch).
		Scan(&d.ID, &d.PublicKey, &d.HighSeq, &d.ChainHead, &d.ConfirmedSeq, &d.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock the register's device: %w", err)
	}
	return d, nil
}

// AdvanceDeviceMark moves the mark and the chain head over a verified item.
func (r *PostgresRepository) AdvanceDeviceMark(ctx context.Context, deviceID uuid.UUID, seq int64, digest string) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_register_devices SET high_seq = $2, chain_head = $3, updated_at = NOW()
		WHERE id = $1`, deviceID, seq, digest); err != nil {
		return fmt.Errorf("failed to advance the register's mark: %w", err)
	}
	return nil
}

// ConfirmDeviceMark records, at the end of a sync, the mark the server
// confirms to the device (its high sequence) and the device's own reported
// mark, and answers the confirmed mark.
func (r *PostgresRepository) ConfirmDeviceMark(ctx context.Context, register string, epoch, reported int64) (int64, error) {
	var confirmed int64
	err := r.ex(ctx).QueryRow(ctx, `UPDATE pos_register_devices SET confirmed_seq = high_seq,
		reported_seq = GREATEST(reported_seq, $3), updated_at = NOW()
		WHERE register_id = $1 AND epoch = $2 RETURNING confirmed_seq`, register, epoch, reported).Scan(&confirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to confirm the register's mark: %w", err)
	}
	return confirmed, nil
}

// OfflinePostedRecord reads a posted sale by id for the duplicate check.
func (r *PostgresRepository) OfflinePostedRecord(ctx context.Context, id uuid.UUID) (*offlinePosted, error) {
	p := &offlinePosted{}
	var digest *string
	err := r.ex(ctx).QueryRow(ctx, `SELECT t.id, t.number, t.signature_digest, t.invoice_id, i.number
		FROM pos_transactions t LEFT JOIN invoices i ON i.id = t.invoice_id WHERE t.id = $1`, id).
		Scan(&p.SaleID, &p.Number, &digest, &p.InvoiceID, &p.InvoiceNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the posted sale: %w", err)
	}
	if digest != nil {
		p.Digest = *digest
	}
	return p, nil
}

const offlineExceptionCols = `id, sale_id, register_id, branch_id, device_epoch, device_seq, offline_ref, till_session_id,
	business_date, snapshot_id, customer_id, signed_sale, signature, signature_digest, prev_digest, reason_code,
	reason_message, cash_cents, check_cents, account_cents, total_cents, status, posted_sale_id`

func scanOfflineException(row pgx.Row) (*offlineExceptionRow, error) {
	e := &offlineExceptionRow{}
	var signed []byte
	err := row.Scan(&e.ID, &e.SaleID, &e.RegisterID, &e.BranchID, &e.DeviceEpoch, &e.DeviceSeq, &e.OfflineRef,
		&e.TillSessionID, &e.BusinessDate, &e.SnapshotID, &e.CustomerID, &signed, &e.Signature, &e.SignatureDigest,
		&e.PrevDigest, &e.ReasonCode, &e.ReasonMessage, &e.CashCents, &e.CheckCents, &e.AccountCents, &e.TotalCents,
		&e.Status, &e.PostedSaleID)
	e.SignedSale = signed
	return e, err
}

// OfflineExceptionsOf reads every exception row recorded for a sale id.
func (r *PostgresRepository) OfflineExceptionsOf(ctx context.Context, id uuid.UUID) ([]offlineExceptionRow, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+offlineExceptionCols+` FROM pos_offline_exceptions
		WHERE sale_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale's exception rows: %w", err)
	}
	defer rows.Close()
	var out []offlineExceptionRow
	for rows.Next() {
		e, err := scanOfflineException(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// OfflineSequenceHolder reports whether another id whose signature verified
// already holds the register's (epoch, sequence), in either table.
func (r *PostgresRepository) OfflineSequenceHolder(ctx context.Context, register string, epoch, seq int64, id uuid.UUID) (bool, error) {
	var held bool
	err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pos_transactions
			WHERE register_id = $1 AND device_epoch = $2 AND device_seq = $3 AND id <> $4)
		OR EXISTS (SELECT 1 FROM pos_offline_exceptions
			WHERE register_id = $1 AND device_epoch = $2 AND device_seq = $3 AND sale_id <> $4
			AND reason_code NOT IN ('bad_signature', 'replay_mismatch', 'sequence_reused', 'sequence_gap'))`,
		register, epoch, seq, id).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("failed to read the sequence's holder: %w", err)
	}
	return held, nil
}

// OfflineGapCovering finds the accepted gap (a sequence_gap item) that
// covers a sequence, not yet found.
func (r *PostgresRepository) OfflineGapCovering(ctx context.Context, register string, epoch, seq int64) (*uuid.UUID, error) {
	var id uuid.UUID
	err := r.ex(ctx).QueryRow(ctx, `SELECT id FROM pos_offline_exceptions
		WHERE register_id = $1 AND device_epoch = $2 AND reason_code = 'sequence_gap' AND status <> 'found'
		AND $3 BETWEEN COALESCE(gap_from_seq, device_seq) AND COALESCE(gap_to_seq, device_seq)
		ORDER BY created_at LIMIT 1 FOR UPDATE`, register, epoch, seq).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the register's accepted gaps: %w", err)
	}
	return &id, nil
}

// CloseGapFound closes a sequence_gap item as found.
func (r *PostgresRepository) CloseGapFound(ctx context.Context, id uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_offline_exceptions SET status = 'found', revision = revision + 1,
		updated_at = NOW() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("failed to close the gap as found: %w", err)
	}
	return nil
}

// SupersedeBadCopies closes the bad_signature rows of a sale id whose true
// sale is now recorded.
func (r *PostgresRepository) SupersedeBadCopies(ctx context.Context, id uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_offline_exceptions SET status = 'superseded', revision = revision + 1,
		updated_at = NOW() WHERE sale_id = $1 AND reason_code = 'bad_signature' AND status = 'open'`, id); err != nil {
		return fmt.Errorf("failed to supersede the bad copies: %w", err)
	}
	return nil
}

// InsertOfflineException writes one exception row and answers its id.
func (r *PostgresRepository) InsertOfflineException(ctx context.Context, e *offlineExceptionRow) (uuid.UUID, error) {
	warnings := e.WarningAlertIDs
	if warnings == nil {
		warnings = []uuid.UUID{}
	}
	ids, err := json.Marshal(warnings)
	if err != nil {
		return uuid.Nil, err
	}
	var first *uuid.UUID
	if len(warnings) > 0 {
		first = &warnings[0]
	}
	signed := e.SignedSale
	if !json.Valid(signed) {
		signed = json.RawMessage(`{}`)
	}
	var id uuid.UUID
	err = r.ex(ctx).QueryRow(ctx, `INSERT INTO pos_offline_exceptions (sale_id, register_id, branch_id, device_epoch,
			device_seq, offline_ref, till_session_id, business_date, snapshot_id, customer_id, warning_alert_id,
			warning_alert_ids, signed_sale, signature, signature_digest, prev_digest, reason_code, reason_message,
			cash_cents, check_cents, account_cents, total_cents, status, posted_sale_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb, $14, $15, $16, $17, $18,
			$19, $20, $21, $22, $23, $24)
		RETURNING id`,
		e.SaleID, e.RegisterID, e.BranchID, e.DeviceEpoch, e.DeviceSeq, e.OfflineRef, e.TillSessionID,
		e.BusinessDate, e.SnapshotID, e.CustomerID, first, string(ids), string(signed), e.Signature,
		e.SignatureDigest, e.PrevDigest, e.ReasonCode, e.ReasonMessage, e.CashCents, e.CheckCents, e.AccountCents,
		e.TotalCents, e.Status, e.PostedSaleID).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to write the offline exception: %w", mapWriteError(err))
	}
	return id, nil
}

// OfflineExceptionRefs resolves the references an exception row may carry
// and its business date: the named session's when it is the register's own,
// else the item's own date, else the branch's today.
func (r *PostgresRepository) OfflineExceptionRefs(ctx context.Context, it *offlineItem) (*offlineRefs, error) {
	out := &offlineRefs{}
	if id, err := uuid.Parse(it.Sale.TillSessionID); err == nil {
		var date time.Time
		err := r.ex(ctx).QueryRow(ctx, `SELECT `+offlineSessionDate+` FROM till_sessions t
			WHERE t.id = $1 AND t.register_id = $2`, id, it.Sale.RegisterID).Scan(&date)
		if err == nil {
			out.session, out.businessDate = &id, date
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("failed to read the sale's session: %w", err)
		}
	}
	if id, err := uuid.Parse(it.Sale.SnapshotID); err == nil {
		var ok bool
		if err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pos_offline_snapshots WHERE id = $1 AND register_id = $2)`,
			id, it.Sale.RegisterID).Scan(&ok); err != nil {
			return nil, fmt.Errorf("failed to read the sale's snapshot: %w", err)
		}
		if ok {
			out.snapshot = &id
		}
	}
	if it.Sale.CustomerID != nil {
		if id, err := uuid.Parse(*it.Sale.CustomerID); err == nil {
			var ok bool
			if err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1)`, id).Scan(&ok); err != nil {
				return nil, fmt.Errorf("failed to read the sale's customer: %w", err)
			}
			if ok {
				out.customer = &id
			}
		}
	}
	if out.session == nil {
		if d, err := time.Parse("2006-01-02", it.Sale.BusinessDate); err == nil {
			out.businessDate = d
		} else if err := r.ex(ctx).QueryRow(ctx, `SELECT (NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
				JOIN pos_registers pr ON pr.branch_id = l.id WHERE pr.id = $1), 'UTC'))::date`, it.Sale.RegisterID).Scan(&out.businessDate); err != nil {
			return nil, fmt.Errorf("failed to read the branch's date: %w", err)
		}
	}
	return out, nil
}

// MarkOfflineSale writes an offline sale's provenance on its new row (ADR
// 0011 sections 2 and 5.3): the epoch, the sequence, the reference, the
// snapshot, the signature digest, the replaced cart and the declaration, the
// queue figures it carried, the device's clock, and the purchase order, job
// and buyer the sale names.
func (r *PostgresRepository) MarkOfflineSale(ctx context.Context, id uuid.UUID, it *offlineItem, snapshot uuid.UUID) error {
	s := &it.Sale
	var soldAt *time.Time
	if t, err := time.Parse(time.RFC3339Nano, s.SoldAt); err == nil {
		soldAt = &t
	}
	_, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET device_epoch = $2, device_seq = $3, offline_ref = $4,
		snapshot_id = $5, signature_digest = $6, replaces_sale_id = $7, different_customer = $8, unknown_cart_id = $9,
		queue_count = $10, queue_total_cents = $11, confirmed_mark = $12, client_created_at = $13, customer_po = $14,
		job_id = $15, buyer_contact_id = $16
		WHERE id = $1`, id, s.DeviceEpoch, s.DeviceSeq, s.OfflineRef, snapshot, it.Digest, uuidArg(s.ReplacesSaleID),
		s.DifferentCustomer, uuidArg(s.UnknownCartID), s.QueueCount, s.QueueTotalCents, s.ConfirmedMark, soldAt,
		strArg(s.CustomerPO), uuidArg(s.JobID), uuidArg(s.BuyerContactID))
	if err != nil {
		return fmt.Errorf("failed to record the offline sale: %w", mapWriteError(err))
	}
	return nil
}

func uuidArg(s *string) any {
	if s == nil {
		return nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil
	}
	return id
}

// replacedCart is the cart an offline sale names, as the sync locks it.
type replacedCart struct {
	RegisterID    string
	TillSessionID *uuid.UUID
	Status        SaleStatus
}

// LockReplacedCart takes the replaced cart FOR NO KEY UPDATE (ADR 0011 3.2
// step 1). Nil when no sale has that id.
func (r *PostgresRepository) LockReplacedCart(ctx context.Context, id uuid.UUID) (*replacedCart, error) {
	c := &replacedCart{}
	err := r.ex(ctx).QueryRow(ctx, `SELECT register_id, till_session_id, status FROM pos_transactions WHERE id = $1
		FOR NO KEY UPDATE`, id).Scan(&c.RegisterID, &c.TillSessionID, &c.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock the replaced cart: %w", err)
	}
	return c, nil
}

// SaleHasOpenTerminalIntent reports whether a card terminal answer of the
// sale is still open: claimed, at the device, or authorized and not booked.
func (r *PostgresRepository) SaleHasOpenTerminalIntent(ctx context.Context, id uuid.UUID) (bool, error) {
	var open bool
	err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payment_terminal_intents WHERE transaction_id = $1
		AND (state IN ('claimed', 'pending_at_device') OR (state = 'authorized' AND spent_at IS NULL AND kind = 'sale')))`, id).Scan(&open)
	if err != nil {
		return false, fmt.Errorf("failed to read the cart's terminal answers: %w", err)
	}
	return open, nil
}

// AbandonSale moves an open or held cart to ABANDONED (ADR 0011 3.2): it
// holds no payment, stock move or entry, so nothing is reversed.
func (r *PostgresRepository) AbandonSale(ctx context.Context, id uuid.UUID) error {
	tag, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET status = 'ABANDONED', updated_at = NOW(),
		revision = revision + 1 WHERE id = $1 AND status IN ('OPEN', 'HELD')`, id)
	if err != nil {
		return fmt.Errorf("failed to abandon the cart: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return refuse("replaced_sale_settled", "the cart this sale replaces is no longer open")
	}
	return nil
}

// OfflineInvoiceNumber reads a posted sale's invoice number.
func (r *PostgresRepository) OfflineInvoiceNumber(ctx context.Context, id uuid.UUID) (string, error) {
	var n string
	err := r.ex(ctx).QueryRow(ctx, `SELECT number FROM invoices WHERE id = $1`, id).Scan(&n)
	return n, err
}

// LockHeldAt takes the held broken_chain row at a sequence FOR UPDATE (lock
// 0b, after 0a).
func (r *PostgresRepository) LockHeldAt(ctx context.Context, register string, epoch, seq int64) (*offlineExceptionRow, error) {
	e, err := scanOfflineException(r.ex(ctx).QueryRow(ctx, `SELECT `+offlineExceptionCols+` FROM pos_offline_exceptions
		WHERE register_id = $1 AND device_epoch = $2 AND device_seq = $3 AND reason_code = 'broken_chain' AND status = 'held'
		FOR UPDATE`, register, epoch, seq))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lock the held item: %w", err)
	}
	return e, nil
}

// ReopenHeldAs gives a re-offered held row the reason it was refused for and
// opens it for a manager.
func (r *PostgresRepository) ReopenHeldAs(ctx context.Context, id uuid.UUID, code, message string) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_offline_exceptions SET reason_code = $2, reason_message = $3,
		status = 'open', revision = revision + 1, updated_at = NOW() WHERE id = $1`, id, code, message); err != nil {
		return fmt.Errorf("failed to open the held item: %w", err)
	}
	return nil
}

// MarkHeldRelinked closes a re-offered held row whose sale posted.
func (r *PostgresRepository) MarkHeldRelinked(ctx context.Context, id, saleID uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_offline_exceptions SET status = 'relinked', posted_sale_id = $2,
		revision = revision + 1, updated_at = NOW() WHERE id = $1`, id, saleID); err != nil {
		return fmt.Errorf("failed to relink the held item: %w", err)
	}
	return nil
}

// OfflineSnapshotByID reads a stored snapshot.
func (r *PostgresRepository) OfflineSnapshotByID(ctx context.Context, id uuid.UUID) (*offlineSnapshotRow, error) {
	s := &offlineSnapshotRow{}
	var content []byte
	err := r.ex(ctx).QueryRow(ctx, `SELECT register_id, device_epoch, content, created_at FROM pos_offline_snapshots
		WHERE id = $1`, id).Scan(&s.RegisterID, &s.DeviceEpoch, &content, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the snapshot: %w", err)
	}
	s.Content = content
	return s, nil
}

// OfflineSessionFacts reads a till session's register, cashier, open time
// and business date.
func (r *PostgresRepository) OfflineSessionFacts(ctx context.Context, id uuid.UUID) (*offlineSessionFacts, error) {
	f := &offlineSessionFacts{}
	err := r.ex(ctx).QueryRow(ctx, `SELECT t.register_id, t.cashier_id, t.opened_at, `+offlineSessionDate+`
		FROM till_sessions t WHERE t.id = $1`, id).Scan(&f.RegisterID, &f.CashierID, &f.OpenedAt, &f.BusinessDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the sale's session: %w", err)
	}
	return f, nil
}

// BranchDayClosedOn reports whether the branch's day of a date is closed.
func (r *PostgresRepository) BranchDayClosedOn(ctx context.Context, branch uuid.UUID, date time.Time) (bool, error) {
	var closed bool
	err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM till_day_closes
		WHERE branch_id = $1 AND status = 'CLOSED' AND business_date = $2::date)`, branch, date.Format("2006-01-02")).Scan(&closed)
	if err != nil {
		return false, fmt.Errorf("failed to read the branch day: %w", err)
	}
	return closed, nil
}

// OfflineAccountUsedSince sums the offline account tenders of a register on
// an account the server recorded after a time (ADR 0011 6.2): posted sales
// and verified exception rows, each sale once, whatever snapshot it names.
func (r *PostgresRepository) OfflineAccountUsedSince(ctx context.Context, register string, customer uuid.UUID, since time.Time) (int64, error) {
	var used int64
	err := r.ex(ctx).QueryRow(ctx, `SELECT
		COALESCE((SELECT SUM(ROUND(tn.amount * 100))::bigint FROM pos_tenders tn JOIN pos_transactions t ON t.id = tn.transaction_id
			WHERE t.register_id = $1 AND t.customer_id = $2 AND t.offline_ref IS NOT NULL AND t.created_at > $3
			AND tn.method = 'ACCOUNT'), 0)
		+ COALESCE((SELECT SUM(e.account_cents) FROM pos_offline_exceptions e
			WHERE e.register_id = $1 AND e.customer_id = $2 AND e.created_at > $3
			AND e.reason_code NOT IN ('bad_signature', 'replay_mismatch', 'sequence_gap', 'sold_past_warning')
			AND NOT EXISTS (SELECT 1 FROM pos_transactions p WHERE p.id = e.sale_id)), 0)`,
		register, customer, since).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("failed to read the account's offline room: %w", err)
	}
	return used, nil
}
