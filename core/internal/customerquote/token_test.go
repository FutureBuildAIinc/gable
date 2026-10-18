// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestMintToken_256BitsFromTheSecureRandomSource(t *testing.T) {
	if entropy != io.Reader(rand.Reader) {
		t.Fatal("the token entropy source is not crypto/rand.Reader")
	}
	raw, hash, err := MintToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) != 32 {
		t.Fatalf("token decodes to %d bytes (%v), want 32", len(b), err)
	}
	if len(raw) != 43 {
		t.Errorf("token is %d characters, want 43", len(raw))
	}
	sum := sha256.Sum256([]byte(raw))
	if hash != hex.EncodeToString(sum[:]) {
		t.Error("the returned hash is not the SHA-256 of the token")
	}
	if !TokenWellFormed(raw) {
		t.Error("a minted token fails its own shape check")
	}
	seen := map[string]bool{raw: true}
	for i := 0; i < 200; i++ {
		r, _, _ := MintToken()
		if seen[r] {
			t.Fatal("two minted tokens collided")
		}
		seen[r] = true
	}
}

func TestMintToken_UsesTheInjectedReader(t *testing.T) {
	zeros := bytes.NewReader(make([]byte, 32))
	raw, _, err := mintToken(zeros)
	if err != nil {
		t.Fatal(err)
	}
	if raw != strings.Repeat("A", 43) {
		t.Errorf("the reader's bytes are not what the token is made of: %q", raw)
	}
	if _, _, err := mintToken(bytes.NewReader(make([]byte, 5))); err == nil {
		t.Error("a short read produced a token")
	}
}

func TestTokenWellFormed(t *testing.T) {
	good, _, _ := MintToken()
	if !TokenWellFormed(good) {
		t.Fatal("good token refused")
	}
	for _, bad := range []string{"", "short", good + "A", good[:42], strings.Repeat("!", 43), good[:42] + "=", " " + good[1:]} {
		if TokenWellFormed(bad) {
			t.Errorf("%q passed the shape check", bad)
		}
	}
}

func TestHashesEqual_ConstantTimeAndExact(t *testing.T) {
	a := HashToken("one")
	if !HashesEqual(a, HashToken("one")) {
		t.Error("equal hashes compared unequal")
	}
	if HashesEqual(a, HashToken("two")) || HashesEqual(a, "") {
		t.Error("different hashes compared equal")
	}
	if HashesEqual(a, a[:63]) {
		t.Error("a prefix compared equal")
	}
}

func TestLinkExpiry(t *testing.T) {
	now := time.Date(2030, 3, 1, 9, 0, 0, 0, time.UTC)
	max := 60 * 24 * time.Hour
	valid := now.Add(10 * 24 * time.Hour)
	got, err := LinkExpiry(now, valid, max)
	if err != nil || !got.Equal(valid) {
		t.Errorf("defaults to valid_until: %v, %v", got, err)
	}
	far := now.Add(400 * 24 * time.Hour)
	got, err = LinkExpiry(now, far, max)
	if err != nil || !got.Equal(now.Add(max)) {
		t.Errorf("bounded by the configured maximum: %v, %v", got, err)
	}
	if _, err := LinkExpiry(now, now, max); !errors.Is(err, ErrQuoteExpired) {
		t.Errorf("a quote already past valid_until: %v", err)
	}
	if _, err := LinkExpiry(now, valid, 0); err == nil {
		t.Error("a zero maximum lifetime was accepted")
	}
}

func TestIsLinkLive(t *testing.T) {
	now := time.Date(2030, 3, 1, 9, 0, 0, 0, time.UTC)
	exp := now.Add(time.Hour)
	if !(Link{ExpiresAt: exp}).Live(now) {
		t.Error("a fresh link is not live")
	}
	if (Link{ExpiresAt: now}).Live(now) {
		t.Error("a link live at the instant it expires")
	}
	rev := now.Add(-time.Minute)
	if (Link{ExpiresAt: exp, RevokedAt: &rev}).Live(now) {
		t.Error("a revoked link is live")
	}
}
