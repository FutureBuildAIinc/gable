// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"encoding/json"
	"net/http"
)

// ListEnvelope is the wire shape of every list response (ADR 0001 §1).
type ListEnvelope[T any] struct {
	// Items is always a JSON array, never null; WriteList enforces it.
	Items []T `json:"items"`
	// NextCursor is the minted cursor of the next page, or null when this
	// page is the last the caller can fetch.
	NextCursor *string `json:"next_cursor"`
	// Limit is the effective page size the server used.
	Limit int `json:"limit"`
	// Total appears only on the ?include=total path (WithTotal below);
	// omitempty keeps it off every ordinary page.
	Total *int64 `json:"total,omitempty"`
}

// listConfig collects the WriteList options.
type listConfig struct {
	total *int64
}

// ListOption is one optional argument to WriteList.
type ListOption func(*listConfig)

// WithTotal adds the `total` field to the page. The handler calls it only
// on its ?include=total path, with the count it computed under the
// request's filters: counting is a second query, which is exactly why it
// is opt-in and carried by the handler that chose to run it.
func WithTotal(total int64) ListOption {
	return func(c *listConfig) { c.total = &total }
}

// WriteList writes a list page envelope with 200 (ADR 0001 §1).
//
// next is the minted cursor of the following page (MintCursor of the last
// row of THIS page when the query that fetched limit+1 rows got limit+1
// rows), or "" when this page is the last, in which case next_cursor
// serializes as null. A nil or empty items slice serializes as []: a client
// never branches on null versus array.
func WriteList[T any](w http.ResponseWriter, items []T, next string, limit int, opts ...ListOption) {
	cfg := listConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	if items == nil {
		items = make([]T, 0)
	}
	var nextCursor *string
	if next != "" {
		nextCursor = &next
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(ListEnvelope[T]{
		Items:      items,
		NextCursor: nextCursor,
		Limit:      limit,
		Total:      cfg.total,
	})
}
