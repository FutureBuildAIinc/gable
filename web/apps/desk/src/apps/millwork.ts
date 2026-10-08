// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Millwork app manifest (reference conversion #1).
 * Backend counterpart: internal/millwork/manifest.go — the app owns the
 * millwork + configurator backend modules.
 *
 * All paths are prefixed /app/ because the desk bundle is mounted at /app/
 * in the combined nginx layout.
 */
import { Hammer } from 'lucide';
import type { FrontendAppManifest } from './types.ts';

export const millworkApp: FrontendAppManifest = {
  key: 'millwork',
  name: 'Millwork',
  routes: [
    { path: '/app/millwork/configure', tag: 'gable-door-configurator', load: () => import('../pages/millwork/DoorConfigurator.ts'), layout: 'erp' },
    { path: '/app/millwork/configurator', tag: 'gable-product-configurator', load: () => import('../pages/millwork/ProductConfigurator.ts'), layout: 'erp' },
    { path: '/app/millwork/blueprint', tag: 'gable-blueprint-verifier', load: () => import('../pages/millwork/BlueprintVerifier.ts'), layout: 'erp' },
  ],
  nav: [
    { label: 'Product Configurator', path: '/app/millwork/configurator', icon: Hammer, order: 10 },
    { label: 'Door Configurator', path: '/app/millwork/configure', icon: Hammer, order: 20 },
    { label: 'Blueprint Verifier', path: '/app/millwork/blueprint', icon: Hammer, order: 30 },
  ],
};
