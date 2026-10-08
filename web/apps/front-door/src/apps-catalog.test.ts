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
    const tiles = tilesFor([app({ key: 'quote' })]);
    expect(tiles[0].key).toBe('desk');
    expect(tiles[0].path).toBe('/app/home');
  });

  it('maps an enabled catalog app to its desk entry', () => {
    const tiles = tilesFor([app({ key: 'quote', name: 'Quotes', category: 'Sales' })]);
    expect(tiles).toHaveLength(2);
    expect(tiles[1]).toMatchObject({ key: 'quote', name: 'Quotes', path: '/app/quotes', category: 'Sales' });
  });

  it('drops a disabled app', () => {
    const tiles = tilesFor([app({ key: 'quote', enabled: false })]);
    expect(tiles.map((t) => t.key)).toEqual(['desk']);
  });

  it('drops catalog apps the door has no entry for (libraries, service-only)', () => {
    const tiles = tilesFor([app({ key: 'ai' }), app({ key: 'config' }), app({ key: 'vision' })]);
    expect(tiles.map((t) => t.key)).toEqual(['desk']);
  });

  it('keeps catalog order among the mapped tiles', () => {
    const tiles = tilesFor([app({ key: 'order' }), app({ key: 'inventory' }), app({ key: 'quote' })]);
    expect(tiles.map((t) => t.key)).toEqual(['desk', 'order', 'inventory', 'quote']);
  });

  it('every entry path is a real desk route shape (leading slash, no query)', () => {
    for (const path of Object.values(DESK_ENTRIES)) {
      expect(path.startsWith('/')).toBe(true);
      expect(path.includes('?')).toBe(false);
    }
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
