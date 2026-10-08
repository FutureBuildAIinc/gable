// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Page limits (ADR 0001 §2): the default page size, and the bound a client
// cannot exceed. A limit outside [1, MaxPageLimit] is refused, not clamped.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// Cursor shape bounds. These exist so a hostile cursor cannot be a decode
// or memory burden: the encoded token is short, the decoded payload is
// bounded, and every keyset part is a printable, bounded string.
const (
	cursorVersion         = 1
	maxCursorDecodedBytes = 1024
	maxCursorKeyParts     = 8
	maxCursorKeyPartBytes = 256
)

// cursorPayload is the JSON object inside every minted cursor: the format
// version, the ordering scope the cursor was minted under, and the keyset
// tuple of the last row of the page, in the ordering's column order.
type cursorPayload struct {
	V int      `json:"v"`
	O string   `json:"o"`
	K []string `json:"k"`
}

// validateKeyPart applies the per-part shape rules shared by minting and
// decoding: a part is a non-empty, bounded, printable string with no
// control characters. Control characters are refused so a cursor part can
// never smuggle structure into a log line or a debug dump.
func validateKeyPart(part string) bool {
	if part == "" || len(part) > maxCursorKeyPartBytes {
		return false
	}
	for i := 0; i < len(part); i++ {
		if part[i] < 0x20 || part[i] == 0x7f {
			return false
		}
	}
	return true
}

// MintCursor mints the opaque next_cursor value from a page's last row:
// the ordering scope (the converting module's stable name for that list's
// ordering) and the keyset values of the last row, in the ordering's column
// order. It refuses to mint what DecodeCursor would refuse to read, so no
// handler can hand a client an unloadable cursor. The error is a server
// bug (bad scope constant or unshapeable row value), not a client input:
// wrap the handler's return in WriteError, which answers it as a 500.
func MintCursor(scope string, key ...string) (string, error) {
	if scope == "" || len(scope) > maxCursorKeyPartBytes {
		return "", &Error{Status: http.StatusInternalServerError, Code: CodeInternalError,
			Message: "cursor scope is empty or too long"}
	}
	for _, part := range scope {
		if part < 0x20 || part == 0x7f {
			return "", &Error{Status: http.StatusInternalServerError, Code: CodeInternalError,
				Message: "cursor scope carries a control character"}
		}
	}
	if len(key) == 0 || len(key) > maxCursorKeyParts {
		return "", &Error{Status: http.StatusInternalServerError, Code: CodeInternalError,
			Message: "cursor keyset must have between 1 and " + strconv.Itoa(maxCursorKeyParts) + " parts"}
	}
	for _, part := range key {
		if !validateKeyPart(part) {
			return "", &Error{Status: http.StatusInternalServerError, Code: CodeInternalError,
				Message: "cursor keyset part is empty, too long, or carries a control character"}
		}
	}

	payload, err := json.Marshal(cursorPayload{V: cursorVersion, O: scope, K: key})
	if err != nil {
		return "", &Error{Status: http.StatusInternalServerError, Code: CodeInternalError,
			Message: "cursor payload cannot be encoded"}
	}
	// The payload bound is what DecodeCursor enforces, so it is checked here
	// on the marshalled bytes: many parts, or characters JSON escapes, grow
	// the payload past the bound even when every part alone is within its own.
	if len(payload) > maxCursorDecodedBytes {
		return "", &Error{Status: http.StatusInternalServerError, Code: CodeInternalError,
			Message: "cursor payload is past the decoded size bound"}
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// cursorBadRequest is the 400 every refused cursor answers with: a client
// error naming the cursor field, so the caller is told its token is
// unusable rather than being quietly wound back to the head of the list.
func cursorBadRequest(message string) *Error {
	return BadRequest(message, FieldError{Field: "cursor", Message: message})
}

// DecodeCursor validates and inverts MintCursor for one ordering scope. A
// cursor minted for a different scope is refused too: resuming a different
// sort would silently repeat or drop rows the client already acted on. The
// cursor is not signed (ADR 0001 §2 records why): strict structure plus the
// scope binding are the integrity bound, and a forged but well-formed
// cursor can only seek within the same ordered, filtered list.
func DecodeCursor(raw, scope string) ([]string, error) {
	// The base64 form of maxCursorDecodedBytes is a fixed ceiling; anything
	// longer is refused before it is touched.
	maxEncoded := base64.RawURLEncoding.EncodedLen(maxCursorDecodedBytes)
	if len(raw) == 0 || len(raw) > maxEncoded {
		return nil, cursorBadRequest("cursor is malformed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, cursorBadRequest("cursor is malformed")
	}
	// Canonical form: the re-encoded value must match byte for byte, so
	// padded variants and alternate trailing bits are refused rather than
	// accepted as aliases.
	if base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return nil, cursorBadRequest("cursor is malformed")
	}
	if len(decoded) > maxCursorDecodedBytes {
		return nil, cursorBadRequest("cursor is malformed")
	}

	var payload cursorPayload
	dec := json.NewDecoder(bytes.NewReader(decoded))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		return nil, cursorBadRequest("cursor is malformed")
	}
	if payload.V != cursorVersion {
		return nil, cursorBadRequest("cursor is from an unsupported version")
	}
	if payload.O != scope {
		return nil, cursorBadRequest("cursor was not minted for this ordering")
	}
	if len(payload.K) == 0 || len(payload.K) > maxCursorKeyParts {
		return nil, cursorBadRequest("cursor keyset has an invalid length")
	}
	for _, part := range payload.K {
		if !validateKeyPart(part) {
			return nil, cursorBadRequest("cursor keyset is malformed")
		}
	}
	return payload.K, nil
}

// CursorPage is the parsed list window: the decoded keyset of the last row
// of the previous page (nil on the first page) and the effective limit.
type CursorPage struct {
	Key   []string
	Limit int
}

// ParseListQuery reads `cursor` and `limit` from a list route's query string
// (ADR 0001 §2). Absent parameters are the first page at the default limit.
// A present but malformed cursor, a repeated cursor, or a limit the server
// will not honor is a 400 *Error naming the field: never a silent restart,
// never a silent clamp.
func ParseListQuery(r *http.Request, scope string) (CursorPage, error) {
	q := r.URL.Query()

	page := CursorPage{Limit: DefaultPageLimit}
	if vals := q["cursor"]; len(vals) > 0 {
		if len(vals) > 1 {
			return CursorPage{}, cursorBadRequest("cursor parameter is repeated")
		}
		key, err := DecodeCursor(vals[0], scope)
		if err != nil {
			return CursorPage{}, err
		}
		page.Key = key
	}

	if vals := q["limit"]; len(vals) > 0 {
		if len(vals) > 1 {
			return CursorPage{}, BadRequest("limit parameter is repeated",
				FieldError{Field: "limit", Message: "parameter is repeated"})
		}
		n, err := strconv.Atoi(strings.TrimSpace(vals[0]))
		if err != nil {
			return CursorPage{}, BadRequest("limit is not an integer",
				FieldError{Field: "limit", Message: "is not an integer"})
		}
		if n < 1 || n > MaxPageLimit {
			return CursorPage{}, &Error{Status: http.StatusBadRequest, Code: CodeValidationFailed,
				Message: "limit is out of range",
				Details: []FieldError{{Field: "limit",
					Message: "must be between 1 and " + strconv.Itoa(MaxPageLimit)}}}
		}
		page.Limit = n
	}

	return page, nil
}
