// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CHROME_TITLE_MESSAGE, SIGNED_OUT_MESSAGE, SIGN_OUT_REQUEST_MESSAGE } from '@gable/design-system';
import { authCustody } from '@gable/auth';
import './host-element.ts';
import type { GableFieldShell } from './host-element.ts';
import { signedIn, type HostSession, type HostState } from './host-state.ts';
import { FIELD_APPS } from './config.ts';

const WAREHOUSE_SESSION: HostSession = { name: 'Morgan Lee', roleLabel: 'Warehouse', roles: ['warehouse'] };
const DRIVER_SESSION: HostSession = { name: 'Riley Vance', roleLabel: 'Driver', roles: ['driver'] };
const DUAL_SESSION: HostSession = {
  name: 'Jordan Cross',
  roleLabel: 'Warehouse · Driver',
  roles: ['warehouse', 'driver'],
};

function setState(el: GableFieldShell, state: HostState): void {
  (el as unknown as { _state: HostState })._state = state;
}

function mount(): GableFieldShell {
  const el = document.createElement('gable-field-shell') as GableFieldShell;
  document.body.appendChild(el);
  return el;
}

function mountField(session: HostSession = DUAL_SESSION): GableFieldShell {
  const el = mount();
  setState(el, signedIn(session, FIELD_APPS));
  return el;
}

function message(data: unknown, source: unknown, origin = window.location.origin): MessageEvent {
  const event = new MessageEvent('message', { data, origin });
  Object.defineProperty(event, 'source', { value: source });
  return event;
}

beforeEach(() => {
  document.body.innerHTML = '';
  window.localStorage.clear();
  window.sessionStorage.clear();
});

afterEach(() => {
  document.body.innerHTML = '';
  vi.useRealTimers();
});

describe('gable-field-shell', () => {
  it('with no session it is the door, pointing at the front door when dev sign in is off', async () => {
    const el = mount();
    await el.updateComplete;
    const shell = el.querySelector('[data-testid="field-shell"]');
    expect(shell?.getAttribute('data-phase')).toBe('door');
    expect(el.querySelector('a[href="/"]')?.textContent).toContain('front door');
    el.remove();
  });

  it('frames the role apps with phone header and bottom tabs by role (warehouse, driver, and both)', async () => {
    const wh = mountField(WAREHOUSE_SESSION);
    await wh.updateComplete;
    expect(wh.querySelector('gable-shell-header header')?.getAttribute('data-mode')).toBe('phone');
    expect(Array.from(wh.querySelectorAll('gable-shell-tabs button[data-tab]')).map((b) => b.getAttribute('data-tab'))).toEqual([
      'yard',
    ]);
    expect(wh.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]')?.getAttribute('src')).toBe(
      '/yard?gable-framed=work-shell',
    );
    wh.remove();

    const dr = mountField(DRIVER_SESSION);
    await dr.updateComplete;
    expect(Array.from(dr.querySelectorAll('gable-shell-tabs button[data-tab]')).map((b) => b.getAttribute('data-tab'))).toEqual([
      'driver',
    ]);
    expect(dr.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]')?.getAttribute('src')).toBe(
      '/driver?gable-framed=work-shell',
    );
    dr.remove();

    const dual = mountField(DUAL_SESSION);
    await dual.updateComplete;
    const tabs = dual.querySelectorAll<HTMLButtonElement>('gable-shell-tabs button[data-tab]');
    expect(Array.from(tabs).map((b) => b.getAttribute('data-tab'))).toEqual(['yard', 'driver']);
    tabs[1]?.click();
    await dual.updateComplete;
    expect(dual.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]')?.getAttribute('src')).toBe(
      '/driver?gable-framed=work-shell',
    );
    dual.remove();
  });

  it('states the frame\'s camera allowance: the scanner is the webview\'s own web API', async () => {
    const el = mountField(WAREHOUSE_SESSION);
    await el.updateComplete;
    // The camera feature's default allowlist already permits a same origin
    // frame; the frame states the rule rather than assuming it, so a remote
    // Gable behind the URL override is not silently refused.
    expect(el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]')?.getAttribute('allow')).toBe('camera');
    el.remove();
  });

  it('the yard picker lists granted yards, stores and announces a granted pick, and refuses an ungranted yard', async () => {
    const el = mountField();
    (el as unknown as { _yards: { id: string; code: string; name: string; is_home?: boolean }[] })._yards = [
      { id: 'b-1', code: 'KEL-MAIN', name: 'Kelowna Main Yard', is_home: true },
      { id: 'b-2', code: 'WK', name: 'West Kelowna Satellite' },
    ];
    (el as unknown as { _currentYardId: string | null })._currentYardId = 'b-1';
    await el.updateComplete;
    const header = el.querySelector('gable-shell-header');
    await (header as unknown as { updateComplete: Promise<boolean> }).updateComplete;

    // Ungranted yard is refused.
    el.dispatchEvent(new CustomEvent('gable-shell-yard', { detail: { branchId: 'b-9' }, bubbles: true }));
    await el.updateComplete;
    expect(window.localStorage.getItem('gable_current_branch_id')).toBeNull();

    // Opening the yard picker in the top bar and picking a granted yard stores it.
    const yardBtn = el.querySelector<HTMLButtonElement>('gable-shell-header button[data-action="yards"]');
    expect(yardBtn?.textContent).toContain('Kelowna Main Yard');
    yardBtn?.click();
    await (header as unknown as { updateComplete: Promise<boolean> }).updateComplete;
    const options = el.querySelectorAll<HTMLButtonElement>('gable-shell-header [role="option"]');
    expect(options.length).toBe(2);
    options[1]?.click();
    await el.updateComplete;
    expect(window.localStorage.getItem('gable_current_branch_id')).toBe('b-2');
    el.remove();
  });

  it('the chrome feed is accepted only from the frame, and feeds the header', async () => {
    const el = mountField();
    await el.updateComplete;
    const frame = el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]');
    const header = el.querySelector('gable-shell-header');

    window.dispatchEvent(message({ type: CHROME_TITLE_MESSAGE, title: 'Forged' }, {}, window.location.origin));
    await el.updateComplete;
    expect((header as unknown as { frameTitle: string }).frameTitle).not.toBe('Forged');

    window.dispatchEvent(
      message({ type: CHROME_TITLE_MESSAGE, title: 'Yard', actions: [] }, frame?.contentWindow, window.location.origin),
    );
    await el.updateComplete;
    expect((header as unknown as { frameTitle: string }).frameTitle).toBe('Yard');
    el.remove();
  });

  it('sign out handshake with good nonce acknowledges immediately and clears custody without timeout notice', async () => {
    authCustody.signInWithToken(`${btoa('{"alg":"none"}')}.${btoa('{"sub":"u-1","exp":4102444800}')}.${btoa('{}')}`);
    const el = mountField();
    await el.updateComplete;
    const frame = el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]');
    const frameWin = frame?.contentWindow;
    expect(frameWin).not.toBeNull();

    let askedNonce = '';
    vi.spyOn(frameWin!, 'postMessage').mockImplementation((msg: unknown) => {
      const data = msg as { type?: string; nonce?: string };
      if (data.type === SIGN_OUT_REQUEST_MESSAGE && typeof data.nonce === 'string') {
        askedNonce = data.nonce;
        window.dispatchEvent(
          message({ type: SIGNED_OUT_MESSAGE, nonce: askedNonce }, frameWin, window.location.origin),
        );
      }
    });

    el.dispatchEvent(new CustomEvent('gable-shell-sign-out', { bubbles: true }));
    await vi.waitFor(() => {
      const shell = el.querySelector('[data-testid="field-shell"]');
      expect(shell?.getAttribute('data-phase')).toBe('door');
    });
    const shell = el.querySelector('[data-testid="field-shell"]');
    expect(shell?.getAttribute('data-leave')).toBe('acknowledged');
    expect(authCustody.session).toBeNull();
    expect(el.textContent).not.toContain('did not answer in time');
    el.remove();
  });

  it('sign out handshake ignores a wrong nonce, times out, and ignores a late nonce arriving afterwards', async () => {
    vi.useFakeTimers();
    authCustody.signInWithToken(`${btoa('{"alg":"none"}')}.${btoa('{"sub":"u-1","exp":4102444800}')}.${btoa('{}')}`);
    const el = mountField();
    await el.updateComplete;
    const frame = el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]');
    const frameWin = frame?.contentWindow;

    let askedNonce = '';
    vi.spyOn(frameWin!, 'postMessage').mockImplementation((msg: unknown) => {
      const data = msg as { type?: string; nonce?: string };
      if (data.type === SIGN_OUT_REQUEST_MESSAGE && typeof data.nonce === 'string') {
        askedNonce = data.nonce;
      }
    });

    el.dispatchEvent(new CustomEvent('gable-shell-sign-out', { bubbles: true }));
    await vi.advanceTimersByTimeAsync(0);
    expect(askedNonce).toMatch(/^[0-9a-f]{32}$/);

    // Wrong nonce from the frame does not end the wait.
    window.dispatchEvent(
      message({ type: SIGNED_OUT_MESSAGE, nonce: 'wrong-nonce' }, frameWin, window.location.origin),
    );
    await vi.advanceTimersByTimeAsync(100);
    expect(el.querySelector('[data-testid="field-shell"]')?.getAttribute('data-phase')).toBe('field');

    // Bounded wait expires: host clears custody and says so.
    await vi.advanceTimersByTimeAsync(5000);
    await el.updateComplete;
    expect(authCustody.session).toBeNull();
    const shell = el.querySelector('[data-testid="field-shell"]');
    expect(shell?.getAttribute('data-phase')).toBe('door');
    expect(shell?.getAttribute('data-leave')).toBe('timed-out');
    expect(el.textContent).toContain('did not answer in time');

    // Late nonce arriving after timeout changes nothing.
    window.dispatchEvent(message({ type: SIGNED_OUT_MESSAGE, nonce: askedNonce }, frameWin, window.location.origin));
    await el.updateComplete;
    expect(shell?.getAttribute('data-leave')).toBe('timed-out');
    el.remove();
  });

  it('shows the honest offline banner (money never queues) when offline and hides it when online', async () => {
    const el = mountField();
    await el.updateComplete;
    const banner = el.querySelector('gable-shell-banner');
    await (banner as unknown as { updateComplete: Promise<boolean> }).updateComplete;
    expect(el.querySelector('gable-shell-banner [role="status"]')).toBeNull();

    Object.defineProperty(window.navigator, 'onLine', { value: false, configurable: true });
    window.dispatchEvent(new Event('offline'));
    await el.updateComplete;
    await (banner as unknown as { updateComplete: Promise<boolean> }).updateComplete;
    const strip = el.querySelector('gable-shell-banner [role="status"]');
    expect(strip).not.toBeNull();
    expect(strip?.textContent).toContain('offline');
    expect(strip?.textContent).toContain('money never queues');

    Object.defineProperty(window.navigator, 'onLine', { value: true, configurable: true });
    window.dispatchEvent(new Event('online'));
    await el.updateComplete;
    await (banner as unknown as { updateComplete: Promise<boolean> }).updateComplete;
    expect(el.querySelector('gable-shell-banner [role="status"]')).toBeNull();
    el.remove();
  });
});
