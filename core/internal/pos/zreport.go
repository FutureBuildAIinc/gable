// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// generateZReport persists the frozen Z snapshot for a just-closed session,
// inside the close's transaction. Idempotent: the unique constraint on
// till_session_id means a re-close (or a retry) never produces a second,
// divergent Z.
func (s *Service) generateZReport(ctx context.Context, report *TillReport) error {
	sess := report.Session
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}
	var overShort httpx.Cents
	if sess.OverShort != nil {
		overShort = *sess.OverShort
	}
	z := &ZReport{
		TillSessionID: sess.ID,
		RegisterID:    sess.RegisterID,
		BranchID:      sess.BranchID,
		OverShort:     overShort,
		Payload:       payload,
	}
	return s.repo.CreateZReport(ctx, z)
}
