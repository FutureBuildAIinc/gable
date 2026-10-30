// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"context"
	"time"
)

// SweepExpired moves SENT quotes past their expires_at to EXPIRED (QE-01-a).
// Stub: the behaviour lands with the expiry rule.
func (s *Service) SweepExpired(ctx context.Context, now time.Time, limit int) (int, error) {
	return 0, nil
}
