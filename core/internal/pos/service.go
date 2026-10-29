// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/units"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// Event types of the counter (ADR 0005 section 12).
const (
	EventSaleCompleted   = "pos_transaction.completed"
	EventSaleVoided      = "pos_transaction.voided"
	EventReturnCompleted = "pos_return.completed"
	EventTillOpened      = "till.opened"
	EventTillClosed      = "till.closed"

	auditLinePriceOverridden = "pos_transaction.line_price_overridden"
	auditLineDiscounted      = "pos_transaction.line_discounted"
	auditLineQuantityChanged = "pos_transaction.line_quantity_changed"
)

// PriceEngine prices a product line for a customer and quantity at the
// counter boundary, exactly the order module's engine (ADR 0005 section 1).
// PriceEngine prices one line for a customer, quantity and unit (ADR 0006
// sections 5.3 and 7.5: pricing.Resolve through the wiring, its answer in the
// price's own unit with the line's conversion pair). When none is wired the
// product's base price is the counter price, in the product's price unit.
type PriceEngine interface {
	PriceFor(ctx context.Context, customerID, productID uuid.UUID, quantity httpx.Quantity, uom string, jobID *uuid.UUID) (salesdoc.EngineAnswer, error)
}

// TaxProvider is the configured provider path behind the rate resolver
// (ADR 0005 section 3), the tax service's own surface.
type TaxProvider interface {
	ProviderConfigured() bool
	PreviewTax(ctx context.Context, req *tax.TaxPreviewRequest) (*tax.TaxResult, error)
}

// InvoiceWriter writes the counter sale's invoice with its entry and its
// subledger debit inside the caller's transaction (invoice.Service).
type InvoiceWriter interface {
	CreateCounterInvoice(ctx context.Context, in *invoice.CounterInvoice) error
}

// LedgerPoster posts the till drawer's over/short inside the close's
// transaction and a movement's entry inside its own (gl.Service).
type LedgerPoster interface {
	PostTillOverShort(ctx context.Context, branchID uuid.UUID, sessionID uuid.UUID, overShortCents int64) (uuid.UUID, error)
	PostEntry(ctx context.Context, in gl.PostingInput) (*gl.JournalEntry, error)
	PostReversal(ctx context.Context, in gl.ReversalInput) (*gl.JournalEntry, error)
}

// EventRecorder writes the act's events, last, in its transaction.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service is the counter: sales, voids, returns, till sessions and the
// offline sync, every money moment one transaction whose events are written
// last (ADR 0005 sections 11 and 14.2 C2-5).
type Service struct {
	db        *database.DB
	grants    *middleware.BranchGuard
	repo      Repository
	logger    *slog.Logger
	now       func() time.Time
	pricer    PriceEngine
	tax       TaxProvider
	invoices  InvoiceWriter
	ledger    LedgerPoster
	gateway   PaymentGateway
	auditLog  *audit.Logger
	events    EventRecorder
	tx        TxRunner
	ar        ARCore
	inventory CounterInventory
	// terminal is the card terminal device seam and terminalLedger its
	// intent ledger (CP-05, IX-01). Both nil keeps the plain gateway path.
	terminal       payment.TerminalProvider
	terminalLedger *payment.TerminalLedger
	terminalWait   time.Duration
	terminalCrash  func(point string) error
	bank           BankLines
	// largeCash is the branch country's large cash rules (CP-17a); nil
	// keeps the completion as it was (a stack with no rules wired).
	largeCash LargeCashCore
	// scale is the branch scale's driver (CP-19); nil keeps the manual
	// weight entry path, which carries its reason instead.
	scale ScaleReader
}

// LargeCashCore is the slice of the large cash module the counter drives:
// the preview the payment panel asks before Pay and the capture that rides
// the completion's transaction.
type LargeCashCore interface {
	Preview(ctx context.Context, f *largecash.CompletionFacts) (*largecash.Preview, error)
	Capture(ctx context.Context, f *largecash.CompletionFacts) error
}

// ARCore is the slice of the AR core the counter drives (account.Service).
type ARCore interface {
	RecordPayment(ctx context.Context, in account.RecordPaymentIn) (uuid.UUID, *account.Effects, error)
	VoidPayment(ctx context.Context, in account.VoidPaymentIn) (*account.Effects, error)
	Reverse(ctx context.Context, in account.ReverseIn) (*account.Effects, error)
	RefundPayment(ctx context.Context, in account.RefundPaymentIn) (uuid.UUID, *account.Effects, error)
	PostCreditMemo(ctx context.Context, in account.PostCreditMemoIn) (*account.Effects, error)
	RefundCreditMemo(ctx context.Context, in account.RefundCreditMemoIn) (uuid.UUID, *account.Effects, error)
	ApplyCreditMemo(ctx context.Context, in account.ApplyCreditMemoIn) (*account.Effects, error)
	VoidInvoice(ctx context.Context, in account.VoidInvoiceIn) (*account.Effects, error)
}

// CounterInventory moves stock at the counter: an issue takes from on hand
// (a counter sale never allocates), a restock returns goods.
type CounterInventory interface {
	IssueQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error
	RestockQty(ctx context.Context, productID, branchID uuid.UUID, qty httpx.Quantity) error
}

// WithLargeCash wires the branch country's large cash rules (CP-17a).
func (s *Service) WithLargeCash(lc LargeCashCore) *Service { s.largeCash = lc; return s }

// NewService builds the counter's service.
func NewService(db *database.DB, repo Repository, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, repo: repo, logger: logger, now: time.Now, grants: middleware.NewBranchGuard(db)}
}

// holdSaleBranch refuses an act on a sale whose own branch is outside what
// the caller may target while the context carries no branch, the record act
// arm of the no header rule (SEC-NOHEADER-1). The branch is read with no
// wall, because the walled read a no header user's grant hides the record
// with would answer before the refusal could: a hidden record reads as
// missing (404), an unheld one refuses by name (403). A sale that does not
// exist answers the module's 404. A caller that sends the header keeps the
// walled read's answer, and an administrator keeps its reach.
func (s *Service) holdSaleBranch(ctx context.Context, saleID uuid.UUID) error {
	branch, found, err := s.repo.SaleBranch(ctx, saleID)
	if err != nil {
		return err
	}
	if !found {
		return httpx.NotFound("sale not found")
	}
	return s.holdRecordBranch(ctx, "sale", &branch, saleID)
}

// holdTillBranch is holdSaleBranch for a till session.
func (s *Service) holdTillBranch(ctx context.Context, sessionID uuid.UUID) error {
	branch, found, err := s.repo.TillSessionBranch(ctx, sessionID)
	if err != nil {
		return err
	}
	if !found {
		return httpx.NotFound("till session not found")
	}
	return s.holdRecordBranch(ctx, "till session", branch, sessionID)
}

// holdDayCloseBranch is holdSaleBranch for a branch day close.
func (s *Service) holdDayCloseBranch(ctx context.Context, closeID uuid.UUID) error {
	branch, found, err := s.repo.DayCloseBranch(ctx, closeID)
	if err != nil {
		return err
	}
	if !found {
		return httpx.NotFound("branch day close not found")
	}
	return s.holdRecordBranch(ctx, "branch day close", &branch, closeID)
}

func (s *Service) holdRecordBranch(ctx context.Context, what string, branch *uuid.UUID, id uuid.UUID) error {
	if err := s.grants.CheckGrantsBranch(ctx, branch); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			return httpx.Forbidden(fmt.Sprintf("%s %s belongs to a branch this caller may not target", what, id))
		}
		return err
	}
	return nil
}

// WithPriceEngine wires the pricing engine.
func (s *Service) WithPriceEngine(p PriceEngine) *Service { s.pricer = p; return s }

// WithTaxProvider wires the configured tax provider behind the resolver.
func (s *Service) WithTaxProvider(p TaxProvider) *Service { s.tax = p; return s }

// WithInvoices wires the invoice writer.
func (s *Service) WithInvoices(w InvoiceWriter) *Service { s.invoices = w; return s }

// WithLedger wires the till over/short poster.
func (s *Service) WithLedger(p LedgerPoster) *Service { s.ledger = p; return s }

// WithGateway wires the card-present terminal gateway.
func (s *Service) WithGateway(g PaymentGateway) *Service { s.gateway = g; return s }

// WithAuditLog attaches the audit logger.
func (s *Service) WithAuditLog(l *audit.Logger) *Service { s.auditLog = l; return s }

// WithOutbox wires the event recorder.
func (s *Service) WithOutbox(e EventRecorder) *Service { s.events = e; return s }

// WithTxRunner wires the transaction runner (serve does; unit tests need
// none).
func (s *Service) WithTxRunner(t TxRunner) *Service { s.tx = t; return s }

// WithAR wires the AR core.
func (s *Service) WithAR(a ARCore) *Service { s.ar = a; return s }

// WithInventory wires the stock moves.
func (s *Service) WithInventory(i CounterInventory) *Service { s.inventory = i; return s }

// PendingVoidClaimsOfInvoice answers the standing void claims of the sale
// that owns the invoice, so the AR core's application guard can refuse an
// application to the invoice's interim open cents while the void's outcome
// is unsettled (C2-5, the fourteenth review's P2-1): an application would leave a
// live row the void's completion cannot reverse, stranding the last claim
// and the drawer. The service satisfies account.InvoiceApplyGuard; serve
// wires the two.
func (s *Service) PendingVoidClaimsOfInvoice(ctx context.Context, invoiceID uuid.UUID) ([]uuid.UUID, error) {
	return s.repo.PendingVoidClaimsOfInvoice(ctx, invoiceID)
}

// HeldTenderCredit answers whether a payment's unapplied cash is a declined
// void tender's credit the counter still holds (C2-5, FROM-PRINCIPAL 60),
// so the AR core's held credit guard can refuse any application of it to an
// invoice: the credit is payable back only to its card. The service
// satisfies account.TenderCreditGuard; serve wires the two.
func (s *Service) HeldTenderCredit(ctx context.Context, paymentID uuid.UUID) (account.HeldTenderCredit, bool, error) {
	facts, ok, err := s.repo.HeldTenderCreditOfPayment(ctx, paymentID)
	if err != nil || !ok {
		return account.HeldTenderCredit{}, ok, err
	}
	return account.HeldTenderCredit{Cents: facts.Cents, SaleID: facts.SaleID, SaleNumber: facts.SaleNumber,
		TenderID: facts.TenderID, IntentID: facts.IntentID}, true, nil
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx != nil {
		return s.tx.RunInTx(ctx, fn)
	}
	if s.db != nil {
		return s.db.RunInTx(ctx, fn)
	}
	return fn(ctx)
}

// actorOf reads the acting user from the request context's claims.
func actorOf(ctx context.Context) string { return "" }

// eventJSON marshals an event's data.
func eventJSON(v map[string]any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return raw
}

// estimateTax is the cart's live estimate through the one resolver; the money
// moment resolves its own tax through the provider or the resolver
// (ADR 0005 section 3: the estimate is not the bill).
func (s *Service) estimateTax(ctx context.Context, sale *Sale, taxableCents httpx.Cents) int64 {
	// the estimate runs the same resolver the completion runs (CP-10): an
	// exempt customer, a delivered sale's ship-to zone, then the branch rate
	customerID := sale.CustomerID
	if customerID == nil {
		if id, _, err := s.repo.WalkInCustomer(ctx); err == nil {
			customerID = &id
		}
	}
	if customerID == nil {
		return 0
	}
	resolved, err := s.ResolveSaleTax(ctx, sale, *customerID)
	if err != nil || resolved.Rate == nil {
		return 0
	}
	scaled, _, err := salesdoc.ParseTaxRate(*resolved.Rate)
	if err != nil {
		return 0
	}
	return int64(salesdoc.TaxAt(taxableCents, scaled))
}

// zoneFor resolves a delivered sale's ship to to the zone that covers it
// (CP-10), nil when the sale is a pickup or names no ship to or no zone
// covers the place of receipt.
func (s *Service) zoneFor(ctx context.Context, sale *Sale) (*taxzone.Snapshot, error) {
	if (sale.DeliveryType != "delivery" && sale.DeliveryType != "DELIVERY") || sale.ShipToID == nil {
		return nil, nil
	}
	snap, _, err := s.repo.ShipTo(ctx, *sale.ShipToID)
	if err != nil {
		return nil, err
	}
	country := ""
	if snap.Country != nil {
		country = *snap.Country
	}
	// The zone's effective window is read on the branch's own business day,
	// never the server's clock (the exemption reads the same day).
	day, err := s.repo.BranchLocalDate(ctx, sale.BranchID, s.now())
	if err != nil {
		return nil, err
	}
	return s.repo.TaxZone(ctx, country, snap.PostalCode, snap.Region, day)
}

// ResolveSaleTax builds the counter sale's tax inputs (CP-10): the
// exemption, the ship to a delivered sale names with its zone and its typed
// rate, and the branch rate, and runs the one resolver Gable has. The
// exemption reads through exemptOn, the one window rule on the branch's
// business day the running totals, the completion, the returns and the
// exchanges all read, and it still outranks the ship to's zone.
func (s *Service) ResolveSaleTax(ctx context.Context, sale *Sale, customerID uuid.UUID) (salesdoc.ResolvedTax, error) {
	exempt, err := s.exemptOn(ctx, sale.BranchID, customerID)
	if err != nil {
		return salesdoc.ResolvedTax{}, err
	}
	inputs := salesdoc.TaxInputs{Exempt: exempt, Delivery: sale.DeliveryType == "delivery" || sale.DeliveryType == "DELIVERY"}
	if inputs.Delivery && sale.ShipToID != nil {
		_, typedRate, err := s.repo.ShipTo(ctx, *sale.ShipToID)
		if err != nil {
			return salesdoc.ResolvedTax{}, err
		}
		inputs.ShipToRate = typedRate
		zone, err := s.zoneFor(ctx, sale)
		if err != nil {
			return salesdoc.ResolvedTax{}, err
		}
		if zone != nil {
			inputs.ZoneRate = &zone.CombinedRate
		}
	}
	rate, ok, err := s.repo.BranchTaxRate(ctx, &sale.BranchID)
	if err != nil {
		return salesdoc.ResolvedTax{}, err
	}
	if ok {
		inputs.BranchRate = &rate
	}
	return salesdoc.ResolveTax(inputs)
}

// exemptOn reads whether the customer's certificate exempts a sale on the
// branch's business day, through tax.ExemptionCovering: the one window rule
// (active, effective on or before the day, not yet expired) the running
// totals, the completion, the returns and the exchanges all read.
func (s *Service) exemptOn(ctx context.Context, branchID, customerID uuid.UUID) (bool, error) {
	day, err := s.repo.BranchLocalDate(ctx, branchID, s.now())
	if err != nil {
		return false, err
	}
	standing, err := s.repo.CustomerExemption(ctx, customerID, day)
	if err != nil {
		return false, err
	}
	return standing != nil, nil
}

func (s *Service) recordEvent(ctx context.Context, ev outbox.Event) error {
	if s.events == nil {
		return nil
	}
	return s.events.Write(ctx, ev)
}

// openDrawer answers the register's open till session, or the till_not_open
// refusal: a counter sale starts and completes only into an open drawer, so
// its money is in a count (CP-08).
func (s *Service) openDrawer(ctx context.Context, registerID string) (*TillSession, error) {
	// The register is held to the caller's wall first: a register of another
	// branch answers its own refusal, never the missing drawer.
	if _, err := s.repo.GetRegisterBranch(ctx, registerID); err != nil {
		return nil, err
	}
	session, err := s.repo.GetOpenTillSession(ctx, registerID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, conflict("till_not_open",
			fmt.Sprintf("register %s has no open till session: open the drawer before selling", registerID))
	}
	return session, nil
}

// StartSale opens a cart on a register, numbered POS- through the sequence
// and attached to the register's open till session when one exists. A sale
// without a customer is a walk-in sale: the walk-in customer is resolved at
// completion, where the invoice needs a real customer.
func (s *Service) StartSale(ctx context.Context, in *StartSale, actor string) (*Sale, error) {
	walkInID, defaultCurrency, err := s.repo.WalkInCustomer(ctx)
	if err != nil {
		return nil, err
	}
	currency := defaultCurrency
	if in.CustomerID != nil {
		_, facts, err := s.namedCustomer(ctx, *in.CustomerID)
		if err != nil {
			return nil, err
		}
		currency = facts.Currency
	}
	var out *Sale
	// The ship to a delivered sale names must be the customer's own (CP-10):
	// the read is unwalled, the customer was walled above.
	if in.ShipToID != nil {
		ok, err := s.repo.ShipToBelongsTo(ctx, *in.ShipToID, *in.CustomerID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, httpx.BadRequest("the ship to is not the customer's",
				httpx.FieldError{Field: "ship_to_id", Message: "no such ship to for this customer"})
		}
	}
	deliveryType := in.DeliveryType
	if deliveryType == "" {
		deliveryType = "pickup"
	}
	err = s.inTx(ctx, func(ctx context.Context) error {
		sale := &Sale{RegisterID: in.RegisterID, CashierID: in.CashierID, CustomerID: in.CustomerID,
			Status: StatusOpen, Currency: currency, WalkInID: walkInID,
			DeliveryType: deliveryType, ShipToID: in.ShipToID,
			Lines: []salesdoc.Line{}, Tenders: []Tender{}}
		session, err := s.openDrawer(ctx, in.RegisterID)
		if err != nil {
			return err
		}
		sale.TillSessionID = &session.ID
		if err := s.repo.CreateSale(ctx, sale); err != nil {
			return err
		}
		out = sale
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// pricingOut comes back from pricing a request line: the line, ready to
// store, and the audit rows the price adjustment owes.
type pricingOut struct {
	overrides []audit.Entry
	discounts []audit.Entry
}

// priceLine turns one parsed request line into a stored line at the counter
// (ADR 0005 sections 2.3 and 2.7, the order module's pricing step applied at
// the counter): defaults from the product or the charge code, the engine's
// price beside an override, the discount on the line, kits exploded.
func (s *Service) priceLine(ctx context.Context, sale *Sale, pl salesdoc.ParsedLine, actor string) (salesdoc.Line, *pricingOut, error) {
	out := &pricingOut{}
	line := salesdoc.Line{ID: uuid.New(), Description: pl.Description, PriceSource: salesdoc.PriceSourceNone}
	if pl.Taxable != nil {
		line.Taxable = *pl.Taxable
	}
	customerID := uuid.Nil
	if sale.CustomerID != nil {
		customerID = *sale.CustomerID
	} else if sale.WalkInID != uuid.Nil {
		// The walk-in customer is the sale's customer of record: the engine
		// prices for it (its retail tier), never for an empty id.
		customerID = sale.WalkInID
	}
	refs, codes, _, sets, catalogue, err := s.lookups(ctx, []salesdoc.ParsedLine{pl})
	if err != nil {
		return line, out, err
	}
	switch pl.LineType {
	case salesdoc.LineText:
		line.LineType = salesdoc.LineText
		return line, out, nil
	case salesdoc.LineCharge:
		line.LineType = salesdoc.LineCharge
		code, ok := codes[pl.ChargeCode]
		if !ok {
			return line, out, invalid(lineField("charge_code"), "no such charge code")
		}
		line.ChargeCodeID = &code.ID
		cc := code.Code
		line.ChargeCode = &cc
		if line.Description == "" {
			line.Description = code.Name
		}
		qty := pl.Quantity
		line.Quantity = &qty
		uom := pl.UOM
		if uom == "" {
			uom = "EA"
		}
		line.UOM, line.PriceUOM = &uom, &uom
		line.UOMQty, line.PriceUOMQty = ptrQty(salesdoc.One), ptrQty(salesdoc.One)
		price := httpx.Price(0)
		if pl.UnitPrice != nil {
			price = *pl.UnitPrice
		} else if code.DefaultUnitPrice != nil {
			price = *code.DefaultUnitPrice
		} else {
			return line, out, invalid("line.unit_price_ten_thousandths",
				"is required when the charge code has no default price")
		}
		line.UnitPrice = &price
		line.PriceSource = salesdoc.PriceSourceManual
		if pl.Taxable != nil {
			line.Taxable = *pl.Taxable
		} else {
			line.Taxable = code.Taxable
		}
		line.RevenueAccountCode = &code.RevenueAccountCode
	default: // product
		line.LineType = salesdoc.LineProduct
		var ref *salesdoc.ProductRef
		var set *salesdoc.ProductUnitSet
		if pl.ProductID != nil {
			if r, ok := refs[pl.ProductID.String()]; ok {
				ref = &r
			} else {
				return line, out, invalid(lineField("product_id"), "no such product")
			}
			if ps, ok := sets[*pl.ProductID]; ok {
				set = &ps
			}
		}
		// The units (ADR 0006 section 3.3): the request's, or the product's
		// defaults; any sale unit of the product is sold, the pair resolves
		// from its set, and the stocking quantity converts exactly or the
		// line is refused.
		var uom string
		if ref != nil {
			line.ProductID = &ref.ID
			if line.SKU == nil || *line.SKU == "" {
				sku := ref.SKU
				line.SKU = &sku
			}
			if line.Description == "" {
				line.Description = ref.Description
			}
			line.Taxable = ref.Taxable
			uom = pl.UOM
			if uom == "" {
				uom = set.SaleUOM
			}
		} else {
			uom = pl.UOM
			if pl.Taxable != nil {
				line.Taxable = *pl.Taxable
			} else {
				line.Taxable = true
			}
		}
		line.UOM = &uom
		priceUOM := pl.PriceUOM
		if priceUOM == "" {
			priceUOM = uom
			if ref != nil && set.PriceUOM != "" {
				priceUOM = set.PriceUOM
			}
		}
		line.PriceUOM = &priceUOM

		// The quantity, or the tally's linear feet (ADR 0006 section 4.3).
		if len(pl.TallyRows) > 0 {
			rows := make([]units.TallyRow, 0, len(pl.TallyRows))
			for _, r := range pl.TallyRows {
				rows = append(rows, units.TallyRow{Pieces: int64(r.Pieces), LengthFT: r.LengthFT})
			}
			linearFeet, lfErr := units.LinearFeet(rows)
			if lfErr != nil {
				return line, out, invalid(lineField("tally"),
					"the tally's linear feet are beyond the quantity bound of 99999999.9999 linear feet")
			}
			line.Quantity = &linearFeet
		} else {
			qty := pl.Quantity
			line.Quantity = &qty
		}

		// The engine's answer decides the line's price unit (ADR 0006
		// sections 3.3 and 7.5); an override must name it, so the audit row
		// compares the two prices in one unit.
		var answer *salesdoc.EngineAnswer
		if ref != nil {
			ans, ansErr := s.enginePrice(ctx, customerID, *ref, set, *line.Quantity, uom)
			if ansErr != nil {
				return line, out, ansErr
			}
			answer = &ans
		}
		if pl.UnitPrice == nil {
			if answer != nil {
				priceUOM = answer.PriceUOM
				line.PriceUOM = &priceUOM
			}
		} else if answer != nil && priceUOM != answer.PriceUOM {
			return line, out, invalid(lineField("price_uom"),
				"an override prices in "+priceUOM+" but the engine answered per "+answer.PriceUOM+
					": the override and the engine's answer must be comparable in the audit row")
		}

		// The pair, the tally's rules and the stocking quantity resolve from
		// the product's set (ADR 0006 sections 3.3, 3.4 and 4).
		v := &httpx.Validator{}
		in := salesdoc.LineUnits{UOM: uom, PriceUOM: priceUOM,
			UOMQty: pl.UOMQty, PriceUOMQty: pl.PriceUOMQty,
			TallyRows: pl.TallyRows, HasQuantity: pl.HasQuantity}
		if !salesdoc.ResolveLineUnits(v, "line", &line, in, set, catalogue) {
			return line, out, v.Err()
		}

		if pl.UnitPrice == nil {
			if answer != nil {
				p := answer.Price
				line.UnitPrice = &p
				line.PricedUnitPrice = &p
				line.PriceSource = salesdoc.PriceSourceList
				basis := answer.Basis
				line.PriceBasis = &basis
			} else {
				return line, out, invalid(lineField("unit_price_ten_thousandths"),
					"is required on a non stock line: there is no product to price it from")
			}
		} else {
			p := *pl.UnitPrice
			line.UnitPrice = &p
			if ref != nil && answer != nil {
				e := answer.Price
				line.PricedUnitPrice = &e
				if p == answer.Price {
					line.PriceSource = salesdoc.PriceSourceList
					basis := answer.Basis
					line.PriceBasis = &basis
				} else {
					line.PriceSource = salesdoc.PriceSourceOverride
					reason := pl.OverrideReason
					line.OverrideReason = &reason
					line.PriceAdjustedBy = actorPtr(actor)
					out.overrides = append(out.overrides, audit.Entry{
						Action: auditLinePriceOverridden, EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
						Changes: map[string]any{"line_id": line.ID, "product_id": line.ProductID,
							"engine_price_ten_thousandths":   int64(answer.Price),
							"engine_price_uom":               answer.PriceUOM,
							"override_price_ten_thousandths": int64(p), "reason": pl.OverrideReason}})
				}
			} else {
				line.PriceSource = salesdoc.PriceSourceManual
			}
		}

		// The line's resolved cost, per the stocking unit (ADR 0006 7.5).
		if ref != nil && ref.AverageCost > 0 {
			source := "AVERAGE_COST"
			cost := ref.AverageCost
			line.CostSource, line.StockingUnitCost = &source, &cost
		}

		// A paint line's tint (row CP-16): the line price is the base price
		// plus the dealer's tint rule. The rule follows the tint, not only
		// the container: a tier covers the base tier it names and the
		// colorant total it names, and the flat figure covers the rest (0
		// when the tint is folded into the shelf price, the dominant retail
		// model). The charge rides the tint record so the documents show it
		// as its own amount.
		//
		// The dealer's rule is the only charge (money checklist item 6: the
		// rule the screen shows is enforced on the server): what the
		// request carried is overwritten here, and the colorant total and
		// the base tier are read from the server's own rows, so the charge
		// frozen on the record is the charge the price carries, never the
		// number the screen happened to send.
		if pl.Tint != nil {
			rule, err := s.repo.TintChargeRule(ctx)
			if err != nil {
				return line, out, err
			}
			baseTier, err := s.repo.TintBaseTier(ctx, pl.Tint.FormulaID)
			if err != nil {
				return line, out, err
			}
			charge := rule.Charge(salesdoc.TotalColorantOZ(pl.Tint.Components), baseTier)
			c := httpx.Cents(charge)
			pl.Tint.TintChargeCents = &c
			if charge > 0 && line.UnitPrice != nil {
				p := *line.UnitPrice + httpx.Price(charge*100)
				line.UnitPrice = &p
			}
			line.Tint = pl.Tint
		}
	}
	if pl.DiscountPercent != nil {
		pct := *pl.DiscountPercent
		line.DiscountPercent = &pct
		line.DiscountReason = &pl.DiscountReason
		line.PriceAdjustedBy = actorPtr(actor)
	} else if pl.DiscountAmount != nil {
		amt := *pl.DiscountAmount
		line.DiscountAmount = &amt
		line.DiscountReason = &pl.DiscountReason
		line.PriceAdjustedBy = actorPtr(actor)
	}
	if line.DiscountPercent != nil || line.DiscountAmount != nil {
		out.discounts = append(out.discounts, audit.Entry{
			Action: auditLineDiscounted, EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
			Changes: map[string]any{"line_id": line.ID, "product_id": line.ProductID,
				"discount_percent": pl.DiscountPercent, "discount_cents": pl.DiscountAmount,
				"reason": pl.DiscountReason}})
	}
	// Kits explode: the kit line priced as a whole, one component per
	// component at zero (2.6). The caller stores what comes back.
	return line, out, nil
}

// explodeAndExtend runs Explode then ExtendLine over the priced lines,
// returning the stored order.
func (s *Service) explodeAndExtend(ctx context.Context, lines []salesdoc.Line) ([]salesdoc.Line, error) {
	var productIDs, kitIDs []uuid.UUID
	for i := range lines {
		if lines[i].ProductID != nil {
			productIDs = append(productIDs, *lines[i].ProductID)
			kitIDs = append(kitIDs, *lines[i].ProductID)
		}
	}
	refs, err := s.repo.LookupProducts(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	kits, err := s.repo.LookupKitComponents(ctx, kitIDs)
	if err != nil {
		return nil, err
	}
	var componentIDs []uuid.UUID
	for kitID, comps := range kits {
		if _, ok := refs[kitID]; !ok {
			continue
		}
		for _, c := range comps {
			if _, seen := refs[c.ComponentProductID.String()]; !seen {
				componentIDs = append(componentIDs, c.ComponentProductID)
			}
		}
	}
	if len(componentIDs) > 0 {
		more, err := s.repo.LookupProducts(ctx, componentIDs)
		if err != nil {
			return nil, err
		}
		for k, v := range more {
			refs[k] = v
		}
	}
	exploded, err := salesdoc.Explode(lines, kits, refs)
	if err != nil {
		return nil, err
	}
	for i := range exploded {
		if err := salesdoc.ExtendLine(&exploded[i]); err != nil {
			return nil, err
		}
	}
	return exploded, nil
}

func (s *Service) enginePrice(ctx context.Context, customerID uuid.UUID, ref salesdoc.ProductRef, set *salesdoc.ProductUnitSet, qty httpx.Quantity, uom string) (salesdoc.EngineAnswer, error) {
	if s.pricer == nil {
		priceUOM := ref.UOMPrimary
		if set != nil && set.PriceUOM != "" {
			priceUOM = set.PriceUOM
		}
		return salesdoc.EngineAnswer{Price: ref.BasePrice, PriceUOM: priceUOM, UOMQty: salesdoc.One, PriceUOMQty: salesdoc.One, Basis: "base"}, nil
	}
	return s.pricer.PriceFor(ctx, customerID, ref.ID, qty, uom, nil)
}

// lookups reads every product, charge code, kit definition and unit set the
// parsed lines name, and the unit catalogue rows their units are checked
// against (ADR 0006 sections 2.1 and 3.3).
func (s *Service) lookups(ctx context.Context, parsed []salesdoc.ParsedLine) (map[string]salesdoc.ProductRef, map[string]salesdoc.ChargeCode, map[string][]salesdoc.KitComponent, map[uuid.UUID]salesdoc.ProductUnitSet, map[string]units.CatalogueUnit, error) {
	var productIDs, kitIDs []uuid.UUID
	var codes []string
	unitCodes := map[string]bool{}
	for _, pl := range parsed {
		if pl.ProductID != nil {
			productIDs = append(productIDs, *pl.ProductID)
			kitIDs = append(kitIDs, *pl.ProductID)
		}
		if pl.ChargeCode != "" {
			codes = append(codes, pl.ChargeCode)
		}
		if pl.UOM != "" {
			unitCodes[pl.UOM] = true
		}
		if pl.PriceUOM != "" {
			unitCodes[pl.PriceUOM] = true
		}
	}
	refs, err := s.repo.LookupProducts(ctx, productIDs)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	kits, err := s.repo.LookupKitComponents(ctx, kitIDs)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	sets, err := s.repo.LookupProductUnits(ctx, productIDs)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	for _, id := range productIDs {
		if set, ok := sets[id]; ok {
			if set.SaleUOM != "" {
				unitCodes[set.SaleUOM] = true
			}
			if set.PriceUOM != "" {
				unitCodes[set.PriceUOM] = true
			}
		}
	}
	codeList := make([]string, 0, len(unitCodes))
	for code := range unitCodes {
		codeList = append(codeList, code)
	}
	catalogue, err := s.repo.LookupCatalogue(ctx, codeList)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	chargeCodes, err := s.repo.LookupChargeCodes(ctx, codes)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return refs, chargeCodes, kits, sets, catalogue, nil
}

// AddLine adds one line to an open cart: priced, extended, with the audit
// rows an override or a discount owes, all in the add's own transaction.
func (s *Service) AddLine(ctx context.Context, saleID uuid.UUID, parsed []salesdoc.ParsedLine, actor string) (*Sale, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot add a line to a %s sale", sale.Status.Status()))
		}
		if sale.CustomerID == nil {
			// The engine prices a walk-in sale for the walk-in customer of
			// record, never for an empty id (the read does not carry it).
			walkInID, _, err := s.repo.WalkInCustomer(ctx)
			if err != nil {
				return err
			}
			sale.WalkInID = walkInID
		}
		var priced []salesdoc.Line
		var audits []audit.Entry
		for i := range parsed {
			line, out, err := s.priceLine(ctx, sale, parsed[i], actor)
			if err != nil {
				return err
			}
			priced = append(priced, line)
			audits = append(audits, out.overrides...)
			audits = append(audits, out.discounts...)
		}
		lines, err := s.explodeAndExtend(ctx, priced)
		if err != nil {
			return err
		}
		existing, err := s.repo.GetLines(ctx, saleID)
		if err != nil {
			return err
		}
		// the new lines keep the order Explode built (a kit before its
		// components), numbered after the cart's own
		for i := range lines {
			lines[i].Position = len(existing) + i
		}
		if err := s.repo.AddLines(ctx, saleID, lines); err != nil {
			return err
		}
		for i := range audits {
			if s.auditLog != nil {
				if err := s.auditLog.Log(ctx, audits[i]); err != nil {
					return fmt.Errorf("failed to write the audit row: %w", err)
				}
			}
		}
		if err := s.recalcTotals(ctx, sale); err != nil {
			return err
		}
		out, err = s.GetSale(ctx, saleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ChangeLineQuantity sets one line's quantity on an open cart. The line is
// priced again by priceLine, the add's own pricing step, at the new
// quantity (so a quantity break, a kit's components and a sale's tax follow),
// keeps its id and its place, and the sale's totals are summed as every other
// line write sums them. A price the cashier overrode and a discount stay on
// the line; a list price is read afresh.
func (s *Service) ChangeLineQuantity(ctx context.Context, saleID, lineID uuid.UUID, qty httpx.Quantity,
	ifMatch string, bodyRevision *int64, actor string) (*Sale, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if err := httpx.CheckRevision(sale.Revision, ifMatch, bodyRevision); err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot change a line of a %s sale", sale.Status.Status()))
		}
		existing, err := s.repo.GetLines(ctx, saleID)
		if err != nil {
			return err
		}
		var old *salesdoc.Line
		for i := range existing {
			if existing[i].ID == lineID {
				old = &existing[i]
			}
		}
		if old == nil {
			return httpx.NotFound("line not found on this sale")
		}
		if old.ParentLineID != nil {
			return invalid("line_id", "is a kit component: change the kit line, its components follow")
		}
		if old.LineType == salesdoc.LineText || old.Quantity == nil {
			return invalid("line_id", "has no quantity to change")
		}
		if old.WeightSource != nil {
			return invalid("line_id", "is weighed: the quantity is the scale's; remove the line and weigh again")
		}
		if sale.CustomerID == nil {
			walkInID, _, err := s.repo.WalkInCustomer(ctx)
			if err != nil {
				return err
			}
			sale.WalkInID = walkInID
		}
		line, priced, err := s.priceLine(ctx, sale, parsedFromLine(old, qty), actor)
		if err != nil {
			return err
		}
		line.ID = old.ID
		// whoever set the price keeps the credit for it: pressing plus does
		// not make the cashier the author of a manager's override
		line.PriceAdjustedBy = old.PriceAdjustedBy
		lines, err := s.explodeAndExtend(ctx, []salesdoc.Line{line})
		if err != nil {
			return err
		}
		// the line and its components take the places the old family held
		var places []int
		for i := range existing {
			if existing[i].ID == old.ID || (existing[i].ParentLineID != nil && *existing[i].ParentLineID == old.ID) {
				places = append(places, existing[i].Position)
			}
		}
		for i := range lines {
			if i < len(places) {
				lines[i].Position = places[i]
			} else {
				lines[i].Position = places[len(places)-1] + (i - len(places) + 1)
			}
		}
		if err := s.repo.RemoveLine(ctx, saleID, old.ID); err != nil {
			return err
		}
		if err := s.repo.AddLines(ctx, saleID, lines); err != nil {
			return err
		}
		if s.auditLog != nil {
			// an override that was already on the line has its row; only an
			// override this change brings (the engine's price moved with the
			// quantity) is new, and it names the line it is on
			if old.PriceSource != salesdoc.PriceSourceOverride {
				for _, e := range priced.overrides {
					e.Changes["line_id"] = old.ID
					if err := s.auditLog.Log(ctx, e); err != nil {
						return fmt.Errorf("failed to write the audit row: %w", err)
					}
				}
			}
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: auditLineQuantityChanged, EntityType: "pos_transaction", EntityID: sale.ID, UserID: actor,
				Changes: map[string]any{"line_id": old.ID, "product_id": old.ProductID,
					"from_quantity": old.Quantity.WireString(), "to_quantity": qty.WireString()}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		if err := s.recalcTotals(ctx, sale); err != nil {
			return err
		}
		out, err = s.GetSale(ctx, saleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// parsedFromLine turns a stored line back into the request line that made it,
// at a new quantity: what the cashier fixed (an override price and its reason,
// a discount, a charge's price) is carried, a list price is left to the engine.
// A paint line's tint (row CP-16) is carried whole: the formula the customer
// bought and the tint charge the dealer set belong to the line, not to the
// request that first made it, so a quantity change or a customer change
// reprices the line and keeps both.
func parsedFromLine(l *salesdoc.Line, qty httpx.Quantity) salesdoc.ParsedLine {
	pl := salesdoc.ParsedLine{
		LineType:    l.LineType,
		ProductID:   l.ProductID,
		SKU:         derefString(l.SKU),
		Description: l.Description,
		Quantity:    qty,
		UOM:         derefString(l.UOM),
	}
	if l.ProductID == nil || l.PriceSource == salesdoc.PriceSourceOverride || l.PriceSource == salesdoc.PriceSourceManual || l.LineType == salesdoc.LineCharge {
		pl.PriceUOM = derefString(l.PriceUOM)
		if l.UOMQty != nil {
			pl.UOMQty = *l.UOMQty
		}
		if l.PriceUOMQty != nil {
			pl.PriceUOMQty = *l.PriceUOMQty
		}
	}
	if l.Tally != nil && len(l.Tally.Rows) > 0 && l.Quantity != nil && qty == *l.Quantity {
		pl.TallyRows = l.Tally.Rows
	}
	if l.ChargeCode != nil {
		pl.ChargeCode = *l.ChargeCode
	}
	// a charge line and a non stock product line carry their own taxable
	// flag; a stocked product's comes from the product
	taxable := l.Taxable
	if l.LineType == salesdoc.LineCharge || l.ProductID == nil {
		pl.Taxable = &taxable
	}
	if l.PriceSource == salesdoc.PriceSourceOverride || l.PriceSource == salesdoc.PriceSourceManual || l.LineType == salesdoc.LineCharge {
		pl.UnitPrice = l.UnitPrice
		pl.OverrideReason = derefString(l.OverrideReason)
	}
	pl.DiscountPercent = l.DiscountPercent
	pl.DiscountAmount = l.DiscountAmount
	pl.DiscountReason = derefString(l.DiscountReason)
	// The tint rides the line (row CP-16). The charge the dealer set is
	// already inside the line's price, so on an override or manual line the
	// frozen tint charge is taken back out before priceLine runs and adds
	// the dealer's tint charge once.
	if l.Tint != nil {
		if pl.UnitPrice != nil && l.Tint.TintChargeCents != nil && *l.Tint.TintChargeCents > 0 {
			base := *pl.UnitPrice - httpx.Price(int64(*l.Tint.TintChargeCents)*100)
			if base < 0 {
				base = 0
			}
			pl.UnitPrice = &base
		}
		tint := *l.Tint
		tint.Components = append([]salesdoc.TintComponent(nil), l.Tint.Components...)
		pl.Tint = &tint
	}
	return pl
}

// RemoveLine removes a line from an open cart.
func (s *Service) RemoveLine(ctx context.Context, saleID, lineID uuid.UUID) (*Sale, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	var out *Sale
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		sale, err := s.repo.GetSale(ctx, saleID)
		if err != nil {
			return err
		}
		if sale.Status != StatusOpen && sale.Status != StatusHeld {
			return httpx.InvalidStateTransition(fmt.Sprintf("cannot remove a line from a %s sale", sale.Status.Status()))
		}
		lines, err := s.repo.GetLines(ctx, saleID)
		if err != nil {
			return err
		}
		for i := range lines {
			if lines[i].ID == lineID && lines[i].ParentLineID != nil {
				return invalid("line_id", "is a kit component: remove the kit line, its components follow")
			}
		}
		if err := s.repo.RemoveLine(ctx, saleID, lineID); err != nil {
			return err
		}
		if err := s.recalcTotals(ctx, sale); err != nil {
			return err
		}
		out, err = s.GetSale(ctx, saleID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetSale reads a sale with its lines and tenders, and, for a named
// customer, the exemption standing: the certificate that exempts the sale on
// the branch's business day, so the counter can say why there is no tax
// (CP-10). Nil when no certificate covers the day. A completed sale reads
// the day it was completed on and only when its posted tax was zero, so a
// taxed sale read later never shows a certificate added after it.
func (s *Service) GetSale(ctx context.Context, saleID uuid.UUID) (*Sale, error) {
	sale, err := s.repo.GetSale(ctx, saleID)
	if err != nil {
		return nil, err
	}
	if sale.Lines, err = s.repo.GetLines(ctx, saleID); err != nil {
		return nil, err
	}
	if sale.Tenders, err = s.repo.GetTenders(ctx, saleID); err != nil {
		return nil, err
	}
	if sale.CustomerID != nil && (sale.Status == StatusOpen || sale.Status == StatusHeld || (sale.Status == StatusCompleted && sale.TaxCents == 0)) {
		at := s.now()
		if sale.CompletedAt != nil {
			at = sale.CompletedAt.Time
		}
		day, err := s.repo.BranchLocalDate(ctx, sale.BranchID, at)
		if err != nil {
			return nil, err
		}
		if sale.TaxExemption, err = s.repo.CustomerExemption(ctx, *sale.CustomerID, day); err != nil {
			return nil, err
		}
	}
	return sale, nil
}

// recalcTotals recomputes the cart's subtotal and the live tax estimate.
// The estimate honours the attached customer's exemption (CP-10): when a
// certificate covers the branch's business day the tax is zero, so the tax
// the screen shows equals what completion posts. The standing itself is read
// by GetSale, so the counter can name the certificate.
func (s *Service) recalcTotals(ctx context.Context, sale *Sale) error {
	lines, err := s.repo.GetLines(ctx, sale.ID)
	if err != nil {
		return err
	}
	totals := salesdoc.SumTotals(lines)
	tax := s.estimateTax(ctx, sale, totals.TaxableCents)
	if sale.CustomerID != nil {
		exempt, err := s.exemptOn(ctx, sale.BranchID, *sale.CustomerID)
		if err != nil {
			return err
		}
		if exempt {
			tax = 0
		}
	}
	return s.repo.UpdateSaleTotals(ctx, sale.ID, int64(totals.SubtotalCents), tax, int64(totals.SubtotalCents)+tax)
}

func invalid(field, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusBadRequest, Code: httpx.CodeValidationFailed,
		Message: "one or more fields failed validation", Details: []httpx.FieldError{{Field: field, Message: message}}}
}

func lineField(name string) string { return "line." + name }

func conflict(code, message string) *httpx.Error {
	return &httpx.Error{Status: http.StatusConflict, Code: httpx.CodeConflict,
		Message: message, Details: []httpx.FieldError{httpx.Blocker(code, message)}}
}

func ptrQty(q httpx.Quantity) *httpx.Quantity { return &q }

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func actorPtr(actor string) *string {
	if actor == "" {
		return nil
	}
	return &actor
}

var errNotConfigured = errors.New("the counter's money path is not wired")

// ReceiptInvoice is the invoice behind a completed sale's receipt. The sale is
// read exactly as GetSale reads it, so a sale outside the caller's reach is
// the missing sale; an open, held or voided sale, and a sale with no invoice,
// is a 409 no_receipt. walkIn is true when the sale names no customer or the
// shared walk in customer, so a caller can refuse to send it anywhere.
func (s *Service) ReceiptInvoice(ctx context.Context, saleID uuid.UUID) (invoiceID uuid.UUID, walkIn bool, err error) {
	sale, err := s.repo.GetSale(ctx, saleID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if sale.Status != StatusCompleted || sale.InvoiceID == nil {
		return uuid.Nil, false, conflict("no_receipt", "this sale has no receipt: only a completed sale with an invoice has one")
	}
	if sale.CustomerID == nil {
		return *sale.InvoiceID, true, nil
	}
	walkInID, _, err := s.repo.WalkInCustomer(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	return *sale.InvoiceID, walkInID == *sale.CustomerID, nil
}
