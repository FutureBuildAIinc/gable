// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CHROME_TITLE_MESSAGE } from '@gable/design-system';
import { authCustody } from '@gable/auth';
import './host-element.ts';
import type { GableWorkShell } from './host-element.ts';
import { signedIn, type HostSession, type HostState } from './host-state.ts';
import { SHELL_APPS } from './config.ts';

const SESSION: HostSession = { name: 'Avery Brooks', roleLabel: 'Sales', roles: ['sales'] };

function setState(el: GableWorkShell, state: HostState): void {
  (el as unknown as { _state: HostState })._state = state;
}

function mount(): GableWorkShell {
  const el = document.createElement('gable-work-shell') as GableWorkShell;
  document.body.appendChild(el);
  return el;
}

function mountDesk(): GableWorkShell {
  const el = mount();
  setState(el, signedIn(SESSION, SHELL_APPS));
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

describe('gable-work-shell', () => {
  it('with no session it is the door, pointing at the front door when dev sign in is off', async () => {
    const el = mount();
    await el.updateComplete;
    const shell = el.querySelector('[data-testid="work-shell"]');
    expect(shell?.getAttribute('data-phase')).toBe('door');
    expect(el.querySelector('a[href="/"]')?.textContent).toContain('front door');
    el.remove();
  });

  it('with a session it frames the landing app under the shell header, with the framed flag on the address', async () => {
    const el = mountDesk();
    await el.updateComplete;
    const shell = el.querySelector('[data-testid="work-shell"]');
    expect(shell?.getAttribute('data-phase')).toBe('desk');
    expect(el.querySelector('gable-shell-header')).not.toBeNull();
    const frame = el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]');
    expect(frame?.getAttribute('src')).toBe('/home?gable-framed=work-shell');
    // Hidden by default: no rail on screen.
    expect(el.querySelector('gable-shell-rail nav')).toBeNull();
    el.remove();
  });

  it('a rail choice event pins the rail and persists the choice', async () => {
    const el = mountDesk();
    await el.updateComplete;
    el.dispatchEvent(new CustomEvent('gable-shell-rail-choice', { detail: { choice: 'icons' }, bubbles: true }));
    await el.updateComplete;
    expect(window.localStorage.getItem('gable-shell.rail.v1')).toBe('icons');
    const rail = el.querySelector('gable-shell-rail');
    expect((rail as unknown as { mode: string }).mode).toBe('icons');
    el.remove();
  });

  it('an open event switches the frame to the app or the place', async () => {
    const el = mountDesk();
    await el.updateComplete;
    el.dispatchEvent(
      new CustomEvent('gable-shell-rail-open', { detail: { appId: 'desk', path: '/orders' }, bubbles: true }),
    );
    await el.updateComplete;
    const frame = el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]');
    expect(frame?.getAttribute('src')).toBe('/orders?gable-framed=work-shell');
    // A rail pick of an app the role does not hold changes nothing.
    el.dispatchEvent(
      new CustomEvent('gable-shell-rail-open', { detail: { appId: 'driver', path: '/driver' }, bubbles: true }),
    );
    await el.updateComplete;
    expect(el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]')?.getAttribute('src')).toBe(
      '/orders?gable-framed=work-shell',
    );
    el.remove();
  });

  it('the chrome feed is accepted only from the frame, and feeds the header', async () => {
    const el = mountDesk();
    await el.updateComplete;
    const frame = el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]');
    const header = el.querySelector('gable-shell-header');

    window.dispatchEvent(message({ type: CHROME_TITLE_MESSAGE, title: 'Forged' }, {}, window.location.origin));
    await el.updateComplete;
    expect(header?.getAttribute('frameTitle') ?? (header as unknown as { frameTitle: string }).frameTitle).not.toBe(
      'Forged',
    );

    window.dispatchEvent(
      message({ type: CHROME_TITLE_MESSAGE, title: 'Orders', actions: [] }, frame?.contentWindow, window.location.origin),
    );
    await el.updateComplete;
    expect((header as unknown as { frameTitle: string }).frameTitle).toBe('Orders');
    el.remove();
  });

  it('sign out with a frame that never answers times out: the shell clears its own custody and says so', async () => {
    vi.useFakeTimers();
    authCustody.signInWithToken(
      `${btoa('{"alg":"none"}')}.${btoa('{"sub":"u-1","exp":4102444800}')}.${btoa('{}')}`,
    );
    const el = mountDesk();
    await el.updateComplete;
    expect(authCustody.session).not.toBeNull();

    el.dispatchEvent(new CustomEvent('gable-shell-sign-out', { bubbles: true }));
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(5000);
    await el.updateComplete;

    expect(authCustody.session).toBeNull();
    const shell = el.querySelector('[data-testid="work-shell"]');
    expect(shell?.getAttribute('data-phase')).toBe('door');
    expect(shell?.getAttribute('data-leave')).toBe('timed-out');
    expect(el.textContent).toContain('did not answer in time');
    el.remove();
  });

  it('a framed app that ended its own walk is not asked again', async () => {
    vi.useFakeTimers();
    const el = mountDesk();
    await el.updateComplete;
    const frame = el.querySelector<HTMLIFrameElement>('iframe[data-testid="app-frame"]');
    window.dispatchEvent(
      message({ type: 'gable-work-shell:switch-role' }, frame?.contentWindow, window.location.origin),
    );
    await vi.advanceTimersByTimeAsync(0);
    await el.updateComplete;
    const shell = el.querySelector('[data-testid="work-shell"]');
    expect(shell?.getAttribute('data-phase')).toBe('door');
    expect(shell?.getAttribute('data-leave')).toBe('ended-by-app');
    el.remove();
  });
});
