// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * @gable/design-system — Gable's shared tokens and the components every
 * frontend bundle renders (the desk and the front door). The Tailwind theme
 * lives at '@gable/design-system/tailwind' and the CSS variables at
 * './tokens.css'; both apps import them so nothing is redrawn.
 *
 * Importing this module registers the shared custom elements (the gable-
 * prefix rule holds across packages).
 */

export { cn } from './cn.ts';
export { GableBrandLogo } from './brand-logo.ts';
