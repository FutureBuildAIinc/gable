// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package millwork

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Configuration is a configured item's normalised choices: the attribute
// categories the dealer picked and the value picked for each, plus the door's
// width and height. It is the one shape both configurators produce, the one
// the SKU is hashed from and the one a quote or order line carries.
//
// The category names are the option catalog's own: snake_case, the spelling
// the catalog stores. The configurator's attribute names ("Species", "Grade")
// are folded into that shape on the way in, so the catalog's price for a
// choice and the configurator's rule for a choice are read from the two
// vocabularies they belong to.
type Configuration struct {
	ProductType string            `json:"product_type,omitempty"`
	Selections  map[string]string `json:"selections"`
	Width       *int              `json:"width,omitempty"`
	Height      *int              `json:"height,omitempty"`
}

// productTypeCategory is the category name the product type carries when a
// configurator also sends it among its selections. It is the configuration's
// own field, never a priced choice.
const productTypeCategory = "product_type"

// categoryOrder is the order a configuration's choices are read in, so the
// description and the price breakdown always read the same way whatever order
// the choices arrived in. A choice outside the order follows, sorted.
var categoryOrder = []string{
	"door_type", "material", "glass", "jamb_depth", "handing", "hinge_finish", "frame_type",
	"species", "grade", "treatment", "dimensions",
}

// typeAbbreviations is the product type's own short name in the SKU. A type
// the catalog does not name falls back to its first three letters, upper
// cased, so an unknown type still yields a readable SKU.
var typeAbbreviations = map[string]string{
	"Lumber": "LBR",
	"Door":   "DR",
	"Trim":   "TRM",
	"Panel":  "PNL",
}

// CategoryName folds a category name into the one spelling the catalog and
// the configuration are matched on: lower cased, every run of characters that
// are neither a letter nor a digit turned into a single underscore.
//
// "Jamb Depth", "jamb-depth" and "jamb_depth" are one category, and so are
// "Species" and "species". The fold runs on both sides of the match: the
// configuration's category names and the catalog's own, so a dealer whose
// catalog spells a category its own way still gets the price.
func CategoryName(name string) string {
	lowered := strings.ToLower(name)
	var b strings.Builder
	pending := false
	for _, r := range lowered {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pending && b.Len() > 0 {
				b.WriteRune('_')
			}
			pending = false
			b.WriteRune(r)
			continue
		}
		pending = true
	}
	return b.String()
}

// Normalized returns the configuration in the one shape the rest of the
// module reads: the category names folded, the values trimmed and an empty
// choice dropped.
func (c Configuration) Normalized() Configuration {
	out := Configuration{ProductType: strings.TrimSpace(c.ProductType)}
	out.Selections = make(map[string]string, len(c.Selections))
	for name, value := range c.Selections {
		cat := CategoryName(name)
		val := strings.TrimSpace(value)
		if cat == "" || val == "" {
			continue
		}
		if cat == productTypeCategory {
			// The product type is the configuration's own field, not a
			// choice: a configurator that also carries it in its
			// selections does not make it a priced option.
			continue
		}
		out.Selections[cat] = val
	}
	if c.Width != nil && *c.Width > 0 {
		w := *c.Width
		out.Width = &w
	}
	if c.Height != nil && *c.Height > 0 {
		h := *c.Height
		out.Height = &h
	}
	return out
}

// Canonical returns the configuration's canonical JSON: the normalised
// configuration marshalled, with the selections' keys sorted. Two
// configurations with the same choices give the same bytes whatever order the
// choices arrived in, which is what makes the SKU stable.
func (c Configuration) Canonical() ([]byte, error) {
	data, err := json.Marshal(c.Normalized())
	if err != nil {
		return nil, fmt.Errorf("failed to canonicalise the configuration: %w", err)
	}
	return data, nil
}

// ConfiguredSKU returns the deterministic non-stock SKU of a configuration:
// NS-, the product type's short name and the first twelve hex characters of
// the SHA-256 of the configuration's canonical JSON. The hash is over the
// canonical form, so the same choices in any order give the same SKU, and two
// different configurations never give the same one.
func ConfiguredSKU(c Configuration) (string, error) {
	norm := c.Normalized()
	data, err := norm.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	abbr, ok := typeAbbreviations[norm.ProductType]
	if !ok {
		abbr = strings.ToUpper(norm.ProductType)
		if len(abbr) > 3 {
			abbr = abbr[:3]
		}
	}
	if abbr == "" {
		abbr = "CFG"
	}
	return "NS-" + abbr + "-" + strings.ToUpper(hex.EncodeToString(sum[:])[:12]), nil
}

// Description returns the configured item's description: the product type,
// then the choices in the catalog's order, then the door's size. It is the
// text every document carries, so the dealer reads what the item is without
// opening the configuration.
func (c Configuration) Description() string {
	norm := c.Normalized()
	parts := make([]string, 0, len(norm.Selections)+3)
	if norm.ProductType != "" {
		parts = append(parts, norm.ProductType)
	}
	for _, cat := range categoryOrder {
		if v, ok := norm.Selections[cat]; ok && !isNone(v) {
			parts = append(parts, v)
		}
	}
	rest := make([]string, 0)
	for cat := range norm.Selections {
		if !knownCategory(cat) {
			rest = append(rest, cat)
		}
	}
	sort.Strings(rest)
	for _, cat := range rest {
		if v := norm.Selections[cat]; !isNone(v) {
			parts = append(parts, v)
		}
	}
	if norm.Width != nil && norm.Height != nil {
		parts = append(parts, fmt.Sprintf("%d x %d", *norm.Width, *norm.Height))
	}
	return strings.Join(parts, " ")
}

// optionNamed finds the option a configuration names among a category's
// options. The catalog is the dealer's own and two rows of one name are the
// dealer's business, so the first wins and the other is history.
func optionNamed(opts []Option, name string) (*Option, bool) {
	for i := range opts {
		if opts[i].Name == name {
			return &opts[i], true
		}
	}
	return nil, false
}

// isNone reports whether a choice is the absence of a choice. The catalog
// carries it as an option of its own (a treatment of none costs nothing),
// and the description leaves it out.
func isNone(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "None") }

func knownCategory(cat string) bool {
	for _, c := range categoryOrder {
		if c == cat {
			return true
		}
	}
	return false
}

// SortedCategories returns the configuration's categories in the order the
// price breakdown reads them.
func (c Configuration) SortedCategories() []string {
	norm := c.Normalized()
	cats := make([]string, 0, len(norm.Selections))
	for cat := range norm.Selections {
		cats = append(cats, cat)
	}
	sort.Slice(cats, func(i, j int) bool {
		oi, oj := orderOf(cats[i]), orderOf(cats[j])
		if oi != oj {
			return oi < oj
		}
		return cats[i] < cats[j]
	})
	return cats
}

func orderOf(cat string) int {
	for i, c := range categoryOrder {
		if c == cat {
			return i
		}
	}
	return len(categoryOrder)
}

// PriceLine is one option's contribution to a configured item's price: the
// option's base plus its adjustment, in cents. The breakdown is what the
// dealer sees next to the total.
type PriceLine struct {
	Category   string `json:"category"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	CostCents  int64  `json:"cost_cents"`
}

// PriceBreakdown is a configured item's price and cost and the options that
// made them. The two money fields are the wire's units: ten-thousandths of
// the major unit, the scale the documents carry their prices in.
type PriceBreakdown struct {
	Lines     []PriceLine `json:"breakdown"`
	UnitPrice httpx.Price `json:"unit_price_ten_thousandths"`
	UnitCost  httpx.Price `json:"unit_cost_ten_thousandths"`
}

// priceConfiguration prices a configuration from the stored options.
//
// The unit price is the sum over the options the configuration selects of
// that option's base price plus its price adjustment; the unit cost is the
// same sum over the cost columns. The base is the option that names the item
// (a door's door type, a board's species) and the rest of the options carry
// adjustments, so the seed's rule of one base per configuration keeps the
// price readable.
//
// Every choice that names a category the catalog maintains must name one of
// that category's options: a dealer naming an option the catalog does not
// have is a 400, because a price computed from half the choices is a price
// the dealer cannot trust, and a silent zero would be a worse answer than a
// refusal. A category the catalog does not maintain at all is not a priced
// choice: the selection is something else (the configurator's own
// attributes, a dimension), and it adds nothing to the price. That is what
// keeps a fresh install, whose catalog is empty, answering rather than
// refusing every configuration.
func (s *Service) priceConfiguration(ctx context.Context, c Configuration) (*PriceBreakdown, error) {
	norm := c.Normalized()
	out := &PriceBreakdown{Lines: make([]PriceLine, 0, len(norm.Selections))}
	cats := norm.SortedCategories()
	catalog, err := s.repo.OptionsByCategories(ctx, cats)
	if err != nil {
		return nil, err
	}
	var priceCents, costCents int64
	for _, cat := range cats {
		name := norm.Selections[cat]
		opts := catalog[cat]
		if len(opts) == 0 {
			// The catalog does not maintain this category: the choice is
			// not a priced one and adds nothing.
			continue
		}
		opt, found := optionNamed(opts, name)
		if !found {
			return nil, httpx.BadRequest("the option catalog carries no such choice",
				httpx.FieldError{Field: "selections." + cat,
					Message: fmt.Sprintf("the catalog carries no %s option named %q", cat, name)})
		}
		line := PriceLine{
			Category:   cat,
			Name:       name,
			PriceCents: opt.BasePriceCents + opt.PriceAdjustmentCents,
			CostCents:  opt.BaseCostCents + opt.CostAdjustmentCents,
		}
		out.Lines = append(out.Lines, line)
		priceCents += line.PriceCents
		costCents += line.CostCents
	}
	// The catalog's money is in cents; the documents' money is in
	// ten-thousandths. The conversion is exact, so nothing is rounded.
	out.UnitPrice = httpx.Price(priceCents * 100)
	out.UnitCost = httpx.Price(costCents * 100)
	return out, nil
}

// ConfiguredItem is a configured item: its SKU, its description, its catalog
// row, the server's price and cost for it, and the canonical configuration
// all of it came from. It is the whole answer the dealer needs to add the
// item to a quote or an order.
type ConfiguredItem struct {
	SKU           string          `json:"sku"`
	Description   string          `json:"description"`
	ProductID     uuid.UUID       `json:"product_id"`
	UnitPrice     httpx.Price     `json:"unit_price_ten_thousandths"`
	UnitCost      httpx.Price     `json:"unit_cost_ten_thousandths"`
	Configuration json.RawMessage `json:"configuration_json"`
	Breakdown     []PriceLine     `json:"breakdown"`
}

// BuildConfigured prices a configuration from the stored options, builds its
// deterministic SKU and finds or creates the product row it becomes.
//
// The selections arrive in the configurator's own vocabulary ("Species",
// "Grade"); they are folded into the option catalog's vocabulary on the way
// in, so the catalog's price for a choice and the configurator's rule for a
// choice are read from the two vocabularies they belong to.
func (s *Service) BuildConfigured(ctx context.Context, productType string, selections map[string]string, width, height *int) (*ConfiguredItem, error) {
	cfg := Configuration{
		ProductType: productType,
		Selections:  selections,
		Width:       width,
		Height:      height,
	}
	norm := cfg.Normalized()
	if len(norm.Selections) == 0 {
		return nil, httpx.BadRequest("the configuration names no choice",
			httpx.FieldError{Field: "selections", Message: "is required"})
	}
	b, err := s.priceConfiguration(ctx, norm)
	if err != nil {
		return nil, err
	}
	sku, err := ConfiguredSKU(norm)
	if err != nil {
		return nil, err
	}
	description := norm.Description()
	productID, err := s.FindOrCreateConfigured(ctx, sku, description, b.UnitPrice, b.UnitCost)
	if err != nil {
		return nil, err
	}
	raw, err := norm.Canonical()
	if err != nil {
		return nil, err
	}
	return &ConfiguredItem{
		SKU:           sku,
		Description:   description,
		ProductID:     productID,
		UnitPrice:     b.UnitPrice,
		UnitCost:      b.UnitCost,
		Configuration: raw,
		Breakdown:     b.Lines,
	}, nil
}

// PriceConfiguration prices a configuration carried as raw JSON. It is the
// seam the quote and the order price a configured line through: the client
// sends the configuration, never a price.
func (s *Service) PriceConfiguration(ctx context.Context, raw json.RawMessage) (httpx.Price, httpx.Price, error) {
	cfg, err := DecodeConfiguration(raw)
	if err != nil {
		return 0, 0, err
	}
	b, err := s.priceConfiguration(ctx, cfg)
	if err != nil {
		return 0, 0, err
	}
	return b.UnitPrice, b.UnitCost, nil
}

// ConfiguredSKU returns the deterministic non-stock SKU and the description of
// a configuration carried as raw JSON.
func (s *Service) ConfiguredSKU(raw json.RawMessage) (string, string, error) {
	cfg, err := DecodeConfiguration(raw)
	if err != nil {
		return "", "", err
	}
	sku, err := ConfiguredSKU(cfg)
	if err != nil {
		return "", "", err
	}
	return sku, cfg.Description(), nil
}

// FindOrCreateConfigured returns the product row a configured item becomes,
// creating it when the SKU is not in the catalog yet. The SKU is unique, so
// two concurrent creates of one configuration make one row: the loser of the
// race reads the row the winner wrote.
func (s *Service) FindOrCreateConfigured(ctx context.Context, sku, description string, price, cost httpx.Price) (uuid.UUID, error) {
	if s.products == nil {
		return uuid.Nil, httpx.BadRequest("the configured item cannot reach the product catalog")
	}
	return s.products.FindOrCreateNonStock(ctx, sku, description, price, cost)
}

// DecodeConfiguration reads a configuration carried as raw JSON.
func DecodeConfiguration(raw json.RawMessage) (Configuration, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return Configuration{}, httpx.BadRequest("the configuration is required")
	}
	var cfg Configuration
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Configuration{}, httpx.BadRequest("the configuration is malformed",
			httpx.FieldError{Field: "configuration_json", Message: "must be a configuration object"})
	}
	norm := cfg.Normalized()
	if len(norm.Selections) == 0 && norm.ProductType == "" {
		return Configuration{}, httpx.BadRequest("the configuration names no choice",
			httpx.FieldError{Field: "configuration_json", Message: "must name at least one choice"})
	}
	return norm, nil
}
