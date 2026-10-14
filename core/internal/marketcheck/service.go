// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package marketcheck is the Market check: our walk in prices read against a
// live competitor store through an FB Connect pricing flow (FROM-PRINCIPAL
// item 112 c). The flow is reached by its webhook, whose id is a secret
// (anyone holding it can run the flow): it is sealed at rest, never answered,
// never logged. The flow's rows are untrusted; every figure the dealer sees
// beside the competitor's price is recomputed here.
package marketcheck

import (
	"context"
	"log/slog"
	"time"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/google/uuid"
)

const (
	maxPick        = 12
	batchSize      = 3
	maxConcurrent  = 4
	maxAnswerBytes = 1 << 20
)

// Store reads and writes the module's system_settings rows.
type Store interface {
	Read(ctx context.Context, key string) (string, bool, error)
	Write(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
}

// TxRunner runs fn in one transaction (database.DB).
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Auditor records a settings change by field name, never by value.
type Auditor interface {
	Record(ctx context.Context, action string, fields []string) error
}

// Yard is the caller's yard as the flow is told it.
type Yard struct {
	Name string
	Town string
}

// Product is what the run reads of one catalogue product: our walk in price
// and our average unit cost, both cents.
type Product struct {
	ID          uuid.UUID
	SKU         string
	Description string
	Category    string
	UOM         string
	PriceCents  int64
	CostCents   int64
}

// Catalog is the read side of the run.
type Catalog interface {
	Yard(ctx context.Context) (Yard, error)
	Products(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Product, error)
}

// Service holds the settings and runs checks.
type Service struct {
	store   Store
	catalog Catalog
	vault   ai.Sealer
	tx      TxRunner
	audit   Auditor
	log     *slog.Logger
	timeout time.Duration
}

// NewService builds the service; settings rows live in store.
func NewService(store Store, catalog Catalog) *Service {
	return &Service{
		store: store, catalog: catalog, log: slog.Default(), timeout: 30 * time.Second,
	}
}

// WithVault wires the vault that seals the webhook id at rest.
func (s *Service) WithVault(v ai.Sealer) *Service { s.vault = v; return s }

// WithTxRunner wires the transaction runner the settings write uses.
func (s *Service) WithTxRunner(t TxRunner) *Service { s.tx = t; return s }

// WithAudit wires the settings change recorder.
func (s *Service) WithAudit(a Auditor) *Service { s.audit = a; return s }

// WithLogger wires the logger (host only lines; never the id or the address).
func (s *Service) WithLogger(l *slog.Logger) *Service {
	if l != nil {
		s.log = l
	}
	return s
}

// BatchTimeout is the longest one batch may wait for the flow.
func (s *Service) BatchTimeout() time.Duration { return s.timeout }

// WithBatchTimeout shortens the per batch context timeout (tests).
func (s *Service) WithBatchTimeout(d time.Duration) *Service { s.timeout = d; return s }
