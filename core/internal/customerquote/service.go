// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Defaults for the configurable limits, read from system_settings.
const (
	settingLinkMaxDays      = "customer_quote.link_max_days"
	settingPriceProtectDays = "customer_quote.price_protection_days"
	settingDefaultValidDays = "customer_quote.default_valid_days"
	defaultLinkMaxDays      = 60
	defaultPriceProtectDays = 30
	defaultDefaultValidDays = 14
	maxLogoBytes            = 512 * 1024
)

// Service is the contractor's customer quoting and the selections loop.
type Service struct {
	db     TxRunner
	store  *Store
	costs  CostSource
	orders OrderPlacer
	mail   Notifier
	pdf    PDFRenderer
	caps   Capabilities
	log    *slog.Logger
	now    func() time.Time

	publicURL string

	// The dealer's own quote link (PE-05); both are optional.
	dealerQuotes DealerQuotes
	audit        AuditSink

	// Rate limits of the public routes: per link and per source address, with
	// a tighter budget for addresses that keep presenting unknown tokens.
	perLink *Limiter
	perAddr *Limiter
	misses  *Limiter
}

// NewService builds the service. The collaborators with a safe absence
// (orders, mail, pdf) may be set after construction.
func NewService(db TxRunner, store *Store, costs CostSource, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		db: db, store: store, costs: costs, caps: RoleCapabilities{}, log: logger, now: time.Now,
		perLink: NewLimiter(60, time.Minute),
		perAddr: NewLimiter(120, time.Minute),
		misses:  NewLimiter(20, time.Minute),
	}
}

// WithOrders wires the order path a confirmation places its order through.
func (s *Service) WithOrders(o OrderPlacer) *Service { s.orders = o; return s }

// WithNotifier wires the sender of the link and the code.
func (s *Service) WithNotifier(n Notifier) *Service { s.mail = n; return s }

// WithPDF wires the customer PDF renderer.
func (s *Service) WithPDF(r PDFRenderer) *Service { s.pdf = r; return s }

// WithCapabilities replaces the role to capability rule.
func (s *Service) WithCapabilities(c Capabilities) *Service { s.caps = c; return s }

// WithPublicURL sets the absolute base of the links the service emails (the
// deployment's public URL); empty leaves the link a path.
func (s *Service) WithPublicURL(u string) *Service { s.publicURL = strings.TrimRight(u, "/"); return s }

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service { s.now = now; return s }

// WithLimits replaces the public route budgets per minute (tests and tuning).
func (s *Service) WithLimits(perLink, perAddr, misses int) *Service {
	s.perLink, s.perAddr, s.misses = NewLimiter(perLink, time.Minute), NewLimiter(perAddr, time.Minute), NewLimiter(misses, time.Minute)
	return s
}

// config reads the configurable limits.
type config struct {
	linkMaxTTL      time.Duration
	priceProtection time.Duration
	defaultValidFor time.Duration
}

func (s *Service) config(ctx context.Context) config {
	days := func(key string, def int) time.Duration {
		if v, err := s.store.Setting(ctx, key); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 && n <= 3650 {
				return time.Duration(n) * 24 * time.Hour
			}
		}
		return time.Duration(def) * 24 * time.Hour
	}
	return config{
		linkMaxTTL:      days(settingLinkMaxDays, defaultLinkMaxDays),
		priceProtection: days(settingPriceProtectDays, defaultPriceProtectDays),
		defaultValidFor: days(settingDefaultValidDays, defaultDefaultValidDays),
	}
}

func (s *Service) need(a Actor, c Capability) error {
	if a.CustomerID == uuid.Nil || !s.caps.Has(a, c) {
		return ErrForbidden
	}
	return nil
}

// needQuoting is the gate every contractor route passes: quoting a customer
// means working in cost, markup and margin, so a role needs both capabilities.
func (s *Service) needQuoting(a Actor) error {
	if err := s.need(a, CapQuoteCustomers); err != nil {
		return err
	}
	return s.need(a, CapSeeMoney)
}

// codesAvailable says whether the server can really send a verification code
// on some channel. While its only email sender writes message bodies to a log
// and its text sender is the stub, it cannot.
func (s *Service) codesAvailable() bool {
	return s.mail != nil && s.mail.Channels().Any()
}

func badRef() error {
	return httpx.BadRequest("a record the quote names does not exist",
		httpx.FieldError{Message: "a product, category or project the quote names was not found"})
}

// mapFK turns a foreign key violation (a product or category that is not
// there) into a 400.
func mapFK(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		return badRef()
	}
	return err
}

// --- the contractor's settings ---

// GetSettings reads the contractor's defaults.
func (s *Service) GetSettings(ctx context.Context, a Actor) (*Settings, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	return s.store.GetSettings(ctx, a.CustomerID)
}

// SettingsInput is the body of a settings save.
type SettingsInput struct {
	DisplayName  string `json:"display_name"`
	PaymentTerms string `json:"payment_terms"`
	DepositBPS   int64  `json:"deposit_bps"`
	Tax          Tax    `json:"tax"`
	Markup       Markup `json:"markup"`
	RequireCode  bool   `json:"require_code"`
}

// PutSettings saves the contractor's defaults.
func (s *Service) PutSettings(ctx context.Context, a Actor, in SettingsInput) (*Settings, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	v := &httpx.Validator{}
	in.DisplayName, in.PaymentTerms = strings.TrimSpace(in.DisplayName), strings.TrimSpace(in.PaymentTerms)
	v.Check(runes(in.DisplayName) <= 200, "display_name", "is at most 200 characters")
	v.Check(runes(in.PaymentTerms) <= 1000, "payment_terms", "is at most 1000 characters")
	v.Check(in.DepositBPS >= 0 && in.DepositBPS <= BPSWhole, "deposit_bps", "must be between 0 and 10000")
	switch in.Tax.Mode {
	case TaxSiteRate:
		v.Check(in.Tax.RateBPS >= 0 && in.Tax.RateBPS <= MaxTaxRateBPS, "tax.rate_bps", fmt.Sprintf("must be between 0 and %d", MaxTaxRateBPS))
	case TaxIncluded, TaxAtCheckout:
		v.Check(in.Tax.RateBPS == 0, "tax.rate_bps", "must be 0 unless the mode is site_rate")
	default:
		v.Check(false, "tax.mode", "must be one of: site_rate, included, at_checkout")
	}
	if err := in.Markup.Validate(); err != nil {
		v.Check(false, "markup", err.Error())
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	if in.RequireCode && !s.codesAvailable() {
		return nil, ErrCodesUnavailable
	}
	st := &Settings{CustomerID: a.CustomerID, DisplayName: in.DisplayName, PaymentTerms: in.PaymentTerms,
		DepositBPS: in.DepositBPS, Tax: in.Tax, Markup: in.Markup, RequireCode: in.RequireCode}
	if err := s.store.PutSettings(ctx, st); err != nil {
		return nil, err
	}
	return s.store.GetSettings(ctx, a.CustomerID)
}

// PutLogo stores the contractor's mark; a nil logo clears it. The caller has
// validated the image.
func (s *Service) PutLogo(ctx context.Context, a Actor, l *Logo) error {
	if err := s.needQuoting(a); err != nil {
		return err
	}
	return s.store.SetLogo(ctx, a.CustomerID, l)
}

// --- building a quote ---

// build prices a validated draft into a quote. existing is the stored draft
// being replaced (nil on create); lines it already priced keep their
// captured cost while their product and quantity are unchanged.
func (s *Service) build(ctx context.Context, a Actor, existing *Quote, d *Draft) (*Quote, error) {
	cfg := s.config(ctx)
	now := s.now()
	st, err := s.store.GetSettings(ctx, a.CustomerID)
	if err != nil {
		return nil, err
	}
	q := &Quote{
		CustomerID: a.CustomerID, Title: d.Title, ProjectID: d.ProjectID, SourceQuoteID: d.SourceQuoteID,
		JobSiteAddress: d.JobSiteAddress, EndCustomer: d.EndCustomer, SiteContact: d.SiteContact,
		Status: StatusDraft, Markup: st.Markup, HideLinePrices: d.HideLinePrices, Tax: st.Tax,
		PaymentTerms: st.PaymentTerms, DepositBPS: st.DepositBPS, CustomerNote: d.CustomerNote, RequireCode: st.RequireCode,
	}
	if d.Markup != nil {
		q.Markup = *d.Markup
	}
	if d.Tax != nil {
		q.Tax = *d.Tax
	}
	if d.PaymentTerms != nil {
		q.PaymentTerms = strings.TrimSpace(*d.PaymentTerms)
	}
	if d.DepositBPS != nil {
		q.DepositBPS = *d.DepositBPS
	}
	if d.RequireCode != nil {
		if *d.RequireCode && !s.codesAvailable() {
			return nil, ErrCodesUnavailable
		}
		q.RequireCode = *d.RequireCode
	}
	if existing != nil {
		q.ID, q.Reference, q.Revision, q.CreatedAt = existing.ID, existing.Reference, existing.Revision, existing.CreatedAt
	} else {
		q.ID = uuid.New()
	}

	if d.ProjectID != nil {
		ok, err := s.store.ProjectOwned(ctx, *d.ProjectID, a.CustomerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, httpx.BadRequest("the project was not found", httpx.FieldError{Field: "project_id", Message: "is not one of your projects"})
		}
	}
	var src *SourceQuote
	if d.SourceQuoteID != nil {
		src, err = s.costs.DealerQuote(ctx, a.CustomerID, *d.SourceQuoteID)
		if err != nil {
			return nil, err
		}
		if src == nil {
			return nil, httpx.BadRequest("the source quote was not found", httpx.FieldError{Field: "source_quote_id", Message: "is not one of your dealer quotes"})
		}
	}

	groupIDs := map[string]uuid.UUID{}
	for i, g := range d.Groups {
		id := uuid.New()
		groupIDs[g.Key] = id
		q.Groups = append(q.Groups, OptionGroup{ID: id, Label: strings.TrimSpace(g.Label), Position: i})
	}

	prior := map[uuid.UUID]Line{}
	if existing != nil {
		for _, l := range existing.Lines {
			prior[l.ID] = l
		}
	}
	srcLines := map[uuid.UUID]SourceQuoteLine{}
	if src != nil {
		for _, l := range src.Lines {
			srcLines[l.ID] = l
		}
	}
	catalogLines := false
	for i, dl := range d.Lines {
		line := Line{ID: uuid.New(), Position: i, ProductID: dl.ProductID, Quantity: dl.Quantity, Note: dl.Note, Override: dl.Override, Selection: SelectionPending}
		if dl.GroupKey != "" {
			gid := groupIDs[dl.GroupKey]
			line.GroupID = &gid
		}
		var cost int64
		switch {
		case dl.SourceLineID != nil:
			sl, ok := srcLines[*dl.SourceLineID]
			if !ok || sl.ProductID != dl.ProductID {
				return nil, httpx.BadRequest("a source line was not found", httpx.FieldError{Field: fmt.Sprintf("lines[%d].source_line_id", i), Message: "is not a line of the source quote for this product"})
			}
			line.SKU, line.Description, line.UOM, line.CategoryID = sl.SKU, sl.Description, sl.UOM, sl.CategoryID
			line.UnitCostTenThousandths, line.PriceBasis, line.PricedAt = sl.UnitCostTenThousandths, "dealer_quote", now
			cost, err = SourceLineCostCents(line.Quantity, sl.Quantity, sl.LineTotalCents, line.UnitCostTenThousandths)
		case dl.ExistingID != nil && prior[*dl.ExistingID].ID != uuid.Nil &&
			prior[*dl.ExistingID].ProductID == dl.ProductID && prior[*dl.ExistingID].Quantity == dl.Quantity &&
			(prior[*dl.ExistingID].PriceBasis != "catalog" || prior[*dl.ExistingID].PricedAt.Add(cfg.priceProtection).After(now)):
			p := prior[*dl.ExistingID]
			line.ID = p.ID
			line.SKU, line.Description, line.UOM, line.CategoryID = p.SKU, p.Description, p.UOM, p.CategoryID
			line.UnitCostTenThousandths, line.PriceBasis, line.PricedAt = p.UnitCostTenThousandths, p.PriceBasis, p.PricedAt
			cost = p.CostCents
		default:
			if dl.ExistingID != nil && prior[*dl.ExistingID].ID != uuid.Nil &&
				prior[*dl.ExistingID].ProductID == dl.ProductID && prior[*dl.ExistingID].Quantity == dl.Quantity {
				line.ID = prior[*dl.ExistingID].ID
			}
			item, err := s.costs.CatalogCost(ctx, a.CustomerID, dl.ProductID, dl.Quantity)
			if err != nil {
				return nil, err
			}
			if item == nil {
				return nil, httpx.BadRequest("a product was not found", httpx.FieldError{Field: fmt.Sprintf("lines[%d].product_id", i), Message: "is not a product"})
			}
			line.SKU, line.Description, line.UOM, line.CategoryID = item.SKU, item.Description, item.UOM, item.CategoryID
			line.UnitCostTenThousandths, line.PriceBasis, line.PricedAt = item.UnitCostTenThousandths, "catalog", now
			if item.CostCents != nil {
				cost = *item.CostCents
			} else {
				cost, err = ExtendedCostCents(line.Quantity, line.UnitCostTenThousandths)
			}
		}
		if err != nil {
			return nil, httpx.BadRequest("a line cannot be priced", httpx.FieldError{Field: fmt.Sprintf("lines[%d]", i), Message: err.Error()})
		}
		line.CostCents = cost
		if line.PriceBasis == "catalog" {
			catalogLines = true
		}
		q.Lines = append(q.Lines, line)
	}

	q.CategoryRules = d.CategoryRules
	rules := map[uuid.UUID]Markup{}
	for _, r := range q.CategoryRules {
		rules[r.CategoryID] = r.Markup
	}
	mats := make([]MaterialLine, len(q.Lines))
	for i, l := range q.Lines {
		mats[i] = MaterialLine{ID: l.ID, CategoryID: l.CategoryID, CostCents: l.CostCents, Override: l.Override}
	}
	if err := PriceLines(mats, rules, q.Markup); err != nil {
		return nil, httpx.BadRequest("the markup cannot be applied", httpx.FieldError{Field: "markup", Message: err.Error()})
	}
	for i := range q.Lines {
		q.Lines[i].Applied = AppliedMarkup{Tier: mats[i].Tier, Kind: mats[i].Applied.Kind, BPS: mats[i].Applied.BPS}
		q.Lines[i].SellCents = mats[i].SellCents
	}

	for i, l := range d.Labour {
		var hours httpx.Quantity
		if l.RateType == RateHourly {
			hours = l.Hours
		}
		q.Labour = append(q.Labour, Labour{ID: uuid.New(), Position: i, Description: strings.TrimSpace(l.Description),
			RateType: l.RateType, RateCents: l.RateCents, Hours: hours, ContractorOnly: l.ContractorOnly})
	}
	for i, o := range d.Overhead {
		q.Overhead = append(q.Overhead, Overhead{ID: uuid.New(), Position: i, Label: strings.TrimSpace(o.Label),
			AmountType: o.AmountType, Value: o.Value, ContractorOnly: o.ContractorOnly})
	}

	// valid_until: never later than the dealer holds the prices.
	requested := now.Add(cfg.defaultValidFor)
	if d.ValidUntil != nil {
		requested = *d.ValidUntil
	}
	var srcExpiry *time.Time
	if src != nil {
		srcExpiry = src.ExpiresAt
	}
	q.ValidUntil = BoundValidUntil(requested, srcExpiry, catalogLines, earliestCatalogHorizon(q.Lines, cfg.priceProtection))
	if !q.ValidUntil.After(now) {
		return nil, httpx.BadRequest("the quote would already be expired",
			httpx.FieldError{Field: "valid_until", Message: "must be in the future and inside the dealer's price protection"})
	}
	if _, err := q.Totals(); err != nil {
		return nil, httpx.BadRequest("the quote cannot be totalled", httpx.FieldError{Message: err.Error()})
	}
	return q, nil
}

// earliestCatalogHorizon is the end of the dealer's price protection for the
// oldest catalog price on the quote.
func earliestCatalogHorizon(lines []Line, protection time.Duration) time.Time {
	var oldest time.Time
	for _, l := range lines {
		if l.PriceBasis != "catalog" {
			continue
		}
		if oldest.IsZero() || l.PricedAt.Before(oldest) {
			oldest = l.PricedAt
		}
	}
	if oldest.IsZero() {
		return time.Time{}
	}
	return oldest.Add(protection)
}

// Create builds and stores a draft.
func (s *Service) Create(ctx context.Context, a Actor, in *QuoteInput) (*Quote, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	d, err := in.Parse()
	if err != nil {
		return nil, err
	}
	var out *Quote
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		q, err := s.build(ctx, a, nil, d)
		if err != nil {
			return err
		}
		if q.Reference, err = s.store.NextReference(ctx); err != nil {
			return err
		}
		if err := s.store.InsertQuote(ctx, q); err != nil {
			return mapFK(err)
		}
		out, err = s.store.GetQuote(ctx, q.ID, &a.CustomerID, false)
		return err
	})
	return out, err
}

// Update replaces a draft. ifMatch is the revision the caller read.
func (s *Service) Update(ctx context.Context, a Actor, id uuid.UUID, ifMatch string, in *QuoteInput) (*Quote, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	d, err := in.Parse()
	if err != nil {
		return nil, err
	}
	var out *Quote
	err = s.db.RunInTx(ctx, func(ctx context.Context) error {
		cur, err := s.store.GetQuote(ctx, id, &a.CustomerID, true)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(cur.Revision, ifMatch, nil); err != nil {
			return err
		}
		if cur.Status != StatusDraft {
			return ErrInvalidState
		}
		q, err := s.build(ctx, a, cur, d)
		if err != nil {
			return err
		}
		if err := s.store.ReplaceDraft(ctx, q); err != nil {
			return mapFK(err)
		}
		out, err = s.store.GetQuote(ctx, id, &a.CustomerID, false)
		return err
	})
	return out, err
}

// Get reads one quote of the contractor's.
func (s *Service) Get(ctx context.Context, a Actor, id uuid.UUID) (*Quote, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	return s.store.GetQuote(ctx, id, &a.CustomerID, false)
}

// List reads the contractor's quotes; the page is one longer than limit when
// more follow.
func (s *Service) List(ctx context.Context, a Actor, f ListFilter) ([]*Quote, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	return s.store.ListQuotes(ctx, a.CustomerID, f)
}

// Preview is the customer's view of the contractor's current draft or sent
// quote, as the customer would see it now.
func (s *Service) Preview(ctx context.Context, a Actor, id uuid.UUID) (*PublicView, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	q, err := s.store.GetQuote(ctx, id, &a.CustomerID, false)
	if err != nil {
		return nil, err
	}
	return s.publicViewOf(ctx, q)
}

// ContractorPDF renders the customer's PDF for the contractor.
func (s *Service) ContractorPDF(ctx context.Context, a Actor, id uuid.UUID) ([]byte, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	q, err := s.store.GetQuote(ctx, id, &a.CustomerID, false)
	if err != nil {
		return nil, err
	}
	return s.renderPDF(ctx, q)
}

func (s *Service) publicViewOf(ctx context.Context, q *Quote) (*PublicView, error) {
	name, hasLogo, err := s.store.ContractorName(ctx, q.CustomerID)
	if err != nil {
		return nil, err
	}
	in := PublicInputs{ContractorName: name, HasLogo: hasLogo, Now: s.now()}
	in.CodesBlocked = q.RequireCode && q.Effective(in.Now) == StatusSent && !s.codesAvailable()
	if ob, err := s.store.OrderBriefOf(ctx, q.OrderID); err != nil {
		return nil, err
	} else if ob != nil {
		in.OrderStatus = ob.Status
	}
	if q.Status == StatusApproved || q.Status == StatusDeclined || q.Status == StatusConfirmed {
		log, err := s.store.ListLog(ctx, q.ID)
		if err != nil {
			return nil, err
		}
		for i := len(log) - 1; i >= 0; i-- {
			if log[i].Step == StepCustomerApproved || log[i].Step == StepCustomerDeclined {
				e := log[i]
				in.Approval = &e
				break
			}
		}
	}
	return BuildPublicView(q, in)
}

func (s *Service) renderPDF(ctx context.Context, q *Quote) ([]byte, error) {
	if s.pdf == nil {
		return nil, httpx.Unavailable("the PDF renderer is not configured")
	}
	v, err := s.publicViewOf(ctx, q)
	if err != nil {
		return nil, err
	}
	logo, err := s.store.GetLogo(ctx, q.CustomerID)
	if err != nil {
		return nil, err
	}
	return s.pdf.CustomerQuotePDF(ctx, v, logo)
}

// --- send, rotate, revoke, withdraw, revise ---

// LinkResult is what issuing a link answers: the raw token exists only here.
type LinkResult struct {
	Quote     *Quote
	Path      string
	URL       string
	ExpiresAt time.Time
	Delivered bool
}

func (s *Service) linkURL(token string) (path, url string) {
	path = "/q/" + token
	if s.publicURL != "" {
		url = s.publicURL + path
	}
	return path, url
}

// Send freezes a draft, mints the capability link and emails it.
func (s *Service) Send(ctx context.Context, a Actor, id uuid.UUID, ifMatch string) (*LinkResult, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	cfg := s.config(ctx)
	var res *LinkResult
	var raw string
	err := s.db.RunInTx(ctx, func(ctx context.Context) error {
		q, err := s.store.GetQuote(ctx, id, &a.CustomerID, true)
		if err != nil {
			return err
		}
		if ifMatch != "" {
			if err := httpx.CheckRevision(q.Revision, ifMatch, nil); err != nil {
				return err
			}
		}
		if q.Status != StatusDraft {
			return ErrInvalidState
		}
		if err := sendable(q); err != nil {
			return err
		}
		if q.RequireCode && !s.codesAvailable() {
			return ErrCodesUnavailable
		}
		now := s.now()
		valid := q.ValidUntil
		var srcExpiry *time.Time
		if q.SourceQuoteID != nil {
			if src, err := s.costs.DealerQuote(ctx, a.CustomerID, *q.SourceQuoteID); err == nil && src != nil {
				srcExpiry = src.ExpiresAt
			}
		}
		catalog := earliestCatalogHorizon(q.Lines, cfg.priceProtection)
		valid = BoundValidUntil(valid, srcExpiry, !catalog.IsZero(), catalog)
		linkExp, err := LinkExpiry(now, valid, cfg.linkMaxTTL)
		if err != nil {
			return httpx.BadRequest("the quote has expired", httpx.FieldError{Field: "valid_until", Message: "is no longer in the future"})
		}
		if !valid.Equal(q.ValidUntil) {
			if _, err := s.store.exec(ctx).Exec(ctx, `UPDATE pro_customer_quotes SET valid_until = $2 WHERE id = $1`, q.ID, valid); err != nil {
				return err
			}
		}
		token, hash, err := MintToken()
		if err != nil {
			return err
		}
		raw = token
		if _, err := s.store.InsertLink(ctx, q.ID, hash, linkExp); err != nil {
			return err
		}
		moved, err := s.store.Transition(ctx, q.ID, StatusDraft, StatusSent, "sent_at = $4, selections_version = 1", now)
		if err != nil {
			return err
		}
		if !moved {
			return ErrInvalidState
		}
		out, err := s.store.GetQuote(ctx, id, &a.CustomerID, false)
		if err != nil {
			return err
		}
		path, url := s.linkURL(raw)
		res = &LinkResult{Quote: out, Path: path, URL: url, ExpiresAt: linkExp}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Delivered = s.deliverLink(ctx, res)
	return res, nil
}

// sendable is the send gate: a quote the customer can be asked to approve.
func sendable(q *Quote) error {
	v := &httpx.Validator{}
	v.Check(len(q.Lines) > 0 || len(q.Labour) > 0, "lines", "the quote has nothing to approve")
	v.Check(q.ProjectID != nil, "project_id", "is required to send: the dealer sees the project")
	v.Check(q.EndCustomer.Email != "" || q.EndCustomer.Phone != "", "end_customer", "needs an email or a phone to send to")
	v.Check(q.JobSiteAddress != "", "job_site_address", "is required to send")
	if q.RequireCode {
		v.Check(q.EndCustomer.Email != "" || q.EndCustomer.Phone != "", "end_customer", "needs an email or a phone for the verification code")
	}
	return v.Err()
}

// deliverLink emails the link; it reports whether anything real was sent.
// Failure to send never fails the send: the link exists and the contractor
// has it.
func (s *Service) deliverLink(ctx context.Context, r *LinkResult) bool {
	if s.mail == nil {
		return false
	}
	link := r.URL
	if link == "" {
		return false
	}
	name, _, err := s.store.ContractorName(ctx, r.Quote.CustomerID)
	if err != nil {
		return false
	}
	ok, err := s.mail.SendLink(ctx, r.Quote.EndCustomer, name, r.Quote.Title, link)
	if err != nil {
		s.log.Warn("customer quote link was not delivered", "quote_id", r.Quote.ID, "error", err)
		return false
	}
	return ok
}

// Rotate kills the live link and issues a new one for a sent quote.
func (s *Service) Rotate(ctx context.Context, a Actor, id uuid.UUID) (*LinkResult, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	cfg := s.config(ctx)
	var res *LinkResult
	err := s.db.RunInTx(ctx, func(ctx context.Context) error {
		q, err := s.store.GetQuote(ctx, id, &a.CustomerID, true)
		if err != nil {
			return err
		}
		now := s.now()
		if q.Status != StatusSent {
			return ErrInvalidState
		}
		linkExp, err := LinkExpiry(now, q.ValidUntil, cfg.linkMaxTTL)
		if err != nil {
			return ErrInvalidState
		}
		if _, err := s.store.RevokeLive(ctx, q.ID, "rotated", now); err != nil {
			return err
		}
		token, hash, err := MintToken()
		if err != nil {
			return err
		}
		if _, err := s.store.InsertLink(ctx, q.ID, hash, linkExp); err != nil {
			return err
		}
		path, url := s.linkURL(token)
		res = &LinkResult{Quote: q, Path: path, URL: url, ExpiresAt: linkExp}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Delivered = s.deliverLink(ctx, res)
	return res, nil
}

// Revoke kills the live link. The quote stays sent; Rotate issues a new one.
func (s *Service) Revoke(ctx context.Context, a Actor, id uuid.UUID) error {
	if err := s.needQuoting(a); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, func(ctx context.Context) error {
		q, err := s.store.GetQuote(ctx, id, &a.CustomerID, true)
		if err != nil {
			return err
		}
		if q.Status != StatusSent {
			return ErrInvalidState
		}
		n, err := s.store.RevokeLive(ctx, q.ID, "revoked", s.now())
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidState
		}
		return nil
	})
}

// Withdraw takes a draft or sent quote off the table and kills its link.
func (s *Service) Withdraw(ctx context.Context, a Actor, id uuid.UUID) (*Quote, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	var out *Quote
	err := s.db.RunInTx(ctx, func(ctx context.Context) error {
		q, err := s.store.GetQuote(ctx, id, &a.CustomerID, true)
		if err != nil {
			return err
		}
		if q.Status != StatusDraft && q.Status != StatusSent {
			return ErrInvalidState
		}
		if _, err := s.store.RevokeLive(ctx, q.ID, "revoked", s.now()); err != nil {
			return err
		}
		if moved, err := s.store.Transition(ctx, q.ID, q.Status, StatusWithdrawn, ""); err != nil {
			return err
		} else if !moved {
			return ErrInvalidState
		}
		out, err = s.store.GetQuote(ctx, id, &a.CustomerID, false)
		return err
	})
	return out, err
}

// Revise takes a sent or declined quote back to a draft for changes. The old
// link dies; the earlier decisions stay in the approval record under their
// revision.
func (s *Service) Revise(ctx context.Context, a Actor, id uuid.UUID) (*Quote, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	var out *Quote
	err := s.db.RunInTx(ctx, func(ctx context.Context) error {
		q, err := s.store.GetQuote(ctx, id, &a.CustomerID, true)
		if err != nil {
			return err
		}
		if q.Status != StatusSent && q.Status != StatusDeclined {
			return ErrInvalidState
		}
		if _, err := s.store.RevokeLive(ctx, q.ID, "revoked", s.now()); err != nil {
			return err
		}
		if moved, err := s.store.Transition(ctx, q.ID, q.Status, StatusDraft, "sent_at = NULL, declined_at = NULL"); err != nil {
			return err
		} else if !moved {
			return ErrInvalidState
		}
		reset := map[uuid.UUID]SelectionState{}
		for _, l := range q.Lines {
			reset[l.ID] = SelectionPending
		}
		if err := s.store.SetSelections(ctx, q.ID, reset, s.now()); err != nil {
			return err
		}
		out, err = s.store.GetQuote(ctx, id, &a.CustomerID, false)
		return err
	})
	return out, err
}

// Approvals reads the approval record of a contractor's quote.
func (s *Service) Approvals(ctx context.Context, a Actor, id uuid.UUID) ([]ApprovalEntry, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	if _, err := s.store.GetQuote(ctx, id, &a.CustomerID, false); err != nil {
		return nil, err
	}
	log, err := s.store.ListLog(ctx, id)
	if err != nil {
		return nil, err
	}
	// The dealer's step reads as the desk user's display name and the dealer's
	// business name; an address never.
	biz, err := s.store.DealerName(ctx)
	if err != nil {
		return nil, err
	}
	for i := range log {
		if log[i].ActorKind == ActorDealer {
			log[i].ActorContact, log[i].ActorRef = "", ""
			if biz != "" {
				log[i].ActorName += ", " + biz
			}
		}
	}
	return log, nil
}

// OrderOf reads the order a quote converted into.
func (s *Service) OrderOf(ctx context.Context, q *Quote) (*OrderBrief, error) {
	return s.store.OrderBriefOf(ctx, q.OrderID)
}

// LiveLink says whether the quote has a live link and when it ends.
func (s *Service) LiveLink(ctx context.Context, q *Quote) (*Link, error) {
	return s.store.LiveLink(ctx, q.ID)
}

// --- confirm to order ---

func (s *Service) confirmPreviewOf(ctx context.Context, q *Quote, now time.Time) (*ConfirmPreview, map[uuid.UUID]int64, []OrderLine, error) {
	tot, err := q.Totals()
	if err != nil {
		return nil, nil, nil, err
	}
	srcValid := false
	var src *SourceQuote
	if q.SourceQuoteID != nil {
		var err error
		src, err = s.costs.DealerQuote(ctx, q.CustomerID, *q.SourceQuoteID)
		if err != nil {
			return nil, nil, nil, err
		}
		if src != nil && (src.ExpiresAt == nil || now.Before(*src.ExpiresAt)) {
			srcValid = true
		}
	}
	prev := &ConfirmPreview{
		CapturedMarginCents: tot.MarginCents,
		Lines:               []ConfirmLinePreview{},
	}
	unitCosts := map[uuid.UUID]int64{}
	var orderLines []OrderLine
	for _, l := range IncludedLines(q.TotalsInput().Lines, q.States()) {
		for _, ql := range q.Lines {
			if ql.ID != l.ID || ql.Selection != SelectionApproved {
				continue
			}
			var curUnit, curCost int64
			var protected *int64
			priceUOM := ql.UOM
			if ql.PriceBasis == "dealer_quote" && srcValid {
				curUnit = ql.UnitCostTenThousandths
				curCost = ql.CostCents
				u := curUnit
				protected = &u
				// The protected price is the dealer quote line's own price
				// in its own unit, never the rounded per sale unit figure,
				// so the order extends through the same conversion the
				// dealer quoted.
				if sl := srcLineOf(src, ql); sl != nil {
					if sl.UnitPriceTenThousandths > 0 {
						p := sl.UnitPriceTenThousandths
						protected = &p
					}
					if sl.PriceUOM != "" {
						priceUOM = sl.PriceUOM
					}
				}
			} else {
				item, err := s.costs.CatalogCost(ctx, q.CustomerID, ql.ProductID, ql.Quantity)
				if err != nil {
					return nil, nil, nil, err
				}
				if item == nil {
					return nil, nil, nil, httpx.BadRequest("an approved product is no longer available")
				}
				curUnit = item.UnitCostTenThousandths
				if item.CostCents != nil {
					curCost = *item.CostCents
				} else {
					curCost, err = ExtendedCostCents(ql.Quantity, curUnit)
					if err != nil {
						return nil, nil, nil, err
					}
				}
			}
			delta := curCost - ql.CostCents
			if delta != 0 {
				prev.PriceChanged = true
			}
			prev.CapturedMaterialCostCents += ql.CostCents
			prev.CurrentMaterialCostCents += curCost
			prev.Lines = append(prev.Lines, ConfirmLinePreview{
				LineID:            ql.ID,
				ProductID:         ql.ProductID,
				SKU:               ql.SKU,
				Description:       ql.Description,
				Quantity:          ql.Quantity,
				UOM:               ql.UOM,
				CapturedCostCents: ql.CostCents,
				CurrentCostCents:  curCost,
				DeltaCents:        delta,
			})
			unitCosts[ql.ID] = curUnit
			orderLines = append(orderLines, OrderLine{
				ProductID:           ql.ProductID,
				Quantity:            ql.Quantity,
				UOM:                 ql.UOM,
				DealerQuoteUnitCost: protected,
				DealerQuotePriceUOM: priceUOM,
			})
		}
	}
	prev.DeltaCents = prev.CurrentMaterialCostCents - prev.CapturedMaterialCostCents
	prev.ProjectedMarginCents = tot.MarginCents - prev.DeltaCents
	return prev, unitCosts, orderLines, nil
}

// srcLineOf finds the source quote's line a quote line was built from, by
// product and unit.
func srcLineOf(src *SourceQuote, ql Line) *SourceQuoteLine {
	if src == nil {
		return nil
	}
	for i := range src.Lines {
		if sl := &src.Lines[i]; sl.ProductID == ql.ProductID && sl.UOM == ql.UOM {
			return sl
		}
	}
	return nil
}

// ConfirmPreview checks whether the dealer's material prices have moved since
// the quote was priced.
func (s *Service) ConfirmPreview(ctx context.Context, a Actor, id uuid.UUID) (*ConfirmPreview, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	if !s.caps.Has(a, CapSeeMoney) {
		return nil, ErrForbidden
	}
	q, err := s.store.GetQuote(ctx, id, &a.CustomerID, false)
	if err != nil {
		return nil, err
	}
	if q.Status != StatusApproved {
		return nil, ErrInvalidState
	}
	prev, _, _, err := s.confirmPreviewOf(ctx, q, s.now())
	return prev, err
}

// Confirm turns the customer's approval into the dealer's order: the approved
// material lines, at the dealer's prices, through the portal's order path, in
// one transaction with the quote's confirmation and its approval record.
func (s *Service) Confirm(ctx context.Context, a Actor, id uuid.UUID, opts ...ConfirmInput) (*Quote, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	if s.orders == nil {
		return nil, httpx.Unavailable("the order path is not configured")
	}
	var in ConfirmInput
	if len(opts) > 0 {
		in = opts[0]
	}
	var out *Quote
	err := s.db.RunInTx(ctx, func(ctx context.Context) error {
		q, err := s.store.GetQuote(ctx, id, &a.CustomerID, true)
		if err != nil {
			return err
		}
		if q.Status != StatusApproved || q.ProjectID == nil {
			return ErrInvalidState
		}
		now := s.now()
		prev, unitCosts, lines, err := s.confirmPreviewOf(ctx, q, now)
		if err != nil {
			return err
		}
		customerLabour := 0
		for _, l := range q.Labour {
			if !l.ContractorOnly {
				customerLabour++
			}
		}
		if len(lines) == 0 && customerLabour == 0 {
			return httpx.InvalidStateTransition("no approved material to order")
		}
		var note string
		var moved bool
		if len(lines) > 0 {
			if prev.PriceChanged && !in.AcceptPriceChanges {
				return ErrPriceChanged
			}
			if prev.PriceChanged {
				for _, lp := range prev.Lines {
					if lp.DeltaCents != 0 {
						if err := s.store.UpdateLineCost(ctx, lp.LineID, unitCosts[lp.LineID], lp.CurrentCostCents, now); err != nil {
							return err
						}
					}
				}
				note = fmt.Sprintf("dealer material cost changed from %d to %d cents (delta %+d cents); projected margin %d cents",
					prev.CapturedMaterialCostCents, prev.CurrentMaterialCostCents, prev.DeltaCents, prev.ProjectedMarginCents)
			}
			orderID, err := s.orders.PlaceOrder(ctx, OrderPlacement{CustomerID: q.CustomerID, ProjectID: *q.ProjectID,
				Reference: q.Reference, SiteAddress: q.JobSiteAddress, Lines: lines})
			if err != nil {
				return err
			}
			moved, err = s.store.Transition(ctx, q.ID, StatusApproved, StatusConfirmed, "confirmed_at = $4, confirmed_by = $5, order_id = $6", now, a.UserID, orderID)
		} else {
			moved, err = s.store.Transition(ctx, q.ID, StatusApproved, StatusConfirmed, "confirmed_at = $4, confirmed_by = $5, order_id = NULL", now, a.UserID)
		}
		if err != nil {
			return err
		}
		if !moved {
			return ErrInvalidState
		}
		ref := ""
		if a.UserID != nil {
			ref = a.UserID.String()
		}
		if err := s.store.AppendLog(ctx, &ApprovalEntry{QuoteID: q.ID, QuoteRevision: q.Revision, Step: StepContractorConfirmed,
			ActorKind: ActorContractor, ActorName: a.Name, ActorContact: a.Email, ActorRef: ref, OccurredAt: now, Note: note}); err != nil {
			return err
		}
		out, err = s.store.GetQuote(ctx, id, &a.CustomerID, false)
		return err
	})
	return out, err
}
