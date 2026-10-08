// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

import (
	"context"
	"errors"
	"fmt"

	"github.com/gablelbm/gable/internal/domain"
)

// errQBONotImplemented is the single truth about this adapter: the QuickBooks
// Online integration is a stub. No method talks to Intuit. Every entry point
// returns this so that a deployment which selects the QBO adapter finds out at
// the health check rather than at month-end close.
var errQBONotImplemented = errors.New("QuickBooks Online adapter is not implemented: no QBO API calls are made and no journal entries are posted")

// QuickBooksOnlineAdapter is a placeholder implementation of GLAdapter. It
// satisfies the interface so the GL layer compiles and can be wired, but it
// posts nothing and verifies nothing. Do not select it in a deployment that
// expects its GL to be in sync.
type QuickBooksOnlineAdapter struct {
	RealmID string
	// Token storage would go here
}

func NewQBOAdapter(realmID string) *QuickBooksOnlineAdapter {
	return &QuickBooksOnlineAdapter{
		RealmID: realmID,
	}
}

func (q *QuickBooksOnlineAdapter) Name() string {
	return "QuickBooksOnline"
}

// PostJournalEntry always fails. TODO: build the QBO payload and POST it to
// the Intuit Accounting API using the realm's stored OAuth token.
func (q *QuickBooksOnlineAdapter) PostJournalEntry(ctx context.Context, entry domain.JournalEntry) (string, error) {
	return "", fmt.Errorf("post journal entry: %w", errQBONotImplemented)
}

// SyncCheck always fails, because the connectivity it is supposed to verify
// does not exist yet. It previously returned nil, which reported this
// unimplemented adapter as healthy — a green health check in front of a
// PostJournalEntry that unconditionally errors, i.e. the operator learns the
// GL never synced only after the entries have piled up. A stub must fail its
// own health check.
//
// TODO: replace with a real liveness probe (e.g. GET /v3/company/{realmID}/
// companyinfo/{realmID}) once the OAuth token store lands.
func (q *QuickBooksOnlineAdapter) SyncCheck(ctx context.Context) error {
	return fmt.Errorf("sync check: %w", errQBONotImplemented)
}

// Ensure interface compliance
var _ GLAdapter = (*QuickBooksOnlineAdapter)(nil)
