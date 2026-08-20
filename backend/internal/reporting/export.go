// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/xuri/excelize/v2"
)

// ExportCSV streams the report definition results directly to an io.Writer.
//
// csv.Writer buffers, so most write failures on the destination only surface
// at Flush time. The deferred flush therefore records writer.Error() into the
// named return: without it a failing destination (a closed HTTP connection, a
// full disk) yields a nil error and an empty export, which the scheduled-report
// path would happily mail out as an attachment.
func ExportCSV(w io.Writer, columns []ReportColumn, results []map[string]interface{}) (err error) {
	writer := csv.NewWriter(w)
	defer func() {
		writer.Flush()
		if flushErr := writer.Error(); flushErr != nil && err == nil {
			err = fmt.Errorf("failed to flush CSV: %w", flushErr)
		}
	}()

	// Write Headers
	headers := make([]string, len(columns))
	for i, col := range columns {
		if col.Label != "" {
			headers[i] = col.Label
		} else {
			headers[i] = col.Field
		}
	}
	if err := writer.Write(headers); err != nil {
		return fmt.Errorf("failed to write CSV headers: %w", err)
	}

	// Write Data Rows
	for _, row := range results {
		record := make([]string, len(columns))
		for i, col := range columns {
			val := row[col.Field]
			record[i] = formatValue(val)
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("failed to write CSV row: %w", err)
		}
	}

	return nil
}

// ExportXLSX writes the report definition results to an io.Writer as an Excel file.
func ExportXLSX(w io.Writer, columns []ReportColumn, results []map[string]interface{}) error {
	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Println("failed to close excel file:", err)
		}
	}()

	sheetName := "Report"
	f.SetSheetName("Sheet1", sheetName)

	// Write Headers
	for i, col := range columns {
		cell, err := excelize.CoordinatesToCellName(i+1, 1)
		if err != nil {
			return err
		}
		label := col.Label
		if label == "" {
			label = col.Field
		}
		f.SetCellValue(sheetName, cell, label)
	}

	// Make headers bold
	style, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err == nil {
		f.SetRowStyle(sheetName, 1, 1, style)
	}

	// Write Data Rows
	for rowIdx, row := range results {
		currentExcelRow := rowIdx + 2
		for colIdx, col := range columns {
			cell, err := excelize.CoordinatesToCellName(colIdx+1, currentExcelRow)
			if err != nil {
				return err
			}
			f.SetCellValue(sheetName, cell, excelValue(row[col.Field]))
		}
	}

	// Output
	if err := f.Write(w); err != nil {
		return fmt.Errorf("failed to write XLSX: %w", err)
	}

	return nil
}

// formatValue renders one cell of a report.
//
// The pgx cases are not decoration. BuildAndExecuteQuery reads rows with
// pgx.Rows.Values(), which hands back the driver's own Go representation, and
// three of those are unreadable under %v — which is what every money, id and
// date column in entitySchemas decodes to:
//
//	numeric     -> pgtype.Numeric -> "{200000 -2 false finite true}"
//	uuid        -> [16]byte       -> "[167 93 18 229 ...]"
//	timestamptz -> time.Time      -> "2026-08-20 16:16:40.520731 -0700 PDT"
//
// A CSV full of those is not a report. They are rendered here as a decimal, a
// canonical UUID and an RFC 3339 timestamp instead.
func formatValue(val interface{}) string {
	if val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%f", v)
	case bool:
		return strconv.FormatBool(v)
	case pgtype.Numeric:
		return formatNumeric(v)
	case *pgtype.Numeric:
		if v == nil {
			return ""
		}
		return formatNumeric(*v)
	case [16]byte:
		return uuid.UUID(v).String()
	case time.Time:
		return v.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// excelValue converts a pgx driver value into something excelize can write.
//
// It differs from formatValue on purpose: a spreadsheet cell should hold a
// number, not the text of a number, or the recipient cannot sum a column.
// excelize already understands the Go primitives and time.Time; the two pgx
// representations it does not understand are converted here, and everything
// else is passed through untouched.
func excelValue(val interface{}) interface{} {
	switch v := val.(type) {
	case pgtype.Numeric:
		if f, err := v.Float64Value(); err == nil && f.Valid {
			return f.Float64
		}
		return formatNumeric(v)
	case *pgtype.Numeric:
		if v == nil {
			return nil
		}
		return excelValue(*v)
	case [16]byte:
		return uuid.UUID(v).String()
	default:
		return val
	}
}

// formatNumeric renders a Postgres numeric at its stored scale, so a money
// column exports as 2000.00 rather than as either a struct dump or a float that
// has been through binary rounding.
func formatNumeric(n pgtype.Numeric) string {
	if !n.Valid {
		return ""
	}
	// MarshalJSON emits the numeric in its exact decimal form; the non-finite
	// values (NaN, Infinity) come back quoted.
	b, err := n.MarshalJSON()
	if err != nil {
		return ""
	}
	return strings.Trim(string(b), `"`)
}
