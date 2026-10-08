// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Command seed populates the database with demo data (gated on DEMO_SEED).
// Its body lives in internal/app/seed so the one core binary (`core seed`)
// and this entry point run the same code.
package main

import "github.com/gablelbm/gable/internal/app/seed"

func main() {
	seed.Run()
}
