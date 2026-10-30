// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The door element's session handling: a 401 on the catalog means the
 * session is gone, so the door runs the sign out reset and shows the sign in
 * card with a "Your session expired" line (retrying could only 401 again).
 * The persona cards on the dev sign in are tested below: one per the
 * DEMO-PERSONAS table, the persona's role shapes the tiles, and the existing
 * "Display name" field sits under a "Sign in with another name" heading.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { authConfig, authCustody, resetAuthMethodsCache } from '@gable/auth';
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
    // The field's accessible name stays "Display name": the e2e suite signs in through it.
    const input = el.querySelector('input[aria-label="Display name"]');
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
  beforeEach(() => {
    localStorage.clear();
    resetAuthMethodsCache();
    // The door asks for the dealer's allowed methods at boot; a failed
    // answer fails closed to email and password.
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 500 })));
  });

  it('renders the staff door with no persona cards and no token paste', async () => {
    authConfig.devMode = false;
    const el = await mount();
    await settle(el);
    expect(el.querySelector('h1')?.textContent).toBe('Sign in to Gable');
    expect(el.querySelector('textarea[aria-label="Bearer token"]')).toBeNull();
    expect(el.querySelector('input[aria-label="Email"]')).not.toBeNull();
    expect(el.querySelector('button[aria-label^="Sign in as"]')).toBeNull();
    expect(el.querySelector('[role="listitem"]')).toBeNull();
  });

  it('does not include the tenant name in the heading', async () => {
    authConfig.devMode = false;
    const el = await mount();
    await settle(el);
    expect(el.querySelector('h1')?.textContent).not.toContain('Building Supply');
  });
});

/**
 * The production door renders the dealer's allowed sign in methods,
 * discovered at runtime from /api/v1/auth/config, in the server's order;
 * a failure fails closed to email and password with the visible notice, and
 * a method the server has not built yet shows its plain refusal inline.
 * The dev sign-in stokes the identity tap with the display block alone.
 */
describe('front door runtime sign in methods (S8)', () => {
  function stubConfig(body: unknown, status = 200) {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (String(url).includes('/api/v1/auth/config')) {
        return new Response(JSON.stringify(body), { status });
      }
      return new Response('{}', { status: 200 });
    }));
  }

  beforeEach(() => {
    localStorage.clear();
    resetAuthMethodsCache();
  });

  it('renders only the allowed methods, in the server\'s order (allowlist one)', async () => {
    authConfig.devMode = false;
    stubConfig({
      auth_mode: 'production',
      methods: [
        { method: 'google', kind: 'redirect', label: 'Google', start_url: '/api/v1/auth/start/google', issuer_url: null, client_id: null },
        { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin', issuer_url: null, client_id: null },
      ],
    });
    const el = await mount();
    await settle(el);
    expect(el.querySelector('h1')?.textContent).toBe('Sign in to Gable');
    // The subtitle speaks to the methods shown.
    expect(el.querySelector('h1 + p')?.textContent).toContain('Sign in with your work account');
    expect(el.querySelector('input[aria-label="Email"]')).not.toBeNull();
    // The form's submit is the card's one primary button, named Sign in.
    const primary = [...el.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Sign in');
    expect(primary).toBeDefined();
    expect(primary?.className).toContain('bg-gable-green');
    // The quiet divider between the form and the other methods.
    expect(el.textContent).toContain('or');
    const methodButtons = [...el.querySelectorAll('button[aria-label^="Sign in with"]')] as HTMLButtonElement[];
    expect(methodButtons.map((b) => b.getAttribute('aria-label'))).toEqual(['Sign in with Google']);
    // Each method button leads with a mark in the icon style.
    expect(methodButtons[0].querySelector('svg')).not.toBeNull();
    // No bearer token paste: a staff session is the core's cookie.
    expect(el.querySelector('h1 + p')?.textContent).not.toContain('bearer token');
    expect(el.querySelector('details')).toBeNull();
  });

  it('renders a different allowlist the same way (allowlist two)', async () => {
    authConfig.devMode = false;
    stubConfig({
      auth_mode: 'production',
      methods: [
        { method: 'oidc', kind: 'redirect', label: 'Dealer SSO', start_url: '/api/v1/auth/start/oidc', issuer_url: 'https://id.example.com', client_id: 'gable' },
        { method: 'passkey', kind: 'passkey', label: 'Passkey', start_url: '/api/v1/auth/start/passkey', issuer_url: null, client_id: null },
        { method: 'magic_link', kind: 'link', label: 'Email a sign in link', start_url: '/api/v1/auth/start/magic_link', issuer_url: null, client_id: null },
        { method: 'microsoft', kind: 'redirect', label: 'Microsoft', start_url: '/api/v1/auth/start/microsoft', issuer_url: null, client_id: null },
      ],
    });
    const el = await mount();
    await settle(el);
    const methodButtons = [...el.querySelectorAll('button[aria-label^="Sign in with"]')] as HTMLButtonElement[];
    expect(methodButtons.map((b) => b.getAttribute('aria-label'))).toEqual([
      'Sign in with Dealer SSO',
      'Sign in with Passkey',
      'Sign in with Email a sign in link',
      'Sign in with Microsoft',
    ]);
    for (const btn of methodButtons) {
      expect(btn.querySelector('svg')).not.toBeNull();
    }
    // With only redirect style methods, the choice leads the subtitle and no
    // divider shows (one kind only).
    expect(el.querySelector('h1 + p')?.textContent).toContain('Choose how you sign in');
    const orDividers = [...el.querySelectorAll('span')].filter((s) => s.textContent?.trim() === 'or');
    expect(orDividers).toHaveLength(0);
    // No email form: the dealer allows no form method.
    expect(el.querySelector('input[aria-label="Email"]')).toBeNull();
    expect(el.querySelector('textarea[aria-label="Bearer token"]')).toBeNull();
  });

  it('fails closed to email and password with a warning notice box and Retry action that re-reads the config', async () => {
    authConfig.devMode = false;
    stubConfig({}, 500);
    const el = await mount();
    await settle(el);
    const alertBox = el.querySelector<HTMLElement>('[role="alert"]');
    expect(alertBox).not.toBeNull();
    expect(alertBox?.textContent).toContain("We could not load this dealer's sign in options, so only email and password is shown.");
    expect(alertBox?.className).toContain('border-amber-300/40');
    expect(alertBox?.className).toContain('bg-amber-300/10');
    expect(el.querySelector('input[aria-label="Email"]')).not.toBeNull();

    const retryBtn = alertBox?.querySelector<HTMLButtonElement>('button');
    expect(retryBtn).not.toBeNull();
    expect(retryBtn?.textContent?.trim()).toBe('Retry');

    stubConfig({
      auth_mode: 'production',
      dealer_name: 'Northridge Lumber',
      dealer_mark_url: null,
      methods: [
        { method: 'google', kind: 'redirect', label: 'Google', start_url: '/api/v1/auth/start/google', issuer_url: null, client_id: null },
      ],
    });
    retryBtn!.click();
    await settle(el);

    expect(el.querySelector('[role="alert"]')).toBeNull();
    expect(el.querySelector('h1')?.textContent).toBe('Sign in to Northridge Lumber');
    expect(el.querySelector('button[aria-label="Sign in with Google"]')).not.toBeNull();
  });

  it('places the error message directly under the control that raised it (form or method button)', async () => {
    authConfig.devMode = false;
    stubConfig({
      auth_mode: 'production',
      methods: [
        { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin', issuer_url: null, client_id: null },
        { method: 'google', kind: 'redirect', label: 'Google', start_url: '/api/v1/auth/start/google', issuer_url: null, client_id: null },
        { method: 'microsoft', kind: 'redirect', label: 'Microsoft', start_url: '/api/v1/auth/start/microsoft', issuer_url: null, client_id: null },
      ],
    });
    const el = await mount();
    await settle(el);

    // 1. Form error renders inside the form block, above the Google button.
    const signInBtn = [...el.querySelectorAll<HTMLButtonElement>('button')].find(
      (b) => b.textContent?.trim() === 'Sign in',
    )!;
    const googleBtn = el.querySelector<HTMLButtonElement>('button[aria-label="Sign in with Google"]')!;
    const microsoftBtn = el.querySelector<HTMLButtonElement>('button[aria-label="Sign in with Microsoft"]')!;

    signInBtn.click();
    await settle(el);

    let alertEl = el.querySelector<HTMLElement>('[role="alert"]')!;
    expect(alertEl).not.toBeNull();
    expect(alertEl.textContent).toContain('Enter your email and password.');
    expect(signInBtn.compareDocumentPosition(alertEl) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(alertEl.compareDocumentPosition(googleBtn) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    // 2. Method start error renders directly under the clicked Google button, above Microsoft.
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ error: { code: 'not_implemented', message: 'sign in by google is not available on this server yet' } }),
      { status: 501 },
    )));
    googleBtn.click();
    await settle(el);

    alertEl = el.querySelector<HTMLElement>('[role="alert"]')!;
    expect(alertEl).not.toBeNull();
    expect(alertEl.textContent).toContain('not available on this server yet');
    expect(googleBtn.compareDocumentPosition(alertEl) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(alertEl.compareDocumentPosition(microsoftBtn) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    // 3. A refused staff sign in shows the core's plain refusal under the form.
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ error: { code: 'unauthorized', message: 'sign in failed' } }),
      { status: 401 },
    )));
    const email = el.querySelector<HTMLInputElement>('input[aria-label="Email"]')!;
    const password = el.querySelector<HTMLInputElement>('input[aria-label="Password"]')!;
    email.value = 'till@example.test';
    email.dispatchEvent(new Event('input'));
    password.value = 'not-the-password';
    password.dispatchEvent(new Event('input'));
    signInBtn.click();
    await settle(el);
    alertEl = el.querySelector<HTMLElement>('[role="alert"]')!;
    expect(alertEl.textContent).toContain('not accepted');
    expect(signInBtn.compareDocumentPosition(alertEl) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(authCustody.session).toBeNull();
  });

  it('a dev persona sign in stokes the identity tap and writes no token anywhere', async () => {
    authConfig.devMode = true;
    stubConfig({});
    const el = await mount();
    const ownerCard = [...el.querySelectorAll('button[aria-label^="Sign in as"]')].find(
      (b) => b.textContent?.includes('Morgan Hale'),
    ) as HTMLButtonElement;
    ownerCard.click();
    await settle(el);
    const tap = localStorage.getItem('gable.identity-tap.v1');
    expect(tap).not.toBeNull();
    expect(tap).toContain('Morgan Hale');
    const allStorage = [...Object.entries({ ...localStorage })].map(([k, v]) => k + v).join(' ')
      + [...Array(sessionStorage.length).keys()].map((i) => sessionStorage.key(i) + sessionStorage.getItem(sessionStorage.key(i) as string)).join(' ');
    expect(allStorage).not.toContain('eyJ');
    expect(allStorage).not.toContain('token');
  });

  it('renders Continue as <name> as a real button with only the method used, and starts that method on click when still allowed', async () => {
    authConfig.devMode = false;
    localStorage.setItem(
      'gable.identity-tap.v1',
      JSON.stringify({ v: 1, displayName: 'Morgan Hale', role: 'owner', yard: 'KEL-MAIN', method: 'google' }),
    );
    const calls: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      calls.push(String(url));
      if (String(url).includes('/api/v1/auth/config')) {
        return new Response(JSON.stringify({
          auth_mode: 'production',
          methods: [
            { method: 'google', kind: 'redirect', label: 'Google', start_url: '/api/v1/auth/start/google', issuer_url: null, client_id: null },
            { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin', issuer_url: null, client_id: null },
          ],
        }), { status: 200 });
      }
      return new Response(
        JSON.stringify({ error: { code: 'not_implemented', message: 'sign in by google is not available on this server yet' } }),
        { status: 501 },
      );
    }));

    const el = await mount();
    await settle(el);

    const continueBtn = el.querySelector<HTMLButtonElement>('button[aria-label^="Continue as Morgan Hale"]');
    expect(continueBtn).not.toBeNull();
    expect(continueBtn!.getAttribute('type')).toBe('button');
    expect(continueBtn!.className).toContain('rounded-lg');
    expect(continueBtn!.className).toContain('hover:border-gable-green');
    expect(continueBtn!.className).toContain('focus-visible:ring-2');
    expect(continueBtn!.textContent).toContain('Continue as Morgan Hale');
    expect(continueBtn!.textContent).toContain('with Google');
    expect(continueBtn!.textContent).not.toContain('owner');
    expect(continueBtn!.textContent).not.toContain('OWNER');
    expect(continueBtn!.textContent).not.toContain('KEL-MAIN');

    continueBtn!.click();
    await settle(el);
    expect(calls.some((u) => u.includes('/api/v1/auth/start/google'))).toBe(true);
  });

  it('focuses the first allowed method when the Continue as method is no longer allowed', async () => {
    authConfig.devMode = false;
    localStorage.setItem(
      'gable.identity-tap.v1',
      JSON.stringify({ v: 1, displayName: 'Morgan Hale', role: 'owner', yard: 'KEL-MAIN', method: 'google' }),
    );
    stubConfig({
      auth_mode: 'production',
      methods: [
        { method: 'passkey', kind: 'passkey', label: 'Passkey', start_url: '/api/v1/auth/start/passkey', issuer_url: null, client_id: null },
        { method: 'microsoft', kind: 'redirect', label: 'Microsoft', start_url: '/api/v1/auth/start/microsoft', issuer_url: null, client_id: null },
      ],
    });

    const el = await mount();
    await settle(el);

    const continueBtn = el.querySelector<HTMLButtonElement>('button[aria-label^="Continue as Morgan Hale"]')!;
    expect(continueBtn).not.toBeNull();
    continueBtn.click();
    await settle(el);

    const firstMethodBtn = el.querySelector<HTMLButtonElement>('button[aria-label="Sign in with Passkey"]')!;
    expect(document.activeElement).toBe(firstMethodBtn);
  });

  it('white labels the header and card title with the dealer mark or monogram and name when configured, and falls back to Gable when empty', async () => {
    authConfig.devMode = false;
    stubConfig({
      auth_mode: 'production',
      dealer_name: 'Northridge Lumber',
      dealer_mark_url: null,
      methods: [
        { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin', issuer_url: null, client_id: null },
      ],
    });
    const el = await mount();
    await settle(el);

    expect(el.querySelector('h1')?.textContent).toBe('Sign in to Northridge Lumber');
    const header = el.querySelector('header')!;
    expect(header.textContent).toContain('NL');
    expect(header.textContent).toContain('Northridge Lumber');
    expect(header.querySelector('gable-brand-logo')).toBeNull();
    expect(el.querySelector('footer')?.textContent).toContain('Powered by Gable');

    // With a dealer_mark_url, the header renders the img mark instead of the monogram.
    document.body.innerHTML = '';
    resetAuthMethodsCache();
    stubConfig({
      auth_mode: 'production',
      dealer_name: 'Northridge Lumber',
      dealer_mark_url: '/api/v1/dealer-profile/logo',
      methods: [
        { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin', issuer_url: null, client_id: null },
      ],
    });
    const elWithLogo = await mount();
    await settle(elWithLogo);
    const logoImg = elWithLogo.querySelector<HTMLImageElement>('header img');
    expect(logoImg).not.toBeNull();
    expect(logoImg?.getAttribute('src')).toBe('/api/v1/dealer-profile/logo');
  });

  it('applies light-theme solid primary Sign in styling, 44px/16px mobile sizing, and grouped icon+label', async () => {
    authConfig.devMode = false;
    stubConfig({
      auth_mode: 'production',
      methods: [
        { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin', issuer_url: null, client_id: null },
        { method: 'oidc', kind: 'redirect', label: 'Northridge Corporate Single Sign On', start_url: '/api/v1/auth/start/oidc', issuer_url: 'https://id.example.com', client_id: 'gable' },
      ],
    });
    const el = await mount();
    await settle(el);

    // V2-1: Primary Sign in button uses solid deep green fill with paper text in Gable Light.
    const primaryBtn = [...el.querySelectorAll<HTMLButtonElement>('button')].find(
      (b) => b.textContent?.trim() === 'Sign in',
    )!;
    expect(primaryBtn.className).toContain('[[data-theme=light]_&]:bg-gable-green');
    expect(primaryBtn.className).toContain('[[data-theme=light]_&]:text-black');

    // V2-5: Inputs and buttons have 44px minimum height and 16px text on phone viewports.
    const emailInput = el.querySelector<HTMLInputElement>('input[aria-label="Email"]')!;
    const passwordInput = el.querySelector<HTMLInputElement>('input[aria-label="Password"]')!;
    for (const input of [emailInput, passwordInput]) {
      expect(input.className).toContain('min-h-[44px]');
      expect(input.className).toContain('text-base');
      expect(input.className).toContain('sm:text-sm');
    }
    expect(primaryBtn.className).toContain('min-h-[44px]');

    // V3-2: Method button wraps icon and label in an inner inline-flex group.
    const oidcBtn = el.querySelector<HTMLButtonElement>('button[aria-label="Sign in with Northridge Corporate Single Sign On"]')!;
    expect(oidcBtn.className).toContain('min-h-[44px]');
    const innerGroup = oidcBtn.firstElementChild as HTMLElement;
    expect(innerGroup).not.toBeNull();
    expect(innerGroup.className).toContain('inline-flex');
    expect(innerGroup.className).toContain('items-center');
    expect(innerGroup.className).toContain('gap-2.5');
  });
});

/**
 * The front-door console launcher (?console=1): a five-group role picker and
 * "Continue as" from identity-tap, never storing or showing a bearer token,
 * with each role opening its target shell (/shell/, /field/, /portal) matching
 * tauri/core/src/profiles.rs.
 */
describe('front door console launcher (?console=1)', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    resetAuthMethodsCache();
    window.history.replaceState(null, '', '/?console=1');
  });

  afterEach(() => {
    window.history.replaceState(null, '', '/');
  });

  it('renders the five-group role picker in both production and dev mode and never shows a bearer token input', async () => {
    for (const devMode of [false, true]) {
      document.body.innerHTML = '';
      authConfig.devMode = devMode;
      const el = await mount();
      await settle(el);

      const groups = [...el.querySelectorAll<HTMLElement>('[data-testid^="role-group-"]')];
      expect(groups).toHaveLength(5);
      expect(groups.map((g) => g.querySelector('h2')?.textContent?.trim())).toEqual([
        'Counter and Sales',
        'Yard and Delivery',
        'Purchasing and Finance',
        'Leadership and Admin',
        'Contractor Portal',
      ]);

      const cards = [...el.querySelectorAll<HTMLButtonElement>('button[data-testid^="role-card-"]')];
      expect(cards).toHaveLength(9);

      // Never shows a bearer token textarea or disclosure in ?console=1.
      expect(el.querySelector('textarea[aria-label="Bearer token"]')).toBeNull();
      expect(el.textContent?.toLowerCase()).not.toContain('use a bearer token instead');
      expect(el.textContent).not.toMatch(/[\u2013\u2014]/);

      for (const card of cards) {
        expect(card.getAttribute('type')).toBe('button');
        expect(card.className).toContain('min-h-[44px]');
        expect(card.className).toContain('focus-visible:ring-2');
      }
    }
  });

  it('routes each role card to its target shell (/shell/, /field/, or /portal) and records identity-tap without storing a token', async () => {
    const expectedRoutes: ReadonlyArray<readonly [string, string, string]> = [
      ['owner', 'work-shell', '/shell/'],
      ['admin', 'work-shell', '/shell/'],
      ['finance', 'work-shell', '/shell/'],
      ['sales', 'work-shell', '/shell/'],
      ['cashier', 'work-shell', '/shell/'],
      ['purchasing', 'work-shell', '/shell/'],
      ['warehouse', 'field-shell', '/field/'],
      ['driver', 'field-shell', '/field/'],
      ['pro', 'pro-shell', '/portal'],
    ];

    authConfig.devMode = false;
    const el = await mount();
    await settle(el);

    const navigated: string[] = [];
    el.navigateTo = (path: string) => {
      navigated.push(path);
    };

    for (const [role, expectedShell, expectedPath] of expectedRoutes) {
      localStorage.clear();
      sessionStorage.clear();
      navigated.length = 0;

      const card = el.querySelector<HTMLButtonElement>(`button[data-testid="role-card-${role}"]`);
      expect(card, `card for ${role}`).not.toBeNull();
      expect(card?.getAttribute('data-shell')).toBe(expectedShell);
      expect(card?.getAttribute('data-shell-path')).toBe(expectedPath);

      card!.click();
      await settle(el);

      expect(navigated).toEqual([expectedPath]);
      const tap = localStorage.getItem('gable.identity-tap.v1');
      expect(tap).not.toBeNull();
      expect(JSON.parse(tap!)).toMatchObject({ v: 1, role });

      const allStorage =
        [...Object.entries({ ...localStorage })].map(([k, v]) => k + v).join(' ') +
        [...Array(sessionStorage.length).keys()]
          .map((i) => (sessionStorage.key(i) ?? '') + (sessionStorage.getItem(sessionStorage.key(i) as string) ?? ''))
          .join(' ');
      expect(allStorage).not.toContain('eyJ');
      expect(allStorage).not.toContain('token');
    }
  });

  it('renders "Continue as" from identity-tap and navigates to the remembered role shell without a token', async () => {
    authConfig.devMode = false;
    const cases: ReadonlyArray<readonly [string, string, string, string]> = [
      ['Sam Ortiz', 'warehouse', 'Gable Field', '/field/'],
      ['Avery Brooks', 'sales', 'Gable Work', '/shell/'],
      ['Tarnwick Construction', 'pro', 'Gable Pro', '/portal'],
    ];

    for (const [displayName, role, shellLabel, expectedPath] of cases) {
      document.body.innerHTML = '';
      localStorage.setItem(
        'gable.identity-tap.v1',
        JSON.stringify({ v: 1, displayName, role, yard: 'KEL-MAIN' }),
      );

      const el = await mount();
      await settle(el);

      const navigated: string[] = [];
      el.navigateTo = (path: string) => {
        navigated.push(path);
      };

      const continueBtn = el.querySelector<HTMLButtonElement>('button[data-testid="console-continue-as"]');
      expect(continueBtn).not.toBeNull();
      expect(continueBtn?.getAttribute('type')).toBe('button');
      expect(continueBtn?.className).toContain('min-h-[44px]');
      expect(continueBtn?.className).toContain('focus-visible:ring-2');
      expect(continueBtn?.textContent).toContain(`Continue as ${displayName}`);
      expect(continueBtn?.textContent).toContain(shellLabel);
      expect(continueBtn?.textContent).not.toContain('eyJ');

      continueBtn!.click();
      await settle(el);

      expect(navigated).toEqual([expectedPath]);
    }
  });
});


/**
 * The staff sign in on the production door (ADR 0012 slice 3): the form posts
 * to the core, the cookie is the session, custody keeps display data, and
 * sign out revokes at the core. A fresh tab asks the core who the cookie is.
 */
describe('front door staff sign in', () => {
  const SUB = '3f2b8c1e-0d5a-5b7e-9c41-6a1d2e3f4a5b';
  const config = {
    auth_mode: 'production',
    methods: [
      { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin', issuer_url: null, client_id: null },
    ],
  };
  const apps = {
    apps: [
      { key: 'pos', name: 'Counter', summary: '', category: 'Front of House', core: true, enabled: true, depends_on: null },
      { key: 'purchase_order', name: 'Purchasing', summary: '', category: 'Purchasing', core: true, enabled: true, depends_on: null },
    ],
  };
  let calls: { url: string; method: string; csrf: string | null }[];

  function stubCore(opts: { session?: number } = {}) {
    calls = [];
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url);
      const headers = new Headers(init?.headers);
      calls.push({ url: u, method: init?.method ?? 'GET', csrf: headers.get('X-Gable-CSRF') });
      if (u.includes('/api/v1/auth/config')) return new Response(JSON.stringify(config), { status: 200 });
      if (u.endsWith('/api/v1/auth/signin')) return new Response(JSON.stringify({ sub: SUB, roles: ['cashier'] }), { status: 200 });
      if (u.endsWith('/api/v1/auth/signout')) return new Response(null, { status: 204 });
      if (u.endsWith('/api/v1/auth/session')) {
        return opts.session === 200
          ? new Response(JSON.stringify({ sub: SUB, roles: ['cashier'] }), { status: 200 })
          : new Response('{}', { status: 401 });
      }
      if (u.endsWith('/api/v1/apps')) return new Response(JSON.stringify(apps), { status: 200 });
      return new Response('{}', { status: 404 });
    }));
  }

  beforeEach(() => {
    localStorage.clear();
    resetAuthMethodsCache();
    authConfig.devMode = false;
  });

  function fill(el: GableFrontDoor, email: string, password: string) {
    const e = el.querySelector<HTMLInputElement>('input[aria-label="Email"]')!;
    const p = el.querySelector<HTMLInputElement>('input[aria-label="Password"]')!;
    e.value = email;
    e.dispatchEvent(new Event('input'));
    p.value = password;
    p.dispatchEvent(new Event('input'));
  }

  it('signs in through the core, shows only the roles\' tiles, and keeps no password', async () => {
    stubCore();
    const el = await mount();
    await settle(el);
    fill(el, 'till@example.test', 'a-password');
    [...el.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === 'Sign in')!.click();
    await settle(el);

    const signIn = calls.find((c) => c.url.endsWith('/api/v1/auth/signin'))!;
    expect(signIn.method).toBe('POST');
    expect(signIn.csrf).toBe('1');
    expect(authCustody.session?.kind).toBe('staff');
    expect(authCustody.roles).toEqual(['cashier']);
    expect(el.querySelector('h1')?.textContent).toContain('till@example.test');
    // A cashier sees the counter and not purchasing.
    expect(el.querySelector('button[aria-label="Open Counter"]')).not.toBeNull();
    expect(el.querySelector('button[aria-label="Open Purchasing"]')).toBeNull();
    expect(JSON.stringify(sessionStorage)).not.toContain('a-password');
    expect(JSON.stringify(localStorage)).not.toContain('a-password');
  });

  it('Sign out revokes the session at the core and returns to the form', async () => {
    stubCore();
    const el = await mount();
    await settle(el);
    fill(el, 'till@example.test', 'a-password');
    [...el.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === 'Sign in')!.click();
    await settle(el);
    [...el.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === 'Sign out')!.click();
    await settle(el);

    const out = calls.find((c) => c.url.endsWith('/api/v1/auth/signout'))!;
    expect(out.method).toBe('POST');
    expect(out.csrf).toBe('1');
    expect(authCustody.session).toBeNull();
    expect(el.querySelector('input[aria-label="Email"]')).not.toBeNull();
  });

  it('a fresh tab with a live cookie lands on the apps without asking for a password', async () => {
    stubCore({ session: 200 });
    const el = await mount();
    await settle(el);
    expect(calls.some((c) => c.url.endsWith('/api/v1/auth/session'))).toBe(true);
    expect(authCustody.roles).toEqual(['cashier']);
    expect(el.querySelector('button[aria-label="Open Counter"]')).not.toBeNull();
  });

  it('a fresh tab with no cookie shows the form', async () => {
    stubCore({ session: 401 });
    const el = await mount();
    await settle(el);
    expect(authCustody.session).toBeNull();
    expect(el.querySelector('input[aria-label="Email"]')).not.toBeNull();
  });
});
