// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The offline snapshot (ADR 0011 sections 5.4 and 6.2, slice O2): what a
// register is given to sell on while the connection is down, issued for its
// enrolled epoch and its open till session, stored so every signed sale can
// name it and the sync can verify against it. The shell's Rust core fetches
// it with the keychain's access token (ADR 0012 slice 5) and keeps the copy
// it prices and signs from.
//
// The stored content is the Rust core's OfflineSnapshot document
// (tauri/core/src/offline/model.rs); the sync reads it as OfflineSnapshot.
// The fields the core needs and the sync does not verify (a product's
// barcode, category and on hand, an account's jobs) ride beside the sync's
// own types in the same document.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OfflinePricer is the price engine's offline face (ADR 0011 section 5.4):
// the engine's answer with rung 2 skipped, for a customer, or for the level
// another customer holds (levelOf), at the branch and its business date, in
// the unit asked. AccountLevel names the level a customer prices at. The
// counter's price engine adapter implements it; with none, the sheet is the
// base price, as the counter's own fallback prices a line.
type OfflinePricer interface {
	SheetPrice(ctx context.Context, customerID uuid.UUID, levelOf *uuid.UUID, productID, branch uuid.UUID, uom string,
		on time.Time) (salesdoc.EngineAnswer, error)
	AccountLevel(ctx context.Context, customerID uuid.UUID) (uuid.UUID, bool, error)
}

// The price keys (ADR 0011 section 5.4): the walk-in's, an account's own,
// an account's level's.
const priceKeyWalkIn = "walkin"

func priceKeyAccount(id uuid.UUID) string { return "customer:" + id.String() }
func priceKeyLevel(id uuid.UUID) string   { return "level:" + id.String() }

// snapshotDoc is the stored document: the sync's OfflineSnapshot with the
// products and accounts carrying the core's extra fields.
type snapshotDoc struct {
	OfflineSnapshot
	Products []snapshotProductDoc `json:"products"`
	Accounts []snapshotAccountDoc `json:"accounts"`
}

type snapshotProductDoc struct {
	OfflineSnapshotProduct
	Barcode  *string `json:"barcode"`
	Category string  `json:"category"`
	InStock  string  `json:"in_stock"`
}

type snapshotAccountDoc struct {
	OfflineSnapshotAccount
	Jobs []snapshotJob `json:"jobs"`
}

type snapshotJob struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// snapshotProductRow is one product the branch sells, as the snapshot reads
// it.
type snapshotProductRow struct {
	ID          uuid.UUID
	SKU         string
	Description string
	Category    string
	Barcode     *string
	Taxable     bool
	IsKit       bool
	InStock     httpx.Quantity
}

// snapshotAccountRow is one customer of the branch the snapshot may cache.
type snapshotAccountRow struct {
	ID            uuid.UUID
	AccountNumber string
}

// IssuedSnapshot is a stored snapshot as the route answers it.
type IssuedSnapshot struct {
	ID      uuid.UUID
	Digest  string
	Content json.RawMessage
}

// ETag is the snapshot's entity tag: its digest, which moves whenever what
// the register may sell on moves.
func (s *IssuedSnapshot) ETag() string { return `"` + s.Digest + `"` }

// IssueSnapshot answers the register's snapshot: the active enrolment's
// epoch, its open session, the branch's policy, tax, products, price sheet
// and cached accounts. It takes lock 0a on the enrolment, so a sync of the
// register in flight records its items first and the drain test and the
// room read see them. A register that holds sales the server has not
// recorded (deviceHighSeq above the recorded mark) or an open bad copy is
// refused queue_not_drained. A snapshot whose content equals the latest one
// issued to the epoch is answered as that one, so its ETag stands still.
func (s *Service) IssueSnapshot(ctx context.Context, register string, deviceHighSeq int64) (*IssuedSnapshot, error) {
	branch, err := s.registerOfCaller(ctx, register)
	if err != nil {
		return nil, err
	}
	var out *IssuedSnapshot
	err = s.inTx(ctx, func(ctx context.Context) error {
		active, _, _, err := s.repo.RegisterDevices(ctx, register)
		if err != nil {
			return err
		}
		if active == nil {
			return conflict("device_not_enrolled", "this register is not enrolled to sell offline: a manager enrols it first")
		}
		if _, err := s.repo.LockRegisterDevice(ctx, register, active.DeviceEpoch); err != nil {
			return err
		}
		policy, err := s.repo.GetOfflinePolicy(ctx, branch)
		if err != nil {
			return err
		}
		if !policy.Enabled {
			return conflict("money_never_queues", "offline selling is off at this branch: complete sales online")
		}
		session, err := s.repo.RegisterOpenSession(ctx, register)
		if err != nil {
			return err
		}
		if session == nil {
			return conflict("till_not_open", "open the till before the register takes its offline snapshot")
		}
		// the drain rule (6.2): answered and recorded are one test
		mark, err := s.repo.RecordedMark(ctx, register, active.DeviceEpoch)
		if err != nil {
			return err
		}
		if deviceHighSeq > mark {
			return conflict("queue_not_drained", fmt.Sprintf(
				"the register holds sales the server has not recorded (sale %d onwards): send them before a new snapshot",
				mark+1))
		}
		if bad, err := s.repo.OpenBadCopy(ctx, register, active.DeviceEpoch); err != nil {
			return err
		} else if bad != nil {
			return conflict("queue_not_drained", fmt.Sprintf(
				"sale %d reached the server as a bad copy: the register keeps selling on the snapshot it holds until the true sale is sent",
				*bad))
		}
		doc, err := s.buildSnapshot(ctx, register, branch, active.DeviceEpoch, policy, session)
		if err != nil {
			return err
		}
		digest, err := snapshotDigest(doc)
		if err != nil {
			return err
		}
		if latest, err := s.repo.LatestOfflineSnapshot(ctx, register, active.DeviceEpoch); err != nil {
			return err
		} else if latest != nil && latest.Digest == digest {
			out = latest
			return nil
		}
		id := uuid.New()
		doc.SnapshotID = id.String()
		doc.IssuedAt = s.now().UTC().Format(time.RFC3339Nano)
		content, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if err := s.repo.InsertOfflineSnapshot(ctx, id, register, branch, active.DeviceEpoch, session.BusinessDate, content,
			digest); err != nil {
			return err
		}
		out = &IssuedSnapshot{ID: id, Digest: digest, Content: content}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// snapshotDigest is the digest of what the register may sell on: the
// document without its id and issue time.
func snapshotDigest(doc *snapshotDoc) (string, error) {
	probe := *doc
	probe.SnapshotID, probe.IssuedAt = "", ""
	raw, err := json.Marshal(&probe)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// buildSnapshot reads everything the register may sell on, as at the
// session's business date.
func (s *Service) buildSnapshot(ctx context.Context, register string, branch uuid.UUID, epoch int64, policy *OfflinePolicy,
	session *offlineOpenSession) (*snapshotDoc, error) {
	day := session.BusinessDate
	rate, ok, err := s.repo.BranchTaxRate(ctx, &branch)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, conflict("branch_rate_missing", "the branch has no tax rate: set it before the register sells offline")
	}
	walkIn, currency, err := s.repo.WalkInCustomer(ctx)
	if err != nil {
		return nil, err
	}
	defaultCheck, err := s.repo.DefaultCheckLimitCents(ctx)
	if err != nil {
		return nil, err
	}
	idOver, err := s.repo.CheckIDRequiredOverCents(ctx)
	if err != nil {
		return nil, err
	}
	// The ID rule as the snapshot carries it: a list of tenders. A rule
	// over a figure is carried as every check, the stricter reading, since
	// the snapshot has no figure to compare with.
	requireID := []string{}
	if idOver != nil {
		requireID = append(requireID, "check")
	}
	rounding, err := s.repo.BranchCashRoundingMode(ctx, branch)
	if err != nil {
		return nil, err
	}
	tenders := append([]string{}, policy.Tenders...)
	doc := &snapshotDoc{OfflineSnapshot: OfflineSnapshot{
		RegisterID: register, BranchID: branch.String(), DeviceEpoch: epoch,
		TillSession: OfflineSnapshotSession{ID: session.ID.String(), CashierID: session.CashierID.String(),
			OpenedAt: session.OpenedAt.UTC().Format(time.RFC3339Nano), BusinessDate: day.Format("2006-01-02"),
			OpeningFloatCents: session.OpeningFloatCents},
		Policy: OfflineSnapshotPolicy{Enabled: policy.Enabled, MaxSaleCents: policy.SaleCapCents,
			MaxCheckCents: policy.CheckCapCents, DefaultCheckLimitCents: defaultCheck, AccountCapCents: policy.AccountCapCents,
			MaxQueueCount: int64(policy.MaxQueued), MaxQueueTotalCents: policy.QueueTotalCapCents, AllowedTenders: tenders,
			RequireIDForTenders: requireID, CashRoundingMode: string(rounding)},
		Tax:            OfflineSnapshotTax{BranchRate: rate, ProviderConfigured: s.tax != nil && s.tax.ProviderConfigured()},
		WalkInCustomer: OfflineSnapshotWalkIn{ID: walkIn.String(), Currency: currency},
		PriceSheet:     []OfflineSnapshotPrice{},
	}, Products: []snapshotProductDoc{}, Accounts: []snapshotAccountDoc{}}

	// The products, their units, the cross section and the on hand.
	products, err := s.repo.SnapshotProducts(ctx, branch)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(products))
	for _, p := range products {
		ids = append(ids, p.ID)
	}
	sets, err := s.repo.LookupProductUnits(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, p := range products {
		set := sets[p.ID]
		doc.Products = append(doc.Products, snapshotProductOf(p, set))
	}

	// The cached accounts: the branch's customers whose verdict is allowed.
	pricer, _ := s.pricer.(OfflinePricer)
	accounts, err := s.repo.SnapshotAccounts(ctx, branch, walkIn)
	if err != nil {
		return nil, err
	}
	levels := map[uuid.UUID]uuid.UUID{} // level id -> a customer holding it
	for _, a := range accounts {
		acct, ok, err := s.snapshotAccount(ctx, register, a, day, policy)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if pricer != nil {
			if level, has, err := pricer.AccountLevel(ctx, a.ID); err != nil {
				return nil, err
			} else if has {
				key := priceKeyLevel(level)
				acct.LevelKey = &key
				if _, seen := levels[level]; !seen {
					levels[level] = a.ID
				}
			}
		}
		doc.Accounts = append(doc.Accounts, acct)
	}

	// The price sheet: the walk-in's rows, each cached account's own rows,
	// and each of their levels' rows, for every product.
	for i := range doc.Products {
		p := &doc.Products[i]
		pid := uuid.MustParse(p.ID)
		row := func(key string, customerID uuid.UUID, levelOf *uuid.UUID) error {
			ans, err := s.sheetAnswer(ctx, pricer, customerID, levelOf, pid, branch, sets[pid], day)
			if err != nil {
				// a product the engine cannot price for this key (a unit set
				// with no pair, say) carries no row: the core refuses its
				// line at the counter, as the live counter refuses it
				s.logger.Warn("offline snapshot: no sheet row", "product_id", pid, "price_key", key, "error", err)
				return nil
			}
			doc.PriceSheet = append(doc.PriceSheet, sheetRowOf(p.ID, key, ans))
			return nil
		}
		if err := row(priceKeyWalkIn, walkIn, nil); err != nil {
			return nil, err
		}
		for _, a := range doc.Accounts {
			cid := uuid.MustParse(a.CustomerID)
			if err := row(a.PriceKey, cid, nil); err != nil {
				return nil, err
			}
		}
		for level, holder := range levels {
			h := holder
			if err := row(priceKeyLevel(level), uuid.Nil, &h); err != nil {
				return nil, err
			}
		}
	}
	sortSheet(doc.PriceSheet)
	return doc, nil
}

// sheetAnswer is one sheet row's engine answer, in the product's sale unit.
func (s *Service) sheetAnswer(ctx context.Context, pricer OfflinePricer, customerID uuid.UUID, levelOf *uuid.UUID,
	productID, branch uuid.UUID, set salesdoc.ProductUnitSet, day time.Time) (salesdoc.EngineAnswer, error) {
	if pricer == nil {
		refs, err := s.repo.LookupProducts(ctx, []uuid.UUID{productID})
		if err != nil {
			return salesdoc.EngineAnswer{}, err
		}
		ref, ok := refs[productID.String()]
		if !ok {
			return salesdoc.EngineAnswer{}, errors.New("no such product")
		}
		return s.enginePrice(ctx, customerID, ref, &set, salesdoc.One, set.SaleUOM)
	}
	return pricer.SheetPrice(ctx, customerID, levelOf, productID, branch, set.SaleUOM, day)
}

// sheetRowOf is a sheet row: the engine's answer, its unit and the pair the
// product's sale unit takes to it, and the rung that won (lowercase) with
// the basis a live line stores.
func sheetRowOf(productID, key string, ans salesdoc.EngineAnswer) OfflineSnapshotPrice {
	basis := ans.Basis
	return OfflineSnapshotPrice{ProductID: productID, PriceKey: key, PriceUOM: ans.PriceUOM,
		UOMQty: ans.UOMQty.WireString(), PriceUOMQty: ans.PriceUOMQty.WireString(),
		UnitPriceTenThousandths: int64(ans.Price), PriceSource: strings.ToLower(ans.Basis), PriceBasis: &basis}
}

func sortSheet(rows []OfflineSnapshotPrice) {
	// products in their own order, then the keys: the walk-in, the accounts,
	// the levels, so the document (and its digest) is stable
	rank := func(k string) string {
		switch {
		case k == priceKeyWalkIn:
			return "0"
		case strings.HasPrefix(k, "customer:"):
			return "1" + k
		}
		return "2" + k
	}
	order := map[string]int{}
	for i, r := range rows {
		if _, ok := order[r.ProductID]; !ok {
			order[r.ProductID] = i
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if order[a.ProductID] != order[b.ProductID] {
			return order[a.ProductID] < order[b.ProductID]
		}
		return rank(a.PriceKey) < rank(b.PriceKey)
	})
}

// snapshotProductOf is one product as the snapshot carries it.
func snapshotProductOf(p snapshotProductRow, set salesdoc.ProductUnitSet) snapshotProductDoc {
	out := snapshotProductDoc{OfflineSnapshotProduct: OfflineSnapshotProduct{ID: p.ID.String(), SKU: p.SKU,
		Description: p.Description, Taxable: p.Taxable, IsKit: p.IsKit, RandomLength: set.RandomLength,
		StockUOM: set.StockUOM, SaleUOM: set.SaleUOM, PriceUOM: set.PriceUOM, UnitSet: []OfflineSnapshotUnitRow{}},
		Barcode: p.Barcode, Category: p.Category, InStock: p.InStock.WireString()}
	if set.BoardThick != nil {
		v := set.BoardThick.WireString()
		out.BoardThicknessIn = &v
	}
	if set.BoardWidth != nil {
		v := set.BoardWidth.WireString()
		out.BoardWidthIn = &v
	}
	for _, r := range set.Rows {
		out.UnitSet = append(out.UnitSet, OfflineSnapshotUnitRow{UOM: r.UOM, UnitQty: r.UnitQty.WireString(),
			StockQty: r.StockQty.WireString(), Sell: r.Sell, Price: r.Price})
	}
	return out
}

// snapshotAccount is one cached account, or false when its verdict at the
// session's day is not allowed. The room at issue (6.2) is the least of the
// credit room and the per account cap, less the register's open account
// exceptions on the account, which a Retry or Accept can still post.
func (s *Service) snapshotAccount(ctx context.Context, register string, a snapshotAccountRow, day time.Time,
	policy *OfflinePolicy) (snapshotAccountDoc, bool, error) {
	facts, err := s.repo.CustomerFacts(ctx, a.ID)
	if err != nil {
		return snapshotAccountDoc{}, false, err
	}
	open, err := s.repo.OpenReceivableCents(ctx, a.ID)
	if err != nil {
		return snapshotAccountDoc{}, false, err
	}
	v := onAccountVerdict(verdictIn{currency: facts.Currency, creditLimit: facts.CreditLimitCents, open: open,
		termsKind: facts.TermsKind, hold: facts.Hold, now: s.now()})
	if v.Verdict != VerdictAllowed {
		return snapshotAccountDoc{}, false, nil
	}
	pending, err := s.repo.OpenAccountExceptionCents(ctx, register, a.ID)
	if err != nil {
		return snapshotAccountDoc{}, false, err
	}
	// a customer with no limit has the cap for its credit room: the limit
	// the snapshot states is the open receivable plus the cap, so the
	// device's and the sync's one rule (the limit less the open receivable)
	// answers the cap
	limit := open + policy.AccountCapCents
	if facts.CreditLimitCents != nil {
		limit = *facts.CreditLimitCents
	}
	room := min64(max64(limit-open, 0), policy.AccountCapCents) - pending
	exemption, err := s.repo.CustomerExemption(ctx, a.ID, day)
	if err != nil {
		return snapshotAccountDoc{}, false, err
	}
	buyers, err := s.repo.Buyers(ctx, a.ID)
	if err != nil {
		return snapshotAccountDoc{}, false, err
	}
	jobs, err := s.repo.SnapshotJobs(ctx, a.ID)
	if err != nil {
		return snapshotAccountDoc{}, false, err
	}
	out := snapshotAccountDoc{OfflineSnapshotAccount: OfflineSnapshotAccount{CustomerID: a.ID.String(),
		AccountNumber: a.AccountNumber, Name: facts.Name, Currency: facts.Currency, TaxExempt: exemption != nil,
		PORequired: facts.PORequired, BuyerRosterRequired: facts.NamedBuyerRequired, CreditLimitCents: limit,
		OpenReceivableCents: open, OfflineRoomCents: max64(room, 0), CheckLimitCents: facts.CheckLimitCents,
		PriceKey: priceKeyAccount(a.ID), Buyers: []OfflineSnapshotBuyer{}}, Jobs: jobs}
	for _, b := range buyers {
		var capCents *int64
		if b.CapCents != nil {
			c := int64(*b.CapCents)
			capCents = &c
		}
		out.Buyers = append(out.Buyers, OfflineSnapshotBuyer{ContactID: b.ID.String(), Name: b.Name, PerPurchaseCapCents: capCents})
	}
	return out, true, nil
}

// GetOfflineSnapshot is GET /pos/offline/snapshot?register_id=&high_seq=:
// the register's snapshot, the person's act of the shell's own sign in,
// never a machine key. If-None-Match with the current ETag answers 304.
func (h *Handler) GetOfflineSnapshot(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "an offline snapshot is taken by a person at the register, never a machine key") {
		return
	}
	q, err := httpx.StrictQuery(r, "register_id", "high_seq")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	register := strings.TrimSpace(q.Get("register_id"))
	high, perr := strconv.ParseInt(q.Get("high_seq"), 10, 64)
	v := &httpx.Validator{}
	v.Required("register_id", register)
	v.Check(perr == nil && high >= 0, "high_seq", "is required: the highest sequence the register has assigned, zero or more")
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	snap, err := h.service.IssueSnapshot(r.Context(), register, high)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", snap.ETag())
	w.Header().Set("Cache-Control", "no-store")
	if match := strings.TrimSpace(r.Header.Get("If-None-Match")); match != "" && match == snap.ETag() {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snap.Content)
}

// ---- repository ----

// SnapshotProducts reads every product with its on hand at the branch (the
// counter catalogue's read, held to the register's branch rather than the
// caller's).
func (r *PostgresRepository) SnapshotProducts(ctx context.Context, branch uuid.UUID) ([]snapshotProductRow, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT p.id, COALESCE(p.sku, ''), COALESCE(p.description, ''), COALESCE(p.category, ''), NULLIF(btrim(p.upc), ''),
			COALESCE(p.taxable, TRUE), COALESCE(p.is_kit, FALSE),
			COALESCE((SELECT ROUND(SUM(i.quantity - i.allocated) * 10000)::bigint FROM inventory i
				JOIN locations l ON l.id = i.location_id WHERE i.product_id = p.id AND l.branch_id = $1), 0)
		FROM products p ORDER BY p.sku, p.id`, branch)
	if err != nil {
		return nil, fmt.Errorf("failed to read the snapshot's products: %w", err)
	}
	defer rows.Close()
	out := []snapshotProductRow{}
	for rows.Next() {
		var p snapshotProductRow
		var stock int64
		if err := rows.Scan(&p.ID, &p.SKU, &p.Description, &p.Category, &p.Barcode, &p.Taxable, &p.IsKit, &stock); err != nil {
			return nil, err
		}
		p.InStock = httpx.Quantity(stock)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SnapshotAccounts reads the branch's active customers (the customer wall
// at the register's branch), the walk-in excepted.
func (r *PostgresRepository) SnapshotAccounts(ctx context.Context, branch, walkIn uuid.UUID) ([]snapshotAccountRow, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT c.id, COALESCE(c.account_number, '') FROM customers c
		WHERE COALESCE(c.is_active, TRUE) AND c.id <> $3 AND `+customer.Wall("c", 1, 2)+`
		ORDER BY c.account_number, c.id`, branch, nil, walkIn)
	if err != nil {
		return nil, fmt.Errorf("failed to read the snapshot's accounts: %w", err)
	}
	defer rows.Close()
	out := []snapshotAccountRow{}
	for rows.Next() {
		var a snapshotAccountRow
		if err := rows.Scan(&a.ID, &a.AccountNumber); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SnapshotJobs reads the account's active jobs.
func (r *PostgresRepository) SnapshotJobs(ctx context.Context, customerID uuid.UUID) ([]snapshotJob, error) {
	jobs, err := r.customerJobs(ctx, customerID)
	if err != nil {
		return nil, err
	}
	out := make([]snapshotJob, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, snapshotJob{ID: j.ID.String(), Code: "", Name: j.Name})
	}
	return out, nil
}

// OpenAccountExceptionCents is the account money of the register's open
// exceptions on the customer: refused, not yet resolved, in no open
// receivable, and a Retry or Accept can still post them (ADR 0011 6.2).
func (r *PostgresRepository) OpenAccountExceptionCents(ctx context.Context, register string, customerID uuid.UUID) (int64, error) {
	var cents int64
	err := r.ex(ctx).QueryRow(ctx, `SELECT COALESCE(SUM(e.account_cents), 0)::bigint FROM pos_offline_exceptions e
		WHERE e.register_id = $1 AND e.customer_id = $2 AND e.status IN ('open', 'held')
		AND e.reason_code NOT IN ('bad_signature', 'replay_mismatch', 'sequence_gap', 'unknown_tail', 'sold_past_warning')
		AND NOT EXISTS (SELECT 1 FROM pos_transactions p WHERE p.id = e.sale_id)`, register, customerID).Scan(&cents)
	if err != nil {
		return 0, fmt.Errorf("failed to read the account's open offline exceptions: %w", err)
	}
	return cents, nil
}

// LatestOfflineSnapshot is the latest snapshot issued to the register's
// epoch.
func (r *PostgresRepository) LatestOfflineSnapshot(ctx context.Context, register string, epoch int64) (*IssuedSnapshot, error) {
	s := &IssuedSnapshot{}
	var content []byte
	err := r.ex(ctx).QueryRow(ctx, `SELECT id, digest, content FROM pos_offline_snapshots
		WHERE register_id = $1 AND device_epoch = $2 ORDER BY created_at DESC, id DESC LIMIT 1`, register, epoch).
		Scan(&s.ID, &s.Digest, &content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the register's latest snapshot: %w", err)
	}
	s.Content = content
	return s, nil
}

// InsertOfflineSnapshot stores an issued snapshot.
func (r *PostgresRepository) InsertOfflineSnapshot(ctx context.Context, id uuid.UUID, register string, branch uuid.UUID,
	epoch int64, businessDate time.Time, content []byte, digest string) error {
	if _, err := r.ex(ctx).Exec(ctx, `INSERT INTO pos_offline_snapshots (id, register_id, branch_id, device_epoch,
		business_date, content, digest, etag) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)`,
		id, register, branch, epoch, businessDate, string(content), digest, `"`+digest+`"`); err != nil {
		return fmt.Errorf("failed to store the snapshot: %w", mapWriteError(err))
	}
	return nil
}
