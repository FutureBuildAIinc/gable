// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/google/uuid"
)

const (
	rowMatched   = "matched"
	rowUnmatched = "unmatched"
	rowError     = "error"

	noteDidNotAnswer = "FB Connect did not answer"
	noteBadHook      = "No flow answers at that webhook id: check it in Tech Admin"
	noteTimedOut     = "FB Connect timed out waiting for the run"
	noteNoRow        = "The flow did not return this item"
	noteNoCost       = "No cost on file, so no price is suggested"
	noteNoPrice      = "No walk in price is on file for this product"
	noteNoProduct    = "This product was not found"

	allowedLinkPrefix = "https://www.homedepot.com/"
)

// Competitor is the flow's match for one item, cleaned.
type Competitor struct {
	Title            string `json:"title"`
	PriceCents       int64  `json:"price_cents"`
	ListedPriceCents int64  `json:"listed_price_cents"`
	Link             string `json:"link"`
	Confidence       int    `json:"confidence"`
	UnitNote         string `json:"unit_note"`
}

// Row is one picked product's result.
type Row struct {
	ProductID           uuid.UUID   `json:"product_id"`
	SKU                 string      `json:"sku"`
	Description         string      `json:"description"`
	UOM                 string      `json:"uom"`
	OurPriceCents       int64       `json:"our_price_cents"`
	CostKnown           bool        `json:"cost_known"`
	Status              string      `json:"status"`
	Competitor          *Competitor `json:"competitor"`
	DiffPct             *float64    `json:"diff_pct"`
	Position            string      `json:"position"`
	SuggestedPriceCents *int64      `json:"suggested_price_cents"`
	Reason              string      `json:"reason"`
	Note                string      `json:"note"`
}

// Result is the run's answer. Cost itself is never in it.
type Result struct {
	FlowName       string  `json:"flow_name"`
	StoreLabel     string  `json:"store_label"`
	StoreSeen      *string `json:"store_seen"`
	BandPct        string  `json:"band_pct"`
	MarginFloorPct string  `json:"margin_floor_pct"`
	Rows           []Row   `json:"rows"`
}

// The request to the flow (docs/integrations/MARKET-CHECK-FLOW.md).
type flowRequest struct {
	Source      string        `json:"source"`
	Yard        flowYard      `json:"yard"`
	Store       flowStore     `json:"store"`
	BandPercent json.Number   `json:"bandPercent"`
	Items       []flowItemOut `json:"items"`
}
type flowYard struct {
	Name string `json:"name"`
	Town string `json:"town"`
}
type flowStore struct {
	ID    string `json:"id"`
	Zip   string `json:"zip"`
	Label string `json:"label"`
}
type flowItemOut struct {
	SKU      string      `json:"sku"`
	Name     string      `json:"name"`
	Category string      `json:"category"`
	Unit     string      `json:"unit"`
	Price    json.Number `json:"price"`
}

// The flow's answer, strict on types; unknown fields are ignored.
type flowAnswer struct {
	Store *struct {
		Name string `json:"name"`
	} `json:"store"`
	Rows []flowRow `json:"rows"`
}
type flowRow struct {
	SKU    string `json:"sku"`
	Status string `json:"status"`
	Match  *struct {
		Title       string  `json:"title"`
		TheirPrice  float64 `json:"theirPrice"`
		ListedPrice float64 `json:"listedPrice"`
		Link        string  `json:"link"`
		Confidence  float64 `json:"confidence"`
		NormNote    string  `json:"normNote"`
	} `json:"match"`
	Note string `json:"note"`
}

type batchOutcome struct {
	rows  map[string]flowRow
	store string
	note  string // set when the batch failed
}

func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

func dollars(cents int64) json.Number {
	return json.Number(fmt.Sprintf("%d.%02d", cents/100, cents%100))
}

// Run checks the picked products against the flow.
func (s *Service) Run(ctx context.Context, productIDs []uuid.UUID) (*Result, error) {
	if len(productIDs) < 1 || len(productIDs) > maxPick {
		return nil, invalidField("product_ids", "pick between 1 and 12 products")
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range productIDs {
		if seen[id] {
			return nil, invalidField("product_ids", "each product may be picked once")
		}
		seen[id] = true
	}
	cfg, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Configured {
		return nil, ErrNotConfigured
	}
	hook, err := ai.OpenValue(s.vault, cfg.sealedID)
	if err != nil || hook == "" {
		// Saved but unreadable (the vault key changed): treat as not configured.
		return nil, ErrNotConfigured
	}
	bandBP, _, err := parsePercent("band_pct", cfg.BandPct, 50)
	if err != nil {
		return nil, err
	}
	floorBP, _, err := parsePercent("margin_floor_pct", cfg.MarginFloorPct, 90)
	if err != nil {
		return nil, err
	}
	yard, err := s.catalog.Yard(ctx)
	if err != nil {
		return nil, err
	}
	products, err := s.catalog.Products(ctx, productIDs)
	if err != nil {
		return nil, err
	}

	res := &Result{FlowName: cfg.FlowName, StoreLabel: cfg.StoreLabel, BandPct: cfg.BandPct,
		MarginFloorPct: cfg.MarginFloorPct, Rows: make([]Row, len(productIDs))}
	var sendIdx []int
	for i, id := range productIDs {
		p, ok := products[id]
		row := Row{ProductID: id, Status: rowError}
		switch {
		case !ok:
			row.Note = noteNoProduct
		case p.PriceCents <= 0:
			row.SKU, row.Description, row.UOM = p.SKU, p.Description, p.UOM
			row.CostKnown = p.CostCents > 0
			row.Note = noteNoPrice
		default:
			row.SKU, row.Description, row.UOM = p.SKU, p.Description, p.UOM
			row.OurPriceCents, row.CostKnown = p.PriceCents, p.CostCents > 0
			sendIdx = append(sendIdx, i)
		}
		res.Rows[i] = row
	}

	// Batches of up to 3 items, at most 4 at once.
	var batches [][]int
	for i := 0; i < len(sendIdx); i += batchSize {
		end := i + batchSize
		if end > len(sendIdx) {
			end = len(sendIdx)
		}
		batches = append(batches, sendIdx[i:end])
	}
	outcomes := make([]batchOutcome, len(batches))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrent)
	for bi := range batches {
		wg.Add(1)
		go func(bi int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			items := make([]flowItemOut, 0, len(batches[bi]))
			for _, idx := range batches[bi] {
				p := products[productIDs[idx]]
				items = append(items, flowItemOut{SKU: p.SKU, Name: p.Description, Category: p.Category,
					Unit: p.UOM, Price: dollars(p.PriceCents)})
			}
			req := flowRequest{Source: "gable", Yard: flowYard{Name: yard.Name, Town: yard.Town},
				Store:       flowStore{ID: cfg.StoreID, Zip: cfg.StoreZip, Label: cfg.StoreLabel},
				BandPercent: json.Number(cfg.BandPct), Items: items}
			outcomes[bi] = s.callBatch(ctx, cfg.ConnectURL, hook, req)
		}(bi)
	}
	wg.Wait()

	for bi, b := range batches {
		out := outcomes[bi]
		if out.store != "" && res.StoreSeen == nil {
			st := out.store
			res.StoreSeen = &st
		}
		for _, idx := range b {
			row := &res.Rows[idx]
			if out.note != "" {
				row.Note = out.note
				continue
			}
			fr, ok := out.rows[row.SKU]
			if !ok {
				row.Note = noteNoRow
				continue
			}
			applyRow(row, fr, bandBP, floorBP, products[row.ProductID].CostCents)
		}
	}
	return res, nil
}

func applyRow(row *Row, fr flowRow, bandBP, floorBP, cost int64) {
	switch fr.Status {
	case rowUnmatched:
		row.Status = rowUnmatched
		row.Note = cleanText(fr.Note, 300)
		return
	case rowMatched:
	default:
		row.Note = cleanText(fr.Note, 300)
		if row.Note == "" {
			row.Note = noteDidNotAnswer
		}
		return
	}
	m := fr.Match
	if m == nil || !validPrice(m.TheirPrice) {
		row.Note = "The flow's price for this item could not be used"
		return
	}
	listed := m.ListedPrice
	if !validPrice(listed) {
		listed = m.TheirPrice
	}
	c := &Competitor{
		Title: cleanText(m.Title, 200), PriceCents: roundCents(m.TheirPrice), ListedPriceCents: roundCents(listed),
		Confidence: int(math.Round(math.Min(100, math.Max(0, m.Confidence)))),
		UnitNote:   cleanText(m.NormNote, 300),
	}
	if strings.HasPrefix(m.Link, allowedLinkPrefix) {
		if u, err := url.Parse(m.Link); err == nil && u.Host == "www.homedepot.com" {
			c.Link = m.Link
		}
	}
	if c.PriceCents <= 0 {
		row.Note = "The flow's price for this item could not be used"
		return
	}
	row.Status, row.Competitor = rowMatched, c
	row.Note = cleanText(fr.Note, 300)
	sg := Suggest(row.OurPriceCents, c.PriceCents, cost, bandBP, floorBP)
	d := sg.DiffPct
	row.DiffPct, row.Position, row.Reason = &d, sg.Position, sg.Reason
	if sg.SuggestedKnown {
		v := sg.SuggestedCents
		row.SuggestedPriceCents = &v
	} else if row.Note == "" {
		row.Note = noteNoCost
	}
}

func validPrice(p float64) bool {
	return !math.IsNaN(p) && !math.IsInf(p, 0) && p > 0 && p < 100000
}

// callBatch posts one batch. Its failures are fixed words: the transport's
// own error text holds the address, so it is never kept, logged or answered.
func (s *Service) callBatch(ctx context.Context, base, hook string, body flowRequest) batchOutcome {
	host := hostOf(base)
	fail := func(note, kind string) batchOutcome {
		s.log.Warn("market check batch failed", "host", host, "kind", kind, "items", len(body.Items))
		return batchOutcome{note: note}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fail(noteDidNotAnswer, "encode")
	}
	cctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, base+"/api/v1/webhooks/"+hook+"/sync", bytes.NewReader(payload))
	if err != nil {
		return fail(noteDidNotAnswer, "request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return fail(noteTimedOut, "timeout")
		}
		return fail(noteDidNotAnswer, "transport")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusBadRequest:
		return fail(noteBadHook, "status_404_400")
	case http.StatusRequestTimeout:
		return fail(noteTimedOut, "status_408")
	default:
		return fail(noteDidNotAnswer, "status")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return fail(noteTimedOut, "timeout")
		}
		return fail(noteDidNotAnswer, "read")
	}
	if len(raw) > maxAnswerBytes {
		return fail(noteDidNotAnswer, "too_large")
	}
	var ans flowAnswer
	if err := json.Unmarshal(raw, &ans); err != nil {
		return fail(noteDidNotAnswer, "decode")
	}
	out := batchOutcome{rows: map[string]flowRow{}}
	if ans.Store != nil {
		out.store = cleanText(ans.Store.Name, 120)
	}
	sent := map[string]bool{}
	for _, it := range body.Items {
		sent[it.SKU] = true
	}
	for _, r := range ans.Rows {
		if sent[r.SKU] { // a SKU we did not send is ignored
			if _, dup := out.rows[r.SKU]; !dup {
				out.rows[r.SKU] = r
			}
		}
	}
	s.log.Info("market check batch answered", "host", host, "items", len(body.Items), "rows", len(out.rows))
	return out
}

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return "unknown"
	}
	return u.Host
}
