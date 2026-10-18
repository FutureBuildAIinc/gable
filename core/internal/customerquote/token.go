// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

// The capability link. A link is a credential, not an authentication: whoever
// holds the token is treated as the customer of exactly one quote and nothing
// else. The token is 256 random bits from the operating system's secure
// random source, shown once (in the send response and the email), never
// logged and never stored: only its SHA-256 hash is, in
// pro_portal_share_tokens. Nothing in a link derives from an id.

// entropy is the token source. It is crypto/rand.Reader and nothing else in
// production; the unit test pins that and injects a fixed reader through
// mintToken.
var entropy io.Reader = rand.Reader

const (
	tokenBytes = 32
	// tokenLen is the unpadded base64url length of 32 bytes.
	tokenLen = 43
)

var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// MintToken returns a fresh raw token and its stored hash.
func MintToken() (raw, hash string, err error) { return mintToken(entropy) }

func mintToken(r io.Reader) (raw, hash string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", "", fmt.Errorf("mint token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashToken(raw), nil
}

// HashToken is the stored form of a raw token: lowercase hex SHA-256.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// TokenWellFormed reports whether a presented value has the shape of a
// minted token. A value that fails it never existed and costs no lookup.
func TokenWellFormed(raw string) bool { return tokenShape.MatchString(raw) }

// HashesEqual compares two stored hashes in constant time.
func HashesEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ErrQuoteExpired is the quote past its valid_until.
var ErrQuoteExpired = errors.New("the quote has expired")

// LinkExpiry is the instant a link dies: the quote's valid_until, bounded by
// the configured maximum lifetime.
func LinkExpiry(now, validUntil time.Time, maxTTL time.Duration) (time.Time, error) {
	if maxTTL <= 0 {
		return time.Time{}, errors.New("the maximum link lifetime must be positive")
	}
	if !validUntil.After(now) {
		return time.Time{}, ErrQuoteExpired
	}
	if limit := now.Add(maxTTL); limit.Before(validUntil) {
		return limit, nil
	}
	return validUntil, nil
}

// Link is a stored share token row.
type Link struct {
	ID string
	// QuoteID is the contractor's customer quote the link opens, empty for a
	// link to a dealer quote; DealerQuoteID is the converse. A link opens one
	// kind of target and no other.
	QuoteID       string
	DealerQuoteID string
	Hash          string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	RevokedAt     *time.Time
}

// Live reports whether the link answers at now: not revoked, not expired.
func (l Link) Live(now time.Time) bool {
	return l.RevokedAt == nil && now.Before(l.ExpiresAt)
}
