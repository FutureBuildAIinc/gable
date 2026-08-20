// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package vision

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The blueprint scanner compares extracted specs against a configurator
// selection and flags mismatches. A false negative here ships the wrong lumber
// to a jobsite; a false positive trains users to ignore the warning.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// --- extraction ----------------------------------------------------------

// CORRECTNESS: cross-section extraction. "2x4" is the single most common spec
// on an LBM blueprint and must be found regardless of spacing or case.
func TestScanBlueprint_CrossSection(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string // empty means "must not be extracted"
	}{
		{"plain", "2x4 studs", "2x4"},
		{"uppercase", "2X6 JOISTS", "2x6"},
		{"spaced", "2 x 8 beams", "2x8"},
		{"wide dimension", "6x6 posts", "6x6"},
		{"engineered sizes", "2x10 rim board", "2x10"},
		{"first match wins when several appear", "2x4 walls with 2x10 headers", "2x4"},
		{"no dimension present", "spruce lumber, kiln dried", ""},
		{"a bare number is not a dimension", "quantity 24 pieces", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text})
			got, ok := resp.ExtractedDimensions["cross_section"]
			if tc.want == "" {
				if ok {
					t.Fatalf("extracted cross_section %q from %q, want none", got, tc.text)
				}
				return
			}
			if !ok {
				t.Fatalf("no cross_section extracted from %q, want %q", tc.text, tc.want)
			}
			if got != tc.want {
				t.Errorf("cross_section = %q, want %q", got, tc.want)
			}
		})
	}
}

// CORRECTNESS: length extraction. A 10' stud and a 92-5/8" stud are different
// products; the parser must recognise the feet notations a drawing uses.
func TestScanBlueprint_Length(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantLength    string
		wantStudLen   string
		lengthAbsent  bool
		studLenAbsent bool
	}{
		// The tick-mark form populates both length (via lengthRegex) and
		// stud_length (via studRegex, which needs the "stud" keyword).
		{name: "tick mark", text: "10' studs", wantLength: "10'", wantStudLen: "10'"},
		{name: "tick mark without the stud keyword", text: "12' joists", wantLength: "12'", studLenAbsent: true},
		{name: "the word foot", text: "8 foot walls", wantLength: "8'", studLenAbsent: true},
		{name: "the word feet", text: "12 feet of blocking", wantLength: "12'", studLenAbsent: true},
		{name: "ft abbreviation", text: "16ft beams", wantLength: "16'", studLenAbsent: true},
		{name: "stud keyword with ft", text: "9 ft stud wall", wantLength: "9'", wantStudLen: "9'"},
		{name: "no length", text: "2x4 spf", lengthAbsent: true, studLenAbsent: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text})

			got, ok := resp.ExtractedDimensions["length"]
			if tc.lengthAbsent {
				if ok {
					t.Errorf("extracted length %q from %q, want none", got, tc.text)
				}
			} else if got != tc.wantLength {
				t.Errorf("length = %q (present=%v), want %q", got, ok, tc.wantLength)
			}

			studLen, ok := resp.ExtractedDimensions["stud_length"]
			if tc.studLenAbsent {
				if ok {
					t.Errorf("extracted stud_length %q from %q, want none", studLen, tc.text)
				}
			} else if studLen != tc.wantStudLen {
				t.Errorf("stud_length = %q (present=%v), want %q", studLen, ok, tc.wantStudLen)
			}
		})
	}
}

// CORRECTNESS: the tick-mark form must populate "length". A word boundary only
// exists between a word character and a non-word one, so an alternation ending
// in `'` cannot be followed by \b:
//
//	(\d+)\s*(?:'|foot|feet|ft)\b
//
// never matches "10' studs" — the way feet are written on essentially every
// construction drawing. The \b must apply only to the alphabetic alternatives.
func TestScanBlueprint_TickMarkLengthIsMissed(t *testing.T) {
	for _, text := range []string{"10' studs", "12' joists", "16' beams"} {
		resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: text})
		if _, ok := resp.ExtractedDimensions["length"]; !ok {
			t.Errorf("no length extracted from %q", text)
		}
	}
}

// CORRECTNESS: every feet notation a drawing uses must reach "length", and
// nothing else may. This is the exhaustive form of the alternation above.
func TestScanBlueprint_LengthNotations(t *testing.T) {
	tests := []struct {
		text string
		want string // "" means no length may be extracted
	}{
		{"10' studs", "10'"},
		{"10 ft beams", "10'"},
		{"10ft beams", "10'"},
		{"10 feet of blocking", "10'"},
		{"10 foot walls", "10'"},
		{"10'", "10'"},
		{"joists are 10 FT", "10'"},
		{"2x4 spf", ""},
		{"quantity 24 pieces", ""},
		{`16" oc`, ""},
		{"10 fter", ""},     // \b still guards the alphabetic spellings
		{"10 footings", ""}, // ditto
		{"16 feetings", ""}, // ditto
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text})
			got, ok := resp.ExtractedDimensions["length"]
			if tc.want == "" {
				if ok {
					t.Errorf("extracted length %q from %q, want none", got, tc.text)
				}
				return
			}
			if got != tc.want {
				t.Errorf("length = %q (present=%v), want %q", got, ok, tc.want)
			}
		})
	}
}

// CORRECTNESS: the same trailing-\b problem in the treatment abbreviation
// check:
//
//	\bpt\b|\bp\.t\.\b
//
// "p.t." ends in a period, so the trailing \b can never match. The abbreviation
// the pattern was written for is then never detected, and a drawing marked
// "2x6 p.t. decking" produces no treatment finding and no mismatch warning
// against an untreated configurator selection.
func TestScanBlueprint_DottedPTAbbreviationIsMissed(t *testing.T) {
	for _, text := range []string{"2x6 p.t. decking", "use p.t. for sill plates"} {
		resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: text})
		if _, ok := resp.ExtractedDimensions["treatment"]; !ok {
			t.Errorf("no treatment detected in %q", text)
		}
	}
}

// CORRECTNESS: detectMismatches must fall back to the general "length"
// dimension. "stud_length" requires the literal word "stud" immediately after
// the measurement, so comparing it alone leaves the length the scanner does
// extract from "8 foot" / "12ft" / "16 feet" compared against nothing: a
// drawing reading "2x6 joists, 8 feet" against a configurator selection of
// 2x6-10 produces no length warning.
func TestDetectMismatches_ExtractedLengthIsNeverCompared(t *testing.T) {
	resp := NewService().ScanBlueprint(BlueprintScanRequest{
		BlueprintText:    "2x6 joists, 8 feet long",
		ConfigSelections: map[string]string{"Dimensions": "2x6-10"},
	})
	if resp.ExtractedDimensions["length"] != "8'" {
		t.Fatalf("length = %q, want 8'", resp.ExtractedDimensions["length"])
	}
	if findMismatch(resp, "Length") == nil {
		t.Fatal("an 8-foot blueprint against a 10-foot selection produced no Length mismatch")
	}
}

// CORRECTNESS: stud spacing drives the quantity take-off. 16" OC and 24" OC
// differ by 50% of the studs on a wall.
func TestScanBlueprint_Spacing(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"inch mark and OC", `16" OC`, `16" OC`},
		{"lower case oc", `24" oc`, `24" OC`},
		{"dotted o.c.", `16" o.c.`, `16" OC`},
		{"on center spelled out", `24" on center`, `24" OC`},
		{"in abbreviation", `16in oc`, `16" OC`},
		{"inch spelled out", `12inch on center`, `12" OC`},
		{"no spacing", "2x4 studs", ""},
		{"a bare inch measurement is not a spacing", `16" wide`, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text})
			got, ok := resp.ExtractedDimensions["spacing"]
			if tc.want == "" {
				if ok {
					t.Fatalf("extracted spacing %q from %q, want none", got, tc.text)
				}
				return
			}
			if got != tc.want {
				t.Errorf("spacing = %q (present=%v), want %q", got, ok, tc.want)
			}
		})
	}
}

// CORRECTNESS: species matching is longest-first so that "southern yellow
// pine" is not shortened to "pine", and "doug fir" is not missed because
// "douglas fir" was checked first. The ordering is load-bearing.
func TestScanBlueprint_Species(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"SPF acronym", "2x4 spf studs", "SPF"},
		{"spruce-pine-fir spelled out", "spruce-pine-fir framing", "SPF"},
		{"douglas fir", "douglas fir beam", "Douglas Fir"},
		{"doug fir shorthand maps to the same species", "doug fir post", "Douglas Fir"},
		{"southern yellow pine", "southern yellow pine decking", "SYP"},
		{"SYP acronym", "syp treated", "SYP"},
		{"western red cedar", "western red cedar siding", "Cedar"},
		{"plain cedar", "cedar fence boards", "Cedar"},
		{"hem-fir", "hem-fir studs", "Hem-Fir"},
		{"hemfir without the hyphen", "hemfir studs", "Hem-Fir"},
		{"case insensitive", "SPRUCE-PINE-FIR", "SPF"},
		{"no species mentioned", "2x4 studs 16in oc", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text})
			got, ok := resp.ExtractedDimensions["species"]
			if tc.want == "" {
				if ok {
					t.Fatalf("extracted species %q from %q, want none", got, tc.text)
				}
				return
			}
			if got != tc.want {
				t.Errorf("species = %q (present=%v), want %q", got, ok, tc.want)
			}
		})
	}
}

// CORRECTNESS: "southern yellow pine" must not be captured by a shorter
// keyword. This is exactly what the ordered slice exists to guarantee, so it
// gets its own assertion.
func TestScanBlueprint_SpeciesLongestMatchWins(t *testing.T) {
	// "western red cedar" contains "cedar"; the longer keyword must win. Both
	// map to Cedar here, so the sharper case is doug/douglas: the text
	// "douglas fir" also contains "doug fir"? It does not — but the ordered
	// list is what stops a future short keyword shadowing a long one, so this
	// asserts the ordering invariant directly.
	svc := NewService()

	if got := svc.ScanBlueprint(BlueprintScanRequest{BlueprintText: "western red cedar"}).ExtractedDimensions["species"]; got != "Cedar" {
		t.Errorf("species = %q, want Cedar", got)
	}
	// A text mentioning two species reports the first one in the ordered list,
	// not the first one in the text — pinned so the behaviour is deterministic.
	got := svc.ScanBlueprint(BlueprintScanRequest{BlueprintText: "spf studs with cedar trim"}).ExtractedDimensions["species"]
	if got != "Cedar" {
		t.Errorf("species = %q, want Cedar (the keyword list order decides, not the text order)", got)
	}
}

// CORRECTNESS: treatment detection must not fire on a word that merely
// contains the letters "pt". That false positive was the reason for the
// word-boundary regex and it must stay.
func TestScanBlueprint_Treatment(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"treated", "pressure treated 2x6", true},
		{"pressure treat", "pressure treat the sill plate", true},
		{"standalone pt", "2x6 pt decking", true},
		{"pt at the start", "pt lumber", true},
		// The dotted "p.t." form — see
		// TestScanBlueprint_DottedPTAbbreviationIsMissed.
		{"dotted p.t.", "2x6 p.t. decking", true},
		{"dotted p.t. at the end of the text", "sill plates: p.t.", true},
		{"apartment must not match", "apartment building framing", false},
		{"receipt must not match", "see receipt for details", false},
		{"optional must not match", "optional upgrade", false},
		{"no treatment mentioned", "2x4 spf studs", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text})
			_, got := resp.ExtractedDimensions["treatment"]
			if got != tc.want {
				t.Errorf("treatment detected = %v for %q, want %v", got, tc.text, tc.want)
			}
		})
	}
}

// --- mismatch detection --------------------------------------------------

// CORRECTNESS: a species mismatch is an ERROR — ordering SPF when the drawing
// calls for pressure-treated SYP is a structural substitution, not a
// preference.
func TestDetectMismatches_Species(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		config       map[string]string
		wantMismatch bool
		wantSeverity string
	}{
		{
			name:   "matching species produces no mismatch",
			text:   "2x4 spf studs",
			config: map[string]string{"Species": "SPF"},
		},
		{
			name:   "case-insensitive match",
			text:   "2x4 spf studs",
			config: map[string]string{"Species": "spf"},
		},
		{
			name:         "different species is an error",
			text:         "douglas fir beam",
			config:       map[string]string{"Species": "SPF"},
			wantMismatch: true,
			wantSeverity: "error",
		},
		{
			name:   "an empty configurator value is not a mismatch",
			text:   "douglas fir beam",
			config: map[string]string{"Species": ""},
		},
		{
			name:   "no species in the configurator is not a mismatch",
			text:   "douglas fir beam",
			config: map[string]string{"Dimensions": "2x6-10"},
		},
		{
			name:   "no species on the blueprint is not a mismatch",
			text:   "2x4 studs",
			config: map[string]string{"Species": "SPF"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text, ConfigSelections: tc.config})
			m := findMismatch(resp, "Species")
			if !tc.wantMismatch {
				if m != nil {
					t.Fatalf("unexpected mismatch: %+v", *m)
				}
				return
			}
			if m == nil {
				t.Fatal("expected a Species mismatch")
			}
			if m.Severity != tc.wantSeverity {
				t.Errorf("Severity = %q, want %q", m.Severity, tc.wantSeverity)
			}
			if m.BlueprintValue == "" || m.ConfigValue == "" {
				t.Errorf("mismatch %+v must carry both values so the user can see what differs", *m)
			}
			if !strings.Contains(m.Message, m.BlueprintValue) || !strings.Contains(m.Message, m.ConfigValue) {
				t.Errorf("message %q must name both values", m.Message)
			}
		})
	}
}

// CORRECTNESS: the configurator encodes dimensions as "2x6-10" (cross-section
// then length). The comparison must split them and check the cross-section
// against the blueprint's, as an error, and the length as a warning.
func TestDetectMismatches_DimensionsAndLength(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		config       string
		wantDim      bool
		wantLength   bool
		lengthSevere string
	}{
		{
			name:   "everything agrees",
			text:   "2x6 10' stud wall",
			config: "2x6-10",
		},
		{
			name:    "cross-section differs",
			text:    "2x4 10' stud wall",
			config:  "2x6-10",
			wantDim: true,
		},
		{
			name:         "length differs",
			text:         "2x6 8' stud wall",
			config:       "2x6-10",
			wantLength:   true,
			lengthSevere: "warning",
		},
		{
			name:         "both differ",
			text:         "2x4 8' stud wall",
			config:       "2x6-10",
			wantDim:      true,
			wantLength:   true,
			lengthSevere: "warning",
		},
		{
			name:   "a configurator value with no length part skips the length check",
			text:   "2x6 8' stud wall",
			config: "2x6",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{
				BlueprintText:    tc.text,
				ConfigSelections: map[string]string{"Dimensions": tc.config},
			})

			dim := findMismatch(resp, "Dimensions")
			if tc.wantDim != (dim != nil) {
				t.Errorf("Dimensions mismatch present = %v, want %v (mismatches: %+v)", dim != nil, tc.wantDim, resp.Mismatches)
			}
			if dim != nil && dim.Severity != "error" {
				t.Errorf("Dimensions severity = %q, want error", dim.Severity)
			}
			if dim != nil && strings.Contains(dim.ConfigValue, "-") {
				t.Errorf("ConfigValue = %q, want only the cross-section part", dim.ConfigValue)
			}

			length := findMismatch(resp, "Length")
			if tc.wantLength != (length != nil) {
				t.Errorf("Length mismatch present = %v, want %v (mismatches: %+v)", length != nil, tc.wantLength, resp.Mismatches)
			}
			if length != nil && length.Severity != tc.lengthSevere {
				t.Errorf("Length severity = %q, want %q", length.Severity, tc.lengthSevere)
			}
		})
	}
}

// CORRECTNESS: a blueprint calling for treated lumber against a configurator
// with no treatment selected is a warning — untreated lumber in ground contact
// is a real defect, but the drawing is the weaker signal so it is not an error.
func TestDetectMismatches_Treatment(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		config       map[string]string
		wantMismatch bool
	}{
		{
			name:         "treated blueprint, no treatment selected",
			text:         "pressure treated 2x6 decking",
			config:       map[string]string{"Treatment": "None"},
			wantMismatch: true,
		},
		{
			name:         "treated blueprint, empty treatment selected",
			text:         "pressure treated 2x6 decking",
			config:       map[string]string{"Treatment": ""},
			wantMismatch: true,
		},
		{
			name:   "treated blueprint, treatment selected",
			text:   "pressure treated 2x6 decking",
			config: map[string]string{"Treatment": "ACQ"},
		},
		{
			name:   "untreated blueprint, no treatment selected",
			text:   "2x6 spf",
			config: map[string]string{"Treatment": "None"},
		},
		{
			name:   "treated blueprint with no Treatment key at all",
			text:   "pressure treated 2x6",
			config: map[string]string{"Species": "SYP"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text, ConfigSelections: tc.config})
			m := findMismatch(resp, "Treatment")
			if tc.wantMismatch != (m != nil) {
				t.Fatalf("Treatment mismatch present = %v, want %v (mismatches: %+v)", m != nil, tc.wantMismatch, resp.Mismatches)
			}
			if m != nil && m.Severity != "warning" {
				t.Errorf("Severity = %q, want warning", m.Severity)
			}
		})
	}
}

// --- summary -------------------------------------------------------------

// CORRECTNESS: the summary is what a user reads. Its counts must match the
// mismatch list exactly — a summary claiming "no mismatches found" while the
// list has entries is worse than no summary.
func TestScanBlueprint_SummaryMatchesTheMismatchList(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		config   map[string]string
		wantErrs int
		wantWarn int
	}{
		{
			name:   "clean scan",
			text:   "2x6 spf 10' stud wall 16in oc",
			config: map[string]string{"Species": "SPF", "Dimensions": "2x6-10"},
		},
		{
			name:     "one error",
			text:     "2x6 douglas fir 10' stud wall",
			config:   map[string]string{"Species": "SPF", "Dimensions": "2x6-10"},
			wantErrs: 1,
		},
		{
			name:     "one error and one warning",
			text:     "2x6 douglas fir 8' stud wall",
			config:   map[string]string{"Species": "SPF", "Dimensions": "2x6-10"},
			wantErrs: 1,
			wantWarn: 1,
		},
		{
			name:     "two errors and two warnings",
			text:     "2x4 douglas fir pressure treated 8' stud wall",
			config:   map[string]string{"Species": "SPF", "Dimensions": "2x6-10", "Treatment": "None"},
			wantErrs: 2,
			wantWarn: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := NewService().ScanBlueprint(BlueprintScanRequest{BlueprintText: tc.text, ConfigSelections: tc.config})

			var errs, warns int
			for _, m := range resp.Mismatches {
				switch m.Severity {
				case "error":
					errs++
				case "warning":
					warns++
				default:
					t.Errorf("mismatch %+v has an unrecognised severity", m)
				}
			}
			if errs != tc.wantErrs || warns != tc.wantWarn {
				t.Fatalf("got %d errors and %d warnings, want %d and %d (mismatches: %+v)",
					errs, warns, tc.wantErrs, tc.wantWarn, resp.Mismatches)
			}

			if errs == 0 && warns == 0 {
				if !strings.Contains(resp.Summary, "No mismatches found") {
					t.Errorf("summary = %q, want it to report a clean scan", resp.Summary)
				}
				return
			}
			if strings.Contains(resp.Summary, "No mismatches found") {
				t.Fatalf("summary claims a clean scan but %d mismatches were reported: %q", len(resp.Mismatches), resp.Summary)
			}
			if !strings.Contains(resp.Summary, itoa(errs)+" error(s)") {
				t.Errorf("summary %q does not report %d errors", resp.Summary, errs)
			}
			if !strings.Contains(resp.Summary, itoa(warns)+" warning(s)") {
				t.Errorf("summary %q does not report %d warnings", resp.Summary, warns)
			}
		})
	}
}

// CORRECTNESS: an empty scan must return usable, non-nil collections so a
// client can iterate without a null check, and must not claim to have found
// anything.
func TestScanBlueprint_EmptyInput(t *testing.T) {
	resp := NewService().ScanBlueprint(BlueprintScanRequest{})

	if resp.ExtractedDimensions == nil {
		t.Error("ExtractedDimensions is nil; it must be an empty map")
	}
	if resp.Mismatches == nil {
		t.Error("Mismatches is nil; it must be an empty slice")
	}
	if len(resp.ExtractedDimensions) != 0 || len(resp.Mismatches) != 0 {
		t.Errorf("empty input produced %+v", resp)
	}
	if !strings.Contains(resp.Summary, "0 dimensions extracted") {
		t.Errorf("summary = %q, want it to report nothing extracted", resp.Summary)
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"mismatches":null`) {
		t.Errorf("mismatches serialised as null: %s", b)
	}
}

// CORRECTNESS: the scan is a pure function of its input — the same blueprint
// twice must give the same answer, including the mismatch ordering, or a user
// re-running a scan sees the warnings shuffle.
func TestScanBlueprint_IsDeterministic(t *testing.T) {
	req := BlueprintScanRequest{
		BlueprintText: "2x4 douglas fir pressure treated studs 8' long 24in on center",
		ConfigSelections: map[string]string{
			"Species": "SPF", "Dimensions": "2x6-10", "Treatment": "None",
		},
	}
	svc := NewService()

	first, err := json.Marshal(svc.ScanBlueprint(req))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		next, err := json.Marshal(svc.ScanBlueprint(req))
		if err != nil {
			t.Fatal(err)
		}
		if string(next) != string(first) {
			t.Fatalf("run %d differed:\n first: %s\n  next: %s", i, first, next)
		}
	}
}

// CORRECTNESS: a nil ConfigSelections map means "nothing to compare against"
// and must not panic or produce mismatches.
func TestScanBlueprint_NilSelections(t *testing.T) {
	resp := NewService().ScanBlueprint(BlueprintScanRequest{
		BlueprintText:    "2x6 douglas fir 10' 16in oc pressure treated",
		ConfigSelections: nil,
	})
	if len(resp.Mismatches) != 0 {
		t.Errorf("got %d mismatches with no configurator selections", len(resp.Mismatches))
	}
	if len(resp.ExtractedDimensions) == 0 {
		t.Error("extraction should still have run")
	}
}

// --- HTTP layer ----------------------------------------------------------

// CORRECTNESS: the scan endpoint requires blueprint text and rejects a
// malformed body, without either case reaching the extraction code.
func TestHandleScan_Validation(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(NewService()).RegisterRoutes(mux)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"valid", `{"blueprint_text":"2x4 spf 16in oc"}`, http.StatusOK},
		{"missing text", `{}`, http.StatusBadRequest},
		{"empty text", `{"blueprint_text":""}`, http.StatusBadRequest},
		{"malformed JSON", `{not json`, http.StatusBadRequest},
		{"wrong type for the text field", `{"blueprint_text":123}`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/vision/scan", strings.NewReader(tc.body)))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusOK {
				var resp BlueprintScanResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if resp.ExtractedDimensions["cross_section"] != "2x4" {
					t.Errorf("the handler did not run the scan: %+v", resp)
				}
			}
		})
	}
}

// CORRECTNESS: the body is capped at 1MB. A larger payload must be rejected
// rather than buffered — this endpoint takes free text from an authenticated
// but untrusted client.
func TestHandleScan_BodySizeLimit(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(NewService()).RegisterRoutes(mux)

	huge := `{"blueprint_text":"` + strings.Repeat("2x4 spf ", 200000) + `"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/vision/scan", strings.NewReader(huge)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a body over the 1MB cap", rec.Code)
	}
}

// CORRECTNESS: the role guard supplied at registration must wrap the endpoint.
func TestRegisterRoutes_RoleGuardApplies(t *testing.T) {
	guard := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	}
	mux := http.NewServeMux()
	NewHandler(NewService()).RegisterRoutes(mux, guard)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/vision/scan",
		strings.NewReader(`{"blueprint_text":"2x4"}`)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// --- helpers -------------------------------------------------------------

func findMismatch(resp *BlueprintScanResponse, field string) *Mismatch {
	for i := range resp.Mismatches {
		if resp.Mismatches[i].Field == field {
			return &resp.Mismatches[i]
		}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
