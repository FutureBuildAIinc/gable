// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The door element's session handling: a 401 on the catalog means the
 * session is gone, so the door runs the sign out reset and shows the sign in
 * card with a "Your session expired" line (retrying could only 401 again).
 * The persona cards on the dev sign in are tested below: one per the
 * DEMO-PERSONAS table, the persona's role shapes the tiles, and the existing
 * "Display name" field becomes "Sign in with another name".
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { authConfig, authCustody } from '@gable/auth';
import './front-door.ts';
import { TILE_ICONS, type GableFrontDoor } from './front-door.ts';
import { DESK_ENTRIES } from './apps-catalog.ts';
import { DEMO_PERSONAS } from './demo-personas.ts';

async function mount(): Promise<GableFrontDoor> {
  const el = document.createElement('gable-front-door') as GableFrontDoor;
  document.body.appendChild(el);
  await el.updateComplete;
  return el;
}

/** Let the catalog promise chain and the re-render settle. */
async function settle(el: GableFrontDoor) {
  for (let i = 0; i < 5; i++) {
    await new Promise((r) => setTimeout(r, 0));
    await el.updateComplete;
  }
}

beforeEach(() => {
  sessionStorage.clear();
  authCustody.signOut();
  authConfig.devMode = true;
});

afterEach(() => {
  document.body.innerHTML = '';
  authConfig.devMode = false;
  authCustody.signOut();
  vi.unstubAllGlobals();
});

describe('front door session expiry', () => {
  it('a 401 on the catalog returns to the sign in card with a session expired line', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 401 })));
    const el = await mount();
    const signIn = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Sign in')!;
    signIn.click();
    await settle(el);

    expect(el.querySelector('h1')?.textContent).toContain('Sign in to Gable');
    expect(el.querySelector('[role="alert"]')?.textContent).toContain('Your session expired');
    expect([...el.querySelectorAll('button')].some((b) => b.textContent?.trim() === 'Sign out')).toBe(false);
    expect(authCustody.session).toBeNull();
  });

  it('a server error keeps the error card with Try again (the session is still held)', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 500 })));
    const el = await mount();
    [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Sign in')!.click();
    await settle(el);

    expect(el.textContent).toContain('The apps catalog did not answer');
    expect([...el.querySelectorAll('button')].some((b) => b.textContent?.trim() === 'Try again')).toBe(true);
    expect(authCustody.session).not.toBeNull();
  });
});

describe('front door tile icons', () => {
  it('every app with an entry has its own icon, and no two apps share one', () => {
    const keys = ['desk', ...Object.keys(DESK_ENTRIES)];
    for (const k of keys) expect(TILE_ICONS[k], `an icon for ${k}`).toBeDefined();
    const icons = keys.map((k) => TILE_ICONS[k]);
    expect(new Set(icons).size).toBe(icons.length);
  });

  it('quotes use the document icon, as the desk does', async () => {
    const { FileText } = await import('lucide');
    expect(TILE_ICONS.quote).toBe(FileText);
  });
});

/**
 * The dev sign-in shows one persona card per persona (DEMO-PERSONAS), each
 * card carries the name, the role in words and the home yard name, and
 * signing in through it stokes the session with the persona's role and home
 * yard so the door filters its tiles with that role.
 */
describe('front door persona cards (dev mode)', () => {
  /** Serve the apps catalog with the four role-discriminating apps enabled. */
  function stubCatalog(apps: Array<Record<string, unknown>>) {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (String(url).includes('/api/v1/apps')) {
        return new Response(JSON.stringify({ apps }), { status: 200 });
      }
      return new Response('{}', { status: 200 });
    }));
  }

  it('renders one card per persona in dev mode, with name, role in words, and home yard', async () => {
    const el = await mount();
    const cards = [...el.querySelectorAll('button[aria-label^="Sign in as"]')];
    expect(cards).toHaveLength(DEMO_PERSONAS.length);
    for (const card of cards) {
      expect(card.tagName.toLowerCase()).toBe('button');
    }
    expect(el.textContent).toContain('Morgan Hale');
    expect(el.textContent).toContain('Owner');
    expect(el.textContent).toContain('Kelowna Main Yard');
    expect(el.textContent).toContain('KEL-MAIN');
    // The yard code is rendered in JetBrains Mono, matching the design
    // system's data font (docs/design-system.md).
    const mono = [...el.querySelectorAll('span')].find((s) => s.className.includes('font-mono'));
    expect(mono?.textContent).toContain('KEL-MAIN');
  });

  it('keeps the free-form dev sign-in as "Sign in with another name" below the cards', async () => {
    const el = await mount();
    const label = [...el.querySelectorAll('span')].find((s) => s.textContent?.trim() === 'Sign in with another name');
    expect(label).toBeDefined();
    const input = el.querySelector('input[aria-label="Sign in with another name"]');
    expect(input).not.toBeNull();
  });

  it('signs in as the persona when a card is clicked: name, role, home yard in custody', async () => {
    stubCatalog([
      { key: 'pos', name: 'Counter', summary: '', category: 'Front of House', core: true, enabled: true, depends_on: null },
      { key: 'purchase_order', name: 'Purchasing', summary: '', category: 'Purchasing', core: true, enabled: true, depends_on: null },
    ]);
    const el = await mount();
    const cashierCard = [...el.querySelectorAll('button[aria-label^="Sign in as"]')].find(
      (b) => b.textContent?.includes('Jordan Pike'),
    ) as HTMLButtonElement;
    expect(cashierCard).toBeDefined();
    cashierCard.click();
    await settle(el);

    expect(authCustody.displayName).toBe('Jordan Pike');
    expect(authCustody.personaRole).toBe('cashier');
    expect(authCustody.personaHomeYardCode).toBe('KEL-MAIN');
  });

  it('the persona role shapes the tiles: a cashier sees the counter, not purchasing', async () => {
    stubCatalog([
      { key: 'pos', name: 'Counter', summary: '', category: 'Front of House', core: true, enabled: true, depends_on: null },
      { key: 'purchase_order', name: 'Purchasing', summary: '', category: 'Purchasing', core: true, enabled: true, depends_on: null },
      { key: 'quote', name: 'Quotes', summary: '', category: 'Sales', core: true, enabled: true, depends_on: null },
    ]);
    const el = await mount();
    const cashierCard = [...el.querySelectorAll('button[aria-label^="Sign in as"]')].find(
      (b) => b.textContent?.includes('Jordan Pike'),
    ) as HTMLButtonElement;
    cashierCard.click();
    await settle(el);

    const tileNames = [...el.querySelectorAll('button')].map((b) => b.textContent?.trim() ?? '');
    // Gable Desk is pinned first; the persona's role admits only the counter.
    expect(tileNames.some((t) => t.includes('Gable Desk'))).toBe(true);
    expect(tileNames.some((t) => t.includes('Counter'))).toBe(true);
    expect(tileNames.some((t) => t.includes('Purchasing'))).toBe(false);
    expect(tileNames.some((t) => t.includes('Quotes'))).toBe(false);
    // The header names the signed in person and renders the role in words.
    expect(el.textContent).toContain('Jordan Pike');
    expect(el.textContent).toContain('Cashier');
  });

  it('owner and admin still see the desk tile (and every enabled app)', async () => {
    stubCatalog([
      { key: 'pos', name: 'Counter', summary: '', category: 'Front of House', core: true, enabled: true, depends_on: null },
      { key: 'purchase_order', name: 'Purchasing', summary: '', category: 'Purchasing', core: true, enabled: true, depends_on: null },
      { key: 'quote', name: 'Quotes', summary: '', category: 'Sales', core: true, enabled: true, depends_on: null },
      { key: 'techadmin', name: 'Tech Admin', summary: '', category: 'Platform', core: true, enabled: true, depends_on: null },
    ]);
    const el = await mount();
    const ownerCard = [...el.querySelectorAll('button[aria-label^="Sign in as"]')].find(
      (b) => b.textContent?.includes('Morgan Hale'),
    ) as HTMLButtonElement;
    ownerCard.click();
    await settle(el);

    const tileNames = [...el.querySelectorAll('button')].map((b) => b.textContent?.trim() ?? '');
    expect(tileNames.some((t) => t.includes('Gable Desk'))).toBe(true);
    expect(tileNames.some((t) => t.includes('Counter'))).toBe(true);
    expect(tileNames.some((t) => t.includes('Purchasing'))).toBe(true);
    expect(tileNames.some((t) => t.includes('Quotes'))).toBe(true);
    expect(tileNames.some((t) => t.includes('Tech Admin'))).toBe(true);
  });

  it('every persona card is reachable by keyboard with a labelled button', async () => {
    const el = await mount();
    const cards = [...el.querySelectorAll('button[aria-label^="Sign in as"]')] as HTMLButtonElement[];
    for (const card of cards) {
      // Visible focus on focus-visible (the browser test runner does not
      // synthesise focus automatically); we assert the styling is in place.
      expect(card.className).toContain('focus-visible:ring-2');
      expect(card.getAttribute('aria-label')).toMatch(/^Sign in as /);
      expect(card.getAttribute('type')).toBe('button');
    }
  });
});

/**
 * The door in a production build (VITE_AUTH_DEV_MODE not set): no persona
 * cards render, the heading is the plain product name, and the wrapper uses
 * the base width. Every dealer's real door will carry their own tenant name,
 * so the production build must not render "Gable Building Supply".
 */
describe('front door production build (no dev mode)', () => {
  it('renders the token door with no persona cards', async () => {
    authConfig.devMode = false;
    const el = await mount();
    expect(el.querySelector('h1')?.textContent).toBe('Sign in to Gable');
    expect(el.querySelector('textarea[aria-label="Bearer token"]')).not.toBeNull();
    expect(el.querySelector('button[aria-label^="Sign in as"]')).toBeNull();
    expect(el.querySelector('[role="listitem"]')).toBeNull();
  });

  it('does not include the tenant name in the heading', async () => {
    authConfig.devMode = false;
    const el = await mount();
    expect(el.querySelector('h1')?.textContent).not.toContain('Building Supply');
  });
});
