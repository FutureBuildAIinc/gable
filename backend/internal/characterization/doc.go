// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package characterization holds the HTTP characterisation golden harness
// (docs/refactor/GOLDENS.md): it migrates and seeds a throwaway database,
// runs the real cmd/server binary against it, replays a fixed script of
// requests, and compares every response to a golden file under testdata/.
//
// The whole harness lives in _test.go files; this file exists so the package
// builds under `go build ./...`.
package characterization
