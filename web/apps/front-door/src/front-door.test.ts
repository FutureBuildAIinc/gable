// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The door element's session handling: a 401 on the catalog means the
 * session is gone, so the door runs the sign out reset and shows the sign in
 * card with a "Your session expired" line (retrying could only 401 again).
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { authConfig, authCustody } from '@gable/auth';
import './front-door.ts';
import { TILE_ICONS, type GableFrontDoor } from './front-door.ts';
import { DESK_ENTRIES } from './apps-catalog.ts';

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
