// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"encoding/json"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Request is the body of POST /projects and PUT /projects/{id}. Fields
// decode as strings and raw JSON, and Parse turns them into a Draft,
// collecting every problem into one 400 (ADR 0001 section 4).
type Request struct {
	Name     *string         `json:"name"`
	Status   *string         `json:"status"`
	Revision json.RawMessage `json:"revision"`
}

// Draft is a validated request.
type Draft struct {
	Name     *string
	Status   ProjectStatus
	Revision *int64
}

// Parse validates the request in one pass.
func (req *Request) Parse(update bool) (*Draft, error) {
	v := &httpx.Validator{}
	d := &Draft{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		v.Check(len(name) > 0, "name", "is required")
		v.Check(len(name) <= maxName, "name", "must be at most 255 characters")
		d.Name = &name
	}
	if !update {
		if req.Name == nil {
			v.Required("name", "")
		}
		v.Check(isAbsent(req.Revision), "revision",
			"is not accepted on create: a new project has no revision to precondition on")
	}
	if req.Status != nil {
		if s, ok := ParseProjectStatus(*req.Status); ok {
			d.Status = s
		} else {
			v.Check(false, "status", "must be one of: "+strings.Join(ProjectStatusNames(), ", "))
		}
	}
	if n, ok := v.Int("revision", req.Revision, false); ok && update {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		d.Revision = &n
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func isAbsent(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "" || t == "null"
}
