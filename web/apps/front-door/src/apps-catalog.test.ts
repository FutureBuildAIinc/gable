// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The tile mapping: the door shows the desk tile pinned first, then every
 * ENABLED catalog app that has an entry path; disabled apps and catalog
 * entries without a UI (platform libraries, service-only apps) render
 * nothing. Enablement stays the backend's answer.
 */
import { describe, it, expect } from 'vitest'
import { tilesFor, loadAppsCatalog, DESK_ENTRIES, type AppInfo } from './apps-catalog'

function app(partial: Partial<AppInfo> & { key: string }): AppInfo {
  return {
    name: partial.key,
    summary: '',
    category: 'Test',
    core: true,
    enabled: true,
    depends_on: null,
    ...partial,
  };
}

describe('tilesFor', () => {
  it('pins the desk tile first', () => {
    const tiles = tilesFor([app({ key: 'quote' })], null);
    expect(tiles[0].key).toBe('desk');
    expect(tiles[0].path).toBe('/home');
  });

  it('maps an enabled catalog app to its desk entry', () => {
    const tiles = tilesFor([app({ key: 'quote', name: 'Quotes', category: 'Sales' })], null);
    expect(tiles).toHaveLength(2);
    expect(tiles[1]).toMatchObject({ key: 'quote', name: 'Quotes', path: '/quotes', category: 'Sales' });
  });

  it('drops a disabled app', () => {
    const tiles = tilesFor([app({ key: 'quote', enabled: false })], null);
    expect(tiles.map((t) => t.key)).toEqual(['desk']);
  });

  it('drops catalog apps the door has no entry for (libraries, service-only)', () => {
    const tiles = tilesFor([app({ key: 'ai' }), app({ key: 'config' }), app({ key: 'vision' })], null);
    expect(tiles.map((t) => t.key)).toEqual(['desk']);
  });

  it('groups the mapped tiles by category in the desk order, catalog order within a category', () => {
    const tiles = tilesFor(
      [
        app({ key: 'order', category: 'Sales', name: 'Orders' }),
        app({ key: 'dashboard', category: 'Front of House', name: 'Dashboard' }),
        app({ key: 'inventory', category: 'Catalog & Inventory', name: 'Inventory' }),
        app({ key: 'quote', category: 'Sales', name: 'Quotes' }),
        app({ key: 'invoice', category: 'Finance', name: 'Invoices' }),
        app({ key: 'reporting', category: 'Front of House', name: 'Reporting' }),
      ],
      null,
    );
    expect(tiles.map((t) => t.key)).toEqual(['desk', 'inventory', 'invoice', 'dashboard', 'reporting', 'order', 'quote']);
  });

  it('every entry path is a real desk route shape (leading slash, no query)', () => {
    for (const path of Object.values(DESK_ENTRIES)) {
      expect(path.startsWith('/')).toBe(true);
      expect(path.includes('?')).toBe(false);
    }
  });
});

describe('tilesFor role filter', () => {
  const catalog = [
    app({ key: 'quote' }),
    app({ key: 'inventory' }),
    app({ key: 'invoice' }),
    app({ key: 'pos' }),
    app({ key: 'techadmin' }),
    app({ key: 'delivery', enabled: false }),
  ];
  const keys = (roles: string[] | null) => tilesFor(catalog, roles).map((t) => t.key);

  it('shows the desk tile for any signed in staff role, even one that admits no app', () => {
    expect(keys(['somebody-else'])).toEqual(['desk']);
    expect(keys([])).toEqual(['desk']);
  });

  it('admits a sales role to quotes and invoices only', () => {
    expect(keys(['sales'])).toEqual(['desk', 'quote', 'invoice']);
  });

  it('admits warehouse to inventory, cashier to the till, finance to invoices', () => {
    expect(keys(['warehouse'])).toEqual(['desk', 'inventory']);
    expect(keys(['cashier'])).toEqual(['desk', 'pos']);
    expect(keys(['finance'])).toEqual(['desk', 'invoice']);
  });

  it('admits admin and owner to every enabled app with an entry', () => {
    const all = ['desk', 'quote', 'inventory', 'invoice', 'pos', 'techadmin'];
    expect(keys(['admin'])).toEqual(all);
    expect(keys(['owner'])).toEqual(all);
  });

  it('unions several roles', () => {
    expect(keys(['warehouse', 'cashier'])).toEqual(['desk', 'inventory', 'pos']);
  });

  it('never shows a disabled app, whatever the role', () => {
    expect(keys(['admin'])).not.toContain('delivery');
  });

  it('treats a dev session (null roles; the core passes dev through) as unrestricted', () => {
    expect(keys(null)).toEqual(['desk', 'quote', 'inventory', 'invoice', 'pos', 'techadmin']);
  });

  it('fails closed for an app the audience table does not name (admin and owner only)', () => {
    const tiles = tilesFor([app({ key: 'vendor' })], ['sales']);
    expect(tiles.map((t) => t.key)).toEqual(['desk']);
  });

  it('lets the catalog name an audience itself, over the door table', () => {
    const custom = [app({ key: 'quote', roles: ['warehouse'] })];
    expect(tilesFor(custom, ['warehouse']).map((t) => t.key)).toEqual(['desk', 'quote']);
    expect(tilesFor(custom, ['sales']).map((t) => t.key)).toEqual(['desk']);
  });
});

describe('loadAppsCatalog', () => {
  it('reads the apps array out of the envelope', async () => {
    const payload = { apps: [app({ key: 'quote' })] };
    const res = new Response(JSON.stringify(payload), { status: 200 });
    const fetchMock = vi.fn().mockResolvedValue(res);
    vi.stubGlobal('fetch', fetchMock);

    const apps = await loadAppsCatalog();
    expect(apps).toHaveLength(1);
    expect(apps[0].key).toBe('quote');
    expect(fetchMock.mock.calls[0][0]).toContain('/api/v1/apps');

    vi.unstubAllGlobals();
  });

  it('throws with the status when the catalog answers badly', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('nope', { status: 503 })));
    await expect(loadAppsCatalog()).rejects.toThrow('503');
    vi.unstubAllGlobals();
  });
});
