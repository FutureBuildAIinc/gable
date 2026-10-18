// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// The approval snapshot: the exact bytes of what the customer decided, hashed
// with SHA-256 and stored beside the hash, so the decision can be re-proved
// from the row alone. The bytes are the JSON encoding of Snapshot below;
// struct field order fixes the key order, lines and extras keep the quote's
// own order. The snapshot is a contractor record: it carries the customer's
// prices, so no dealer surface reads its body (the dealer gets the hash).

// SnapshotVersion is bumped when the shape changes.
const SnapshotVersion = 1

// Snapshot is the record of one customer decision.
type Snapshot struct {
	Version    int            `json:"v"`
	QuoteID    uuid.UUID      `json:"quote_id"`
	Reference  string         `json:"reference"`
	Revision   int64          `json:"revision"`
	Decision   string         `json:"decision"`
	ValidUntil time.Time      `json:"valid_until"`
	HidePrices bool           `json:"hide_line_prices"`
	Selections []SnapshotLine `json:"selections"`
	Extras     []SnapshotLine `json:"extras"`
	Subtotal   int64          `json:"subtotal_cents"`
	Tax        int64          `json:"tax_cents"`
	TaxMode    string         `json:"tax_mode"`
	Total      int64          `json:"total_cents"`
	Deposit    int64          `json:"deposit_cents"`
}

// SnapshotLine is one selection or extra as the customer was shown it.
type SnapshotLine struct {
	ID          uuid.UUID  `json:"id"`
	GroupID     *uuid.UUID `json:"group_id,omitempty"`
	Description string     `json:"description"`
	Quantity    string     `json:"quantity,omitempty"`
	UOM         string     `json:"uom,omitempty"`
	State       string     `json:"state,omitempty"`
	PriceCents  int64      `json:"price_cents"`
}

// BuildSnapshot captures the decision of a quote as it stands now. For an
// approval every line is decided (the caller checks); the totals count the
// approved lines.
func BuildSnapshot(q *Quote, decision string) (body string, sum string, total int64, err error) {
	t, err := q.Totals()
	if err != nil {
		return "", "", 0, err
	}
	s := Snapshot{
		Version: SnapshotVersion, QuoteID: q.ID, Reference: q.Reference, Revision: q.Revision, Decision: decision,
		ValidUntil: q.ValidUntil.UTC(), HidePrices: q.HideLinePrices, Selections: []SnapshotLine{}, Extras: []SnapshotLine{},
		Subtotal: t.SubtotalCents, Tax: t.TaxCents, TaxMode: q.Tax.Mode, Total: t.TotalCents, Deposit: t.DepositCents,
	}
	for _, l := range q.Lines {
		s.Selections = append(s.Selections, SnapshotLine{ID: l.ID, GroupID: l.GroupID, Description: l.Description,
			Quantity: l.Quantity.WireString(), UOM: l.UOM, State: string(l.Selection), PriceCents: l.SellCents})
	}
	base := t.MaterialSellCents + t.LabourCents
	for _, l := range q.Labour {
		if l.ContractorOnly {
			continue
		}
		amt, err := LabourTotalCents(LabourLine{RateType: l.RateType, RateCents: l.RateCents, Hours: l.Hours})
		if err != nil {
			return "", "", 0, err
		}
		s.Extras = append(s.Extras, SnapshotLine{ID: l.ID, Description: l.Description, PriceCents: amt})
	}
	for _, o := range q.Overhead {
		if o.ContractorOnly {
			continue
		}
		amt, err := OverheadTotalCents(OverheadLine{AmountType: o.AmountType, Value: o.Value}, base)
		if err != nil {
			return "", "", 0, err
		}
		s.Extras = append(s.Extras, SnapshotLine{ID: o.ID, Description: o.Label, PriceCents: amt})
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", "", 0, err
	}
	h := sha256.Sum256(b)
	return string(b), hex.EncodeToString(h[:]), t.TotalCents, nil
}
