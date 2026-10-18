// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"bytes"
	"errors"
	"fmt"
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

func TestLimiter_BoundedMemoryNeverRefusesANewCaller(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(1, time.Minute)
	l.now = func() time.Time { return now }
	l.maxKeys = 10
	// A flood of distinct keys inside one window fills the map ...
	for i := 0; i < 1000; i++ {
		now = now.Add(time.Millisecond)
		if !l.Allow(fmt.Sprintf("flood-%d", i)) {
			t.Fatalf("flood key %d was refused: a new caller must never be refused for want of room", i)
		}
	}
	if len(l.hits) > l.maxKeys {
		t.Errorf("the map holds %d keys, bound %d", len(l.hits), l.maxKeys)
	}
	// ... and a real caller arriving in the middle of it is served.
	if !l.Allow("a real customer") {
		t.Fatal("a new caller was refused while the map was full")
	}
	// The oldest keys went first: the most recent flood key is still counted.
	if l.Allow("flood-999") {
		t.Error("the newest key lost its count")
	}
}

func TestClientKey_GroupsIPv6By64(t *testing.T) {
	a := clientKey("2001:db8:1:2:aaaa:bbbb:cccc:dddd")
	b := clientKey("2001:db8:1:2:1111:2222:3333:4444")
	c := clientKey("2001:db8:1:3::1")
	if a != b || a == c {
		t.Errorf("keys %q %q %q: one /64 is one caller, the next /64 another", a, b, c)
	}
	if clientKey("203.0.113.9") == clientKey("203.0.113.10") {
		t.Error("two IPv4 callers share a key")
	}
	if clientKey("::ffff:203.0.113.9") != clientKey("203.0.113.9") {
		t.Error("a mapped IPv4 address keys differently")
	}
	if clientKey("not an address") == "" {
		t.Error("an unparseable address has no key")
	}
}
