// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// The contractor's one click export of their own customers, projects and
// quotes (ruling 5a). It is the contractor's own data, markup and margin
// included, and it is read by the contractor alone.

// ExportCustomer is one of the contractor's customers, as the quotes name them.
type ExportCustomer struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	QuoteCount int    `json:"quote_count"`
	LastQuote  string `json:"last_quote_reference"`
}

// Export is the whole export.
type Export struct {
	Customers []ExportCustomer `json:"customers"`
	Projects  []ExportProject  `json:"projects"`
	Quotes    []*QuoteView     `json:"quotes"`
}

const exportPage = 200

// Export reads everything the contractor account owns here.
func (s *Service) Export(ctx context.Context, a Actor) (*Export, error) {
	if err := s.needQuoting(a); err != nil {
		return nil, err
	}
	now := s.now()
	out := &Export{Customers: []ExportCustomer{}, Quotes: []*QuoteView{}}
	var f ListFilter
	f.Limit = exportPage
	index := map[string]int{}
	for {
		page, err := s.store.ListQuotes(ctx, a.CustomerID, f)
		if err != nil {
			return nil, err
		}
		more := len(page) > f.Limit
		if more {
			page = page[:f.Limit]
		}
		for _, q := range page {
			order, err := s.OrderOf(ctx, q)
			if err != nil {
				return nil, err
			}
			v, err := BuildQuoteView(q, now, nil, order)
			if err != nil {
				return nil, err
			}
			out.Quotes = append(out.Quotes, v)
			key := strings.ToLower(q.EndCustomer.Name + "\x00" + q.EndCustomer.Email + "\x00" + q.EndCustomer.Phone)
			if i, ok := index[key]; ok {
				out.Customers[i].QuoteCount++
			} else {
				index[key] = len(out.Customers)
				out.Customers = append(out.Customers, ExportCustomer{Name: q.EndCustomer.Name, Email: q.EndCustomer.Email,
					Phone: q.EndCustomer.Phone, QuoteCount: 1, LastQuote: q.Reference})
			}
		}
		if !more {
			break
		}
		last := page[len(page)-1]
		t := last.CreatedAt
		f.AfterTime, f.AfterID = &t, last.ID
	}
	projects, err := s.store.ListProjects(ctx, a.CustomerID)
	if err != nil {
		return nil, err
	}
	out.Projects = projects
	return out, nil
}

// csvCell defuses a spreadsheet formula: a cell that starts with a formula
// character is prefixed with a quote.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func cents(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

// WriteCSV writes one table of the export as CSV: quotes, customers or projects.
func (e *Export) WriteCSV(w io.Writer, kind string) error {
	cw := csv.NewWriter(w)
	var rows [][]string
	switch kind {
	case "customers":
		rows = append(rows, []string{"name", "email", "phone", "quote_count", "last_quote_reference"})
		for _, c := range e.Customers {
			rows = append(rows, []string{csvCell(c.Name), csvCell(c.Email), csvCell(c.Phone), strconv.Itoa(c.QuoteCount), c.LastQuote})
		}
	case "projects":
		rows = append(rows, []string{"id", "name", "status", "created_at"})
		for _, p := range e.Projects {
			rows = append(rows, []string{p.ID.String(), csvCell(p.Name), p.Status, p.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z")})
		}
	case "quotes":
		rows = append(rows, []string{"reference", "title", "status", "customer", "customer_email", "customer_phone", "job_site_address",
			"material_cost", "material_sell", "labour", "overhead", "subtotal", "tax", "total", "deposit", "contractor_cost", "margin", "margin_bps",
			"valid_until", "order_status"})
		for _, q := range e.Quotes {
			os := ""
			if q.Order != nil {
				os = q.Order.Fulfilment
			}
			t := q.Totals
			rows = append(rows, []string{q.Reference, csvCell(q.Title), q.EffectiveStatus, csvCell(q.EndCustomer.Name), csvCell(q.EndCustomer.Email),
				csvCell(q.EndCustomer.Phone), csvCell(q.JobSiteAddress), cents(t.MaterialCostCents), cents(t.MaterialSellCents), cents(t.LabourCents),
				cents(t.OverheadCents), cents(t.SubtotalCents), cents(t.TaxCents), cents(t.TotalCents), cents(t.DepositCents),
				cents(t.ContractorCostCents), cents(t.MarginCents), strconv.FormatInt(t.MarginBPS, 10),
				q.ValidUntil.Time.UTC().Format("2006-01-02T15:04:05Z"), os})
		}
	default:
		return httpx.BadRequest("the export kind is not known", httpx.FieldError{Field: "kind", Message: "must be one of: quotes, customers, projects"})
	}
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	return cw.Error()
}
