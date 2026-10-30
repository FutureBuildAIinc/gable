// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/units"
	"github.com/google/uuid"
)

// OfflineSnapshot is the content of a pos_offline_snapshots row: what a
// register was given to sell on offline (ADR 0011 section 5.4; the twin of
// OfflineSnapshot in tauri/core/src/offline/model.rs). Slice O2's route
// issues it; the sync verifies every signed sale against the one it names.
type OfflineSnapshot struct {
	SnapshotID     string                   `json:"snapshot_id"`
	RegisterID     string                   `json:"register_id"`
	BranchID       string                   `json:"branch_id"`
	DeviceEpoch    int64                    `json:"device_epoch"`
	IssuedAt       string                   `json:"issued_at"`
	TillSession    OfflineSnapshotSession   `json:"till_session"`
	Policy         OfflineSnapshotPolicy    `json:"policy"`
	Tax            OfflineSnapshotTax       `json:"tax"`
	WalkInCustomer OfflineSnapshotWalkIn    `json:"walk_in_customer"`
	Products       []OfflineSnapshotProduct `json:"products"`
	PriceSheet     []OfflineSnapshotPrice   `json:"price_sheet"`
	Accounts       []OfflineSnapshotAccount `json:"accounts"`
}

// OfflineSnapshotSession is the till session the snapshot was issued for.
type OfflineSnapshotSession struct {
	ID                string `json:"id"`
	CashierID         string `json:"cashier_id"`
	OpenedAt          string `json:"opened_at"`
	BusinessDate      string `json:"business_date"`
	OpeningFloatCents int64  `json:"opening_float_cents"`
}

// OfflineSnapshotPolicy is the branch's offline policy as the snapshot
// carried it, with the check limit default and the ID rule.
type OfflineSnapshotPolicy struct {
	Enabled                bool     `json:"enabled"`
	MaxSaleCents           int64    `json:"max_sale_cents"`
	MaxCheckCents          int64    `json:"max_check_cents"`
	DefaultCheckLimitCents *int64   `json:"default_check_limit_cents"`
	AccountCapCents        int64    `json:"account_cap_cents"`
	MaxQueueCount          int64    `json:"max_queue_count"`
	MaxQueueTotalCents     int64    `json:"max_queue_total_cents"`
	AllowedTenders         []string `json:"allowed_tenders"`
	RequireIDForTenders    []string `json:"require_id_for_tenders"`
	CashRoundingMode       string   `json:"cash_rounding_mode"`
}

// OfflineSnapshotTax is the branch rate the device taxes with.
type OfflineSnapshotTax struct {
	BranchRate         string `json:"branch_rate"`
	ProviderConfigured bool   `json:"provider_configured"`
}

// OfflineSnapshotWalkIn is the walk-in customer.
type OfflineSnapshotWalkIn struct {
	ID       string `json:"id"`
	Currency string `json:"currency"`
}

// OfflineSnapshotProduct is one product the branch sells offline.
type OfflineSnapshotProduct struct {
	ID               string                   `json:"id"`
	SKU              string                   `json:"sku"`
	Description      string                   `json:"description"`
	Taxable          bool                     `json:"taxable"`
	IsKit            bool                     `json:"is_kit"`
	RandomLength     bool                     `json:"random_length"`
	BoardThicknessIn *string                  `json:"board_thickness_in"`
	BoardWidthIn     *string                  `json:"board_width_in"`
	StockUOM         string                   `json:"stock_uom"`
	SaleUOM          string                   `json:"sale_uom"`
	PriceUOM         string                   `json:"price_uom"`
	UnitSet          []OfflineSnapshotUnitRow `json:"unit_set"`
}

// OfflineSnapshotUnitRow is one row of a product's unit set: the exact pair
// the unit takes to the stocking unit.
type OfflineSnapshotUnitRow struct {
	UOM      string `json:"uom"`
	UnitQty  string `json:"unit_qty"`
	StockQty string `json:"stock_qty"`
	Sell     bool   `json:"sell"`
	Price    bool   `json:"price"`
}

// OfflineSnapshotPrice is one row of the price sheet, keyed by price key.
type OfflineSnapshotPrice struct {
	ProductID               string  `json:"product_id"`
	PriceKey                string  `json:"price_key"`
	PriceUOM                string  `json:"price_uom"`
	UOMQty                  string  `json:"uom_qty"`
	PriceUOMQty             string  `json:"price_uom_qty"`
	UnitPriceTenThousandths int64   `json:"unit_price_ten_thousandths"`
	PriceSource             string  `json:"price_source"`
	PriceBasis              *string `json:"price_basis"`
}

// OfflineSnapshotAccount is one cached account (verdict allowed at issue).
type OfflineSnapshotAccount struct {
	CustomerID          string                 `json:"customer_id"`
	AccountNumber       string                 `json:"account_number"`
	Name                string                 `json:"name"`
	Currency            string                 `json:"currency"`
	TaxExempt           bool                   `json:"tax_exempt"`
	PORequired          bool                   `json:"po_required"`
	BuyerRosterRequired bool                   `json:"buyer_roster_required"`
	CreditHold          bool                   `json:"credit_hold"`
	CreditLimitCents    int64                  `json:"credit_limit_cents"`
	OpenReceivableCents int64                  `json:"open_receivable_cents"`
	OfflineRoomCents    int64                  `json:"offline_room_cents"`
	CheckLimitCents     *int64                 `json:"check_limit_cents"`
	PriceKey            string                 `json:"price_key"`
	LevelKey            *string                `json:"level_key"`
	Buyers              []OfflineSnapshotBuyer `json:"buyers"`
}

// OfflineSnapshotBuyer is one buyer on an account's roster.
type OfflineSnapshotBuyer struct {
	ContactID           string `json:"contact_id"`
	Name                string `json:"name"`
	PerPurchaseCapCents *int64 `json:"per_purchase_cap_cents"`
}

// offlineIDTypes are the kinds of ID an offline check or account tender may
// name when the snapshot's ID rule asks for one (the counter's own list).
var offlineIDTypes = idTypes

// verifyOfflineSale is the snapshot verification of ADR 0011 sections 3.1,
// 5.4, 6.2 and 6.3: every check that judges whether the clerk MAY make the
// sale, against what the register was given. A failure is an
// *offlineRefusal; the verdict carries what the completion's body books.
func (s *Service) verifyOfflineSale(ctx context.Context, branch uuid.UUID, dev *registerDevice, it *offlineItem) (*offlineVerdict, error) {
	sale := &it.Sale
	if dev.RevokedAt != nil {
		return nil, refuse("device_revoked", "the register's device was revoked: its later sales go to a manager")
	}
	// every id the posting writes is a UUID
	for _, f := range []struct{ name, v string }{{"till_session_id", sale.TillSessionID}, {"cashier_id", sale.CashierID},
		{"snapshot_id", sale.SnapshotID}} {
		if _, err := uuid.Parse(f.v); err != nil {
			return nil, refuse("out_of_wall", "the sale's %s is not an id the server knows", f.name)
		}
	}
	for _, p := range []*string{sale.CustomerID, sale.ReplacesSaleID, sale.UnknownCartID} {
		if p != nil {
			if _, err := uuid.Parse(*p); err != nil {
				return nil, refuse("out_of_wall", "the sale names an id the server does not know")
			}
		}
	}
	if len(sale.Lines) == 0 {
		return nil, refuse("price_unverified", "the sale has no lines")
	}
	for _, l := range sale.Lines {
		if _, err := uuid.Parse(l.ProductID); err != nil {
			return nil, refuse("out_of_wall", "a line names a product the server does not know")
		}
	}

	// The snapshot, issued to this register and epoch.
	snapID := uuid.MustParse(sale.SnapshotID)
	snapRow, err := s.repo.OfflineSnapshotByID(ctx, snapID)
	if err != nil {
		return nil, err
	}
	if snapRow == nil || snapRow.RegisterID != sale.RegisterID || snapRow.DeviceEpoch != sale.DeviceEpoch {
		return nil, refuse("snapshot_unknown", "the sale names a snapshot this register was not issued under its epoch")
	}
	var snap OfflineSnapshot
	if err := json.Unmarshal(snapRow.Content, &snap); err != nil {
		return nil, refuse("snapshot_unknown", "the snapshot the sale names cannot be read")
	}

	// The session (section 6.3): the register's own, the sale's cashier its
	// cashier. Its being open is the body's live check.
	sessionID := uuid.MustParse(sale.TillSessionID)
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
	// A snapshot older than the session never sells (5.4).
	if snapRow.CreatedAt.Before(session.OpenedAt) || (snap.TillSession.ID != "" && snap.TillSession.ID != sale.TillSessionID) {
		return nil, refuse("snapshot_stale", "the sale names a snapshot issued before its till session opened")
	}
	if sale.BusinessDate != session.BusinessDate.Format("2006-01-02") {
		return nil, refuse("snapshot_stale", "the sale's business date is not its till session's")
	}
	// The device's clock lies between the session's open and the sync (3.3).
	soldAt, terr := time.Parse(time.RFC3339Nano, sale.SoldAt)
	if terr != nil || soldAt.Before(session.OpenedAt) || soldAt.After(s.now().Add(time.Minute)) {
		return nil, refuse("clock_unverified", "the sale's time does not lie between its session's open and the sync")
	}
	if closed, err := s.repo.BranchDayClosedOn(ctx, branch, session.BusinessDate); err != nil {
		return nil, err
	} else if closed {
		return nil, refuse("day_closed", "the branch day the sale was made in is closed")
	}
	if !snap.Policy.Enabled {
		return nil, refuse("tender_not_offline", "the snapshot the sale names did not allow offline selling")
	}

	// The tenders allowed offline, and the legs (section 1, 3.1).
	accountLegs := 0
	for _, t := range sale.Tenders {
		m := strings.ToLower(strings.TrimSpace(t.Method))
		if (m != "cash" && m != "check" && m != "account") || !containsFold(snap.Policy.AllowedTenders, m) {
			return nil, refuse("tender_not_offline", "a %s tender is not taken offline at this branch", m)
		}
		if t.AmountCents <= 0 {
			return nil, refuse("tender_mismatch", "a tender carries no amount")
		}
		if m == "account" {
			accountLegs++
		}
	}
	if accountLegs > 1 {
		return nil, refuse("one_account_leg", "a sale takes at most one account tender: put the whole account amount on one")
	}

	// The customer: the walk-in, or a cached account of the snapshot.
	var acct *OfflineSnapshotAccount
	if sale.CustomerID != nil {
		for i := range snap.Accounts {
			if snap.Accounts[i].CustomerID == *sale.CustomerID {
				acct = &snap.Accounts[i]
				break
			}
		}
		if acct == nil {
			return nil, refuse("price_unverified", "the sale's customer is not a cached account of its snapshot")
		}
	}

	// Each line against the sheet row its customer binds it to (6.2, 5.4).
	products := map[string]*OfflineSnapshotProduct{}
	for i := range snap.Products {
		products[snap.Products[i].ID] = &snap.Products[i]
	}
	var subtotal, taxable int64
	for i := range sale.Lines {
		if r := verifyOfflineLine(&snap, products, acct, &sale.Lines[i], i); r != nil {
			return nil, r
		}
		subtotal += sale.Lines[i].LineTotalCents
		if sale.Lines[i].Taxable {
			taxable += sale.Lines[i].LineTotalCents
		}
	}
	if subtotal != sale.SubtotalCents {
		return nil, refuse("price_unverified", "the sale's subtotal is not the sum of its lines")
	}

	// Tax as at the sale (3.3): the snapshot's rate or the account's
	// exemption, and the arithmetic.
	exempt := acct != nil && acct.TaxExempt
	if sale.TaxExempt != exempt {
		return nil, refuse("tax_unverified", "the sale's exemption is not its customer's in the snapshot")
	}
	rateText := snap.Tax.BranchRate
	if exempt {
		rateText = "0.000000"
	}
	wantRate, _, rerr := salesdoc.ParseTaxRate(rateText)
	gotRate, ok, gerr := salesdoc.ParseTaxRate(sale.TaxRate)
	if rerr != nil || gerr != nil || !ok || gotRate != wantRate {
		return nil, refuse("tax_unverified", "the sale's tax rate is not its snapshot's")
	}
	wantTax := int64(0)
	if !exempt {
		wantTax = int64(salesdoc.TaxAt(httpx.Cents(taxable), wantRate))
	}
	if sale.TaxCents != wantTax {
		return nil, refuse("tax_unverified", "the sale's tax is %d cents and its taxable lines at the recorded rate make %d", sale.TaxCents, wantTax)
	}
	if sale.TotalCents != sale.SubtotalCents+sale.TaxCents {
		return nil, refuse("tender_mismatch", "the sale's total is not its subtotal and tax")
	}

	// The tender sums and the change, under the branch's cash rounding as
	// the snapshot carried it (the shared arithmetic).
	calc := make([]salesdoc.OfflineTender, 0, len(sale.Tenders))
	for _, t := range sale.Tenders {
		calc = append(calc, salesdoc.OfflineTender{Method: t.Method, AmountCents: httpx.Cents(t.AmountCents)})
	}
	mode := ParseRoundingMode(snap.Policy.CashRoundingMode)
	settle, serr := salesdoc.SettleOfflineTenders(httpx.Cents(sale.TotalCents), calc,
		func(c int64) (int64, int64) { return RoundCashFor(mode, c) })
	if serr != nil {
		return nil, refuse("tender_mismatch", "%s", serr.Error())
	}
	if int64(settle.ChangeCents) != sale.ChangeCents {
		return nil, refuse("tender_mismatch", "the sale's change is %d cents and its tenders make %d", sale.ChangeCents, settle.ChangeCents)
	}

	// The caps, the check limit and the ID rule (section 1, 6.2).
	if sale.TotalCents > snap.Policy.MaxSaleCents {
		return nil, refuse("cap_exceeded", "the sale is over the branch's offline sale cap")
	}
	for _, t := range sale.Tenders {
		m := strings.ToLower(strings.TrimSpace(t.Method))
		if m == "check" {
			if t.AmountCents > snap.Policy.MaxCheckCents {
				return nil, refuse("cap_exceeded", "a check is over the branch's offline check cap")
			}
			limit := snap.Policy.DefaultCheckLimitCents
			if acct != nil && acct.CheckLimitCents != nil {
				limit = acct.CheckLimitCents
			}
			if limit != nil && t.AmountCents > *limit {
				return nil, refuse("cap_exceeded", "a check is over the customer's check limit as the snapshot knew it")
			}
		}
		if containsFold(snap.Policy.RequireIDForTenders, m) &&
			(!t.IDChecked || t.IDType == nil || !slices.Contains(offlineIDTypes, *t.IDType)) {
			return nil, refuse("id_unverified", "a %s tender needs the ID the snapshot's rule asks for", m)
		}
	}
	if settle.AccountCents > 0 {
		if acct == nil {
			return nil, refuse("walk_in_account", "an ACCOUNT tender needs a named customer: the walk-in customer buys on the barrelhead")
		}
		if acct.CreditHold {
			return nil, refuse("account_unverified", "the account was on hold in the snapshot")
		}
		if acct.PORequired && strings.TrimSpace(derefString(sale.CustomerPO)) == "" {
			return nil, refuse("account_unverified", "the account required a purchase order in the snapshot and the sale carries none")
		}
		if acct.BuyerRosterRequired && sale.BuyerContactID == nil {
			return nil, refuse("account_unverified", "the account required a buyer from its roster in the snapshot")
		}
		if sale.BuyerContactID != nil {
			var buyer *OfflineSnapshotBuyer
			for i := range acct.Buyers {
				if acct.Buyers[i].ContactID == *sale.BuyerContactID {
					buyer = &acct.Buyers[i]
				}
			}
			if buyer == nil {
				return nil, refuse("account_unverified", "the sale's buyer is not on the account's roster in the snapshot")
			}
			if buyer.PerPurchaseCapCents != nil && int64(settle.AccountCents) > *buyer.PerPurchaseCapCents {
				return nil, refuse("cap_exceeded", "the account tender is over the buyer's cap in the snapshot")
			}
		}
		// The room across batches and snapshots (6.2): the snapshot's room
		// at issue less every account tender of this register on this
		// account the server recorded after the snapshot was issued.
		room := min64(acct.OfflineRoomCents, max64(acct.CreditLimitCents-acct.OpenReceivableCents, 0))
		room = max64(min64(room, snap.Policy.AccountCapCents), 0)
		used, err := s.repo.OfflineAccountUsedSince(ctx, sale.RegisterID, uuid.MustParse(acct.CustomerID), snapRow.CreatedAt,
			uuid.MustParse(sale.ClientID))
		if err != nil {
			return nil, err
		}
		if int64(settle.AccountCents) > room-used {
			return nil, refuse("cap_exceeded", "the account tender is over the account's offline room on this register")
		}
	}

	// The queue figures the sale carries (6.2): the confirmed mark is one
	// the server gave, and the count covers every sale not yet answered.
	if sale.ConfirmedMark > dev.ConfirmedSeq || sale.ConfirmedMark < 0 {
		return nil, refuse("queue_mark_unverified", "the sale carries a confirmed mark the server never gave the register")
	}
	if sale.QueueCount < sale.DeviceSeq-sale.ConfirmedMark {
		return nil, refuse("queue_mark_unverified", "the sale's queue count is below the sales not yet answered when it was made")
	}
	if sale.QueueCount > snap.Policy.MaxQueueCount || sale.QueueTotalCents > snap.Policy.MaxQueueTotalCents {
		return nil, refuse("cap_exceeded", "the register's queue was over the branch's offline caps when the sale was made")
	}

	v := &offlineVerdict{snapshotID: snapID, taxCents: sale.TaxCents, taxRate: sale.TaxRate, exempt: exempt,
		businessDate: session.BusinessDate, item: it}
	return v, nil
}

// verifyOfflineLine checks one recorded line against the snapshot: the
// product and its kit flag, the price key bound to the customer and its
// row, the unit pair and the extension, the tally, the taxable flag and the
// stock figures.
func verifyOfflineLine(snap *OfflineSnapshot, products map[string]*OfflineSnapshotProduct, acct *OfflineSnapshotAccount,
	l *SignedSaleLine, i int) *offlineRefusal {
	p, ok := products[l.ProductID]
	if !ok {
		return refuse("price_unverified", "line %d names a product its snapshot does not carry", i)
	}
	if p.IsKit {
		return refuse("price_unverified", "line %d is a kit, and kits do not sell offline", i)
	}
	// the key is the customer's (5.4): the account's own row, else its
	// level's, else nothing; the walk-in's is walkin
	var row *OfflineSnapshotPrice
	fallback := false
	find := func(key string) *OfflineSnapshotPrice {
		for k := range snap.PriceSheet {
			if snap.PriceSheet[k].ProductID == p.ID && snap.PriceSheet[k].PriceKey == key {
				return &snap.PriceSheet[k]
			}
		}
		return nil
	}
	if acct == nil {
		row = find("walkin")
	} else {
		row = find(acct.PriceKey)
		if row == nil && acct.LevelKey != nil {
			row, fallback = find(*acct.LevelKey), true
		}
	}
	if row == nil || l.PriceKey != row.PriceKey || l.LevelFallback != fallback {
		return refuse("price_unverified", "line %d is not priced under its customer's own price key", i)
	}
	if l.PriceUOM != row.PriceUOM || l.UnitPriceTenThousandths != row.UnitPriceTenThousandths ||
		l.PriceSource != row.PriceSource || derefString(l.PriceBasis) != derefString(row.PriceBasis) {
		return refuse("price_unverified", "line %d's price is not its snapshot row's", i)
	}
	var uRow *OfflineSnapshotUnitRow
	for k := range p.UnitSet {
		if p.UnitSet[k].UOM == l.UOM && p.UnitSet[k].Sell {
			uRow = &p.UnitSet[k]
		}
	}
	if uRow == nil {
		return refuse("price_unverified", "line %d is sold in a unit its product does not sell in", i)
	}
	uPair, err1 := pairOf(uRow.UnitQty, uRow.StockQty)
	if err1 != nil {
		return refuse("price_unverified", "line %d's unit row cannot be read", i)
	}
	// the pair the engine builds for the row's price unit (ADR 0006 3.3)
	var want units.Pair
	switch {
	case l.UOM == row.PriceUOM:
		want = units.One()
	case l.UOM == p.SaleUOM:
		pr, err := pairOf(row.UOMQty, row.PriceUOMQty)
		if err != nil {
			return refuse("price_unverified", "line %d's price row pair cannot be read", i)
		}
		want = pr
	default:
		var pRow *OfflineSnapshotUnitRow
		for k := range p.UnitSet {
			if p.UnitSet[k].UOM == row.PriceUOM && p.UnitSet[k].Price {
				pRow = &p.UnitSet[k]
			}
		}
		if pRow == nil {
			return refuse("price_unverified", "line %d's price unit is not a price unit of its product", i)
		}
		pPair, err := pairOf(pRow.UnitQty, pRow.StockQty)
		if err != nil {
			return refuse("price_unverified", "line %d's price unit row cannot be read", i)
		}
		if want, err = units.ResolveLinePair(uPair, pPair); err != nil {
			return refuse("price_unverified", "line %d's unit has no exact pair to its price unit", i)
		}
	}
	uq, e1 := httpx.ParseQuantity(l.UOMQty)
	pq, e2 := httpx.ParseQuantity(l.PriceUOMQty)
	q, e3 := httpx.ParseQuantity(l.Quantity)
	if e1 != nil || e2 != nil || e3 != nil || q <= 0 || uq != want.A || pq != want.B {
		return refuse("price_unverified", "line %d's unit pair is not the one its snapshot gives", i)
	}
	// the tally the core completed (5.4): the rows, the cross section, the
	// linear feet the quantity is
	if l.Tally != nil {
		if !p.RandomLength || l.UOM != "LF" {
			return refuse("price_unverified", "line %d carries a tally its product does not take", i)
		}
		t := &salesdoc.Tally{}
		if p.BoardThicknessIn != nil {
			th := mustQty(*p.BoardThicknessIn)
			t.ThicknessIn = &th
		}
		if p.BoardWidthIn != nil {
			w := mustQty(*p.BoardWidthIn)
			t.WidthIn = &w
		}
		for _, r := range l.Tally.Rows {
			lf, err := httpx.ParseQuantity(r.LengthFT)
			if err != nil {
				return refuse("price_unverified", "line %d's tally row cannot be read", i)
			}
			t.Rows = append(t.Rows, salesdoc.TallyRow{Pieces: r.Pieces, LengthFT: lf})
		}
		probe := []salesdoc.Line{{LineType: salesdoc.LineProduct, Tally: t}}
		salesdoc.CompleteTallies(probe)
		got := probe[0].Tally
		recLF, _ := httpx.ParseQuantity(l.Tally.LinearFeet)
		if got == nil || got.LinearFeet != q || recLF != q ||
			(got.BoardFeet == nil) != (l.Tally.BoardFeet == nil) ||
			(got.BoardFeet != nil && *got.BoardFeet != mustQty(*l.Tally.BoardFeet)) {
			return refuse("price_unverified", "line %d's tally is not the one its rows and cross section make", i)
		}
	}
	price := httpx.Price(l.UnitPriceTenThousandths)
	probe := salesdoc.Line{LineType: salesdoc.LineProduct, Quantity: &q, UOM: &l.UOM, PriceUOM: &l.PriceUOM,
		UOMQty: &uq, PriceUOMQty: &pq, UnitPrice: &price}
	if err := salesdoc.ExtendLine(&probe); err != nil || probe.LineTotal == nil || int64(*probe.LineTotal) != l.LineTotalCents {
		return refuse("price_unverified", "line %d's extension is not its quantity at its price", i)
	}
	if l.Taxable != p.Taxable {
		return refuse("tax_flag_unverified", "line %d's taxable flag is not its product's in the snapshot", i)
	}
	// the stock figures (5.4): the snapshot's stocking unit and the exact
	// conversion of the quantity sold
	sq, err := httpx.ParseQuantity(l.StockQuantity)
	if err != nil || l.StockUOM != p.StockUOM {
		return refuse("stock_unverified", "line %d's stock figures are not its snapshot's", i)
	}
	conv, err := units.ConvertStock(q, l.UOM, p.StockUOM, uPair)
	if err != nil || conv != sq {
		return refuse("stock_unverified", "line %d's stock quantity is not the exact conversion of its quantity", i)
	}
	return nil
}

func pairOf(a, b string) (units.Pair, error) {
	qa, err := httpx.ParseQuantity(a)
	if err != nil {
		return units.Pair{}, err
	}
	qb, err := httpx.ParseQuantity(b)
	if err != nil {
		return units.Pair{}, err
	}
	return units.Pair{A: qa, B: qb}, nil
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), v) {
			return true
		}
	}
	return false
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
