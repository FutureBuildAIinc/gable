// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//go:build linux

package characterization

import "syscall"

// serverProcAttr puts the server subprocess in its own process group and, on
// Linux, asks the kernel to SIGKILL it the moment the test process dies: a
// timed-out `go test` panics without running t.Cleanup, and Pdeathsig is the
// one guarantee that does not depend on the harness's own cleanup running.
func serverProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
