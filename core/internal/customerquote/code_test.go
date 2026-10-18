// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestMintCode_SixDigitsUniformlyFromTheSecureSource(t *testing.T) {
	re := regexp.MustCompile(`^[0-9]{6}$`)
	seen := map[string]int{}
	for i := 0; i < 500; i++ {
		c, err := MintCode()
		if err != nil || !re.MatchString(c) {
			t.Fatalf("code %q, %v", c, err)
		}
		seen[c]++
	}
	if len(seen) < 450 {
		t.Errorf("only %d distinct codes in 500 draws", len(seen))
	}
	// Leading zeros are kept: a reader of zeros gives 000000.
	c, err := mintCode(bytes.NewReader(make([]byte, 64)))
	if err != nil || c != "000000" {
		t.Errorf("zero reader: %q, %v", c, err)
	}
}

func TestHashCode_SaltedAndBoundToTheQuote(t *testing.T) {
	salt, err := NewSalt()
	if err != nil || len(salt) != 32 {
		t.Fatalf("salt %q, %v", salt, err)
	}
	h := HashCode(salt, "q1", "123456")
	if len(h) != 64 || strings.Contains(h, "123456") {
		t.Errorf("hash %q", h)
	}
	if h == HashCode(salt, "q2", "123456") {
		t.Error("the hash does not depend on the quote")
	}
	if h == HashCode(salt+"x", "q1", "123456") {
		t.Error("the hash does not depend on the salt")
	}
	if h == HashCode(salt, "q1", "123457") {
		t.Error("the hash does not depend on the code")
	}
}

func TestCodeRecordCheck(t *testing.T) {
	now := time.Date(2030, 5, 1, 10, 0, 0, 0, time.UTC)
	salt := "00112233445566778899aabbccddeeff"
	rec := func() CodeRecord {
		return CodeRecord{QuoteID: "q", Salt: salt, Hash: HashCode(salt, "q", "424242"), MaxAttempts: 3, ExpiresAt: now.Add(10 * time.Minute)}
	}
	if err := rec().Check(now, "424242"); err != nil {
		t.Errorf("the right code: %v", err)
	}
	if err := rec().Check(now, "424243"); !errors.Is(err, ErrCodeWrong) {
		t.Errorf("a wrong code: %v", err)
	}
	if err := rec().Check(now, "42424"); !errors.Is(err, ErrCodeWrong) {
		t.Errorf("a short code: %v", err)
	}
	if err := rec().Check(now.Add(10*time.Minute), "424242"); !errors.Is(err, ErrCodeExpired) {
		t.Errorf("expired at the instant: %v", err)
	}
	used := rec()
	at := now.Add(-time.Minute)
	used.ConsumedAt = &at
	if err := used.Check(now, "424242"); !errors.Is(err, ErrCodeConsumed) {
		t.Errorf("a used code: %v", err)
	}
	locked := rec()
	locked.Attempts = 3
	if err := locked.Check(now, "424242"); !errors.Is(err, ErrCodeLocked) {
		t.Errorf("after the attempt limit even the right code is refused: %v", err)
	}
	// Order of refusals: consumed and expired outrank a wrong guess.
	if err := used.Check(now, "000000"); !errors.Is(err, ErrCodeConsumed) {
		t.Errorf("consumed and wrong: %v", err)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("hit %d refused", i+1)
		}
	}
	if l.Allow("k") {
		t.Error("the fourth hit in the window was allowed")
	}
	if !l.Allow("other") {
		t.Error("another key shares the budget")
	}
	now = now.Add(61 * time.Second)
	if !l.Allow("k") {
		t.Error("the window did not slide")
	}
}

func TestLimiter_BoundedMemory(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(1, time.Minute)
	l.now = func() time.Time { return now }
	l.maxKeys = 10
	for i := 0; i < 10; i++ {
		if !l.Allow(string(rune('a' + i))) {
			t.Fatalf("key %d refused under the cap", i)
		}
	}
	if l.Allow("overflow") {
		t.Error("a key past the cap was allowed (the limiter fails closed)")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("overflow") {
		t.Error("after the window the expired keys were not released")
	}
}
