// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package scheduling owns the dealer side of the pro portal's delivery
// windows and pickup slots (the parity plan's PE-08): the per yard recurring
// rules a yard manager defines (weekday, start, end, capacity, cutoff). The
// contractor side, including the holds that consume the capacity, lives in
// core/internal/portal, which reads and writes the same tables scoped to the
// customer; this module never writes holds.
package scheduling

import (
	"time"

	"github.com/google/uuid"
)

// Kinds of window: a delivery books a truck's time, a pickup books a counter.
const (
	KindDelivery = "delivery"
	KindPickup   = "pickup"
)

// ValidKind reports whether s is one of the two kinds.
func ValidKind(s string) bool {
	return s == KindDelivery || s == KindPickup
}

// Window is the dealer's recurring rule: every week on Weekday, from
// StartTime to EndTime local to the yard (locations.timezone), with Capacity
// places and a booking cutoff of CutoffMinutes before the start.
type Window struct {
	ID            uuid.UUID `json:"id"`
	YardID        uuid.UUID `json:"yard_id"`
	YardCode      string    `json:"yard_code"`
	YardTimezone  string    `json:"yard_timezone"`
	Kind          string    `json:"kind"`
	Weekday       int       `json:"weekday"`    // 0 Sunday .. 6 Saturday
	StartTime     string    `json:"start_time"` // hh:mm, local to the yard
	EndTime       string    `json:"end_time"`
	Capacity      int       `json:"capacity"`
	CutoffMinutes int       `json:"cutoff_minutes"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// WindowRequest is the create and update payload. Times are hh:mm strings in
// the yard's own zone; the API never exchanges a naive local timestamp for an
// instant.
type WindowRequest struct {
	YardID        *uuid.UUID `json:"yard_id"`
	Kind          *string    `json:"kind"`
	Weekday       *int       `json:"weekday"`
	StartTime     *string    `json:"start_time"`
	EndTime       *string    `json:"end_time"`
	Capacity      *int       `json:"capacity"`
	CutoffMinutes *int       `json:"cutoff_minutes"`
	IsActive      *bool      `json:"is_active"`
}
