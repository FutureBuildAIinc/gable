// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package dealerprofile holds the one dealer wide profile every printed form
// shows: the dealer's legal and trading name, contact lines, remit to address
// and logo. It is a single row (migration 115).
package dealerprofile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
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

// The decoded size a logo may have. The byte cap alone is no bound on what
// printing allocates: a small file can declare a very large image.
const (
	MaxLogoSide   = 2000
	MaxLogoPixels = 4_000_000
)

// LogoSizeError is a logo whose declared dimensions are past the cap.
type LogoSizeError struct{ Width, Height int }

func (e *LogoSizeError) Error() string {
	return fmt.Sprintf("the logo is %d by %d pixels; the cap is %d pixels a side and %d pixels in all",
		e.Width, e.Height, MaxLogoSide, MaxLogoPixels)
}

// CheckLogoImage accepts raw only when it is one whole image of the named
// format ("png" or "jpeg") within the dimension cap: the header is read and
// the size checked before any pixel is decoded, then the whole image is
// decoded, and bytes after the image's end are refused.
func CheckLogoImage(raw []byte, format string) error {
	cfg, got, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if got != format {
		return fmt.Errorf("the image is %s, not %s", got, format)
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxLogoSide || cfg.Height > MaxLogoSide || cfg.Width*cfg.Height > MaxLogoPixels {
		return &LogoSizeError{Width: cfg.Width, Height: cfg.Height}
	}
	rd := bytes.NewReader(raw)
	switch format {
	case "png":
		// png.Decode on the bare reader, not image.Decode, which buffers
		// ahead and would hide what follows the image.
		if _, err := png.Decode(rd); err != nil {
			return err
		}
		if rd.Len() != 0 {
			return errors.New("bytes follow the end of the image")
		}
	case "jpeg":
		if _, err := jpeg.Decode(rd); err != nil {
			return err
		}
		// The JPEG decoder reads ahead, so the end marker is the check.
		if !bytes.HasSuffix(raw, []byte{0xff, 0xd9}) {
			return errors.New("bytes follow the end of the image")
		}
	}
	return nil
}
