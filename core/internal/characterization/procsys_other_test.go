// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//go:build !linux

package characterization

import "syscall"

// serverProcAttr puts the server subprocess in its own process group.
// Pdeathsig is Linux-only; elsewhere the group kill in t.Cleanup remains the
// stop mechanism.
func serverProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
