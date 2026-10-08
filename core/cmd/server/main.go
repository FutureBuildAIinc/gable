// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Command server runs the HTTP API server. Its body lives in
// internal/app/serve so the one core binary (`core serve`) and this entry
// point run the same code.
package main

import "github.com/gablelbm/gable/internal/app/serve"

func main() {
	serve.Run()
}
