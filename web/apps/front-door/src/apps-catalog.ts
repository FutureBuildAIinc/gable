// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The apps catalog behind the front door's tiles.
 *
 * The data is the same catalog the desk's home launcher reads
 * (GET /api/v1/apps, core/pkg/apps): names, categories and enablement come
 * from the backend. This module adds only the DOOR's own routing knowledge:
 * which catalog app opens where. Today every entry opens inside the desk
 * bundle (the desk is the one app that serves them all); when the role
 * micro-apps split out (the manifest's frontends list), this map becomes
 * manifest-driven and the tiles follow.
 *
 * The map is deliberately door-owned: the desk keeps its own launcher for
 * its /home grid, and the two surfaces can grow apart (the door will gain
 * role gating from the manifest; the desk's launcher stays in-app nav).
 */
import { fetchWithAuth } from '@gable/auth';

/** One app's status as returned by GET /api/v1/apps (the desk's AppInfo). */
export interface AppInfo {
  key: string;
  name: string;
  summary: string;
  category: string;
  core: boolean;
  enabled: boolean;
  depends_on: string[] | null;
  orphaned?: boolean;
}

/** One tile on the door: the catalog record plus where it opens. */
export interface DoorTile {
  key: string;
  name: string;
  summary: string;
  category: string;
  /** Where the tile opens, inside the desk bundle today. */
  path: string;
}

/**
 * Catalog key → entry path under the desk bundle's /app/ mount.
 * Keys without an entry (platform libraries, apps whose UI lives inside
 * another app's pages) simply render no tile, exactly like the desk's
 * launcher.
 */
export const DESK_ENTRIES: Readonly<Record<string, string>> = {
  // Catalog & Inventory
  inventory: '/app/inventory',
  location: '/app/admin/branches',
  // Sales
  quote: '/app/quotes',
  order: '/app/orders',
  pricing: '/app/pricing',
  // Finance
  invoice: '/app/invoices',
  gl: '/app/accounting/chart-of-accounts',
  // Purchasing
  purchase_order: '/app/purchasing',
  vendor: '/app/purchasing/vendors',
  // Logistics
  delivery: '/app/dispatch',
  // Front of House
  pos: '/app/pos',
  dashboard: '/app/dashboard',
  reporting: '/app/reports/saved',
  // CRM & People
  customer: '/app/accounts',
  // External Surfaces
  portal: '/app/portal',
  // Converted apps (their manifests live in core/internal/<module>)
  millwork: '/app/millwork/configurator',
  governance: '/app/governance',
  // Platform
  techadmin: '/app/admin',
};

/** The whole-workspace tile: the desk is the one micro-app bundle today. */
export const DESK_HOME_TILE: DoorTile = {
  key: 'desk',
  name: 'Gable Desk',
  summary: 'The full ERP workspace: every module under one roof.',
  category: 'Workspace',
  path: '/app/home',
};

/**
 * The tiles the door shows, in catalog order: the desk tile pinned first,
 * then every ENABLED catalog app that has an entry. Enablement is the
 * backend's answer; the door never gates on roles (the core gates every
 * route; role-shaped tiles arrive with the manifest's frontends list).
 */
export function tilesFor(apps: AppInfo[]): DoorTile[] {
  const tiles = apps
    .filter((a) => a.enabled)
    .filter((a) => Object.hasOwn(DESK_ENTRIES, a.key))
    .map((a) => ({
      key: a.key,
      name: a.name,
      summary: a.summary,
      category: a.category,
      path: DESK_ENTRIES[a.key],
    }));
  return [DESK_HOME_TILE, ...tiles];
}

/** Fetch the catalog. Throws with the status when the backend answers badly. */
export async function loadAppsCatalog(apiUrl = ''): Promise<AppInfo[]> {
  const res = await fetchWithAuth(`${apiUrl}/api/v1/apps`);
  if (!res.ok) throw new Error(`Failed to load apps (${res.status})`);
  const data = await res.json();
  return (data.apps ?? []) as AppInfo[];
}
