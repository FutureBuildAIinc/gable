// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"encoding/json"
	"fmt"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
)

// Tally is a random length line's pieces by length (ADR 0006 section 4.3),
// the same wire shape on every sales document: the cross section the line
// snapshotted from its product, the rows in request order, the exact linear
// feet the line's quantity carries, and the board feet as the record's
// display rounding (R4.3), which no arithmetic reads. The request carries
// only rows; the derived fields are the server's.
type Tally struct {
	ThicknessIn *httpx.Quantity `json:"thickness_in"`
	WidthIn     *httpx.Quantity `json:"width_in"`
	Rows        []TallyRow      `json:"rows"`
	LinearFeet  httpx.Quantity  `json:"linear_feet"`
	BoardFeet   *httpx.Quantity `json:"board_feet"`
}

// TallyRow is one length of a tally: pieces at that length.
type TallyRow struct {
	Pieces   int            `json:"pieces"`
	LengthFT httpx.Quantity `json:"length_ft"`
}

// TallyThickness is the cross section the tally snapshotted from the
// product, for the line's board_thickness_in and board_width_in columns.
func (l *Line) TallyThickness() *httpx.Quantity {
	if l.Tally == nil {
		return nil
	}
	return l.Tally.ThicknessIn
}

// TallyWidth is the other half of the snapshotted cross section.
func (l *Line) TallyWidth() *httpx.Quantity {
	if l.Tally == nil {
		return nil
	}
	return l.Tally.WidthIn
}

// CompleteTallies finishes every line's tally after its rows arrive from
// line_tally_rows: the exact linear feet the wire carries and, with a cross
// section, the display board feet (R4.3), which no arithmetic reads.
func CompleteTallies(lines []Line) {
	for i := range lines {
		t := lines[i].Tally
		if t == nil {
			continue
		}
		if len(t.Rows) == 0 {
			lines[i].Tally = nil
			continue
		}
		rows := make([]units.TallyRow, 0, len(t.Rows))
		for _, r := range t.Rows {
			rows = append(rows, units.TallyRow{Pieces: int64(r.Pieces), LengthFT: r.LengthFT})
		}
		lf, err := units.LinearFeet(rows)
		if err != nil {
			continue // the rows are already stored; a bad sum cannot happen
		}
		t.LinearFeet = lf
		if t.ThicknessIn != nil && t.WidthIn != nil {
			if bf, err := units.BoardFeet(lf, *t.ThicknessIn, *t.WidthIn); err == nil {
				display := units.DisplayBoardFeet(bf)
				t.BoardFeet = &display
			}
		}
	}
}

// TallyRequest is the tally a line request carries (ADR 0006 section 4.3):
// rows only. thickness_in, width_in, linear_feet and board_feet are read
// only, and one in a request is a 400 naming the field, the recipe's rule
// for fields a write does not apply.
type TallyRequest struct {
	Rows []TallyRowRequest `json:"rows"`
}

// TallyRowRequest is one length of a tally request.
type TallyRowRequest struct {
	Pieces   json.RawMessage `json:"pieces"`
	LengthFT json.RawMessage `json:"length_ft"`
}

// ParseTally validates a request's tally: one to one hundred rows, pieces a
// positive count, the length a positive decimal (ADR 0006 section 4.2). It
// answers the parsed rows, or false with the field errors collected in v.
func ParseTally(v *httpx.Validator, path string, req *TallyRequest) ([]TallyRow, bool) {
	rows := make([]TallyRow, 0, len(req.Rows))
	v.Check(len(req.Rows) > 0, path+".tally", "carries at least one row")
	v.Check(len(req.Rows) <= 100, path+".tally", "carries at most 100 rows")
	for j, r := range req.Rows {
		rpath := fmt.Sprintf("%s.tally.rows[%d]", path, j)
		pieces, pok := v.Int(rpath+".pieces", r.Pieces, true)
		if pok {
			v.Check(pieces > 0, rpath+".pieces", "must be greater than zero")
		}
		length, lok := v.Quantity(rpath+".length_ft", r.LengthFT, true)
		if lok {
			v.Check(length > 0, rpath+".length_ft", "must be greater than zero")
		}
		if pok && lok && pieces > 0 && length > 0 {
			rows = append(rows, TallyRow{Pieces: int(pieces), LengthFT: length})
		}
	}
	if len(rows) != len(req.Rows) {
		return nil, false
	}
	return rows, true
}
