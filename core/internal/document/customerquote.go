// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package document

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // registered so a stored JPEG logo is read
	_ "image/png"  // registered so a stored PNG logo is read
	"strings"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"

	mimage "github.com/johnfercher/maroto/v2/pkg/components/image"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/extension"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// CustomerQuotePDF renders the contractor's quote to their customer under the
// contractor's own mark. It is built from the public view alone (the
// allowlisted type of price wall 2), so a dealer's cost, a markup or a
// contractor only line has no way onto the page: the dealer's own letterhead
// does not appear either. Amounts and dates are written in the dealer
// deployment's locale, like every other form.
func (s *Service) CustomerQuotePDF(ctx context.Context, v *customerquote.PublicView, logo *customerquote.Logo) ([]byte, error) {
	b := s.branding(ctx, uuid.Nil)
	m := newDoc()

	if logo != nil && len(logo.Bytes) > 0 {
		if _, _, err := image.DecodeConfig(bytes.NewReader(logo.Bytes)); err == nil {
			ext := extension.Png
			if logo.ContentType == "image/jpeg" {
				ext = extension.Jpeg
			}
			m.AddRow(22,
				mimage.NewFromBytesCol(2, logo.Bytes, ext, props.Rect{Center: true, Percent: 90}),
				text.NewCol(10, ""),
			)
		}
	}
	block(m, joinLines(v.Contractor.Name),
		joinLines("Quote #: "+v.Reference, "Valid until: "+b.style.Date(v.ValidUntil.Time)), 10)
	m.AddRow(16, text.NewCol(12, "QUOTE", props.Text{Size: 18, Style: fontstyle.Bold, Align: align.Center, Top: 4}))

	left := joinLines("Prepared for: "+v.CustomerName, "Job site: "+v.JobSiteAddress, v.Title)
	block(m, left, nil, 10)

	money := func(c int64) string { return b.style.Money(httpx.Cents(c)) }
	showPrices := !v.HidePrices

	m.AddRow(10,
		text.NewCol(7, "Description", props.Text{Style: fontstyle.Bold}),
		text.NewCol(2, "Qty", props.Text{Style: fontstyle.Bold, Align: align.Center}),
		text.NewCol(3, priceHead(showPrices), props.Text{Style: fontstyle.Bold, Align: align.Right}),
	)
	groupLabel := map[uuid.UUID]string{}
	for _, g := range v.Groups {
		groupLabel[g.ID] = g.Label
	}
	for _, l := range v.Lines {
		desc := l.Description
		if l.GroupID != nil {
			desc += " (option: " + groupLabel[*l.GroupID] + ")"
		}
		if l.Selection == string(customerquote.SelectionDeclined) {
			desc += " - declined"
		}
		price := ""
		if showPrices && l.PriceCents != nil {
			price = money(*l.PriceCents)
		}
		m.AddRow(10,
			text.NewCol(7, desc, props.Text{Size: 9}),
			text.NewCol(2, l.Quantity.WireString()+" "+l.UOM, props.Text{Size: 9, Align: align.Center}),
			text.NewCol(3, price, props.Text{Size: 9, Align: align.Right}),
		)
	}
	for _, e := range v.Extras {
		price := ""
		if showPrices && e.PriceCents != nil {
			price = money(*e.PriceCents)
		}
		m.AddRow(10,
			text.NewCol(9, e.Description, props.Text{Size: 9}),
			text.NewCol(3, price, props.Text{Size: 9, Align: align.Right}),
		)
	}
	m.keepTogether()
	m.AddRow(8, text.NewCol(12, "SUBTOTAL: "+money(v.Totals.SubtotalCents), props.Text{Align: align.Right, Size: 10, Top: 3}))
	if v.Totals.TaxMode == customerquote.TaxSiteRate {
		m.AddRow(8, text.NewCol(12, "TAX: "+money(v.Totals.TaxCents), props.Text{Align: align.Right, Size: 10}))
	}
	m.AddRow(15, text.NewCol(12, "TOTAL: "+money(v.Totals.TotalCents), props.Text{Top: 4, Style: fontstyle.Bold, Align: align.Right, Size: 12}))
	if v.Totals.DepositCents > 0 {
		m.AddRow(8, text.NewCol(12, "DEPOSIT: "+money(v.Totals.DepositCents), props.Text{Align: align.Right, Size: 10}))
	}
	m.AddRow(8, text.NewCol(12, v.Totals.TaxNote, props.Text{Align: align.Right, Size: 9}))
	if v.PaymentTerms != "" {
		m.AddRow(10, text.NewCol(12, "Payment terms: "+v.PaymentTerms, props.Text{Size: 9, Top: 3}))
	}
	if v.Note != "" {
		for _, line := range joinLines(v.Note) {
			m.AddRow(rowHeight, text.NewCol(12, line, props.Text{Size: 9}))
		}
	}
	if v.Approval != nil {
		m.AddRow(10, text.NewCol(12, fmt.Sprintf("%s by %s on %s", titleCase(v.Approval.Decision), v.Approval.SignedBy, b.style.Date(v.Approval.At.Time)),
			props.Text{Size: 9, Top: 3}))
	}
	m.footFrom = len(m.rows)
	return finish(m)
}

func priceHead(show bool) string {
	if show {
		return "Price"
	}
	return ""
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
