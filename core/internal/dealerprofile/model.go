// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package dealerprofile holds the one dealer wide profile every printed form
// shows: the dealer's legal and trading name, contact lines, remit to address
// and logo. It is a single row (migration 114).
package dealerprofile

import (
	"context"
	"errors"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// LogoPath is the route the logo is served from; Profile.LogoURL carries it
// when a logo is set.
const LogoPath = "/api/v1/dealer-profile/logo"

// MaxLogoBytes caps an uploaded logo.
const MaxLogoBytes = 512 * 1024

// ErrNotFound is the logo read's answer when none is set.
var ErrNotFound = errors.New("dealer profile: no logo")

// Profile is the wire shape of GET and PUT /api/v1/dealer-profile.
type Profile struct {
	LegalName   string          `json:"legal_name"`
	TradingName string          `json:"trading_name"`
	Phone       string          `json:"phone"`
	Email       string          `json:"email"`
	Website     string          `json:"website"`
	RemitTo     string          `json:"remit_to"`
	LogoURL     *string         `json:"logo_url"`
	Revision    int64           `json:"revision"`
	UpdatedAt   httpx.Timestamp `json:"updated_at"`
}

// DisplayName is the name a form leads with: the trading name, else the legal.
func (p Profile) DisplayName() string {
	if n := strings.TrimSpace(p.TradingName); n != "" {
		return n
	}
	return strings.TrimSpace(p.LegalName)
}

// Input is the writable slice of the profile.
type Input struct {
	LegalName   string
	TradingName string
	Phone       string
	Email       string
	Website     string
	RemitTo     string
}

// Logo is the stored image.
type Logo struct {
	Bytes       []byte
	ContentType string
}

// Reader is what a printing module needs: the profile and its logo.
type Reader interface {
	Get(ctx context.Context) (*Profile, error)
	GetLogo(ctx context.Context) (*Logo, error)
}
