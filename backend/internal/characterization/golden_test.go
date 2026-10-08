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
)

// normaliseTranscript rewrites every value that legitimately varies between
// two runs of the same script on the same code onto stable placeholders, in
// order of first appearance, and leaves everything else untouched.
//
//   - UUIDs (primary keys, request ids, dev-mode POS cashier ids) become
//     <id-1>, <id-2>, ... in order of first appearance anywhere in the
//     transcript, so an id read back in a later step keeps its placeholder.
//   - RFC 3339 timestamps and bare calendar dates become <ts> and <date>.
//     Seeded rows are dated relative to the seed clock, so absolute times are
//     per-run; their day granularity is normalised the same way.
//   - Generated secrets and tokens (sk_live_ keys, JWTs) become placeholders;
//     their values are random by construction.
//   - Elapsed-time measurements (parse_time_ms) become <ms>; a duration is
//     not behaviour.
//
// Everything else - money, statuses, field names, error text, null versus
// empty array - is real behaviour and is compared byte for byte.
func normaliseTranscript(steps []capturedStep) []capturedStep {
	n := &normaliser{ids: map[string]string{}}
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
	ids   map[string]string
	count int
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

// timingFields are response fields that measure elapsed time. Their values
// vary with machine speed, not behaviour.
var timingFields = map[string]bool{
	"parse_time_ms": true,
}

func (n *normaliser) value(v any, key string) any {
	switch x := v.(type) {
	case string:
		return n.string(x)
	case json.Number:
		if timingFields[key] {
			return "<ms>"
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
	s = reTS.ReplaceAllStringFunc(s, func(string) string { return "<ts>" })
	s = rePgTS.ReplaceAllStringFunc(s, func(string) string { return "<ts>" })
	s = reDate.ReplaceAllStringFunc(s, func(string) string { return "<date>" })
	return s
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
	n := &normaliser{ids: map[string]string{}}
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
