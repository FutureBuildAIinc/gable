// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

import "time"

// WithClockForTest swaps the service clock; it exists only in test builds.
func (s *Service) WithClockForTest(now func() time.Time) *Service { s.now = now; return s }
