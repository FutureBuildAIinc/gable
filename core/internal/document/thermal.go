// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"

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

// thermalPageWidthMM is the paper width of an 80mm thermal roll.
// thermalMarginMM is the side margin so the printable width is 72mm, the
// figure a 72mm print head cuts.
const (
	thermalPageWidthMM = 80.0
	thermalMarginMM    = 4.0
	thermalMarginTop   = 4.0
	thermalMarginBot   = 4.0
)

// thermalGridSize is the column grid every row sums to. With 4mm margins the
// printable width is 72mm, so each grid unit is 72 / thermalGridSize millimetres.
const thermalGridSize = 12

// Heights used by every row. They are explicit so the page height can be set
// to the sum of them, never larger than the content needs.
const (
	thermalHairline = 1.5
	thermalRowTiny  = 3.0
	thermalRowSmall = 3.6
	thermalRowBody  = 4.0
	thermalRowMed   = 4.6
	thermalRowLarge = 6.0
	thermalRowSale  = 7.0
)

// charsPerLineFull is the character budget a description has at the receipt's
// printable width for 7pt Arial. The estimate is conservative so a long line
// wraps before it overflows the print head.
const charsPerLineFull = 50

// ThermalReceipt is the plain input the counter hands the receipt renderer:
// the sale number, the moment it sold, the cashier, the customer or walk in,
// the PO and job when present, each line (description, quantity, unit, unit
// price, extension), the subtotal, tax, total, each tender leg (method,
// amount, last four for a card, change from cash), and the account balance
// when the sale went on account. A signature and a reprint marker carry
// their own blocks at the head and foot of the paper.
type ThermalReceipt struct {
	SaleNumber    string
	SoldAt        time.Time
	Cashier       string
	Customer      string // empty when the customer is the walk in
	AccountNumber string // empty when the customer is the walk in
	PONumber      string // empty when none
	JobName       string // empty when none
	Lines         []ThermalLine
	SubtotalCents httpx.Cents
	TaxCents      httpx.Cents
	TaxLabel      string // "TAX", "HST", "Sales tax"; empty prints "Tax"
	TotalCents    httpx.Cents
	// RoundingCents is the cash rounding the branch's rule made on this
	// sale: the difference between the cash the drawer kept and the exact
	// cash the sale needed. It prints only when it is not zero, the way the
	// change prints only when there is change.
	RoundingCents httpx.Cents
	Tenders       []ThermalTender
	BalanceCents  httpx.Cents       // 0 when the sale did not go on account
	Signature     *ThermalSignature // nil when none captured
	Reprint       *ThermalReprint   // nil for the first print
}

// ThermalLine is one sold line as the paper prints it: the product's own
// SKU and description, the quantity with its unit, the unit price with its
// price unit, and the extension in cents. A weighed line (CP-19) also
// carries its ticket number and the gross, tare and net the ticket proves;
// a typed weight prints the Handbook 44 marking MAN WT.
type ThermalLine struct {
	SKU         string
	Description string
	Quantity    httpx.Quantity
	UOM         string
	UnitPrice   httpx.Price
	PriceUOM    string
	LineTotal   httpx.Cents

	ScaleTicket   string
	WeightGrossLb *httpx.Quantity
	WeightTareLb  *httpx.Quantity
	WeightNetLb   *httpx.Quantity
	ManualWeight  bool
}

// ThermalTender is one tender leg: the method, the amount tendered, the
// last four for a card, and the change due back to the customer when a
// cash over-tender produces one. A zero change and a blank last four print
// nothing beyond the amount.
type ThermalTender struct {
	Method      string
	AmountCents httpx.Cents
	LastFour    string // empty when not a card
	ChangeCents httpx.Cents // 0 when no change due
}

// ThermalSignature is the signature the ring captured at sale time: the
// signer's name, the moment, the seal's first twelve hex characters, and
// optionally the captured PNG. A nil pointer prints nothing; the receipt
// prints a blank signature line only when the sale went on account.
type ThermalSignature struct {
	Name       string
	SignedAt   time.Time
	SealPrefix string // first twelve hex characters of the seal
	PNG        []byte // optional
}

// ThermalReprint carries the print number for a reprint: 2 for the second
// print (the first reprint), 3 for the third, and so on, matching
// ReceiptPrint.Number. A nil pointer renders the original print, with no
// banner at the head of the paper.
type ThermalReprint struct {
	PrintNumber int
}

// GenerateThermalReceiptPDF renders the 80mm thermal receipt for one
// committed counter sale, in the package's PDF library and style. The
// function takes a plain input struct so the counter service owns the
// lookups; the package only formats the data.
func (s *Service) GenerateThermalReceiptPDF(ctx context.Context, branch uuid.UUID, r ThermalReceipt) ([]byte, error) {
	b := s.branding(ctx, branch)

	type planned struct {
		height float64
		cols   []core.Col
	}
	var rows []planned
	add := func(height float64, cols ...core.Col) {
		rows = append(rows, planned{height: height, cols: cols})
	}

	// Reprint banner at the top.
	if r.Reprint != nil {
		add(thermalRowLarge, text.NewCol(thermalGridSize, fmt.Sprintf("REPRINT  #%d", r.Reprint.PrintNumber), props.Text{Size: 12, Style: fontstyle.Bold, Align: align.Center}))
		add(thermalHairline, text.NewCol(thermalGridSize, strings.Repeat("-", 32), props.Text{Size: 7, Align: align.Center}))
	}

	// Dealer identity block.
	if b.dealer != nil {
		if name := b.dealer.DisplayName(); name != "" {
			add(thermalRowMed, text.NewCol(thermalGridSize, name, props.Text{Size: 11, Style: fontstyle.Bold, Align: align.Center}))
		}
		if db := b.dealerBlock(); len(db) > 1 {
			for _, l := range db[1:] {
				add(thermalRowTiny, text.NewCol(thermalGridSize, l, props.Text{Size: 7, Align: align.Center}))
			}
		}
	}

	// Yard identity block.
	if b.yard != nil {
		for _, l := range b.yardBlock() {
			add(thermalRowTiny, text.NewCol(thermalGridSize, l, props.Text{Size: 7, Align: align.Center}))
		}
	}
	add(thermalHairline, text.NewCol(thermalGridSize, strings.Repeat("-", 32), props.Text{Size: 7, Align: align.Center}))

	// Sale number, date, cashier.
	add(thermalRowSale, text.NewCol(thermalGridSize, r.SaleNumber, props.Text{Size: 14, Style: fontstyle.Bold, Align: align.Center}))
	add(thermalRowSmall, text.NewCol(thermalGridSize, b.style.Date(r.SoldAt), props.Text{Size: 8, Align: align.Center}))
	if r.Cashier != "" {
		add(thermalRowTiny, text.NewCol(thermalGridSize, "Served by "+r.Cashier, props.Text{Size: 7, Align: align.Center}))
	}
	add(thermalHairline, text.NewCol(thermalGridSize, strings.Repeat("-", 32), props.Text{Size: 7, Align: align.Center}))

	// Customer / walk in / PO / job.
	if r.Customer != "" {
		add(thermalRowBody, text.NewCol(thermalGridSize, r.Customer, props.Text{Size: 8}))
		if r.AccountNumber != "" {
			add(thermalRowTiny, text.NewCol(thermalGridSize, r.AccountNumber, props.Text{Size: 7}))
		}
	} else {
		add(thermalRowBody, text.NewCol(thermalGridSize, "Walk-in", props.Text{Size: 8}))
	}
	if r.PONumber != "" {
		add(thermalRowTiny, text.NewCol(thermalGridSize, "PO: "+r.PONumber, props.Text{Size: 7}))
	}
	if r.JobName != "" {
		add(thermalRowTiny, text.NewCol(thermalGridSize, "Job: "+r.JobName, props.Text{Size: 7}))
	}
	add(thermalHairline, text.NewCol(thermalGridSize, strings.Repeat("-", 32), props.Text{Size: 7, Align: align.Center}))

	// Lines: description (left, may wrap), qty + price (right), extension (right).
	for _, l := range r.Lines {
		desc := l.Description
		if l.SKU != "" {
			desc = l.SKU + " - " + l.Description
		}
		for _, d := range wrapForWidth(desc, charsPerLineFull) {
			add(thermalRowTiny, text.NewCol(thermalGridSize, d, props.Text{Size: 7}))
		}
		qty := strings.TrimSpace(l.Quantity.WireString() + " " + l.UOM)
		price := b.style.UnitPrice(l.UnitPrice) + "/" + l.PriceUOM
		ext := b.style.Money(l.LineTotal)
		rightSide := qty + "    " + price + "    " + ext
		add(thermalRowTiny, text.NewCol(thermalGridSize, rightSide, props.Text{Size: 7, Align: align.Right}))
		// The weighed line's ticket line (CP-19): the marking the handbook
		// names for a typed weight, the ticket number, and the gross, tare
		// and net the ticket proves.
		if l.ScaleTicket != "" {
			mark := ""
			if l.ManualWeight {
				mark = "MAN WT "
			}
			weigh := ""
			if l.WeightGrossLb != nil && l.WeightTareLb != nil && l.WeightNetLb != nil {
				weigh = "  G " + l.WeightGrossLb.WireString() + " lb  T " + l.WeightTareLb.WireString() +
					" lb  N " + l.WeightNetLb.WireString() + " lb"
			}
			add(thermalRowTiny, text.NewCol(thermalGridSize, mark+l.ScaleTicket+weigh, props.Text{Size: 7, Align: align.Right}))
		}
	}
	add(thermalHairline, text.NewCol(thermalGridSize, strings.Repeat("-", 32), props.Text{Size: 7, Align: align.Center}))

	// Totals.
	add(thermalRowBody, text.NewCol(thermalGridSize, "Subtotal: "+b.style.Money(r.SubtotalCents), props.Text{Size: 9, Align: align.Right}))
	label := r.TaxLabel
	if label == "" {
		label = "Tax"
	}
	add(thermalRowBody, text.NewCol(thermalGridSize, label+": "+b.style.Money(r.TaxCents), props.Text{Size: 9, Align: align.Right}))
	add(thermalRowLarge, text.NewCol(thermalGridSize, "Total: "+b.style.Money(r.TotalCents), props.Text{Size: 12, Style: fontstyle.Bold, Align: align.Right}))
	if r.RoundingCents != 0 {
		add(thermalRowBody, text.NewCol(thermalGridSize, "Cash rounding: "+b.style.Money(r.RoundingCents), props.Text{Size: 9, Align: align.Right}))
	}
	add(thermalHairline, text.NewCol(thermalGridSize, strings.Repeat("-", 32), props.Text{Size: 7, Align: align.Center}))

	// Tenders.
	for _, t := range r.Tenders {
		label := strings.TrimSpace(t.Method)
		if t.LastFour != "" {
			label = label + "  ****" + t.LastFour
		}
		add(thermalRowBody, text.NewCol(thermalGridSize, label, props.Text{Size: 8}))
		add(thermalRowTiny, text.NewCol(thermalGridSize, b.style.Money(t.AmountCents), props.Text{Size: 8, Align: align.Right}))
		if t.ChangeCents > 0 {
			add(thermalRowTiny, text.NewCol(thermalGridSize, "Change: "+b.style.Money(t.ChangeCents), props.Text{Size: 7, Align: align.Right}))
		}
	}

	// Balance due on account.
	if r.BalanceCents > 0 {
		add(thermalRowMed, text.NewCol(thermalGridSize, "Balance due: "+b.style.Money(r.BalanceCents), props.Text{Size: 9, Style: fontstyle.Bold, Align: align.Right}))
	}

	// Signature block or blank line for an on-account sale.
	if r.Signature != nil {
		add(thermalHairline, text.NewCol(thermalGridSize, strings.Repeat("-", 32), props.Text{Size: 7, Align: align.Center}))
		add(thermalRowTiny, text.NewCol(thermalGridSize, "Signed by "+r.Signature.Name, props.Text{Size: 7}))
		add(thermalRowTiny, text.NewCol(thermalGridSize, "at "+b.style.Date(r.Signature.SignedAt)+", sealed "+r.Signature.SealPrefix, props.Text{Size: 7}))
		if r.Signature.PNG != nil {
			add(18.0, mimage.NewFromBytesCol(thermalGridSize, r.Signature.PNG, extension.Png, props.Rect{Center: true, Percent: 60}))
		}
	} else if r.BalanceCents > 0 {
		add(thermalHairline, text.NewCol(thermalGridSize, strings.Repeat("-", 32), props.Text{Size: 7, Align: align.Center}))
		add(thermalRowBody, text.NewCol(thermalGridSize, "Signature: ____________________", props.Text{Size: 8}))
	}

	// Total content height + the page margins + a 2mm footer gap.
	total := 0.0
	for _, p := range rows {
		total += p.height
	}
	pageHeight := total + thermalMarginTop + thermalMarginBot + 2.0

	cfg := config.NewBuilder().
		WithDimensions(thermalPageWidthMM, pageHeight).
		WithLeftMargin(thermalMarginMM).
		WithTopMargin(thermalMarginTop).
		WithRightMargin(thermalMarginMM).
		WithBottomMargin(thermalMarginBot).
		WithMaxGridSize(thermalGridSize).
		Build()

	m := maroto.New(cfg)
	for _, p := range rows {
		m.AddRow(p.height, p.cols...)
	}
	doc, err := m.Generate()
	if err != nil {
		return nil, err
	}
	return doc.GetBytes(), nil
}

// wrapForWidth wraps s at word boundaries so no line exceeds maxChars. A word
// longer than maxChars is hard-broken so the paper never overflows. An
// empty string or a string already within budget returns a one-element
// slice.
func wrapForWidth(s string, maxChars int) []string {
	if maxChars <= 0 || len(s) <= maxChars {
		return []string{s}
	}
	var out []string
	for len(s) > maxChars {
		cut := strings.LastIndex(s[:maxChars+1], " ")
		if cut <= 0 {
			cut = maxChars
		}
		out = append(out, strings.TrimRight(s[:cut], " "))
		s = strings.TrimLeft(s[cut:], " ")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}