// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package dealerprofile holds the one dealer wide profile every printed form
// shows: the dealer's legal and trading name, contact lines, remit to address,
// logo and locale (the BCP 47 tag every date the app shows is formatted for).
// It is a single row (migration 115; the locale column is migration 119).
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
	"golang.org/x/text/language"
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
	Locale      string          `json:"locale"`
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

// Input is the writable slice of the profile. An empty Locale leaves the
// stored one alone (the service fills it from the locked row), so a caller
// from before the field existed never resets it.
type Input struct {
	LegalName   string
	TradingName string
	Phone       string
	Email       string
	Website     string
	RemitTo     string
	Locale      string
}

// Logo is the stored image.
type Logo struct {
	Bytes       []byte
	ContentType string
}

// MaxLocaleTagLength caps a locale tag (BCP 47's own suggestion is 35).
const MaxLocaleTagLength = 35

// CheckLocale accepts a BCP 47 tag the date formatter can use ("en-US",
// "en-CA", "fr-CA") and refuses anything else in words. The canonical form
// the parser would rewrite to (it title cases the region, for example) is
// not required; the tag is stored as given.
func CheckLocale(tag string) error {
	if tag == "" {
		return errors.New("the locale must be a language tag such as en-US")
	}
	if len(tag) > MaxLocaleTagLength {
		return fmt.Errorf("the locale must be at most %d characters", MaxLocaleTagLength)
	}
	if _, err := language.Parse(tag); err != nil {
		return fmt.Errorf("the locale %q is not a language tag the date formatter accepts (a BCP 47 tag such as en-US)", tag)
	}
	return nil
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

// logoFormat names the format a logo's first bytes declare, "png" or "jpeg",
// or "" for anything else. The logo accepts only those two, so they are
// decoded by their own packages and never through the image registry, whose
// other decoders (WebP, TIFF, BMP, GIF) a logo has no business reaching.
func logoFormat(raw []byte) string {
	switch {
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(raw, []byte{0xff, 0xd8, 0xff}):
		return "jpeg"
	}
	return ""
}

// logoConfig reads a logo's header with the decoder of its sniffed format.
func logoConfig(raw []byte) (image.Config, string, error) {
	format := logoFormat(raw)
	var cfg image.Config
	var err error
	switch format {
	case "png":
		cfg, err = png.DecodeConfig(bytes.NewReader(raw))
	case "jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(raw))
	default:
		err = errors.New("the logo must be a png or a jpeg image")
	}
	return cfg, format, err
}

// LogoEmbeddable reports whether the form renderer can embed raw: a PNG or
// JPEG within the dimension cap, and for a PNG not 16 bit and not interlaced
// (the PDF writer refuses both, which would fail every form). Only headers
// are read.
func LogoEmbeddable(raw []byte) bool {
	cfg, format, err := logoConfig(raw)
	if err != nil {
		return false
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxLogoSide || cfg.Height > MaxLogoSide || cfg.Width*cfg.Height > MaxLogoPixels {
		return false
	}
	if format == "png" {
		// IHDR: 8 byte signature, 8 byte chunk head, width, height, then
		// bit depth at 24, colour type, compression, filter, interlace at 28.
		if len(raw) < 29 || raw[24] == 16 || raw[28] != 0 {
			return false
		}
	}
	return true
}

// CheckLogoImage accepts raw only when it is one whole image of the named
// format ("png" or "jpeg") within the dimension cap: the header is read and
// the size checked before any pixel is decoded, then the whole image is
// decoded, and bytes after the image's end are refused (for a JPEG, after its
// end marker: bytes a segment walk cannot tell from scan data are not caught
// here, which is why a JPEG upload is then written again, see
// ReencodeLogoJPEG). A PNG's stored bytes are the uploaded ones.
func CheckLogoImage(raw []byte, format string) error {
	cfg, got, err := logoConfig(raw)
	if err != nil {
		return err
	}
	if got != format {
		return fmt.Errorf("the image is %s, not %s", got, format)
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxLogoSide || cfg.Height > MaxLogoSide || cfg.Width*cfg.Height > MaxLogoPixels {
		return &LogoSizeError{Width: cfg.Width, Height: cfg.Height}
	}
	if !LogoEmbeddable(raw) {
		return errors.New("the png is 16 bit or interlaced, which the forms cannot embed; save it as an 8 bit, non interlaced png")
	}
	rd := bytes.NewReader(raw)
	switch format {
	case "png":
		// The decoder reads the bare reader, so what follows the image
		// stays unread and shows as bytes left.
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
		// The JPEG decoder reads ahead, so the image's own end is found by
		// walking its segments, not by looking for FF D9 at the tail.
		if end, ok := jpegEnd(raw); !ok || end != len(raw) {
			return errors.New("bytes follow the end of the image")
		}
	}
	return nil
}

// LogoJPEGQuality is the quality a JPEG logo is written again at.
const LogoJPEGQuality = 90

// ReencodeLogoJPEG writes the decoded image of a JPEG that CheckLogoImage
// passed as a new JPEG at LogoJPEGQuality, so nothing carried in its
// segments, between its scans or inside its last scan survives. The content
// type and the picture's size are unchanged.
func ReencodeLogoJPEG(raw []byte) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: LogoJPEGQuality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// jpegEnd walks a JPEG's segments and returns the offset just past the EOI
// that follows its last scan. It reads marker segments by their lengths up to
// each SOS, then the entropy coded data to the next marker that is not a
// stuffed FF 00 or a restart marker, and continues through further scans
// (progressive files have many). ok is false for a structure it cannot walk.
func jpegEnd(raw []byte) (end int, ok bool) {
	n := len(raw)
	if n < 4 || raw[0] != 0xff || raw[1] != 0xd8 {
		return 0, false
	}
	i := 2
	for i < n {
		// A marker: FF, any fill FFs, then the marker code.
		if raw[i] != 0xff {
			return 0, false
		}
		for i < n && raw[i] == 0xff {
			i++
		}
		if i >= n {
			return 0, false
		}
		m := raw[i]
		i++
		switch {
		case m == 0xd9:
			return i, true
		case m == 0x01 || (m >= 0xd0 && m <= 0xd8):
			continue // standalone markers carry no length
		case m == 0x00:
			return 0, false
		}
		if i+2 > n {
			return 0, false
		}
		l := int(raw[i])<<8 | int(raw[i+1])
		if l < 2 || i+l > n {
			return 0, false
		}
		i += l
		if m != 0xda {
			continue
		}
		// Entropy coded data: runs to a marker that is neither a stuffed
		// zero nor a restart marker.
		for i < n {
			if raw[i] != 0xff {
				i++
				continue
			}
			if i+1 >= n {
				return 0, false
			}
			c := raw[i+1]
			if c == 0x00 || (c >= 0xd0 && c <= 0xd7) {
				i += 2
				continue
			}
			if c == 0xff {
				i++ // fill byte before a marker
				continue
			}
			break // a real marker at i
		}
	}
	return 0, false
}
