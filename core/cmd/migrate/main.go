// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Command migrate applies the SQL migrations. Its body lives in
// internal/app/migrate so the one core binary (`core migrate`) and this
// entry point run the same code.
package main

import "github.com/gablelbm/gable/internal/app/migrate"

func main() {
	migrate.Run()
}
