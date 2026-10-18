// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The public surface of @gable/shell-chrome: the pure shell state (the rail's
// posture and modes, the app catalog model, the banner's words) and the
// chrome elements the work shell mounts (header, rail, banner). The shell
// wires the elements to the auth custody and to its own state; this package
// never reaches for a platform global itself.

export * from './shell-state.ts';
export * from './shell-banner.ts';
export { GableShellHeader, type ShellYard } from './shell-header.ts';
export { GableShellRail } from './shell-rail.ts';
