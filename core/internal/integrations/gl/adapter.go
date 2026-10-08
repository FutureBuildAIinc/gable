// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

import (
	"context"

	"github.com/gablelbm/gable/internal/domain"
)

// GLAdapter defines the interface for interacting with external accounting systems
type GLAdapter interface {
	// Name returns the name of the adapter (e.g., "QBO", "NetSuite", "Mock")
	Name() string

	// PostJournalEntry sends a journal entry to the external system
	PostJournalEntry(ctx context.Context, entry domain.JournalEntry) (string, error)

	// SyncCheck validates connectivity to the external system. It must
	// return a non-nil error whenever PostJournalEntry could not succeed —
	// including when the adapter is an unimplemented stub. Reporting a
	// stub as healthy hides a GL that is silently not syncing.
	SyncCheck(ctx context.Context) error
}
