// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// normaliseTranscript rewrites every value that legitimately varies between
// two runs of the same script on the same code onto stable placeholders, in
// order of first appearance, and leaves everything else untouched.
//
//   - UUIDs (primary keys, request ids, dev-mode POS cashier ids) become
//     <id-1>, <id-2>, ... in order of first appearance anywhere in the
//     transcript, so an id read back in a later step keeps its placeholder.
//   - Timestamps become <ts+Nd> / <ts-Nd>: the placeholder is emitted only
//     after the match itself has asserted the RFC 3339 (or Postgres text)
//     shape - a timestamp that changes format can no longer hide behind it -
//     and it carries the value's day offset from the seed day, so date
//     arithmetic carried in timestamp fields (net-30 due dates, quote
//     expiry) is pinned while the within-day time, which rides on run
//     timing, stays free.
//   - Calendar dates become their day offset from the seed day: <day+30> for
//     a month ahead, <day-45> for six weeks back, <day+0> for the seed day
//     itself. Absolute times are per-run (the seed dates everything relative
//     to its own clock), but their day arithmetic is behaviour: a net-30 due
//     date that becomes net-0, or a 30-day quote expiry that becomes 31,
//     changes the placeholder and fails the golden.
//   - Generated secrets and tokens (sk_live_ keys, JWTs) become placeholders;
//     their values are random by construction.
//   - Elapsed-time measurements (parse_time_ms) become <ms>; a duration is
//     not behaviour.
//
// Everything else - money, statuses, field names, error text, null versus
// empty array - is real behaviour and is compared byte for byte.
func normaliseTranscript(steps []capturedStep) []capturedStep {
	n := &normaliser{ids: map[string]string{}, seedDay: seedDay}
	for i := range steps {
		steps[i].Request.Path = n.string(steps[i].Request.Path)
		steps[i].Request.Body = n.value(steps[i].Request.Body, "")
		for k, v := range steps[i].Request.Headers {
			steps[i].Request.Headers[k] = n.string(v)
		}
		steps[i].Response.Body = n.value(steps[i].Response.Body, "")
	}
	return steps
}

type normaliser struct {
	ids     map[string]string
	count   int
	seedDay string // UTC day of the seed, "2006-01-02"; dates normalise to offsets from it
}

var (
	reUUID  = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	reJWT   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	reTS    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})`)
	rePgTS  = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d+)?(\+\d{2})?`)
	reDate  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	reKey   = regexp.MustCompile(`sk_live_[A-Za-z0-9_-]{2,}`)
	rePDFDT = regexp.MustCompile(`D:\d{14}Z?`)
)

// volatileNumberFields are response fields whose numeric values measure the
// run, not the behaviour: elapsed time, the readiness pool census taken at
// request time (pool_max stays pinned; it is configured, not observed), and
// the quote analytics average days-to-close, which at this base averages
// only the quotes the script itself closes milliseconds after creating them
// (the seeded book has none), so its value is sub-second run timing.
var volatileNumberFields = map[string]string{
	"parse_time_ms":     "<ms>",
	"pool_total":        "<poolstat>",
	"pool_idle":         "<poolstat>",
	"pool_in_use":       "<poolstat>",
	"avg_days_to_close": "<days>",
}

// volatileStringFields are response fields whose string values are random by
// construction: /healthz/ready's uptime is a duration since process start,
// and an exposure event's idempotency key is a hash over its creation
// instant, and a list's next_cursor, which encodes the last row's creation
// instant and its random id (a null cursor, the last page, stays null).
var volatileStringFields = map[string]string{
	"uptime":          "<uptime>",
	"idempotency_key": "<idem-key>",
	"next_cursor":     "<cursor>",
}

// seedFixedDateFields carry fixed calendar dates in the demo seed's vehicle
// fixture: hardcoded calendar values, unlike every other date in the seed,
// which is drawn relative to the seed clock. A seed-day offset for them would
// drift by one every day the calendar advances, so they normalise to the
// plain timestamp placeholder (the shape check still applies).
var seedFixedDateFields = map[string]bool{
	"insurance_expiry":  true,
	"next_service_date": true,
}

func (n *normaliser) value(v any, key string) any {
	switch x := v.(type) {
	case string:
		if ph, ok := volatileStringFields[key]; ok {
			return ph
		}
		if seedFixedDateFields[key] && (reTS.MatchString(x) || rePgTS.MatchString(x)) {
			return "<ts>"
		}
		return n.string(x)
	case json.Number:
		if ph, ok := volatileNumberFields[key]; ok {
			return ph
		}
		return x
	case map[string]any:
		// Walk keys in sorted order so id numbering by first appearance is
		// stable across runs regardless of Go's random map iteration.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			x[k] = n.value(x[k], k)
		}
		return x
	case []any:
		for i, vv := range x {
			x[i] = n.value(vv, key)
		}
		return x
	case *multipartDef:
		y := *x
		return &y
	default:
		return v
	}
}

func (n *normaliser) string(s string) string {
	// JWTs first: their base64url segments can contain date-like runs.
	s = reJWT.ReplaceAllStringFunc(s, func(m string) string { return "<jwt>" })
	s = reKey.ReplaceAllStringFunc(s, func(m string) string { return "<api-key>" })
	s = reUUID.ReplaceAllStringFunc(s, func(m string) string {
		if ph, ok := n.ids[m]; ok {
			return ph
		}
		n.count++
		ph := fmt.Sprintf("<id-%d>", n.count)
		n.ids[m] = ph
		return ph
	})
	s = reTS.ReplaceAllStringFunc(s, n.tsPlaceholder)
	s = rePgTS.ReplaceAllStringFunc(s, n.tsPlaceholder)
	s = reDate.ReplaceAllStringFunc(s, n.dayOffset)
	return s
}

// tsPlaceholder rewrites one timestamp as <ts+Nd> / <ts-Nd>: N is the value's
// day offset from the seed day (taken from the leading date; the within-day
// time is run timing). The regex has already asserted the shape; a parse
// failure is a harness bug and keeps the raw value (loudly failing every
// comparison) rather than silently degrading.
func (n *normaliser) tsPlaceholder(m string) string {
	d, err := time.Parse(dayLayout, m[:10])
	if err != nil {
		return "<ts>"
	}
	off := int(d.Sub(parseSeedDay(n.seedDay)).Hours() / 24)
	if off >= 0 {
		return fmt.Sprintf("<ts+%dd>", off)
	}
	return fmt.Sprintf("<ts%dd>", off)
}

// dayOffset rewrites one calendar date as its day offset from the seed day.
// The regex guarantees the "2006-01-02" shape, so a parse failure is a harness
// bug and keeps the raw date (loudly failing every comparison) rather than
// silently degrading to a placeholder.
func (n *normaliser) dayOffset(m string) string {
	d, err := time.Parse(dayLayout, m)
	if err != nil {
		return "<date>"
	}
	off := int(d.Sub(parseSeedDay(n.seedDay)).Hours() / 24)
	if off >= 0 {
		return fmt.Sprintf("<day+%d>", off)
	}
	return fmt.Sprintf("<day%d>", off)
}

// parseSeedDay parses the seed day; only TestMain writes it, in dayLayout.
func parseSeedDay(s string) time.Time {
	d, err := time.Parse(dayLayout, s)
	if err != nil {
		panic("goldens: seed day not in " + dayLayout + ": " + s)
	}
	return d
}

// binarySummary describes a non-text body: its byte length and a hash over
// the document's printed text. Byte-level hashes are impossible for these
// PDFs: the layout engine positions every string by its rendered width, and
// the strings embed per-run uuids, so coordinates shift with every run. The
// hash is therefore taken over the text-showing strings of the page content
// streams (Flate streams decompressed first), with the same volatile-value
// normalisation applied: it pins what the document says, not where each glyph
// sits.
func binarySummary(raw []byte) map[string]any {
	n := &normaliser{ids: map[string]string{}, seedDay: seedDay}
	text := pdfText(raw)
	sum := sha256.Sum256([]byte(n.string(text)))
	return map[string]any{
		"length":      json.Number(fmt.Sprintf("%d", len(raw))),
		"text_sha256": hex.EncodeToString(sum[:]),
	}
}

var (
	rePDFStream = regexp.MustCompile(`stream\r?\n`)
	rePDFString = regexp.MustCompile(`\(((?:\\.|[^\\()])*)\)`)
)

// pdfText concatenates the string literals of every PDF page content stream.
func pdfText(raw []byte) string {
	expanded := raw
	if bytes.Contains(raw, []byte("FlateDecode")) {
		expanded = expandPDFStreams(raw)
	}

	var texts []string
	rest := expanded
	for {
		loc := rePDFStream.FindIndex(rest)
		if loc == nil {
			break
		}
		bodyStart := loc[1]
		end := bytes.Index(rest[bodyStart:], []byte("endstream"))
		if end < 0 {
			break
		}
		body := rest[bodyStart : bodyStart+end]
		rest = rest[bodyStart+end+len("endstream"):]

		// Page content streams carry text operators; font programs and other
		// binary objects do not and may hold arbitrary bytes.
		if !bytes.Contains(body, []byte("BT")) || !bytes.Contains(body, []byte("ET")) {
			continue
		}
		for _, m := range rePDFString.FindAllSubmatch(body, -1) {
			texts = append(texts, pdfUnescape(m[1]))
		}
	}
	return strings.Join(texts, "\n")
}

// pdfUnescape resolves the PDF string escapes gofpdf emits.
func pdfUnescape(b []byte) string {
	s := strings.ReplaceAll(string(b), "\\(", "(")
	s = strings.ReplaceAll(s, "\\)", ")")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// expandPDFStreams replaces every `stream ... endstream` section with its
// zlib-decompressed content (or the original bytes when decompression
// fails), leaving the surrounding PDF structure in place.
func expandPDFStreams(raw []byte) []byte {
	var out []byte
	rest := raw
	for {
		loc := rePDFStream.FindIndex(rest)
		if loc == nil {
			out = append(out, rest...)
			return out
		}
		head := rest[:loc[1]]
		bodyStart := loc[1]
		end := bytes.Index(rest[bodyStart:], []byte("endstream"))
		if end < 0 {
			out = append(out, rest...)
			return out
		}
		body := rest[bodyStart : bodyStart+end]
		// Trailing EOL before endstream belongs to the PDF syntax, not the
		// compressed data; trim exactly one.
		body = bytes.TrimSuffix(bytes.TrimSuffix(body, []byte("\n")), []byte("\r"))
		if dec, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			if plain, err := io.ReadAll(dec); err == nil {
				_ = dec.Close()
				body = plain
			}
		}
		out = append(out, head...)
		out = append(out, body...)
		out = append(out, []byte("\nendstream")...)
		rest = rest[bodyStart+end+len("endstream"):]
	}
}

// ---------------------------------------------------------------------------
// Golden files
// ---------------------------------------------------------------------------

const goldenDir = "testdata/goldens"

func goldenPath(group string) string {
	return filepath.Join(goldenDir, group+".json")
}

func canonical(t *testing.T, steps []capturedStep) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(steps); err != nil {
		t.Fatalf("marshal transcript: %v", err)
	}
	return buf.Bytes()
}

// compareGoldens writes or checks the golden for one group.
func compareGoldens(t *testing.T, group string, steps []capturedStep) {
	t.Helper()
	path := goldenPath(group)
	actual := canonical(t, steps)

	if *updateGoldens {
		// Re-recording is a deliberate, reviewed act (it needs an entry in
		// docs/refactor/CONTRACT-CHANGES.md); CI only ever compares, so the
		// flag is refused outright there rather than trusted to stay unset.
		if os.Getenv("CI") != "" {
			t.Fatalf("-update refuses to run under CI: re-recording goldens is a local, reviewed act")
		}
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("mkdir goldens: %v", err)
		}
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Fatalf("golden %s missing; record it with: go test ./internal/characterization -update", path)
	}
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if bytes.Equal(want, actual) {
		return
	}
	t.Errorf("golden %s drifted from recorded behaviour:\n%s", path, diffSnippet(string(want), string(actual)))
}

// TestGoldenFilesMatchCurrentGroups pins the golden directory to the script:
// every group must have its file, and every file must belong to a live group.
// Without this, deleting a scenario (or renaming a group) leaves a stale
// golden behind that still passes `go test -run nothing` and quietly rots.
// The check is pure bookkeeping and runs without DATABASE_URL.
func TestGoldenFilesMatchCurrentGroups(t *testing.T) {
	inScript := map[string]bool{}
	for _, g := range allGroups() {
		inScript[g.name] = true
		if _, err := os.Stat(goldenPath(g.name)); err != nil {
			t.Errorf("group %q has no golden file; record it with -update", g.name)
		}
	}

	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatalf("read golden dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		if !inScript[name] {
			t.Errorf("golden file %s matches no group in the script; delete it", e.Name())
		}
	}
}

// diffSnippet renders a compact line diff between two transcripts.
func diffSnippet(want, actual string) string {
	wl := strings.Split(want, "\n")
	al := strings.Split(actual, "\n")
	type op struct {
		kind byte
		line string
	}
	// Simple LCS-free diff via a map of line counts (good enough for golden
	// drift, which is small and localized).
	wcount := map[string]int{}
	for _, l := range wl {
		wcount[l]++
	}
	var out []op
	for _, l := range al {
		if wcount[l] > 0 {
			wcount[l]--
			continue
		}
		out = append(out, op{'-', l})
	}
	acount := map[string]int{}
	for _, l := range al {
		acount[l]++
	}
	var missing []string
	for _, l := range wl {
		if acount[l] > 0 {
			acount[l]--
			continue
		}
		missing = append(missing, l)
	}

	var b strings.Builder
	b.WriteString("--- golden\n+++ actual\n")
	limit := 40
	shown := 0
	for _, l := range missing {
		if shown >= limit {
			fmt.Fprintf(&b, "... (%d more)\n", len(missing)-shown)
			break
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		b.WriteString("-" + l + "\n")
		shown++
	}
	shown = 0
	for _, o := range out {
		if shown >= limit {
			break
		}
		if strings.TrimSpace(o.line) == "" {
			continue
		}
		b.WriteString("+" + o.line + "\n")
		shown++
	}
	return b.String()
}
