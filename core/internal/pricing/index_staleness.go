// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"time"
)

// The market index staleness banner (QE-07-b1).
//
// A deployment says how old a market index value may get before the desk is
// told. When an active index's newest value is older than that, the market
// indices screen, the quotes exposure screen and the quote page's exposure
// banner say so: the numbers on the screen are older than the dealer
// agreed they may be.

// IndexStalenessDocument is the staleness answer: the threshold the
// deployment set, whether anything is stale, and each index's freshness.
type IndexStalenessDocument struct {
	ThresholdDays int              `json:"threshold_days"`
	Stale         bool             `json:"stale"`
	StaleIndices  []IndexFreshness `json:"stale_indices"`
	Indices       []IndexFreshness `json:"indices"`
}

// IndexStalenessService answers how fresh the market index values are.
type IndexStalenessService struct {
	source IndexSourceRepository
}

// NewIndexStalenessService returns the staleness service.
func NewIndexStalenessService(source IndexSourceRepository) *IndexStalenessService {
	return &IndexStalenessService{source: source}
}

// Staleness reads the threshold and each active index's freshness. An index
// whose newest value carries no day cannot be judged old: it says nothing
// about how old the value is, so it is not stale (days_old stays -1).
func (s *IndexStalenessService) Staleness(ctx context.Context, now time.Time) (*IndexStalenessDocument, error) {
	days, err := s.source.StalenessDays(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.source.ListFreshness(ctx)
	if err != nil {
		return nil, err
	}
	out := &IndexStalenessDocument{
		ThresholdDays: days,
		StaleIndices:  []IndexFreshness{},
		Indices:       []IndexFreshness{},
	}
	today := now.UTC().Truncate(24 * time.Hour)
	for _, f := range list {
		if f.NewestDate == nil {
			f.DaysOld = -1
			f.Stale = false
		} else {
			newest, err := time.Parse(dayFormat, *f.NewestDate)
			if err != nil {
				// A day the server cannot read says nothing about how old
				// the value is.
				f.DaysOld = -1
				f.Stale = false
				out.Indices = append(out.Indices, f)
				continue
			}
			f.DaysOld = int(today.Sub(newest.UTC()).Hours() / 24)
			f.Stale = f.DaysOld > days
		}
		out.Indices = append(out.Indices, f)
		if f.Stale {
			out.StaleIndices = append(out.StaleIndices, f)
			out.Stale = true
		}
	}
	return out, nil
}
