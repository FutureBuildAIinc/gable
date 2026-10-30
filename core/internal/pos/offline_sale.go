// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// SignedOfflineSale is one sale a register made offline, as the device's
// Rust core signed it (ADR 0011 sections 2, 5.1 and 6.1; the twin of
// SignedOfflineSale in tauri/core/src/offline/model.rs). The server reads its
// figures from here and verifies them against the snapshot it names; it never
// prices the sale again.
type SignedOfflineSale struct {
	ClientID        string             `json:"client_id"`
	RegisterID      string             `json:"register_id"`
	DeviceEpoch     int64              `json:"device_epoch"`
	DeviceSeq       int64              `json:"device_seq"`
	PrevDigest      string             `json:"prev_digest"`
	OfflineRef      string             `json:"offline_ref"`
	SnapshotID      string             `json:"snapshot_id"`
	TillSessionID   string             `json:"till_session_id"`
	CashierID       string             `json:"cashier_id"`
	CustomerID      *string            `json:"customer_id"`
	BuyerContactID  *string            `json:"buyer_contact_id"`
	CustomerPO      *string            `json:"customer_po"`
	JobID           *string            `json:"job_id"`
	PickedUpBy      *string            `json:"picked_up_by"`
	BusinessDate    string             `json:"business_date"`
	SoldAt          string             `json:"sold_at"`
	Currency        string             `json:"currency"`
	SubtotalCents   int64              `json:"subtotal_cents"`
	TaxCents        int64              `json:"tax_cents"`
	TotalCents      int64              `json:"total_cents"`
	ChangeCents     int64              `json:"change_cents"`
	TaxRate         string             `json:"tax_rate"`
	TaxExempt       bool               `json:"tax_exempt"`
	QueueCount      int64              `json:"queue_count"`
	QueueTotalCents int64              `json:"queue_total_cents"`
	ConfirmedMark   int64              `json:"confirmed_mark"`
	Lines           []SignedSaleLine   `json:"lines"`
	Tenders         []SignedSaleTender `json:"tenders"`
	// The replaced cart and the declaration (ADR 0011 section 5.3). They are
	// signed fields when the device sends them: the canonical form is built
	// from the item as received, so a field present is a field signed.
	ReplacesSaleID    *string `json:"replaces_sale_id,omitempty"`
	DifferentCustomer bool    `json:"different_customer,omitempty"`
	UnknownCartID     *string `json:"unknown_cart_id,omitempty"`
	DeviceSig         string  `json:"device_sig"`
}

// SignedSaleLine is one line of a signed offline sale.
type SignedSaleLine struct {
	ID                      string       `json:"id"`
	Position                int          `json:"position"`
	ProductID               string       `json:"product_id"`
	SKU                     string       `json:"sku"`
	Description             string       `json:"description"`
	Quantity                string       `json:"quantity"`
	UOM                     string       `json:"uom"`
	PriceUOM                string       `json:"price_uom"`
	UOMQty                  string       `json:"uom_qty"`
	PriceUOMQty             string       `json:"price_uom_qty"`
	StockUOM                string       `json:"stock_uom"`
	StockQuantity           string       `json:"stock_quantity"`
	PriceKey                string       `json:"price_key"`
	PriceSource             string       `json:"price_source"`
	PriceBasis              *string      `json:"price_basis"`
	LevelFallback           bool         `json:"level_fallback"`
	UnitPriceTenThousandths int64        `json:"unit_price_ten_thousandths"`
	LineTotalCents          int64        `json:"line_total_cents"`
	Taxable                 bool         `json:"taxable"`
	Tally                   *SignedTally `json:"tally"`
}

// SignedTally is a tallied line's recorded tally.
type SignedTally struct {
	ThicknessIn *string          `json:"thickness_in"`
	WidthIn     *string          `json:"width_in"`
	Rows        []SignedTallyRow `json:"rows"`
	LinearFeet  string           `json:"linear_feet"`
	BoardFeet   *string          `json:"board_feet"`
}

// SignedTallyRow is one row of a recorded tally.
type SignedTallyRow struct {
	Pieces   int    `json:"pieces"`
	LengthFT string `json:"length_ft"`
}

// SignedSaleTender is one tender leg of a signed offline sale.
type SignedSaleTender struct {
	Method      string  `json:"method"`
	AmountCents int64   `json:"amount_cents"`
	Reference   *string `json:"reference"`
	IDChecked   bool    `json:"id_checked"`
	IDType      *string `json:"id_type"`
}

// offlineItem is one item of a sync batch: the sale as parsed, the canonical
// bytes its signature covers, and their digest (the chain link the next sale
// carries, and the signature digest stored on the sale and the exception).
type offlineItem struct {
	Sale      SignedOfflineSale
	Raw       json.RawMessage
	Canonical []byte
	Digest    string
}

// parseOfflineItem reads one signed sale as received. A body that is not a
// JSON object, or whose identity fields cannot be read, is a malformed item
// the batch refuses whole; every other fault is judged by the sync.
func parseOfflineItem(raw json.RawMessage) (*offlineItem, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("the item is not a JSON object: %w", err)
	}
	var sale SignedOfflineSale
	if err := json.Unmarshal(raw, &sale); err != nil {
		return nil, fmt.Errorf("the item does not read as a signed offline sale: %w", err)
	}
	canonical, err := canonicalSaleBytes(obj)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	return &offlineItem{Sale: sale, Raw: raw, Canonical: canonical, Digest: hex.EncodeToString(sum[:])}, nil
}

// canonicalSaleBytes is the byte string a device signs (ADR 0011 section
// 6.1, canonical_sale_bytes in the Rust core): the sale as a JSON object
// without device_sig, keys sorted at every level, no whitespace, strings
// escaped the way serde_json escapes them. It is built from the item as
// received, so every field the device sent is covered by its signature.
func canonicalSaleBytes(obj map[string]any) ([]byte, error) {
	clone := make(map[string]any, len(obj))
	for k, v := range obj {
		if k == "device_sig" {
			continue
		}
		clone[k] = v
	}
	var b bytes.Buffer
	if err := writeCanonical(&b, clone); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeCanonical(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		b.WriteString(t.String())
	case string:
		writeCanonicalString(b, t)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonicalString(b, k)
			b.WriteByte(':')
			if err := writeCanonical(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("the item holds a value of kind %T the canonical form cannot carry", v)
	}
	return nil
}

// writeCanonicalString escapes a string as serde_json does: the quote, the
// backslash and the control characters, the five short forms where serde
// has them and \u00xx (lower case hex) for the rest; every other character,
// U+2028 and U+2029 included, is written as its UTF-8 bytes.
func writeCanonicalString(b *bytes.Buffer, s string) {
	const hexDigits = "0123456789abcdef"
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[r>>4])
			b.WriteByte(hexDigits[r&0xf])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// verifySaleSignature checks the device signature over the canonical bytes
// against the enrolled key (a 64 character hex Ed25519 public key; the
// signature is standard base64), as verify_sale_signature does on the device.
func verifySaleSignature(publicKeyHex string, item *offlineItem) bool {
	pk, err := hex.DecodeString(strings.TrimSpace(publicKeyHex))
	if err != nil || len(pk) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.Sale.DeviceSig))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pk), item.Canonical, sig)
}
