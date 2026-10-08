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
 * its /home grid, and the two surfaces can grow apart (the door filters its
 * tiles by the user's roles; the desk's launcher stays in-app nav).
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
  /** Roles that may open the app, when the catalog names them (not yet sent today). */
  roles?: string[];
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
 * Catalog key → entry path. Keys without an entry (platform libraries,
 * apps whose UI lives inside another app's pages) simply render no tile,
 * exactly like the desk's launcher.
 */
export const DESK_ENTRIES: Readonly<Record<string, string>> = {
  // Catalog & Inventory
  inventory: '/inventory',
  location: '/admin/branches',
  // Sales
  quote: '/quotes',
  order: '/orders',
  pricing: '/pricing',
  // Finance
  invoice: '/invoices',
  gl: '/accounting/chart-of-accounts',
  // Purchasing
  purchase_order: '/purchasing',
  vendor: '/purchasing/vendors',
  // Logistics
  delivery: '/dispatch',
  // Front of House
  pos: '/pos',
  dashboard: '/dashboard',
  reporting: '/reports/saved',
  // CRM & People
  customer: '/accounts',
  // External Surfaces
  portal: '/portal',
  // Converted apps (their manifests live in core/internal/<module>)
  millwork: '/millwork/configurator',
  governance: '/governance',
  // Platform
  techadmin: '/admin',
};

/** The whole-workspace tile: the desk is the one micro-app bundle today. */
export const DESK_HOME_TILE: DoorTile = {
  key: 'desk',
  name: 'Gable Desk',
  summary: 'The full ERP workspace: every module under one roof.',
  category: 'Workspace',
  path: '/home',
};

/**
 * Who may open each app: the roles the core's route guards admit
 * (core/internal/app/serve, middleware.RequireRole), which the catalog does
 * not carry yet. `admin` and `owner` pass every guard, so they are added in
 * code rather than repeated. The core stays the authority (it answers 403 to
 * a role it does not admit); this table only keeps the door from offering a
 * tile the user cannot use. An app missing here is admin and owner only.
 */
const ALWAYS: readonly string[] = ['admin', 'owner'];
export const APP_AUDIENCE: Readonly<Record<string, readonly string[]>> = {
  inventory: ['warehouse'],
  location: ['warehouse', 'sales'],
  quote: ['sales'],
  order: ['sales'],
  pricing: [],
  invoice: ['sales', 'finance'],
  gl: [],
  purchase_order: ['purchasing'],
  vendor: ['purchasing'],
  delivery: ['warehouse', 'driver'],
  pos: ['cashier'],
  dashboard: ['finance'],
  reporting: ['finance'],
  customer: ['sales'],
  portal: ['sales'],
  millwork: ['sales'],
  governance: [],
  techadmin: [],
};

/** Whether `roles` admit `app`. The catalog's own `roles`, when sent, win. */
function admits(app: AppInfo, roles: readonly string[]): boolean {
  const allowed = app.roles ?? [...ALWAYS, ...(APP_AUDIENCE[app.key] ?? [])];
  return roles.some((r) => allowed.includes(r));
}

/**
 * The tiles the door shows, in catalog order: the desk tile pinned first
 * (every signed in staff role gets it), then every ENABLED catalog app that
 * has an entry and that the user's roles admit. Enablement is the backend's
 * answer. `roles` is null for a dev session, which carries no roles because
 * the core passes dev callers through every guard: it sees every tile.
 */
export function tilesFor(apps: AppInfo[], roles: readonly string[] | null): DoorTile[] {
  const tiles = apps
    .filter((a) => a.enabled)
    .filter((a) => Object.hasOwn(DESK_ENTRIES, a.key))
    .filter((a) => roles === null || admits(a, roles))
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
