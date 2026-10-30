// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// The market index CSV import (QE-07-b1).
//
// A file of index values is previewed and then applied. The preview names
// every row's fate: which index it matched, what it will change, and, for a
// row that cannot be applied, why. The apply writes the same rows the
// preview showed, through the same path a manual refresh takes, so the
// exposure scan runs for each index that moved exactly as it does today.
//
// The file says which day each value is for, so a value for one index, day
// and source is stored once. Importing the same file twice changes nothing
// the second time and scans nothing.

// The reasons a row is refused. They are the whole vocabulary a file's
// problems are reported in.
const (
	// importReasonUnknownCode: no market index carries the code.
	importReasonUnknownCode = "unknown index code"
	// importReasonBadValue: the value is not a number, or not positive.
	importReasonBadValue = "value must be a positive number"
	// importReasonBadDate: the as of date is not a date.
	importReasonBadDate = "as of date must be a date (YYYY-MM-DD)"
	// importReasonDuplicate: the file already carries a row for this index
	// and day.
	importReasonDuplicate = "this file already has a row for this index and day"
	// importReasonConflict: a value for this index, day and source is
	// already stored, and it is not the value this row carries.
	importReasonConflict = "a value for this index and day is already stored"
)

// The statuses a row can hold after a preview.
const (
	// importStatusAccepted: the row will be written.
	importStatusAccepted = "accepted"
	// importStatusUnchanged: the value is already stored, so nothing moves
	// and no scan runs.
	importStatusUnchanged = "unchanged"
	// importStatusRefused: the row cannot be applied; the reason says why.
	importStatusRefused = "refused"
)

// ImportSourceManual is the source an imported value carries when the file
// names none. It matches the source a hand typed value carries, so a file
// and a typed value for one index and day are the same stored value.
const ImportSourceManual = "MANUAL"

// IndexImportRow is one row of a file and its fate.
type IndexImportRow struct {
	Line      int    `json:"line"`
	IndexCode string `json:"index_code"`
	IndexID   string `json:"index_id,omitempty"`
	IndexName string `json:"index_name,omitempty"`
	Value     string `json:"value"`
	AsOfDate  string `json:"as_of_date"`
	Source    string `json:"source"`
	SourceURL string `json:"source_url,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`

	// asOf is the parsed day, kept beside the row so the apply can find
	// each index's newest day without parsing the text again.
	asOf time.Time
	// index is the matched index, nil when the row was refused.
	index *MarketIndex
}

// IndexImportPreview is what a file would do: every row's fate, and the
// counts that answer "how much of this file lands".
type IndexImportPreview struct {
	Rows            []IndexImportRow `json:"rows"`
	AcceptedCount   int              `json:"accepted_count"`
	UnchangedCount  int              `json:"unchanged_count"`
	RefusedCount    int              `json:"refused_count"`
	IndicesAffected int              `json:"indices_affected"`
}

// IndexImportResult is what a file did: the rows that were written, the
// indices whose current value moved, and the exposure scans that ran.
type IndexImportResult struct {
	Rows           []IndexImportRow `json:"rows"`
	AppliedCount   int              `json:"applied_count"`
	ChangedIndices int              `json:"changed_indices"`
	Scans          int              `json:"scans"`
	UnchangedCount int              `json:"unchanged_count"`
	RefusedCount   int              `json:"refused_count"`
}

// indexImportCSVRow is one parsed row of the file, before it is matched.
type indexImportCSVRow struct {
	line      int
	indexCode string
	value     string
	asOfDate  string
	source    string
	sourceURL string
}

// IndexImportService previews and applies a market index CSV.
type IndexImportService struct {
	escalators EscalatorRepository
	source     IndexSourceRepository
	db         *database.DB
	scanner    *ExposureScanner
	logger     *slog.Logger
}

// NewIndexImportService returns the import service.
func NewIndexImportService(escalators EscalatorRepository, source IndexSourceRepository, db *database.DB, scanner *ExposureScanner, logger *slog.Logger) *IndexImportService {
	if logger == nil {
		logger = slog.Default()
	}
	return &IndexImportService{escalators: escalators, source: source, db: db, scanner: scanner, logger: logger}
}

// ParseIndexCSV reads the file's bytes into rows. The header names the
// columns: index_code, value and as_of_date are required; source and
// source_url are optional. A row that cannot be parsed is not an error here:
// the row's problem is reported in the preview, with its reason.
func ParseIndexCSV(data []byte) ([]indexImportCSVRow, error) {
	reader := csv.NewReader(strings.NewReader(string(data)))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, csv.ErrFieldCount) {
			return nil, errors.New("the file could not be read as CSV")
		}
		return nil, fmt.Errorf("read the file's header: %w", err)
	}
	cols := map[string]int{}
	for i, name := range header {
		cols[strings.ToLower(strings.TrimSpace(name))] = i
	}
	for _, required := range []string{"index_code", "value", "as_of_date"} {
		if _, ok := cols[required]; !ok {
			return nil, fmt.Errorf("the file's header must name a %s column", required)
		}
	}
	valueCol, asOfCol := cols["value"], cols["as_of_date"]
	codeCol := cols["index_code"]
	sourceCol, hasSource := cols["source"]
	urlCol, hasURL := cols["source_url"]

	rows := []indexImportCSVRow{}
	line := 1
	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, csv.ErrFieldCount) {
				return nil, errors.New("the file could not be read as CSV")
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read the file: %w", err)
		}
		line++
		if len(record) == 0 {
			continue
		}
		row := indexImportCSVRow{line: line}
		row.indexCode = strings.TrimSpace(fieldAt(record, codeCol))
		row.value = strings.TrimSpace(fieldAt(record, valueCol))
		row.asOfDate = strings.TrimSpace(fieldAt(record, asOfCol))
		if hasSource {
			row.source = strings.ToUpper(strings.TrimSpace(fieldAt(record, sourceCol)))
		}
		if hasURL {
			row.sourceURL = strings.TrimSpace(fieldAt(record, urlCol))
		}
		if row.indexCode == "" && row.value == "" && row.asOfDate == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// fieldAt reads one column of a record, or the empty string when the record
// is shorter than the column.
func fieldAt(record []string, col int) string {
	if col < 0 || col >= len(record) {
		return ""
	}
	return record[col]
}

// Preview reads the file and answers what it would do. It writes nothing.
func (s *IndexImportService) Preview(ctx context.Context, data []byte) (*IndexImportPreview, error) {
	parsed, err := ParseIndexCSV(data)
	if err != nil {
		return nil, err
	}
	out := &IndexImportPreview{Rows: []IndexImportRow{}}
	seen := map[string]bool{}
	affected := map[uuid.UUID]bool{}
	for _, raw := range parsed {
		row := IndexImportRow{
			Line:      raw.line,
			IndexCode: raw.indexCode,
			Value:     raw.value,
			AsOfDate:  raw.asOfDate,
			Source:    raw.source,
			SourceURL: raw.sourceURL,
		}
		if row.Source == "" {
			row.Source = ImportSourceManual
		}
		idx, err := s.escalators.GetMarketIndexByCode(ctx, raw.indexCode)
		if err != nil {
			return nil, fmt.Errorf("read the market index %q: %w", raw.indexCode, err)
		}
		if idx == nil {
			row.Status = importStatusRefused
			row.Reason = importReasonUnknownCode
			out.Rows = append(out.Rows, row)
			out.RefusedCount++
			continue
		}
		row.IndexID = idx.ID.String()
		row.IndexName = idx.Name
		row.index = idx
		value, err := strconv.ParseFloat(raw.value, 64)
		if err != nil || value <= 0 {
			row.Status = importStatusRefused
			row.Reason = importReasonBadValue
			out.Rows = append(out.Rows, row)
			out.RefusedCount++
			continue
		}
		asOf, err := time.Parse("2006-01-02", raw.asOfDate)
		if err != nil {
			row.Status = importStatusRefused
			row.Reason = importReasonBadDate
			out.Rows = append(out.Rows, row)
			out.RefusedCount++
			continue
		}
		row.asOf = asOf
		key := idx.ID.String() + "|" + asOf.Format("2006-01-02")
		if seen[key] {
			row.Status = importStatusRefused
			row.Reason = importReasonDuplicate
			out.Rows = append(out.Rows, row)
			out.RefusedCount++
			continue
		}
		seen[key] = true
		stored, err := s.source.HistoryByAsOf(ctx, idx.ID, asOf, row.Source)
		if err != nil {
			return nil, fmt.Errorf("read the stored value for %q on %s: %w", raw.indexCode, raw.asOfDate, err)
		}
		if stored != nil {
			if stored.Value == value {
				row.Status = importStatusUnchanged
			} else {
				row.Status = importStatusRefused
				row.Reason = importReasonConflict
				out.RefusedCount++
			}
			out.Rows = append(out.Rows, row)
			if row.Status == importStatusUnchanged {
				out.UnchangedCount++
			}
			continue
		}
		row.Status = importStatusAccepted
		affected[idx.ID] = true
		out.Rows = append(out.Rows, row)
		out.AcceptedCount++
	}
	out.IndicesAffected = len(affected)
	return out, nil
}

// Apply writes the file's rows. Every row the preview accepted is written
// through the same path a manual refresh takes: the newest as-of date an
// index's rows carry becomes the index's current value, older rows are
// history only, and the exposure scan runs once for each index whose value
// moved. A row already stored is left alone, so importing the same file
// twice changes nothing the second time.
func (s *IndexImportService) Apply(ctx context.Context, data []byte, recordedBy string) (*IndexImportResult, error) {
	preview, err := s.Preview(ctx, data)
	if err != nil {
		return nil, err
	}
	out := &IndexImportResult{
		Rows:           preview.Rows,
		UnchangedCount: preview.UnchangedCount,
		RefusedCount:   preview.RefusedCount,
	}
	// Group the accepted rows by index and find each index's newest day: the
	// value for that day is the one that becomes the index's current value,
	// and the older days are history.
	newest := map[uuid.UUID]int{}
	for i, row := range preview.Rows {
		if row.Status != importStatusAccepted {
			continue
		}
		id, err := uuid.Parse(row.IndexID)
		if err != nil {
			return nil, fmt.Errorf("read the imported row's index: %w", err)
		}
		if at, ok := newest[id]; !ok || row.asOf.After(preview.Rows[at].asOf) {
			newest[id] = i
		}
	}
	for i, row := range preview.Rows {
		if row.Status != importStatusAccepted {
			continue
		}
		id, err := uuid.Parse(row.IndexID)
		if err != nil {
			return nil, fmt.Errorf("read the imported row's index: %w", err)
		}
		value, err := strconv.ParseFloat(row.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("read the imported row's value: %w", err)
		}
		asOf, err := time.Parse("2006-01-02", row.AsOfDate)
		if err != nil {
			return nil, fmt.Errorf("read the imported row's day: %w", err)
		}
		moved, err := s.recordValue(ctx, id, value, row.Source, row.SourceURL, &asOf, recordedBy, newest[id] == i)
		if err != nil {
			return nil, err
		}
		out.AppliedCount++
		if moved {
			out.ChangedIndices++
			out.Scans++
		}
	}
	return out, nil
}

// recordValue writes one imported value: the history row always, and the
// index's current value when this row is the index's newest day and the
// value differs from the one that stands. moved reports whether the current
// value moved, which is what decides the exposure scan.
//
// The history write is conditional on the unique index for one index, day
// and source, so two imports of the same file racing each other store the
// value once and scan once.
func (s *IndexImportService) recordValue(ctx context.Context, indexID uuid.UUID, value float64, source, sourceURL string, asOf *time.Time, recordedBy string, isNewest bool) (bool, error) {
	moved := false
	hist := &MarketIndexHistory{
		MarketIndexID: indexID,
		Value:         value,
		Source:        source,
		RecordedBy:    recordedBy,
		AsOfDate:      asOf,
		SourceURL:     sourceURL,
	}
	err := s.db.RunInTx(ctx, func(txCtx context.Context) error {
		idx, err := s.escalators.GetMarketIndex(txCtx, indexID)
		if err != nil {
			return fmt.Errorf("read the market index: %w", err)
		}
		if idx == nil {
			return fmt.Errorf("no market index %s", indexID)
		}
		inserted, err := s.source.InsertHistoryIfAbsent(txCtx, hist)
		if err != nil {
			return err
		}
		if !inserted {
			// Another writer stored this value first. Nothing moves and no
			// scan runs: the value is already there.
			return nil
		}
		if !isNewest || idx.CurrentValue == value {
			return nil
		}
		prev := idx.CurrentValue
		idx.PreviousValue = &prev
		idx.CurrentValue = value
		if err := s.escalators.UpdateMarketIndex(txCtx, idx); err != nil {
			return fmt.Errorf("update market index: %w", err)
		}
		moved = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if moved && s.scanner != nil {
		if err := s.scanner.OnMarketIndexUpdated(ctx, indexID, hist.ID); err != nil {
			s.logger.Warn("index import: scanner failed; safety-net cron will recover",
				"index_id", indexID, "err", err)
		}
	}
	return moved, nil
}
