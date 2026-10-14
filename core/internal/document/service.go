// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/dealerprofile"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"

	"github.com/johnfercher/maroto/v2"
	mimage "github.com/johnfercher/maroto/v2/pkg/components/image"
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

// branding is what the header of every form is built from. Every part is
// best effort: a form still prints when the profile, the logo or the yard
// cannot be read.
type branding struct {
	dealer *dealerprofile.Profile
	logo   *dealerprofile.Logo
	yard   *Yard
}

func (s *Service) branding(ctx context.Context, branch uuid.UUID) *branding {
	b := &branding{}
	if s.profiles != nil {
		if p, err := s.profiles.Get(ctx); err != nil {
			slog.Warn("document: dealer profile unreadable", "error", err)
		} else {
			b.dealer = p
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
	name := d.DisplayName()
	legal := ""
	if strings.TrimSpace(d.LegalName) != "" && d.LegalName != name {
		legal = d.LegalName
	}
	return joinLines(name, legal, d.Phone, d.Email, d.Website)
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

func newDoc() core.Maroto {
	return maroto.New(config.NewBuilder().WithPageNumber().Build())
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
// and name and contact lines on the left, the issuing yard's own name and
// address on the right, then the form's title.
func (b *branding) header(m core.Maroto, title string, titleSize float64) {
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
	block(m, b.dealerBlock(), b.yardBlock(), 9)
	m.AddRow(16, text.NewCol(12, title, props.Text{Size: titleSize, Style: fontstyle.Bold, Align: align.Center, Top: 4}))
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

func finish(m core.Maroto) ([]byte, error) {
	doc, err := m.Generate()
	if err != nil {
		return nil, err
	}
	return doc.GetBytes(), nil
}

// money renders an amount as a customer reads it: -$22.70, never $-22.70.
func money(c interface{ DecimalString() string }) string {
	d := c.DecimalString()
	if strings.HasPrefix(d, "-") {
		return "-$" + d[1:]
	}
	return "$" + d
}

func (s *Service) GenerateInvoicePDF(ctx context.Context, inv *invoice.Invoice, cust *customer.Customer) ([]byte, error) {
	b := s.branding(ctx, inv.BranchID)
	m := newDoc()

	title := "INVOICE"
	if inv.Status == invoice.InvoiceStatusVoid {
		title = "INVOICE - VOID"
	}
	b.header(m, title, 18)

	block(m,
		joinLines(fmt.Sprintf("Bill To: %s", cust.Name), fmt.Sprintf("Account: %s", cust.AccountNumber), deref(cust.Address)),
		joinLines(fmt.Sprintf("Invoice #: %s", inv.Number), fmt.Sprintf("Date: %s", inv.InvoiceDate)), 10)

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
			text.NewCol(3, fmt.Sprintf("$%s%s", line.UnitPrice.DecimalString(), priceUOM), props.Text{Size: 9, Align: align.Right}),
			text.NewCol(3, "$"+line.LineTotal.DecimalString(), props.Text{Size: 9, Align: align.Right}),
		)
	}

	// Subtotal and tax are shown separately, or the line extensions visibly do
	// not add up to TOTAL DUE; the tax as its own line is also a statutory
	// requirement in the GST/HST jurisdictions this product targets.
	m.AddRow(10,
		text.NewCol(12, "SUBTOTAL: $"+inv.SubtotalCents.DecimalString(), props.Text{Top: 5, Align: align.Right, Size: 10}),
	)

	taxLabel := "TAX"
	if inv.TaxRatePercent != nil {
		taxLabel = fmt.Sprintf("TAX @ %s%%", *inv.TaxRatePercent)
	}
	m.AddRow(10,
		text.NewCol(12, fmt.Sprintf("%s: $%s", taxLabel, inv.TaxCents.DecimalString()), props.Text{Align: align.Right, Size: 10}),
	)

	m.AddRow(15,
		text.NewCol(12, "TOTAL DUE: $"+inv.TotalCents.DecimalString(), props.Text{Top: 5, Style: fontstyle.Bold, Align: align.Right, Size: 12}),
	)
	b.remitRow(m)
	return finish(m)
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
	block(m, left, joinLines(fmt.Sprintf("Order #: %s", number), fmt.Sprintf("Date: %s", o.CreatedAt.Format("2006-01-02"))), 10)

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
	block(m, left, joinLines(fmt.Sprintf("From: %s", st.From), fmt.Sprintf("To: %s", st.To)), 10)
	aging := s.statementAging(ctx, st)

	for _, c := range st.Currencies {
		m.AddRow(10, text.NewCol(12, "Currency: "+c.Currency, props.Text{Style: fontstyle.Bold, Top: 3}))
		m.AddRow(8, text.NewCol(12, "Opening balance: "+money(c.OpeningBalanceCents), props.Text{Size: 10}))
		m.AddRow(8,
			text.NewCol(2, "Date", props.Text{Style: fontstyle.Bold, Size: 9}),
			text.NewCol(5, "Description", props.Text{Style: fontstyle.Bold, Size: 9}),
			text.NewCol(2, "Amount", props.Text{Style: fontstyle.Bold, Size: 9, Align: align.Right}),
			text.NewCol(3, "Balance", props.Text{Style: fontstyle.Bold, Size: 9, Align: align.Right}),
		)
		for _, l := range c.Lines {
			m.AddRow(8,
				text.NewCol(2, l.Date, props.Text{Size: 9}),
				text.NewCol(5, l.Description, props.Text{Size: 9}),
				text.NewCol(2, money(l.AmountCents), props.Text{Size: 9, Align: align.Right}),
				text.NewCol(3, money(l.BalanceAfterCents), props.Text{Size: 9, Align: align.Right}),
			)
		}
		m.AddRow(10, text.NewCol(12, "Closing balance: "+money(c.ClosingBalanceCents), props.Text{Style: fontstyle.Bold, Align: align.Right, Top: 2}))
		if t := aging[c.Currency]; t != nil {
			right := props.Text{Size: 9, Align: align.Right}
			rightBold := props.Text{Style: fontstyle.Bold, Size: 9, Align: align.Right}
			m.AddRow(10, text.NewCol(12, "Aging as of "+st.To, props.Text{Style: fontstyle.Bold, Top: 3}))
			m.AddRow(8,
				text.NewCol(2, "Current", rightBold),
				text.NewCol(2, "1-30", rightBold),
				text.NewCol(2, "31-60", rightBold),
				text.NewCol(2, "61-90", rightBold),
				text.NewCol(2, "Over 90", rightBold),
				text.NewCol(2, "Unapplied", rightBold),
			)
			m.AddRow(8,
				text.NewCol(2, money(t.CurrentCents), right),
				text.NewCol(2, money(t.Days1To30), right),
				text.NewCol(2, money(t.Days31To60), right),
				text.NewCol(2, money(t.Days61To90), right),
				text.NewCol(2, money(t.Over90), right),
				text.NewCol(2, money(t.UnappliedCents), right),
			)
			m.AddRow(8, text.NewCol(12, "Total: "+money(t.TotalCents), props.Text{Style: fontstyle.Bold, Size: 9, Align: align.Right}))
		}
		if len(c.UnappliedPayments) > 0 {
			m.AddRow(10, text.NewCol(12, "Unapplied cash", props.Text{Style: fontstyle.Bold, Top: 3}))
			for _, p := range c.UnappliedPayments {
				m.AddRow(8,
					text.NewCol(3, p.Number, props.Text{Size: 9}),
					text.NewCol(3, p.ReceivedOn, props.Text{Size: 9}),
					text.NewCol(6, money(p.UnappliedCents), props.Text{Size: 9, Align: align.Right}),
				)
			}
			m.AddRow(10, text.NewCol(12, "Net balance: "+money(c.NetBalanceCents), props.Text{Style: fontstyle.Bold, Align: align.Right, Top: 2}))
		}
		if len(c.OpenDocuments) > 0 {
			m.AddRow(10, text.NewCol(12, "Open documents", props.Text{Style: fontstyle.Bold, Top: 3}))
			for _, d := range c.OpenDocuments {
				due := ""
				if d.DueDate != nil {
					due = "due " + *d.DueDate
				}
				m.AddRow(8,
					text.NewCol(3, d.Number, props.Text{Size: 9}),
					text.NewCol(3, d.Date, props.Text{Size: 9}),
					text.NewCol(3, due, props.Text{Size: 9}),
					text.NewCol(3, money(d.OpenCents), props.Text{Size: 9, Align: align.Right}),
				)
			}
		}
	}
	// The documents listed follow the printing yard's reach; the balance and the
	// aging cover the whole account (FROM-PRINCIPAL 117): one line says so.
	if branch, grants := middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx); branch != nil || grants != nil {
		listed := false
		for _, c := range st.Currencies {
			listed = listed || len(c.OpenDocuments) > 0 || len(c.UnappliedPayments) > 0
		}
		if listed {
			m.AddRow(10, text.NewCol(12, "Documents listed are those this yard can see; the balance covers the whole account.", props.Text{Size: 8, Top: 3}))
		}
	}
	b.remitRow(m)
	return finish(m)
}
