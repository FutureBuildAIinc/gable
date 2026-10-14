// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"sync"
	"time"

	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// cache is a type-safe, TTL-based in-memory cache.
type cache[T any] struct {
	data      T
	timestamp time.Time
	valid     bool
}

// reachKey is a caller's reach (SEC-NOHEADER-1, the principal's ruling on
// the reporting surface): the branch a header names, and the user sub whose
// user_locations grants scope a no header caller. Two callers share an entry
// only when both are nil (the every branch view an administrator or owner
// keeps), so a primed administrator entry never serves a finance caller.
type reachKey struct {
	branch uuid.UUID
	sub    string
}

// reachCache is a per-reach keyed cache.
type reachCache[T any] struct {
	mu      sync.RWMutex
	entries map[reachKey]*cache[T]
}

func newReachCache[T any]() *reachCache[T] {
	return &reachCache[T]{entries: make(map[reachKey]*cache[T])}
}

func (b *reachCache[T]) get(key reachKey, ttl time.Duration) (T, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if e, ok := b.entries[key]; ok && e.valid && time.Since(e.timestamp) < ttl {
		return e.data, true
	}
	var zero T
	return zero, false
}

func (b *reachCache[T]) set(key reachKey, data T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[key] = &cache[T]{data: data, timestamp: time.Now(), valid: true}
}

// cacheStore holds all dashboard caches, keyed by the caller's reach.
type cacheStore struct {
	ttl             time.Duration
	summary         *reachCache[*DashboardSummary]
	inventoryAlerts *reachCache[[]InventoryAlert]
	topCustomers    *reachCache[[]TopCustomer]
	orderActivity   *reachCache[*OrderActivity]
	revenueTrend    *reachCache[[]RevenueTrendPoint]
}

func newCacheStore(ttl time.Duration) *cacheStore {
	return &cacheStore{
		ttl:             ttl,
		summary:         newReachCache[*DashboardSummary](),
		inventoryAlerts: newReachCache[[]InventoryAlert](),
		topCustomers:    newReachCache[[]TopCustomer](),
		orderActivity:   newReachCache[*OrderActivity](),
		revenueTrend:    newReachCache[[]RevenueTrendPoint](),
	}
}

// Service provides dashboard business logic with type-safe per-reach caching.
type Service struct {
	repo  Repository
	store *cacheStore
}

// NewService creates a new dashboard service.
func NewService(repo Repository) *Service {
	return &Service{
		repo:  repo,
		store: newCacheStore(60 * time.Second),
	}
}

// reach reads the caller's reach from the request's branch context: the
// branch a header names and the grants sub of a no header user, either of
// which keys the cache apart from every other reach.
func reach(ctx context.Context) (reachKey, *uuid.UUID, *string) {
	key := reachKey{branch: uuid.Nil}
	if id := middleware.BranchIDForQuery(ctx); id != nil {
		key.branch = *id
	}
	if sub := middleware.GrantsSubForQuery(ctx); sub != nil {
		key.sub = *sub
	}
	return key, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)
}

// GetSummary returns the dashboard summary with caching.
func (s *Service) GetSummary(ctx context.Context) (*DashboardSummary, error) {
	key, branchID, grantsSub := reach(ctx)
	if cached, ok := s.store.summary.get(key, s.store.ttl); ok {
		return cached, nil
	}

	data, err := s.repo.GetDashboardSummary(ctx, branchID, grantsSub)
	if err != nil {
		return nil, err
	}

	s.store.summary.set(key, data)
	return data, nil
}

// GetInventoryAlerts returns inventory alerts with caching.
func (s *Service) GetInventoryAlerts(ctx context.Context) ([]InventoryAlert, error) {
	key, branchID, grantsSub := reach(ctx)
	if cached, ok := s.store.inventoryAlerts.get(key, s.store.ttl); ok {
		return cached, nil
	}

	data, err := s.repo.GetInventoryAlerts(ctx, branchID, grantsSub, 10)
	if err != nil {
		return nil, err
	}

	s.store.inventoryAlerts.set(key, data)
	return data, nil
}

// GetTopCustomers returns top customers with caching.
func (s *Service) GetTopCustomers(ctx context.Context) ([]TopCustomer, error) {
	key, branchID, grantsSub := reach(ctx)
	if cached, ok := s.store.topCustomers.get(key, s.store.ttl); ok {
		return cached, nil
	}

	data, err := s.repo.GetTopCustomers(ctx, branchID, grantsSub, 5, 30)
	if err != nil {
		return nil, err
	}

	s.store.topCustomers.set(key, data)
	return data, nil
}

// GetOrderActivity returns order activity with caching.
func (s *Service) GetOrderActivity(ctx context.Context) (*OrderActivity, error) {
	key, branchID, grantsSub := reach(ctx)
	if cached, ok := s.store.orderActivity.get(key, s.store.ttl); ok {
		return cached, nil
	}

	data, err := s.repo.GetOrderActivity(ctx, branchID, grantsSub, 10)
	if err != nil {
		return nil, err
	}

	s.store.orderActivity.set(key, data)
	return data, nil
}

// GetRevenueTrend returns revenue trend for chart.
func (s *Service) GetRevenueTrend(ctx context.Context) ([]RevenueTrendPoint, error) {
	key, branchID, grantsSub := reach(ctx)
	if cached, ok := s.store.revenueTrend.get(key, s.store.ttl); ok {
		return cached, nil
	}

	data, err := s.repo.GetRevenueTrend(ctx, branchID, grantsSub, 7)
	if err != nil {
		return nil, err
	}

	s.store.revenueTrend.set(key, data)
	return data, nil
}
