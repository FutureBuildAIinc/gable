// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package parsing

import (
	"context"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/product"
)

// Service handles material list parsing and product matching.
type Service struct {
	productRepo product.Repository
	// aiClient performs the file -> text step. When it is nil or
	// unconfigured, ExtractItemsWithAI does NOT read the file at all; see
	// that method's doc comment for what it returns instead.
	aiClient *ai.Client
}

// NewService creates a new parsing Service.
func NewService(productRepo product.Repository, aiClient *ai.Client) *Service {
	return &Service{productRepo: productRepo, aiClient: aiClient}
}

// ExtractItemsWithAI uses the configured AI model to extract text from a file,
// then parses structured items out of that text. It never returns an error:
// the upload path is required to degrade rather than fail (CLAUDE.md "degrade
// gracefully"), and service_degradation_test.go pins that invariant.
//
// Read this before trusting the result. The fallback is NOT "rule-based
// extraction of fileBytes" — there is no rule-based reader for PDFs or images
// in this package. When the AI client is nil, unconfigured, or the call fails,
// fileBytes is discarded unread and the items come from
// generateFallbackMaterialList: a fixed, hard-coded demo list that has nothing
// to do with what the user uploaded. Since OPENROUTER_API_KEY is unset by
// default, this is the DEFAULT path, not an edge case.
//
// The second return value reports exactly that. synthetic==true means "these
// items are canned demo data, not the caller's file". Any caller that shows
// the result to a user, prices it, or persists it as a quote MUST propagate
// that flag — presenting a synthetic list as the user's takeoff is the
// failure mode this signature exists to prevent.
func (s *Service) ExtractItemsWithAI(ctx context.Context, fileBytes []byte, contentType string) (items []extractedLine, synthetic bool, err error) {
	if s.aiClient == nil || !s.aiClient.IsConfigured(ctx) {
		slog.Warn("AI client not configured; returning the canned demo material list, NOT the uploaded file's contents",
			"content_type", contentType, "bytes_discarded", len(fileBytes))
		return s.ExtractItems(generateFallbackMaterialList()), true, nil
	}

	rawText, aiErr := s.aiClient.ExtractMaterialList(ctx, fileBytes, contentType)
	if aiErr != nil {
		slog.Error("AI extraction failed; returning the canned demo material list, NOT the uploaded file's contents",
			"error", aiErr, "content_type", contentType, "bytes_discarded", len(fileBytes))
		return s.ExtractItems(generateFallbackMaterialList()), true, nil
	}

	return s.ExtractItems(rawText), false, nil
}

// generateFallbackMaterialList returns a fixed demo material list that stands
// in for a real extraction when AI is unavailable. It is unrelated to any
// uploaded file — callers surface it as synthetic (see ExtractItemsWithAI).
func generateFallbackMaterialList() string {
	return `50 pcs - 2x4x8 SPF Stud
25 pcs - 2x6x12 Doug Fir #2
30 sheets - OSB 7/16 4x8
10 sheets - CDX Plywood 1/2 4x8
15 pcs - 2x10x16 Hem Fir
8 pcs - 2x12x20
20 bags - Quikrete 80lb
4 rolls - Tyvek House Wrap
100 lf - 2x4 Pressure Treated
Custom powder-coat railing 12ft bronze
6 pcs - Simpson Strong-Tie A35
Specialty glass panel 48x72 frosted`
}

// --- Text Extraction (Rule-Based Simulator) ---

// extractedLine is an intermediate representation of a parsed text line.
type extractedLine struct {
	rawText  string
	quantity float64
	uom      string
	keywords []string
}

// lumberPattern matches common dimensional lumber patterns like "2x4x8", "2x6 x 12", "2 x 4"
var lumberPattern = regexp.MustCompile(`(?i)(\d+)\s*x\s*(\d+)(?:\s*x\s*(\d+))?`)

// quantityPattern matches leading quantities like "50 pcs", "100", "(20)"
var quantityPattern = regexp.MustCompile(`(?i)^\s*\(?(\d+(?:\.\d+)?)\)?\s*(?:pcs?|ea|pieces?|each|lf|sf|bf|sheets?|bags?|rolls?|bundles?|gal(?:lons?)?)?\.?\s*[-–—]?\s*`)

// uomPattern extracts UOM from text
var uomPattern = regexp.MustCompile(`(?i)\b(pcs?|ea|pieces?|each|lf|sf|bf|sheets?|bags?|rolls?|bundles?|gal(?:lons?)?)\b`)

// sheetGoodPattern matches sheet goods like "OSB 7/16", "CDX 1/2", "plywood 3/4"
var sheetGoodPattern = regexp.MustCompile(`(?i)(osb|cdx|plywood|drywall|sheathing|hardiboard|mdf)\s*(\d+/\d+)?`)

// ExtractItems parses raw text lines from a material list document.
// This is the rule-based AI simulator — designed to be swapped with a real AI provider.
func (s *Service) ExtractItems(rawText string) []extractedLine {
	lines := strings.Split(rawText, "\n")
	var results []extractedLine

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || len(line) < 3 {
			continue
		}

		extracted := extractedLine{
			rawText:  line,
			quantity: 1,
			uom:      "PCS",
		}

		// Extract quantity from line start
		if matches := quantityPattern.FindStringSubmatch(line); len(matches) > 1 {
			if q, err := strconv.ParseFloat(matches[1], 64); err == nil {
				extracted.quantity = q
			}
			// Remove quantity prefix from line for keyword extraction
			line = quantityPattern.ReplaceAllString(line, "")
		}

		// Extract UOM
		if matches := uomPattern.FindStringSubmatch(line); len(matches) > 1 {
			extracted.uom = normalizeUOM(matches[1])
		}

		// Extract keywords (lumber dimensions, sheet goods, etc.)
		if matches := lumberPattern.FindStringSubmatch(line); len(matches) > 0 {
			extracted.keywords = append(extracted.keywords, matches[0])
		}
		if matches := sheetGoodPattern.FindStringSubmatch(line); len(matches) > 0 {
			extracted.keywords = append(extracted.keywords, matches[0])
		}

		// Add any remaining significant words as keywords
		words := strings.Fields(strings.ToLower(line))
		for _, w := range words {
			w = strings.Trim(w, ".,;:-()\"'")
			if len(w) >= 2 && !isStopWord(w) {
				extracted.keywords = append(extracted.keywords, w)
			}
		}

		results = append(results, extracted)
	}
	return results
}

// --- Product Matching ---

// MatchProducts matches extracted lines against the product catalog.
func (s *Service) MatchProducts(ctx context.Context, extracted []extractedLine) ([]ParsedItem, error) {
	products, err := s.productRepo.ListProducts(ctx)
	if err != nil {
		return nil, err
	}

	var items []ParsedItem
	for _, ext := range extracted {
		item := s.matchSingleItem(ext, products)
		items = append(items, item)
	}
	return items, nil
}

type scoredProduct struct {
	product product.Product
	score   float64
}

func (s *Service) matchSingleItem(ext extractedLine, products []product.Product) ParsedItem {
	item := ParsedItem{
		RawText:  ext.rawText,
		Quantity: ext.quantity,
		UOM:      ext.uom,
	}

	if len(products) == 0 {
		item.IsSpecialOrder = true
		item.Confidence = 0
		return item
	}

	// Score each product
	var scored []scoredProduct
	for _, p := range products {
		score := scoreMatch(ext, p)
		if score > 0 {
			scored = append(scored, scoredProduct{product: p, score: score})
		}
	}

	// Sort by score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	if len(scored) == 0 {
		// No match at all — Special Order
		item.IsSpecialOrder = true
		item.Confidence = 0
		return item
	}

	bestScore := scored[0].score
	confidence := math.Min(bestScore/100.0, 1.0)

	if confidence < 0.50 {
		// Below threshold — Special Order
		item.IsSpecialOrder = true
		item.Confidence = confidence
		item.MatchedProduct = toMatchedProduct(scored[0].product)
		// Still provide alternatives
		for i := 1; i < len(scored) && i <= 3; i++ {
			item.Alternatives = append(item.Alternatives, *toMatchedProduct(scored[i].product))
		}
		return item
	}

	// Matched
	item.MatchedProduct = toMatchedProduct(scored[0].product)
	item.Confidence = confidence
	item.UOM = string(scored[0].product.UOMPrimary)

	// Add alternatives for items below 90% confidence
	if confidence < 0.90 {
		for i := 1; i < len(scored) && i <= 3; i++ {
			item.Alternatives = append(item.Alternatives, *toMatchedProduct(scored[i].product))
		}
	}

	return item
}

// scoreMatch calculates a relevance score between an extracted line and a product.
func scoreMatch(ext extractedLine, p product.Product) float64 {
	score := 0.0

	descLower := strings.ToLower(p.Description)
	skuLower := strings.ToLower(p.SKU)
	rawLower := strings.ToLower(ext.rawText)

	for _, kw := range ext.keywords {
		kwLower := strings.ToLower(kw)

		// Exact keyword in description
		if strings.Contains(descLower, kwLower) {
			score += 30
		}

		// Exact keyword in SKU
		if strings.Contains(skuLower, kwLower) {
			score += 25
		}

		// Fuzzy match — check Levenshtein distance for description words
		descWords := strings.Fields(descLower)
		for _, dw := range descWords {
			dist := levenshtein(kwLower, dw)
			if dist <= 2 && len(kwLower) > 3 {
				score += float64(15 - dist*3)
			}
		}
	}

	// Bonus for dimensional lumber pattern matching in SKU
	if lumberPattern.MatchString(ext.rawText) && lumberPattern.MatchString(p.SKU) {
		extDim := lumberPattern.FindString(strings.ToLower(ext.rawText))
		prodDim := lumberPattern.FindString(strings.ToLower(p.SKU))
		if normalizeDimension(extDim) == normalizeDimension(prodDim) {
			score += 40
		}
	}

	// Bonus for raw text containing SKU directly
	if strings.Contains(rawLower, skuLower) && len(skuLower) > 3 {
		score += 50
	}

	return score
}

// --- Helpers ---

func toMatchedProduct(p product.Product) *MatchedProduct {
	return &MatchedProduct{
		ProductID:   p.ID,
		SKU:         p.SKU,
		Description: p.Description,
		UOM:         string(p.UOMPrimary),
		BasePrice:   p.BasePrice,
	}
}

func normalizeUOM(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch {
	case strings.HasPrefix(s, "PC") || strings.HasPrefix(s, "PIECE"):
		return "PCS"
	case strings.HasPrefix(s, "EA"):
		return "EA"
	case s == "LF":
		return "LF"
	case s == "SF":
		return "SF"
	case s == "BF":
		return "BF"
	case strings.HasPrefix(s, "SHEET"):
		return "PCS"
	case strings.HasPrefix(s, "BAG"):
		return "BAG"
	case strings.HasPrefix(s, "ROLL"):
		return "RL"
	case strings.HasPrefix(s, "BUNDLE"):
		return "BUNDLE"
	case strings.HasPrefix(s, "GAL"):
		return "GAL"
	default:
		return "PCS"
	}
}

func normalizeDimension(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func isStopWord(w string) bool {
	stops := map[string]bool{
		"the": true, "and": true, "for": true, "with": true,
		"per": true, "each": true, "of": true, "or": true,
		"at": true, "to": true, "in": true, "on": true,
		"ft": true, "pc": true, "pcs": true, "ea": true,
	}
	return stops[w]
}

// levenshtein computes the Levenshtein distance between two strings.
func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}

	for i := 1; i <= len(a); i++ {
		prev := i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			val := min3(prev+1, row[j]+1, row[j-1]+cost)
			row[j-1] = prev
			prev = val
		}
		row[len(b)] = prev
	}
	return row[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
