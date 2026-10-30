// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// RULE (ADR 0011 section 6.1): the bytes a device signs are the sale without
// device_sig, keys sorted at every level, no whitespace, strings escaped as
// serde_json escapes them, and the server builds the same bytes from the item
// as received, so a field the device sent is a field its signature covers.
func TestCanonicalSaleBytesAreTheDevicesForm(t *testing.T) {
	raw := json.RawMessage(`{"z":1,"device_sig":"x","a":{"b":[2,{"d":null,"c":true}],"s":"q\"\\\n\u0001<&> é"},"m":-5}`)
	item, err := parseOfflineItem(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"b":[2,{"c":true,"d":null}],"s":"q\"\\\n\u0001<&>` + " é" + `"},"m":-5,"z":1}`
	if string(item.Canonical) != want {
		t.Errorf("canonical =\n%s\nwant\n%s", item.Canonical, want)
	}
}

// RULE (ADR 0011 section 6.1): a sale verifies under the enrolled key over
// its canonical bytes; any change to a signed figure, a field the device
// added, or the signature itself fails.
func TestSaleSignatureVerifiesOnlyTheSignedBytes(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	sign := func(body map[string]any) json.RawMessage {
		item, err := parseOfflineItem(mustJSON(t, body))
		if err != nil {
			t.Fatal(err)
		}
		body["device_sig"] = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, item.Canonical))
		return mustJSON(t, body)
	}
	good := sign(map[string]any{"client_id": "a", "total_cents": 1000, "lines": []any{}, "tenders": []any{}})
	item, err := parseOfflineItem(good)
	if err != nil {
		t.Fatal(err)
	}
	if !verifySaleSignature(pub, item) {
		t.Fatal("a signed sale does not verify")
	}
	var tampered map[string]any
	if err := json.Unmarshal(good, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["total_cents"] = 1
	if it, _ := parseOfflineItem(mustJSON(t, tampered)); verifySaleSignature(pub, it) {
		t.Error("a changed figure verifies")
	}
	tampered["total_cents"] = 1000
	tampered["replaces_sale_id"] = "11111111-1111-1111-1111-111111111111"
	if it, _ := parseOfflineItem(mustJSON(t, tampered)); verifySaleSignature(pub, it) {
		t.Error("a field added after signing verifies")
	}
	delete(tampered, "replaces_sale_id")
	tampered["device_sig"] = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if it, _ := parseOfflineItem(mustJSON(t, tampered)); verifySaleSignature(pub, it) {
		t.Error("a zero signature verifies")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
