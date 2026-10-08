// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"fmt"
	"math"
	"net/http"
	"strings"
)

// WriteRevisionETag sets the response ETag for a document revision: the
// revision in quotes, the strong validator form IfMatchRevision reads.
// Every read of a mutable document writes it, so a client always leaves
// with the revision it must send back (ADR 0001 §11).
func WriteRevisionETag(w http.ResponseWriter, revision int64) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, revision))
}

// IfMatchRevision parses an If-Match header value into the revision it
// names. The strong form `"3"` and the weak form `W/"3"` both carry
// revision 3; anything else (a bare number, `*`, a list, an entity tag
// that is not one decimal integer between the quotes) is not a revision
// for this package and reports false. The header's absence is the empty
// string, which also reports false: absence is CheckRevision's case.
func IfMatchRevision(value string) (int64, bool) {
	quoted := value
	weak := strings.HasPrefix(quoted, `W/`)
	if weak {
		quoted = quoted[len(`W/`):]
	}
	if len(quoted) < 3 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return 0, false
	}
	digits := quoted[1 : len(quoted)-1]
	if digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	var n int64
	for i := 0; i < len(digits); i++ {
		d := digits[i]
		if d < '0' || d > '9' {
			return 0, false
		}
		if n > math.MaxInt64/10 || (n == math.MaxInt64/10 && d > '7') {
			return 0, false
		}
		n = n*10 + int64(d-'0')
	}
	return n, true
}

// CheckRevision resolves the precondition of a write against the revision
// the document currently holds. The client's revision arrives as the
// If-Match header, the body's revision field, or both; current is what the
// row carries now.
//
// Neither carrier present is 428 precondition_required: a write without a
// revision would silently discard a concurrent editor's work. A revision
// that does not match current is 409 stale_revision. A header that is not
// a revision, or two carriers that disagree with each other, is a 400: the
// client stated its precondition two ways and both cannot be honored.
func CheckRevision(current int64, ifMatch string, bodyRevision *int64) error {
	if ifMatch == "" && bodyRevision == nil {
		return PreconditionRequired("this write needs If-Match or a body revision")
	}

	var fromHeader int64
	var haveHeader bool
	if ifMatch != "" {
		n, ok := IfMatchRevision(ifMatch)
		if !ok {
			return BadRequest("If-Match does not carry a revision",
				FieldError{Field: "If-Match", Message: "must be the quoted revision, for example \"3\""})
		}
		fromHeader, haveHeader = n, true
	}

	switch {
	case haveHeader && bodyRevision != nil:
		if fromHeader != *bodyRevision {
			return BadRequest("If-Match and the body revision disagree",
				FieldError{Field: "revision", Message: "does not match If-Match"})
		}
	case haveHeader:
		bodyRevision = &fromHeader
	}

	if *bodyRevision != current {
		return StaleRevision("the document was changed after this revision was read; reload and retry")
	}
	return nil
}
