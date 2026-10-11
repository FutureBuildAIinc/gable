// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// ActivityType is the closed vocabulary of a logged activity. The storage
// vocabulary is uppercase (CALL, MEETING, EMAIL, NOTE); the wire vocabulary
// is lowercase snake_case and the mapping happens here, at the boundary
// (ADR 0001 section 6).
type ActivityType string

const (
	ActivityCall    ActivityType = "CALL"
	ActivityMeeting ActivityType = "MEETING"
	ActivityEmail   ActivityType = "EMAIL"
	ActivityNote    ActivityType = "NOTE"
)

// wireNames maps the storage vocabulary onto the wire's lowercase spelling.
var wireNames = map[ActivityType]string{
	ActivityCall:    "call",
	ActivityMeeting: "meeting",
	ActivityEmail:   "email",
	ActivityNote:    "note",
}

// MarshalText writes the lowercase wire spelling. A value outside the four
// degrades to the catch-all note instead of failing: the column held free
// text for the base's whole life, and an error here fires while the response
// is being encoded (a 200 already sent, then a truncated body). Migration
// 096 normalises and constrains the column; this is the read-side backstop.
func (t ActivityType) MarshalText() ([]byte, error) {
	if s, ok := wireNames[t]; ok {
		return []byte(s), nil
	}
	return []byte("note"), nil
}

// ParseActivityType accepts only the lowercase spelling; any other casing is
// a refusal, not a synonym.
func ParseActivityType(s string) (ActivityType, bool) {
	for storage, wire := range wireNames {
		if s == wire {
			return storage, true
		}
	}
	return "", false
}

// ActivityTypeNames lists the wire vocabulary, for error messages.
func ActivityTypeNames() []string {
	return []string{"call", "meeting", "email", "note"}
}

// Activity is one logged customer activity, the wire shape of
// /api/v1/activities and /api/v1/customers/{id}/activities. Optional fields
// are pointers without omitempty, so they serialize as null (ADR 0001
// section 12).
type Activity struct {
	ID           uuid.UUID       `json:"id"`
	CustomerID   uuid.UUID       `json:"customer_id"`
	ContactID    *uuid.UUID      `json:"contact_id"`
	ActivityType ActivityType    `json:"activity_type"`
	Description  string          `json:"description"`
	LoggedBy     *uuid.UUID      `json:"logged_by"`
	ActivityDate httpx.Timestamp `json:"activity_date"`
	Revision     int64           `json:"revision"`
	CreatedAt    httpx.Timestamp `json:"created_at"`
	UpdatedAt    httpx.Timestamp `json:"updated_at"`
}

// ErrNotFound reports that a read or write named an activity that is not
// there (or is behind the caller's branch wall); the handler answers 404.
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "activity not found" }
