// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

// The retired request shape of POST /pos/sync (S6). The route still answers
// 409 money_never_queues for every batch that parses, and the malformed
// shapes keep their 400; the three step replay that rebuilt a sale through
// StartSaleAt, AddLine and CompleteSale is deleted (ADR 0011 section 3: one
// transaction per sale through the completion's own body, SyncOffline in
// offline_sync.go). Slice C2 moves the route onto the signed batch.

// OfflineSync is a parsed batch of offline sales.
type OfflineSync struct {
	BatchID    string
	RegisterID string
	Items      []OfflineSale
}

// OfflineSale is one completed sale captured offline.
type OfflineSale struct {
	ClientID        uuid.UUID
	CashierID       uuid.UUID
	CustomerID      *uuid.UUID
	Lines           []salesdoc.ParsedLine
	Tenders         []TenderIn
	ClientCreatedAt *httpx.Timestamp
}
