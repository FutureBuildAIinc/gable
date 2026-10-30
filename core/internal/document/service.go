// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"fmt"
	"github.com/gablelbm/gable/internal/taxzone"
	"log/slog"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"

	"github.com/johnfercher/maroto/v2"
	mimage "github.com/johnfercher/maroto/v2/pkg/components/image"
	"github.com/johnfercher/maroto/v2/pkg/components/page"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/extension"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// Yard is the issuing yard's own identity as a form prints it: the branch
// location's name and address.
type Yard struct {
	Name    string
	Address string
	City    string
	State   string
	Zip     string
	Phone   string
	// Country is the branch's own country, else the dealer's default
	// (CP-17a): a CA branch's tax documents carry the GST/HST number.
	Country string
}

// YardReader reads a branch's yard block.
type YardReader interface {
	Yard(ctx context.Context, branch uuid.UUID) (*Yard, error)
}

// JobReader reads a job's name.
type JobReader interface {
	JobName(ctx context.Context, id uuid.UUID) (string, error)
}

// StatementReader is the AR statement the statement form prints, with the
// branch calendar the statement's default period is read on.
type StatementReader interface {
	Statement(ctx context.Context, q account.StatementQuery) (*account.Statement, error)
	AgingSummary(ctx context.Context, q account.AgingQuery) (*account.AgingSummary, error)
	LocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error)
	// BusinessDate is the calendar day of a moment in the branch's zone, the
	// default branch's when the branch is nil.
	BusinessDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error)
	Now() time.Time
}

// Service renders the forms. The profile, yard and job readers are optional:
// a form built without them prints what it has and omits the rest, which is
// how the unit tests and an unconfigured process behave.
type Service struct {
	productRepo product.Repository
	profiles    dealerprofile.Reader
	yards       YardReader
	jobs        JobReader
	statements  StatementReader
	payments    InvoicePaymentReader
}

func NewService(productRepo product.Repository) *Service {
	return &Service{productRepo: productRepo}
}

// WithProfile sets the dealer profile every form shows.
func (s *Service) WithProfile(r dealerprofile.Reader) *Service { s.profiles = r; return s }

// WithYards sets the reader of the issuing yard's block.
func (s *Service) WithYards(r YardReader) *Service { s.yards = r; return s }

// WithJobs sets the reader of a job's name.
func (s *Service) WithJobs(r JobReader) *Service { s.jobs = r; return s }

// DocumentKind says which document a print makes of an invoice.
type DocumentKind int

const (
	// DocumentInvoice is the invoice: a bill to pay, with the total due and
	// the remit to.
	DocumentInvoice DocumentKind = iota
	// DocumentReceipt is the receipt of a sale that took the money: the
	// tenders that paid it, no total due and no remit to (CP-14).
	DocumentReceipt
)

// SalePayment is the money a counter sale took: its tender legs as the paper
// shows them, the cash rounding the branch's rule made (CP-18), the change
// given back and the share of the total that went on account.
type SalePayment struct {
	Tenders       []ThermalTender
	RoundingCents httpx.Cents
	ChangeCents   httpx.Cents
	AccountCents  httpx.Cents
}

// InvoicePaymentReader reads the money the counter sale behind an invoice
// took; an invoice that is not a counter sale has none.
type InvoicePaymentReader interface {
	InvoicePayment(ctx context.Context, invoiceID uuid.UUID) (SalePayment, error)
}

// WithInvoicePayments sets the reader of a counter sale's money for the
// documents that print it.
func (s *Service) WithInvoicePayments(r InvoicePaymentReader) *Service { s.payments = r; return s }

// WithStatements sets the AR statement reader.
func (s *Service) WithStatements(r StatementReader) *Service { s.statements = r; return s }

// Statement reads the AR statement for the statement form.
func (s *Service) Statement(ctx context.Context, q account.StatementQuery) (*account.Statement, error) {
	if s.statements == nil {
		return nil, fmt.Errorf("document: no statement reader")
	}
	return s.statements.Statement(ctx, q)
}

// DealerName is the dealer's display name for an email's subject and
// signature; empty when no profile is wired or set.
func (s *Service) DealerName(ctx context.Context) string {
	if s.profiles == nil {
		return ""
	}
	p, err := s.profiles.Get(ctx)
	if err != nil {
		return ""
	}
	return p.DisplayName()
}

// momentDay is the day a moment prints as: the document's branch's own day
// (the AR core's and the dashboard's rule), the default branch's when the
// document has none. A form built without the reader prints the moment's own
// day.
func (s *Service) momentDay(ctx context.Context, branch uuid.UUID, at time.Time) time.Time {
	if s.statements == nil {
		return at
	}
	d, err := s.statements.BusinessDate(ctx, branch, at)
	if err != nil {
		slog.Warn("document: yard day unreadable", "branch_id", branch, "error", err)
		return at
	}
	return d
}

// StatementPeriod is a statement's period as the email names it, in the
// dealer's date style ("Jun 1, 2026 to Jun 30, 2026"), the same words the
// attachment prints.
func (s *Service) StatementPeriod(ctx context.Context, st *account.Statement) string {
	style := newStyle("")
	if s.profiles != nil {
		if p, err := s.profiles.Get(ctx); err == nil && p != nil {
			style = newStyle(p.Locale)
		}
	}
	return style.Day(st.From) + " to " + style.Day(st.To)
}

// branding is what the header of every form is built from. Every part is
// best effort: a form still prints when the profile, the logo or the yard
// cannot be read.
type branding struct {
	dealer *dealerprofile.Profile
	logo   *dealerprofile.Logo
	yard   *Yard
	// style writes every amount and date the form prints, in the dealer's locale.
	style style
}

func (s *Service) branding(ctx context.Context, branch uuid.UUID) *branding {
	b := &branding{style: newStyle("")}
	if s.profiles != nil {
		if p, err := s.profiles.Get(ctx); err != nil {
			slog.Warn("document: dealer profile unreadable", "error", err)
		} else {
			b.dealer = p
			b.style = newStyle(p.Locale)
		}
		if b.dealer != nil && b.dealer.LogoURL != nil {
			if l, err := s.profiles.GetLogo(ctx); err == nil && validLogo(l) {
				b.logo = l
			}
		}
	}
	if s.yards != nil && branch != uuid.Nil {
		if y, err := s.yards.Yard(ctx, branch); err != nil {
			slog.Warn("document: yard unreadable", "branch_id", branch, "error", err)
		} else {
			b.yard = y
		}
	}
	return b
}

func validLogo(l *dealerprofile.Logo) bool {
	if l == nil || len(l.Bytes) == 0 {
		return false
	}
	// Only headers are read: the dimension cap and the formats the writer
	// refuses keep a stored logo from being decoded, or failing, on every form.
	return dealerprofile.LogoEmbeddable(l.Bytes)
}

// joinLines drops the empty lines; a block is a slice of lines because the PDF
// writer wraps a multi line string at an unpredictable width, so every line
// of a block is written as a row of its own.
func joinLines(lines ...string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		for _, part := range strings.Split(l, "\n") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func (b *branding) dealerBlock() []string {
	if b.dealer == nil {
		return nil
	}
	d := b.dealer
	return joinLines(d.DisplayName(), d.Phone, d.Email, d.Website)
}

// registrationLine is the GST/HST line every Canadian tax document prints
// under the dealer block (CP-14 A1): the number as CRA writes it, or a plain
// notice when none is on file (item 364: never a refusal). A dealer marked
// not registered for GST/HST, and every US branch, print nothing.
func (b *branding) registrationLine() string {
	if b.dealer == nil || b.yard == nil || b.yard.Country != taxzone.CountryCanada || b.dealer.GSTHSTNotRegistered {
		return ""
	}
	if n := strings.TrimSpace(b.dealer.GSTHSTNumber); n != "" {
		return "GST/HST No. " + dealerprofile.FormatGSTHSTNumber(n)
	}
	return "GST/HST No.: not on file"
}

// taxLineText is one Canadian tax line as the documents print it:
// "GST 5%: $1.65".
func (b *branding) taxLineText(l taxzone.TaxLine) string {
	return fmt.Sprintf("%s %s%%: %s", l.Label, l.RatePercent, b.style.Money(httpx.Cents(l.Cents)))
}

// legalName is the dealer's legal name when the lockup does not already say
// it: the lockup leads with the trading name, so the legal name is its own
// labelled line, never a second lockup line.
func (b *branding) legalName() string {
	if b.dealer == nil {
		return ""
	}
	legal := strings.TrimSpace(b.dealer.LegalName)
	if legal == "" || legal == b.dealer.DisplayName() {
		return ""
	}
	return legal
}

func (b *branding) yardBlock() []string {
	if b.yard == nil {
		return nil
	}
	y := b.yard
	cityLine := strings.TrimSpace(strings.Join(nonEmpty(y.City, y.State, y.Zip), " "))
	return joinLines(y.Name, y.Address, cityLine, y.Phone)
}

func nonEmpty(parts ...string) []string {
	out := []string{}
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return out
}

func (b *branding) remitTo() string {
	if b.dealer == nil {
		return ""
	}
	return strings.TrimSpace(b.dealer.RemitTo)
}

// collector holds a document's rows until finish, so the last group of them
// (the totals, then the foot) can be placed on one page: a rule maroto has no
// call for. Rows have the fixed heights they were added with.
type collector struct {
	core.Maroto
	rows    []core.Row
	heights []float64
	// keepFrom is the index of the first row of the closing group; -1 until
	// a document marks it, when the closing group is the last row and the foot.
	keepFrom int
	// footFrom is the index of the foot's first row; -1 until it is written.
	footFrom int
}

func newDoc() *collector {
	return &collector{Maroto: maroto.New(config.NewBuilder().WithPageNumber().Build()), keepFrom: -1, footFrom: -1}
}

// AddRow buffers the row until finish.
func (c *collector) AddRow(rowHeight float64, cols ...core.Col) core.Row {
	r := row.New(rowHeight).Add(cols...)
	c.rows = append(c.rows, r)
	c.heights = append(c.heights, rowHeight)
	return r
}

// keepTogether marks the rows added from here on as the closing group: they
// and the foot go on one page.
func (c *collector) keepTogether() { c.keepFrom = len(c.rows) }

// maxGroupHeight is the tallest closing group moved to a page of its own; a
// taller one (a page's worth of rows) is written as it comes, with its last row
// and the foot kept together.
const maxGroupHeight = 200.0

// groupSlack is the room kept beyond a group's own height when asking whether
// it fits, for the rounding in maroto's own running height.
const groupSlack = 2.0

// flush writes every row, moving the closing group to a new page whole when
// it does not fit on the current one. With no mark the group is the last row
// and what follows it, so the foot never stands alone.
func (c *collector) flush() {
	start := c.keepFrom
	if start < 0 || start > len(c.rows) {
		// the last row before the foot, or the last row when there is none
		start = len(c.rows) - 1
		if c.footFrom > 0 {
			start = c.footFrom - 1
		}
	}
	if start < 0 {
		return
	}
	tail := 0.0
	for _, h := range c.heights[start:] {
		tail += h
	}
	// a marked group taller than the cap runs as it comes, but its last row
	// stays with the foot: fall back to the default group
	if tail > maxGroupHeight && c.footFrom > start {
		start = c.footFrom - 1
		tail = 0
		for _, h := range c.heights[start:] {
			tail += h
		}
	}
	c.Maroto.AddRows(c.rows[:start]...)
	if tail <= maxGroupHeight && !c.Maroto.FitlnCurrentPage(tail+groupSlack) {
		c.Maroto.AddPages(page.New())
	}
	c.Maroto.AddRows(c.rows[start:]...)
}

// rowHeight is the height of one line of a block.
const rowHeight = 5.5

// block writes two blocks of lines side by side, one row per line pair: the
// left block left aligned, the right block right aligned.
func block(m core.Maroto, left, right []string, size float64) {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for i := 0; i < n; i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		m.AddRow(rowHeight,
			text.NewCol(6, l, props.Text{Size: size}),
			text.NewCol(6, r, props.Text{Size: size, Align: align.Right}),
		)
	}
	m.AddRow(3)
}

// header writes the identity block every form opens with: the dealer's logo
// and name and contact lines on the left (with any lines a tax document adds
// under them, CP-14 A1), the issuing yard's own name and address on the
// right, then the form's title.
func (b *branding) header(m core.Maroto, title string, titleSize float64, underDealer ...string) {
	if b.logo != nil {
		ext := extension.Png
		if b.logo.ContentType == "image/jpeg" {
			ext = extension.Jpeg
		}
		m.AddRow(22,
			mimage.NewFromBytesCol(2, b.logo.Bytes, ext, props.Rect{Center: true, Percent: 90}),
			text.NewCol(10, ""),
		)
	}
	block(m, append(b.dealerBlock(), joinLines(underDealer...)...), b.yardBlock(), 9)
	m.AddRow(16, text.NewCol(12, title, props.Text{Size: titleSize, Style: fontstyle.Bold, Align: align.Center, Top: 4}))
}

// foot closes every form the same way: the dealer's legal name once, labelled,
// and the remit to beside it on the forms that ask for money.
func (b *branding) foot(m *collector, withRemit bool) {
	m.footFrom = len(m.rows)
	if legal := b.legalName(); legal != "" {
		m.AddRow(10, text.NewCol(12, "Legal name: "+legal, props.Text{Size: 9, Top: 4}))
	}
	if withRemit {
		b.remitRow(m)
	}
}

// remitRow writes the remit to block the documents that ask for money carry.
func (b *branding) remitRow(m core.Maroto) {
	r := b.remitTo()
	if r == "" {
		return
	}
	m.AddRow(10, text.NewCol(12, "REMIT TO:", props.Text{Size: 10, Top: 4, Style: fontstyle.Bold}))
	for _, l := range joinLines(r) {
		m.AddRow(rowHeight, text.NewCol(12, l, props.Text{Size: 10}))
	}
}

func finish(m *collector) ([]byte, error) {
	m.flush()
	doc, err := m.Generate()
	if err != nil {
		return nil, err
	}
	return doc.GetBytes(), nil
}

func (s *Service) GenerateInvoicePDF(ctx context.Context, inv *invoice.Invoice, cust *customer.Customer) ([]byte, error) {
	return s.GenerateDocumentPDF(ctx, inv, cust, DocumentInvoice)
}

// GenerateReceiptPDF renders the receipt of a sale that took the money: the
// tenders that paid it, no total due and no remit to (CP-14).
func (s *Service) GenerateReceiptPDF(ctx context.Context, inv *invoice.Invoice, cust *customer.Customer) ([]byte, error) {
	return s.GenerateDocumentPDF(ctx, inv, cust, DocumentReceipt)
}

// SaleDocumentKind says which document the counter sale behind an invoice
// prints by default: the receipt of a sale that took the money, the invoice
// of one that did not (a sale that went on account, or one the books have not
// settled).
func (s *Service) SaleDocumentKind(ctx context.Context, inv *invoice.Invoice) DocumentKind {
	pay := s.salePayment(ctx, inv.ID)
	if len(pay.Tenders) == 0 || pay.AccountCents > 0 {
		return DocumentInvoice
	}
	if inv.Status != invoice.InvoiceStatusPaid {
		return DocumentInvoice
	}
	return DocumentReceipt
}

// salePayment reads the money the counter sale behind an invoice took. An
// invoice that is not a counter sale, and a reader that is not wired, have
// none: the document prints no tenders and no rounding.
func (s *Service) salePayment(ctx context.Context, invoiceID uuid.UUID) SalePayment {
	if s.payments == nil {
		return SalePayment{}
	}
	pay, err := s.payments.InvoicePayment(ctx, invoiceID)
	if err != nil {
		return SalePayment{}
	}
	return pay
}

// GenerateDocumentPDF renders the invoice or the receipt of an invoice. The
// invoice is a bill: the total due and the remit to. The receipt is what a
// sale that took the money leaves behind: the tenders that paid it, the cash
// rounding the branch's rule made and the change, no total due and no remit
// to (CP-14).
func (s *Service) GenerateDocumentPDF(ctx context.Context, inv *invoice.Invoice, cust *customer.Customer, kind DocumentKind) ([]byte, error) {
	b := s.branding(ctx, inv.BranchID)
	m := newDoc()

	title := "INVOICE"
	switch {
	case kind == DocumentReceipt:
		title = "RECEIPT"
	case inv.Status == invoice.InvoiceStatusVoid:
		title = "INVOICE - VOID"
	}
	b.header(m, title, 18, b.registrationLine())
	// The receipt of a settled sale says so, under its heading: the money
	// was taken, so the document is not a bill.
	if kind == DocumentReceipt && inv.Status == invoice.InvoiceStatusPaid {
		m.AddRow(12, text.NewCol(12, "PAID", props.Text{Size: 12, Style: fontstyle.Bold, Align: align.Center, Top: 4}))
	}

	block(m,
		joinLines(fmt.Sprintf("Bill To: %s", cust.Name), fmt.Sprintf("Account: %s", cust.AccountNumber), deref(cust.Address)),
		joinLines(fmt.Sprintf("Invoice #: %s", inv.Number), fmt.Sprintf("Date: %s", b.style.Day(inv.InvoiceDate))), 10)

	m.AddRow(10,
		text.NewCol(4, "SKU / Description", props.Text{Style: fontstyle.Bold}),
		text.NewCol(2, "Qty", props.Text{Style: fontstyle.Bold, Align: align.Center}),
		text.NewCol(3, "Price", props.Text{Style: fontstyle.Bold, Align: align.Right}),
		text.NewCol(3, "Total", props.Text{Style: fontstyle.Bold, Align: align.Right}),
	)

	for i := range inv.Lines {
		line := &inv.Lines[i]
		desc := line.Description
		if line.SKU != nil && *line.SKU != "" {
			desc = fmt.Sprintf("%s - %s", *line.SKU, line.Description)
		}
		// A text line is a note; a kit's components are shown with their kit
		// at no charge; every other line prints its quantity, its price per
		// price unit and its extension, exactly as the invoice stores them.
		if line.Quantity == nil || line.UnitPrice == nil || line.LineTotal == nil {
			m.AddRow(10, text.NewCol(12, desc, props.Text{Size: 9}))
			continue
		}
		uom, priceUOM := "", ""
		if line.UOM != nil {
			uom = " " + *line.UOM
		}
		if line.PriceUOM != nil {
			priceUOM = "/" + *line.PriceUOM
		}
		m.AddRow(10,
			text.NewCol(4, desc, props.Text{Size: 9}),
			text.NewCol(2, line.Quantity.WireString()+uom, props.Text{Size: 9, Align: align.Center}),
			text.NewCol(3, b.style.UnitPrice(*line.UnitPrice)+priceUOM, props.Text{Size: 9, Align: align.Right}),
			text.NewCol(3, b.style.Money(line.LineTotal), props.Text{Size: 9, Align: align.Right}),
		)
		// The tint block (row CP-16) under its line: the colour, the base,
		// the container size and the colorant amounts, so the invoice lets
		// the customer reorder the colour.
		if line.Tint != nil {
			head := fmt.Sprintf("Tint: %s", line.Tint.ColorName)
			if line.Tint.ColorCode != "" {
				head += fmt.Sprintf(" (%s)", line.Tint.ColorCode)
			}
			head += fmt.Sprintf("  base %s  size %s", line.Tint.BaseCode, line.Tint.ContainerSizeCode)
			m.AddRow(5, text.NewCol(12, head, props.Text{Size: 8}))
			for _, c := range line.Tint.Components {
				m.AddRow(4, text.NewCol(12,
					fmt.Sprintf("  %s %s  %s", c.ColorantCode, c.ColorantName, c.Amount.String()),
					props.Text{Size: 8}))
			}
			if line.Tint.TintChargeCents != nil && *line.Tint.TintChargeCents > 0 {
				discounted := line.DiscountPercent != nil || line.DiscountAmount != nil
				m.AddRow(4, text.NewCol(12,
					fmt.Sprintf("  %s", tintChargeLine(b.style, *line.Tint.TintChargeCents, discounted)),
					props.Text{Size: 8}))
			}
		}
	}

	// Subtotal and tax are shown separately, or the line extensions visibly do
	// not add up to the total; the tax as its own line is also a statutory
	// requirement in the GST/HST jurisdictions this product targets.
	m.keepTogether()
	m.AddRow(10,
		text.NewCol(12, "SUBTOTAL: "+b.style.Money(inv.SubtotalCents), props.Text{Top: 5, Align: align.Right, Size: 10}),
	)

	// A Canadian invoice prints each frozen part as its own line, GST then
	// PST (BC's PSTR s. 85(1), CP-14 A1); every other invoice its one line.
	if lines := taxzone.Lines(inv.TaxRateParts); lines != nil {
		for _, l := range lines {
			m.AddRow(10, text.NewCol(12, b.taxLineText(l), props.Text{Align: align.Right, Size: 10}))
		}
	} else {
		taxLabel := "TAX"
		if inv.TaxRatePercent != nil {
			taxLabel = fmt.Sprintf("TAX @ %s%%", *inv.TaxRatePercent)
		}
		m.AddRow(10,
			text.NewCol(12, taxLabel+": "+b.style.Money(inv.TaxCents), props.Text{Align: align.Right, Size: 10}),
		)
	}

	// The invoice asks for the total due; the receipt states the total that
	// was taken (CP-14).
	totalLabel := "TOTAL DUE: "
	if kind == DocumentReceipt {
		totalLabel = "TOTAL: "
	}
	m.AddRow(15,
		text.NewCol(12, totalLabel+b.style.Money(inv.TotalCents), props.Text{Top: 5, Style: fontstyle.Bold, Align: align.Right, Size: 12}),
	)

	// The money the counter sale took: the cash rounding the branch's rule
	// made (CP-18), the tenders that paid the sale and the change given.
	pay := s.salePayment(ctx, inv.ID)
	if line := roundingLine(b, pay.RoundingCents); line != "" {
		m.AddRow(8, text.NewCol(12, line, props.Text{Align: align.Right, Size: 10}))
	}
	s.invoiceTenderRows(m, b, pay.Tenders)
	// The remit to is for the money a customer still owes; a receipt of a
	// settled sale has none to remit.
	b.foot(m, kind == DocumentInvoice)
	return finish(m)
}

// invoiceTenderRows adds the tender lines of a counter sale under its total:
// each leg's method (a card's brand and last four) and amount, and the change
// given. An invoice with no counter sale behind it prints none.
func (s *Service) invoiceTenderRows(m *collector, b *branding, legs []ThermalTender) {
	for _, t := range legs {
		m.AddRow(6, text.NewCol(12, tenderLabel(t)+": "+b.style.Money(t.AmountCents), props.Text{Align: align.Right, Size: 9}))
		if t.ChangeCents > 0 {
			m.AddRow(5, text.NewCol(12, "Change: "+b.style.Money(t.ChangeCents), props.Text{Align: align.Right, Size: 8}))
		}
	}
}

// roundingLine is the cash rounding line of a document (CP-18): the amount
// the branch's rule moved the cash by, with its direction, so the cash the
// drawer kept and the total the customer owes account for each other. No
// rounding is no line.
func roundingLine(b *branding, cents httpx.Cents) string {
	if cents == 0 {
		return ""
	}
	return "Cash rounding " + b.style.SignedMoney(cents)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func shipToLine(st *salesdoc.ShipToSnapshot) string {
	if st == nil {
		return ""
	}
	return strings.Join(nonEmpty(st.Line1, deref(st.Line2), st.City, strings.Join(nonEmpty(st.Region, st.PostalCode), " ")), ", ")
}

func (s *Service) jobName(ctx context.Context, id *uuid.UUID) string {
	if id == nil || s.jobs == nil {
		return ""
	}
	n, err := s.jobs.JobName(ctx, *id)
	if err != nil {
		slog.Warn("document: job unreadable", "job_id", *id, "error", err)
		return ""
	}
	return n
}

func (s *Service) GeneratePickTicketPDF(ctx context.Context, o *order.Order, cust *customer.Customer) ([]byte, error) {
	b := s.branding(ctx, o.BranchID)
	m := newDoc()
	b.header(m, "PICK TICKET", 24)

	number := o.Number
	if number == "" {
		number = o.ID.String()
	}
	job := "none"
	if n := s.jobName(ctx, o.JobID); n != "" {
		job = n
	} else if o.JobID != nil {
		job = o.JobID.String()
	}
	left := joinLines(fmt.Sprintf("Customer: %s", cust.Name), fmt.Sprintf("Job: %s", job))
	if site := shipToLine(o.ShipTo); site != "" {
		left = append(left, "Site: "+site)
	}
	block(m, left, joinLines(fmt.Sprintf("Order #: %s", number), fmt.Sprintf("Date: %s", b.style.Date(s.momentDay(ctx, o.BranchID, o.CreatedAt.Time)))), 10)

	m.AddRow(10,
		text.NewCol(8, "SKU / Description", props.Text{Style: fontstyle.Bold}),
		text.NewCol(4, "Qty to Pick", props.Text{Style: fontstyle.Bold, Align: align.Center}),
	)

	for i := range o.Lines {
		line := &o.Lines[i]
		// A pick ticket prints the goods: text notes and charge lines have
		// nothing to pick.
		if line.LineType != salesdoc.LineProduct && line.LineType != salesdoc.LineComponent {
			continue
		}
		desc := line.Description
		uom := "EA"
		if line.UOM != nil {
			uom = *line.UOM
		}
		if line.SKU != nil && *line.SKU != "" {
			desc = fmt.Sprintf("%s - %s", *line.SKU, desc)
		}

		m.AddRow(18,
			text.NewCol(8, desc, props.Text{Size: 12}),
			text.NewCol(4, fmt.Sprintf("%s [%s]", line.Quantity.WireString(), uom), props.Text{Size: 14, Style: fontstyle.Bold, Align: align.Center}),
		)
	}
	b.foot(m, false)
	return finish(m)
}

// statementAging reads the customer's aging as of the statement's last day,
// the buckets the AR Aging page shows, per currency. A statement for one job
// carries none (the aging is the account's), and a form built without a
// statement reader prints what it has.
func (s *Service) statementAging(ctx context.Context, st *account.Statement) map[string]*account.AgingTotal {
	out := map[string]*account.AgingTotal{}
	if s.statements == nil || st.JobID != nil {
		return out
	}
	asOf, err := time.Parse("2006-01-02", st.To)
	if err != nil {
		return out
	}
	cust := st.CustomerID
	sum, err := s.statements.AgingSummary(ctx, account.AgingQuery{GroupBy: "customer", AsOf: asOf, Basis: "due_date", CustomerID: &cust, WholeAccount: true})
	if err != nil {
		slog.Warn("document: statement aging unreadable", "customer_id", cust, "error", err)
		return out
	}
	for i := range sum.Totals {
		out[sum.Totals[i].Currency] = &sum.Totals[i]
	}
	// an account that owes nothing still draws its aging, with zeros
	for _, c := range st.Currencies {
		if out[c.Currency] == nil {
			out[c.Currency] = &account.AgingTotal{Currency: c.Currency}
		}
	}
	return out
}

// GenerateStatementPDF renders the customer's statement, in the shape the AR
// statement route computes it, under the identity of the customer's primary
// yard.
func (s *Service) GenerateStatementPDF(ctx context.Context, st *account.Statement, cust *customer.Customer) ([]byte, error) {
	b := s.branding(ctx, cust.PrimaryBranchID)
	m := newDoc()
	b.header(m, "STATEMENT", 18)

	left := joinLines(fmt.Sprintf("Customer: %s", cust.Name), fmt.Sprintf("Account: %s", cust.AccountNumber), deref(cust.Address))
	if n := s.jobName(ctx, st.JobID); n != "" {
		left = append(left, "Job: "+n)
	}
	block(m, left, joinLines(fmt.Sprintf("From: %s", b.style.Day(st.From)), fmt.Sprintf("To: %s", b.style.Day(st.To))), 10)
	aging := s.statementAging(ctx, st)

	for ci, c := range st.Currencies {
		m.AddRow(10, text.NewCol(12, "Currency: "+c.Currency, props.Text{Style: fontstyle.Bold, Top: 3}))
		m.AddRow(8, text.NewCol(12, "Opening balance: "+b.style.Money(c.OpeningBalanceCents), props.Text{Size: 10}))
		m.AddRow(8,
			text.NewCol(2, "Date", props.Text{Style: fontstyle.Bold, Size: 9}),
			text.NewCol(5, "Description", props.Text{Style: fontstyle.Bold, Size: 9}),
			text.NewCol(2, "Amount", props.Text{Style: fontstyle.Bold, Size: 9, Align: align.Right}),
			text.NewCol(3, "Balance", props.Text{Style: fontstyle.Bold, Size: 9, Align: align.Right}),
		)
		for _, l := range c.Lines {
			m.AddRow(8,
				text.NewCol(2, b.style.Day(l.Date), props.Text{Size: 9}),
				text.NewCol(5, l.Description, props.Text{Size: 9}),
				text.NewCol(2, b.style.Money(l.AmountCents), props.Text{Size: 9, Align: align.Right}),
				text.NewCol(3, b.style.Money(l.BalanceAfterCents), props.Text{Size: 9, Align: align.Right}),
			)
		}
		if ci == len(st.Currencies)-1 {
			// the closing balance, the aging, any unapplied cash and open
			// documents go with the foot: the last currency's closing group.
			// Earlier currencies of a multi currency statement are not
			// marked and still break where the page ends. A group past the
			// cap (about 15 open documents) falls back to the last row with
			// the foot.
			m.keepTogether()
		}
		m.AddRow(10, text.NewCol(12, "Closing balance: "+b.style.Money(c.ClosingBalanceCents), props.Text{Style: fontstyle.Bold, Align: align.Right, Top: 2}))
		if t := aging[c.Currency]; t != nil {
			right := props.Text{Size: 9, Align: align.Right}
			rightBold := props.Text{Style: fontstyle.Bold, Size: 9, Align: align.Right}
			m.AddRow(10, text.NewCol(12, "Aging as of "+b.style.Day(st.To), props.Text{Style: fontstyle.Bold, Top: 3}))
			m.AddRow(8,
				text.NewCol(2, "Current", rightBold),
				text.NewCol(2, "1-30", rightBold),
				text.NewCol(2, "31-60", rightBold),
				text.NewCol(2, "61-90", rightBold),
				text.NewCol(2, "Over 90", rightBold),
				text.NewCol(2, "Unapplied", rightBold),
			)
			m.AddRow(8,
				text.NewCol(2, b.style.Money(t.CurrentCents), right),
				text.NewCol(2, b.style.Money(t.Days1To30), right),
				text.NewCol(2, b.style.Money(t.Days31To60), right),
				text.NewCol(2, b.style.Money(t.Days61To90), right),
				text.NewCol(2, b.style.Money(t.Over90), right),
				text.NewCol(2, b.style.Money(t.UnappliedCents), right),
			)
			m.AddRow(8, text.NewCol(12, "Total: "+b.style.Money(t.TotalCents), props.Text{Style: fontstyle.Bold, Size: 9, Align: align.Right}))
		}
		if len(c.UnappliedPayments) > 0 {
			m.AddRow(10, text.NewCol(12, "Unapplied cash", props.Text{Style: fontstyle.Bold, Top: 3}))
			for _, p := range c.UnappliedPayments {
				m.AddRow(8,
					text.NewCol(3, p.Number, props.Text{Size: 9}),
					text.NewCol(3, b.style.Day(p.ReceivedOn), props.Text{Size: 9}),
					text.NewCol(6, b.style.Money(p.UnappliedCents), props.Text{Size: 9, Align: align.Right}),
				)
			}
			m.AddRow(10, text.NewCol(12, "Net balance: "+b.style.Money(c.NetBalanceCents), props.Text{Style: fontstyle.Bold, Align: align.Right, Top: 2}))
		}
		if len(c.OpenDocuments) > 0 {
			m.AddRow(10, text.NewCol(12, "Open documents", props.Text{Style: fontstyle.Bold, Top: 3}))
			for _, d := range c.OpenDocuments {
				due := ""
				if d.DueDate != nil {
					due = "due " + b.style.Day(*d.DueDate)
				}
				m.AddRow(8,
					text.NewCol(3, d.Number, props.Text{Size: 9}),
					text.NewCol(3, b.style.Day(d.Date), props.Text{Size: 9}),
					text.NewCol(3, due, props.Text{Size: 9}),
					text.NewCol(3, b.style.Money(d.OpenCents), props.Text{Size: 9, Align: align.Right}),
				)
			}
		}
	}
	// The open documents and unapplied payments follow the caller's reach; the
	// activity and the balances cover the whole account (FROM-PRINCIPAL 117):
	// whenever the caller is scoped, a line says so, and names the unapplied
	// cash the caller's yards cannot see.
	branch, grants := middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)
	if branch != nil || grants != nil {
		for _, c := range st.Currencies {
			elsewhere := c.UnappliedCents
			for _, p := range c.UnappliedPayments {
				elsewhere -= p.UnappliedCents
			}
			if elsewhere > 0 {
				label := "Unapplied cash at other yards: "
				if len(st.Currencies) > 1 {
					label = "Unapplied cash at other yards (" + c.Currency + "): "
				}
				m.AddRow(8, text.NewCol(12, label+b.style.Money(elsewhere), props.Text{Size: 9, Top: 3}))
			}
		}
		whose := "this yard's"
		if branch == nil {
			whose = "those of your yards"
		}
		m.AddRow(10, text.NewCol(12, "Open documents and unapplied payments are "+whose+"; the activity and the balances cover the whole account.", props.Text{Size: 8, Top: 3}))
	}
	b.foot(m, true)
	return finish(m)
}
