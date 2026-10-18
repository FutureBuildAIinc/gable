// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// The pro portal's core added these Repository methods (M35 Parts 1, 4 and
// 7). The fake carries the same contract the real repository does: the
// defaults answer with empty data, and the fields let a test plant exactly
// one thing to see the service rule above it.
func (f *fakePortalRepo) GetCustomerUserByID(_ context.Context, userID, customerID uuid.UUID) (*CustomerUser, error) {
	if f.meUser != nil {
		return f.meUser, nil
	}
	return &CustomerUser{ID: userID, CustomerID: customerID, Role: f.meRole, Status: "Active"}, nil
}
func (f *fakePortalRepo) GetCustomerAccount(_ context.Context, customerID uuid.UUID) (*PortalAccountDTO, error) {
	return &PortalAccountDTO{ID: customerID, Name: "Fake Account", AccountNumber: "FAKE-1"}, nil
}
func (f *fakePortalRepo) ListCustomerYards(_ context.Context, _ uuid.UUID) ([]PortalYardDTO, error) {
	return []PortalYardDTO{}, nil
}
func (f *fakePortalRepo) TodayQuotes(_ context.Context, _ uuid.UUID, _ *time.Time, _ int) ([]TodayQuoteRow, error) {
	return f.todayQuotes, nil
}
func (f *fakePortalRepo) TodayQuotesCount(_ context.Context, _ uuid.UUID) (int, error) {
	return len(f.todayQuotes), nil
}
func (f *fakePortalRepo) TodayDeliveries(_ context.Context, _ uuid.UUID, _ bool, _ *time.Time, _ int) ([]TodayDeliveryRow, error) {
	return f.todayDeliveries, nil
}
func (f *fakePortalRepo) TodayDeliveriesCount(_ context.Context, _ uuid.UUID, _ bool) (int, error) {
	return len(f.todayDeliveries), nil
}
func (f *fakePortalRepo) NextDeliveries(_ context.Context, _ uuid.UUID, _ bool, _ *time.Time, _ int) ([]TodayDeliveryRow, error) {
	return nil, nil
}
func (f *fakePortalRepo) NextDeliveriesCount(_ context.Context, _ uuid.UUID, _ bool) (int, error) {
	return 0, nil
}
func (f *fakePortalRepo) TodayInvoices(_ context.Context, _ uuid.UUID, _ *time.Time, _ int) ([]TodayInvoiceRow, error) {
	return f.todayInvoices, nil
}
func (f *fakePortalRepo) TodayInvoicesCount(_ context.Context, _ uuid.UUID) (int, error) {
	return len(f.todayInvoices), nil
}
func (f *fakePortalRepo) TodayOpenOrders(_ context.Context, _ uuid.UUID, _ *time.Time, _ int) ([]TodayOrderRow, error) {
	return f.todayOrders, nil
}
func (f *fakePortalRepo) TodayOpenOrdersCount(_ context.Context, _ uuid.UUID) (int, error) {
	return len(f.todayOrders), nil
}
func (f *fakePortalRepo) ReadOrderDeliveryState(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*orderDeliveryState, error) {
	return f.deliveryState, nil
}
func (f *fakePortalRepo) SetOrderSiteInstructions(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ string) error {
	return nil
}
func (f *fakePortalRepo) InTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *fakePortalRepo) ListScheduleWindows(_ context.Context, _ uuid.UUID, _ uuid.UUID, _, _, _ string) ([]ScheduleWindowView, error) {
	return nil, nil
}
func (f *fakePortalRepo) CreateScheduleHold(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ ScheduleWindowRequest) (*ScheduleHoldDTO, error) {
	return nil, errors.New("not wired in the fake")
}
func (f *fakePortalRepo) ChangeScheduleHold(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ uuid.UUID, _ ScheduleWindowRequest) (*ScheduleHoldDTO, error) {
	return nil, errors.New("not wired in the fake")
}
func (f *fakePortalRepo) AttachScheduleHoldToOrder(_ context.Context, _, _, _ uuid.UUID) error {
	return nil
}
func (f *fakePortalRepo) ReleaseScheduleHold(_ context.Context, _ uuid.UUID, _ uuid.UUID) error {
	return nil
}
func (f *fakePortalRepo) GetScheduleHold(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*ScheduleHoldDTO, error) {
	return nil, ErrHoldNotFound
}
func (f *fakePortalRepo) ListInvoicesByCustomerFiltered(_ context.Context, _ uuid.UUID, _ *time.Time) ([]PortalInvoiceDTO, error) {
	return f.invoices, nil
}
func (f *fakePortalRepo) ListDeliveriesByCustomerFiltered(_ context.Context, _ uuid.UUID, _ *time.Time) ([]PortalDeliveryDTO, error) {
	return f.deliveries, nil
}
func (f *fakePortalRepo) ListPortalQuotesFiltered(_ context.Context, _ uuid.UUID, _ *time.Time) ([]PortalQuoteDTO, error) {
	return f.quotes, nil
}
