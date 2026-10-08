// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"bytes"
	"encoding/csv"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// --- formatValue ---------------------------------------------------------

// CORRECTNESS: exported values are what a finance user opens in Excel. A nil
// must be blank rather than the literal "<nil>", and a byte slice (how pgx
// hands back some text and numeric columns) must render as its text.
func TestFormatValue(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil is blank", nil, ""},
		{"string", "PAID", "PAID"},
		{"empty string", "", ""},
		{"bytes render as text", []byte("INV-1001"), "INV-1001"},
		{"int", 42, "42"},
		{"int64", int64(-7), "-7"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"struct falls back to %v", struct{ A int }{3}, "{3}"},
		{"slice falls back to %v", []string{"a", "b"}, "[a b]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatValue(tc.in); got != tc.want {
				t.Errorf("formatValue(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// CHARACTERIZATION: floats are rendered with the default %f verb, which is
// always six decimal places. A money column exports as 1234.560000 and a
// quantity as 2.500000. Excel will still parse these as numbers, but the CSV
// is not what a human would write, and six places is both too many for money
// and potentially too few for a unit price carried to more precision.
func TestFormatValue_FloatsGetSixDecimalPlaces(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{float64(1234.56), "1234.560000"},
		{float64(0), "0.000000"},
		{float32(2.5), "2.500000"},
		{float64(0.1234567), "0.123457"}, // rounded away at the 7th place
	}
	for _, tc := range tests {
		if got := formatValue(tc.in); got != tc.want {
			t.Errorf("formatValue(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// CORRECTNESS: BuildAndExecuteQuery reads rows with pgx.Rows.Values(), so the
// values reaching formatValue are the driver's own Go types, and three of the
// ones entitySchemas exposes are unreadable under %v. A money column arrived as
// "{200000 -2 false finite true}", an id column as "[167 93 18 229 ...]" and a
// date column as Go's "2026-08-20 16:16:40.520731 -0700 PDT" — in every CSV and
// XLSX this package has ever produced.
//
// The exact driver representations are pinned against a live database in
// TestExport_RendersRealDriverValues (scheduler_postgres_test.go); this test
// pins the rendering itself, which needs no database.
func TestFormatValue_RendersPgxDriverTypes(t *testing.T) {
	// numeric(10,2) 2000.00 as pgx decodes it: unscaled 200000 with exponent -2.
	money := pgtype.Numeric{Int: big.NewInt(200000), Exp: -2, Valid: true}
	id := uuid.MustParse("a75d12e5-28c1-4375-8e62-2895d2c89d96")
	ts := time.Date(2026, 8, 20, 16, 16, 40, 0, time.UTC)

	tests := []struct {
		name string
		in   any
		want string
	}{
		{"numeric keeps its stored scale", money, "2000.00"},
		{"numeric pointer", &money, "2000.00"},
		{"a NULL numeric is blank", pgtype.Numeric{}, ""},
		{"negative numeric", pgtype.Numeric{Int: big.NewInt(-12345), Exp: -2, Valid: true}, "-123.45"},
		{"integral numeric", pgtype.Numeric{Int: big.NewInt(7), Exp: 0, Valid: true}, "7"},
		{"uuid renders canonically", [16]byte(id), "a75d12e5-28c1-4375-8e62-2895d2c89d96"},
		{"timestamp renders as RFC 3339", ts, "2026-08-20T16:16:40Z"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatValue(tc.in); got != tc.want {
				t.Errorf("formatValue(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A spreadsheet cell must hold a number, not the text of one, or the recipient
// cannot sum the column. excelValue therefore converts where formatValue
// renders.
func TestExcelValue_KeepsNumbersNumeric(t *testing.T) {
	money := pgtype.Numeric{Int: big.NewInt(200000), Exp: -2, Valid: true}
	if got := excelValue(money); got != 2000.00 {
		t.Errorf("excelValue(numeric) = %#v, want the float64 2000.00", got)
	}

	id := uuid.MustParse("a75d12e5-28c1-4375-8e62-2895d2c89d96")
	if got := excelValue([16]byte(id)); got != id.String() {
		t.Errorf("excelValue(uuid) = %#v, want %q", got, id.String())
	}

	// Everything excelize already understands is passed through untouched, so
	// a time stays a date cell rather than becoming a string.
	ts := time.Date(2026, 8, 20, 16, 16, 40, 0, time.UTC)
	if got := excelValue(ts); got != any(ts) {
		t.Errorf("excelValue(time.Time) = %#v, want it passed through", got)
	}
	if got := excelValue("PAID"); got != any("PAID") {
		t.Errorf("excelValue(string) = %#v, want it passed through", got)
	}
}

// --- ExportCSV -----------------------------------------------------------

// CORRECTNESS: the header uses the column label when there is one and falls
// back to the field name, and every data row must line up with that header in
// both order and arity — a shifted column silently misattributes money.
func TestExportCSV_HeaderAndRowAlignment(t *testing.T) {
	cols := []ReportColumn{
		{Field: "invoice_number", Label: "Invoice #"},
		{Field: "customer_name"}, // no label: falls back to the field name
		{Field: "total_amount", Label: "Total"},
	}
	rows := []map[string]any{
		{"invoice_number": "INV-1", "customer_name": "Acme", "total_amount": 100.0},
		{"invoice_number": "INV-2", "customer_name": "Beta", "total_amount": 250.5},
	}

	var buf bytes.Buffer
	if err := ExportCSV(&buf, cols, rows); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}

	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("the produced CSV does not parse: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 1 header + 2 rows", len(records))
	}
	wantHeader := []string{"Invoice #", "customer_name", "Total"}
	for i, h := range wantHeader {
		if records[0][i] != h {
			t.Errorf("header[%d] = %q, want %q", i, records[0][i], h)
		}
	}
	if records[1][0] != "INV-1" || records[1][1] != "Acme" || records[1][2] != "100.000000" {
		t.Errorf("row 1 = %v", records[1])
	}
	if records[2][0] != "INV-2" {
		t.Errorf("row 2 = %v", records[2])
	}
}

// CORRECTNESS: a row missing a column is a hole in the data, not a reason to
// shift every later value one cell to the left.
func TestExportCSV_MissingKeysBecomeEmptyCellsNotShifts(t *testing.T) {
	cols := []ReportColumn{{Field: "a"}, {Field: "b"}, {Field: "c"}}
	rows := []map[string]any{
		{"a": "1", "c": "3"}, // b absent
		{},                   // everything absent
	}

	var buf bytes.Buffer
	if err := ExportCSV(&buf, cols, rows); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := records[1]; got[0] != "1" || got[1] != "" || got[2] != "3" {
		t.Errorf("row = %v, want [1  3] with an empty middle cell", got)
	}
	if got := records[2]; len(got) != 3 || got[0] != "" || got[1] != "" || got[2] != "" {
		t.Errorf("empty row = %v, want three empty cells", got)
	}
}

// CORRECTNESS: values containing the delimiter, a quote, or a newline must be
// quoted so the file still parses as the same table.
func TestExportCSV_QuotesValuesThatNeedIt(t *testing.T) {
	cols := []ReportColumn{{Field: "name"}, {Field: "note"}}
	rows := []map[string]any{
		{"name": "Acme, Inc.", "note": "said \"hello\"\nthen left"},
	}

	var buf bytes.Buffer
	if err := ExportCSV(&buf, cols, rows); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("the produced CSV does not round-trip: %v", err)
	}
	if records[1][0] != "Acme, Inc." {
		t.Errorf("got %q, want the comma preserved inside one field", records[1][0])
	}
	if records[1][1] != "said \"hello\"\nthen left" {
		t.Errorf("got %q, want quotes and newline preserved", records[1][1])
	}
}

// CORRECTNESS: no rows still produces a header, so the recipient sees an empty
// report rather than an empty file they mistake for a failed job.
func TestExportCSV_EmptyResultStillWritesHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportCSV(&buf, []ReportColumn{{Field: "id", Label: "ID"}}, nil); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "ID" {
		t.Errorf("body = %q, want just the header row", got)
	}
}

// CHARACTERIZATION + security note: a value beginning with =, +, - or @ is
// written verbatim, so Excel and LibreOffice treat it as a formula when the
// exported file is opened. Report data comes from customer- and vendor-supplied
// fields (names, notes, references), which makes this a CSV-injection vector.
// Mitigation would be prefixing such values with a single quote or a tab; the
// export does neither today.
func TestExportCSV_DoesNotNeutraliseFormulaInjection(t *testing.T) {
	cols := []ReportColumn{{Field: "customer_name"}}
	rows := []map[string]any{
		{"customer_name": `=cmd|'/c calc'!A1`},
		{"customer_name": `+1234`},
		{"customer_name": `@SUM(A1:A9)`},
	}

	var buf bytes.Buffer
	if err := ExportCSV(&buf, cols, rows); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	out := buf.String()
	for _, payload := range []string{"=cmd|'/c calc'!A1", "+1234", "@SUM(A1:A9)"} {
		if !strings.Contains(out, payload) {
			t.Fatalf("expected the payload %q to be present verbatim; if it is now escaped, this characterization test should become a correctness test", payload)
		}
	}
	if strings.Contains(out, "'=cmd") || strings.Contains(out, "\t=cmd") {
		t.Error("a formula guard appears to have been added — update this test to assert it properly")
	}
}

// CORRECTNESS: ExportCSV must not swallow a write failure on the destination.
// csv.Writer buffers, so a small report only fails at Flush time; if Flush is
// deferred and writer.Error() never checked, the function returns nil while
// producing no output at all. The scheduled-report path uses this to build an
// email attachment, and an HTTP handler uses it to stream a download — in both
// cases a silent empty success is worse than an error.
func TestExportCSV_MustReportAFailingWriter(t *testing.T) {
	cols := []ReportColumn{{Field: "id", Label: "ID"}}
	rows := []map[string]any{{"id": "1"}}

	err := ExportCSV(failingWriter{}, cols, rows)
	if err == nil {
		t.Fatal("ExportCSV returned nil for a writer that fails every write")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("destination closed") }

// --- ExportXLSX ----------------------------------------------------------

// CORRECTNESS: the XLSX export must produce a real workbook (a ZIP container),
// not an empty or truncated file.
func TestExportXLSX_ProducesAWorkbook(t *testing.T) {
	cols := []ReportColumn{{Field: "invoice_number", Label: "Invoice #"}, {Field: "total_amount", Label: "Total"}}
	rows := []map[string]any{
		{"invoice_number": "INV-1", "total_amount": 100.0},
		{"invoice_number": "INV-2", "total_amount": 250.5},
	}

	var buf bytes.Buffer
	if err := ExportXLSX(&buf, cols, rows); err != nil {
		t.Fatalf("ExportXLSX: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("ExportXLSX wrote nothing")
	}
	// Every .xlsx is a ZIP archive, which starts with the "PK\x03\x04" magic.
	if got := buf.Bytes()[:4]; string(got) != "PK\x03\x04" {
		t.Fatalf("output starts with %q, want the ZIP magic PK\\x03\\x04", got)
	}
	// The sheet the code names must be in the workbook manifest.
	if !bytes.Contains(buf.Bytes(), []byte("xl/worksheets")) {
		t.Error("the archive has no worksheets part")
	}
}

// CORRECTNESS: an empty result set is still a valid workbook with a header row,
// matching the CSV behaviour, so a scheduled empty report is not mistaken for a
// failed one.
func TestExportXLSX_EmptyResultIsStillAValidWorkbook(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportXLSX(&buf, []ReportColumn{{Field: "id"}}, nil); err != nil {
		t.Fatalf("ExportXLSX: %v", err)
	}
	if buf.Len() == 0 || string(buf.Bytes()[:4]) != "PK\x03\x04" {
		t.Fatal("empty result did not produce a valid workbook")
	}
}

// CORRECTNESS: no columns at all is a degenerate but reachable definition
// (the builder rejects it, but the exporters are also called directly); it must
// not panic.
func TestExporters_NoColumns(t *testing.T) {
	var csvBuf, xlsxBuf bytes.Buffer
	if err := ExportCSV(&csvBuf, nil, []map[string]any{{"a": 1}}); err != nil {
		t.Fatalf("ExportCSV with no columns: %v", err)
	}
	if err := ExportXLSX(&xlsxBuf, nil, []map[string]any{{"a": 1}}); err != nil {
		t.Fatalf("ExportXLSX with no columns: %v", err)
	}
}
