// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/google/uuid"
)

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

// OfflineSyncResponse reports the batch's outcome.
type OfflineSyncResponse struct {
	BatchID        string           `json:"batch_id"`
	SyncedCount    int              `json:"synced_count"`
	DuplicateCount int              `json:"duplicate_count"`
	ErrorCount     int              `json:"error_count"`
	PendingCount   int              `json:"pending_count"`
	Errors         []SyncItemResult `json:"errors"`
	Pending        []SyncItemResult `json:"pending"`
}

// SyncOfflineTransactions replays a batch of offline sales, each through the
// same completion path a live sale takes (ADR 0005 section 14.2 C2-5): the
// cart is rebuilt and completed in one transaction per sale. Client
// generated ids make a replayed batch idempotent. A sale already made
// offline is never REJECTED for a tax provider failure: it stays pending in
// the sync log and the next sync retries it (ADR 0005 section 3).
func (s *Service) SyncOfflineTransactions(ctx context.Context, batch *OfflineSync, actor string) (*OfflineSyncResponse, error) {
	resp := &OfflineSyncResponse{BatchID: batch.BatchID, Errors: []SyncItemResult{}, Pending: []SyncItemResult{}}
	log := LogBatch{BatchID: batch.BatchID, RegisterID: batch.RegisterID}
	for i := range batch.Items {
		item := &batch.Items[i]
		exists, err := s.repo.SaleExists(ctx, item.ClientID)
		if err != nil {
			resp.ErrorCount++
			e := SyncItemResult{ClientID: item.ClientID.String(), Reason: "existence check: " + err.Error()}
			resp.Errors = append(resp.Errors, e)
			log.Errors = append(log.Errors, e)
			continue
		}
		if exists {
			resp.DuplicateCount++
			continue
		}
		// Rebuild and complete the sale through the live path. The sale row
		// carries the client's id, so a retry of this batch finds it.
		register := batch.RegisterID
		sale, err := s.StartSaleAt(ctx, item, register, actor)
		if err != nil {
			resp.ErrorCount++
			e := SyncItemResult{ClientID: item.ClientID.String(), Reason: "create: " + err.Error()}
			resp.Errors = append(resp.Errors, e)
			log.Errors = append(log.Errors, e)
			continue
		}
		if _, err := s.AddLine(ctx, sale.ID, item.Lines, actor); err != nil {
			resp.ErrorCount++
			e := SyncItemResult{ClientID: item.ClientID.String(), Reason: "lines: " + err.Error()}
			resp.Errors = append(resp.Errors, e)
			log.Errors = append(log.Errors, e)
			continue
		}
		if _, err := s.CompleteSale(ctx, sale.ID, "", nil, item.Tenders, "", actor); err != nil {
			if errors.Is(err, ErrTaxProviderUnavailable) {
				// never a rejection: the sale was made offline, and the
				// provider will be asked again on the next sync
				p := SyncItemResult{ClientID: item.ClientID.String(), Reason: "pending: the tax provider did not answer"}
				resp.PendingCount++
				resp.Pending = append(resp.Pending, p)
				log.Details = append(log.Details, p)
				continue
			}
			resp.ErrorCount++
			e := SyncItemResult{ClientID: item.ClientID.String(), Reason: "complete: " + err.Error()}
			resp.Errors = append(resp.Errors, e)
			log.Errors = append(log.Errors, e)
			continue
		}
		resp.SyncedCount++
	}
	log.Synced, log.Duplicates, log.ErrorCount = resp.SyncedCount, resp.DuplicateCount, resp.ErrorCount
	log.Pending = resp.PendingCount
	if err := s.repo.LogSyncBatch(ctx, log); err != nil {
		return nil, err
	}
	return resp, nil
}

// StartSaleAt creates the sale row for an offline item with the client's
// id, so the batch replays idempotently.
func (s *Service) StartSaleAt(ctx context.Context, item *OfflineSale, register, actor string) (*Sale, error) {
	in := &StartSale{RegisterID: register, CashierID: item.CashierID, CustomerID: item.CustomerID}
	if in.RegisterID == "" {
		in.RegisterID = "REG-01"
	}
	walkInID, defaultCurrency, err := s.repo.WalkInCustomer(ctx)
	if err != nil {
		return nil, err
	}
	currency := defaultCurrency
	if in.CustomerID != nil {
		facts, err := s.repo.CustomerFacts(ctx, *in.CustomerID)
		if err != nil {
			return nil, err
		}
		currency = facts.Currency
	}
	var out *Sale
	err = s.inTx(ctx, func(ctx context.Context) error {
		sale := &Sale{ID: item.ClientID, RegisterID: in.RegisterID, CashierID: in.CashierID, CustomerID: in.CustomerID,
			Status: StatusOpen, Currency: currency, WalkInID: walkInID}
		if session, err := s.repo.GetOpenTillSession(ctx, in.RegisterID); err == nil && session != nil {
			sale.TillSessionID = &session.ID
		}
		if err := s.repo.CreateSale(ctx, sale); err != nil {
			return err
		}
		out = sale
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
