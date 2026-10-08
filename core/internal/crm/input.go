// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"encoding/json"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// maxDescription bounds a description: it is prose a person reads, not a
// document body.
const maxDescription = 4000

// Request is the body of POST /customers/{id}/activities and
// PUT /activities/{id}. Fields decode as strings, pointers and raw JSON, and
// Parse turns them into a Draft, collecting every problem into one 400
// (ADR 0001 section 4).
type Request struct {
	ContactID    *string         `json:"contact_id"`
	ActivityType *string         `json:"activity_type"`
	Description  *string         `json:"description"`
	LoggedBy     *string         `json:"logged_by"`
	ActivityDate json.RawMessage `json:"activity_date"`
	CustomerID   json.RawMessage `json:"customer_id"`
	Revision     json.RawMessage `json:"revision"`
}

// Draft is a validated request.
type Draft struct {
	ContactID    *uuid.UUID
	ActivityType ActivityType
	Description  string
	LoggedBy     *uuid.UUID
	ActivityDate *httpx.Timestamp
	Revision     *int64
}

// Parse validates the request in one pass.
func (req *Request) Parse(update bool) (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{}
	if req.ContactID != nil {
		if id, ok := v.UUID("contact_id", req.ContactID, false); ok {
			d.ContactID = &id
		}
	}
	v.Required("activity_type", deref(req.ActivityType))
	if req.ActivityType != nil {
		if t, ok := ParseActivityType(*req.ActivityType); ok {
			d.ActivityType = t
		} else {
			v.Check(false, "activity_type",
				"must be one of: "+strings.Join(ActivityTypeNames(), ", "))
		}
	}
	v.Required("description", deref(req.Description))
	if req.Description != nil {
		desc := strings.TrimSpace(*req.Description)
		v.Check(len(desc) > 0, "description", "is required")
		v.Check(len(desc) <= maxDescription, "description", "must be at most 4000 characters")
		d.Description = desc
	}
	if req.LoggedBy != nil {
		if id, ok := v.UUID("logged_by", req.LoggedBy, false); ok {
			d.LoggedBy = &id
		}
	}
	if req.ActivityDate != nil {
		if ts, ok := v.Timestamp("activity_date", req.ActivityDate, false); ok {
			d.ActivityDate = ts
		}
	}
	// The customer is named by the path on both routes; a body spelling of
	// it is refused rather than silently overriding or dropping (the base
	// overrode it on create and ignored it on update).
	if !isAbsent(req.CustomerID) {
		v.Check(false, "customer_id", "is named by the path, not the body")
	}
	if n, ok := v.Int("revision", req.Revision, false); ok && update {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		d.Revision = &n
	} else if !update {
		v.Check(isAbsent(req.Revision), "revision",
			"is not accepted on create: a new activity has no revision to precondition on")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func isAbsent(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}
