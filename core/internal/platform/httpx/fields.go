// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Timestamp is the wire type of every timestamp (ADR 0001 section 12):
// RFC 3339 UTC with the Z, always at microsecond precision, whatever zone
// the value was read in. Optional timestamps are *Timestamp, so an unset one
// marshals as null (PtrTimestamp builds them from the database's *time.Time).
type Timestamp struct{ time.Time }

// TimestampOf wraps t for the wire.
func TimestampOf(t time.Time) Timestamp { return Timestamp{t} }

// PtrTimestamp wraps an optional time; nil stays nil.
func PtrTimestamp(t *time.Time) *Timestamp {
	if t == nil {
		return nil
	}
	ts := TimestampOf(*t)
	return &ts
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(`"` + FormatKeyTime(t.Time) + `"`), nil
}

func (t *Timestamp) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return errors.New("a timestamp is a JSON string")
	}
	parsed, err := parseWireTime(s)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

func parseWireTime(s string) (time.Time, error) {
	if !strings.HasSuffix(s, "Z") {
		return time.Time{}, errors.New("must be an RFC 3339 UTC timestamp ending in Z")
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, errors.New("must be an RFC 3339 UTC timestamp, for example 2030-01-02T03:04:05Z")
	}
	return parsed.UTC(), nil
}

// isAbsent reports whether a raw JSON field was missing or null.
func isAbsent(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// Int reads a raw JSON field that must be a plain integer: no decimal point,
// no exponent, no string (ADR 0001 section 7: a JSON number with a decimal
// point in a money field is a decode error, not a rounding event). Absent and
// null are the same: an error when required (null is not zero), otherwise
// (0, false) with no error. The field path names the entry in the 400.
func (v *Validator) Int(field string, raw json.RawMessage, required bool) (int64, bool) {
	if isAbsent(raw) {
		v.Check(!required, field, "is required")
		return 0, false
	}
	n, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil {
		v.Check(false, field, "must be an integer (no decimal point, exponent or quotes)")
		return 0, false
	}
	return n, true
}

// Quantity reads a raw JSON field that must be a JSON string holding a plain
// decimal (ADR 0001 section 7a), through ParseQuantity. Absent and null are
// handled as Int does.
func (v *Validator) Quantity(field string, raw json.RawMessage, required bool) (Quantity, bool) {
	if isAbsent(raw) {
		v.Check(!required, field, "is required")
		return 0, false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		v.Check(false, field, `must be a decimal string such as "12.5", not a JSON number`)
		return 0, false
	}
	q, err := ParseQuantity(s)
	if err != nil {
		v.Check(false, field, "must be a decimal string with at most 4 fraction digits: "+err.Error())
		return 0, false
	}
	return q, true
}

// UUID reads an identifier in the canonical lowercase hyphenated form (the
// form the wire writes). An absent value is an error when required.
func (v *Validator) UUID(field string, s *string, required bool) (uuid.UUID, bool) {
	if s == nil {
		v.Check(!required, field, "is required")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(*s)
	if err != nil || id.String() != *s || id == uuid.Nil {
		v.Check(false, field, "must be a UUID in lowercase hyphenated form")
		return uuid.Nil, false
	}
	return id, true
}

// Timestamp reads a raw JSON field that must be an RFC 3339 UTC string.
// Absent and null are handled as Int does; the result is nil when absent.
func (v *Validator) Timestamp(field string, raw json.RawMessage, required bool) (*Timestamp, bool) {
	if isAbsent(raw) {
		v.Check(!required, field, "is required")
		return nil, false
	}
	var ts Timestamp
	if err := json.Unmarshal(raw, &ts); err != nil {
		v.Check(false, field, err.Error())
		return nil, false
	}
	return &ts, true
}

// DecodeJSON decodes one JSON object from the request body into dst and
// refuses everything else as a 400 bad_request: an empty body, malformed
// JSON, a second document after the first, a field dst does not declare
// (a typo in a field name must not be a silent no-op, the same posture as
// unknown query parameters), and a value of the wrong JSON type, which names
// the field where the decoder knows it. A body past the route's size bound
// is a 413 payload_too_large.
//
// The usual dst declares strings, pointers and json.RawMessage fields, and
// the handler's validator (Int, Quantity, UUID, Timestamp) parses them with
// the full field path, index included: "lines[2].quantity".
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeFailure(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return BadRequest("request body must be a single JSON object")
		}
		return decodeFailure(err)
	}
	return nil
}

func decodeFailure(err error) error {
	var tooLarge *http.MaxBytesError
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooLarge):
		return PayloadTooLarge("request body is too large")
	case errors.Is(err, io.EOF):
		return BadRequest("request body is empty")
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		return BadRequest("request body is not valid JSON")
	case errors.As(err, &typeErr):
		field := indexPath.ReplaceAllString(typeErr.Field, "[$1]")
		if field == "" {
			return BadRequest("request body must be a JSON object")
		}
		return BadRequest("request body has a value of the wrong type",
			FieldError{Field: field, Message: "must be a JSON " + jsonKind(typeErr.Type)})
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		name = strings.Trim(name, `"`)
		return BadRequest("request body has a field this route does not accept",
			FieldError{Field: name, Message: "unknown field"})
	}
	return BadRequest("request body could not be read")
}

// indexPath turns the decoder's "lines.0.quantity" into "lines[0].quantity".
var indexPath = regexp.MustCompile(`\.(\d+)`)

// jsonKind names the JSON type a Go type decodes from, for the 400's message.
func jsonKind(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return "number"
	}
}
