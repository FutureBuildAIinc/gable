// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sync"
	"time"
)

// The verification code that may gate acceptance: six digits from the secure
// random source, hashed with a per row salt and the quote's id, single use,
// short lived, attempt limited. The code itself is sent to the customer and
// never stored.

const (
	// CodeTTL is how long a code lives.
	CodeTTL = 10 * time.Minute
	// CodeMaxAttempts is the wrong guesses a code survives.
	CodeMaxAttempts = 5
)

var codeShape = regexp.MustCompile(`^[0-9]{6}$`)

// MintCode returns a uniformly random six digit code, leading zeros kept.
func MintCode() (string, error) { return mintCode(entropy) }

func mintCode(r io.Reader) (string, error) {
	n, err := rand.Int(r, big.NewInt(1_000_000))
	if err != nil {
		return "", fmt.Errorf("mint code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// NewSalt returns a fresh 16 byte salt as hex.
func NewSalt() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(entropy, b); err != nil {
		return "", fmt.Errorf("mint salt: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashCode is the stored form of a code: SHA-256 over the salt, the quote id
// and the code, lowercase hex.
func HashCode(salt, quoteID, code string) string {
	sum := sha256.Sum256([]byte("gable.customer-quote.code\x00" + salt + "\x00" + quoteID + "\x00" + code))
	return hex.EncodeToString(sum[:])
}

// The reasons a code is refused.
var (
	ErrCodeWrong    = errors.New("the code is not right")
	ErrCodeExpired  = errors.New("the code has expired")
	ErrCodeConsumed = errors.New("the code has already been used")
	ErrCodeLocked   = errors.New("too many wrong codes")
)

// CodeRecord is a stored code row.
type CodeRecord struct {
	QuoteID     string
	Salt        string
	Hash        string
	Attempts    int
	MaxAttempts int
	ExpiresAt   time.Time
	ConsumedAt  *time.Time
}

// Check judges a presented code against the record. The caller holds the row
// lock, counts the attempt whatever the answer is, and marks the row consumed
// on success; Check is pure.
func (c CodeRecord) Check(now time.Time, presented string) error {
	switch {
	case c.ConsumedAt != nil:
		return ErrCodeConsumed
	case !now.Before(c.ExpiresAt):
		return ErrCodeExpired
	case c.Attempts >= c.MaxAttempts:
		return ErrCodeLocked
	}
	if !codeShape.MatchString(presented) {
		return ErrCodeWrong
	}
	if !HashesEqual(HashCode(c.Salt, c.QuoteID, presented), c.Hash) {
		return ErrCodeWrong
	}
	return nil
}

// Limiter is a sliding window request limiter keyed by a string. It holds a
// bounded number of keys and fails closed past the bound: under a flood of
// distinct keys, new keys are refused until old ones age out.
type Limiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	hits    map[string][]time.Time
	maxKeys int
	now     func() time.Time
}

// NewLimiter allows max hits per key per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, hits: map[string][]time.Time{}, maxKeys: 100_000, now: time.Now}
}

// Allow counts a hit on key and reports whether it is within the budget.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	h, known := l.hits[key]
	if !known && len(l.hits) >= l.maxKeys {
		l.sweep(cutoff)
		if len(l.hits) >= l.maxKeys {
			return false
		}
	}
	i := 0
	for i < len(h) && !h[i].After(cutoff) {
		i++
	}
	h = h[i:]
	if len(h) >= l.max {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	return true
}

func (l *Limiter) sweep(cutoff time.Time) {
	for k, h := range l.hits {
		if len(h) == 0 || !h[len(h)-1].After(cutoff) {
			delete(l.hits, k)
		}
	}
}
