// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The desk's framed mode, framed half: bootFramedMode(win) with a fake framed
 * window proves the controls announcement, the title feed on route changes,
 * the chrome action and yard handling (including the boundary refusals: a
 * message whose source is not the parent, and a yard the person was not
 * granted), and the sign out handshake wiring. The standalone half lives in
 * framed.test.ts.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  CHROME_ACTION_MESSAGE,
  CHROME_CONTROLS_MESSAGE,
  CHROME_TITLE_MESSAGE,
  SIGNED_OUT_MESSAGE,
  SIGN_OUT_REQUEST_MESSAGE,
  YARD_CHANGED_MESSAGE,
  currentTheme,
} from '@gable/design-system';
import { authCustody } from '@gable/auth';
import { bootFramedMode } from './framed.ts';
import { router } from './router.ts';
import { branchContext } from '../services/BranchContext.ts';

function framedWindow() {
  const posted: { message: unknown; targetOrigin: string }[] = [];
  const listeners = new Set<(event: MessageEvent) => unknown>();
  const parent = {
    location: { origin: window.location.origin, pathname: '/shell/' },
    postMessage(message: unknown, targetOrigin: string) {
      posted.push({ message, targetOrigin });
    },
  };
  const win = {
    location: { search: '?gable-framed=work-shell', origin: window.location.origin },
    parent,
    posted,
    addEventListener: (_t: 'message', l: (event: MessageEvent) => unknown) => listeners.add(l),
    removeEventListener: (_t: 'message', l: (event: MessageEvent) => unknown) => listeners.delete(l),
    fire(data: unknown, source: unknown = parent, origin: string = window.location.origin) {
      const event = new MessageEvent('message', { data, origin });
      Object.defineProperty(event, 'source', { value: source });
      for (const l of [...listeners]) void l(event);
    },
  };
  return win;
}

beforeEach(() => {
  document.documentElement.removeAttribute('data-theme');
  window.localStorage.clear();
  window.sessionStorage.clear();
});

describe('bootFramedMode framed', () => {
  it('announces the chrome controls the desk can act on, scoped to its origin', () => {
    const win = framedWindow();
    bootFramedMode(win as never);
    expect(win.posted).toEqual([
      { message: { type: CHROME_CONTROLS_MESSAGE, actions: ['theme'] }, targetOrigin: window.location.origin },
    ]);
  });

  it('posts the desk title up on a route change', () => {
    const win = framedWindow();
    bootFramedMode(win as never);
    router.dispatchEvent(new CustomEvent('route-changed', { detail: { route: { path: '/orders' }, params: {} } }));
    const titles = win.posted.filter((p) => (p.message as { type: string }).type === CHROME_TITLE_MESSAGE);
    expect(titles).toEqual([
      { message: { type: CHROME_TITLE_MESSAGE, title: 'Orders', actions: [] }, targetOrigin: window.location.origin },
    ]);
  });

  it('acts on a theme chrome action from the parent, and refuses any other source', () => {
    const win = framedWindow();
    bootFramedMode(win as never);
    expect(currentTheme()).toBe('dark');
    win.fire({ type: CHROME_ACTION_MESSAGE, action: 'theme' }, { location: { origin: window.location.origin } });
    expect(currentTheme()).toBe('dark');
    win.fire({ type: CHROME_ACTION_MESSAGE, action: 'theme' });
    expect(currentTheme()).toBe('light');
  });

  it('follows a granted yard, refuses a yard the person was not granted, and allows all yards', () => {
    const win = framedWindow();
    (branchContext as unknown as { _branches: { id: string }[] })._branches = [
      { id: 'b-1' },
      { id: 'b-2' },
    ];
    const setCurrent = vi.spyOn(branchContext, 'setCurrent').mockImplementation(() => {});
    bootFramedMode(win as never);
    win.fire({ type: YARD_CHANGED_MESSAGE, branchId: 'b-9' });
    expect(setCurrent).not.toHaveBeenCalled();
    win.fire({ type: YARD_CHANGED_MESSAGE, branchId: 'b-2' });
    expect(setCurrent).toHaveBeenCalledWith('b-2');
    win.fire({ type: YARD_CHANGED_MESSAGE, branchId: null });
    expect(setCurrent).toHaveBeenCalledWith(null);
    setCurrent.mockRestore();
  });

  it('a yard message from another source never reaches the branch context', () => {
    const win = framedWindow();
    (branchContext as unknown as { _branches: { id: string }[] })._branches = [{ id: 'b-1' }];
    const setCurrent = vi.spyOn(branchContext, 'setCurrent').mockImplementation(() => {});
    bootFramedMode(win as never);
    win.fire({ type: YARD_CHANGED_MESSAGE, branchId: 'b-1' }, { location: { origin: window.location.origin } });
    expect(setCurrent).not.toHaveBeenCalled();
    setCurrent.mockRestore();
  });

  it('the sign out ask runs the desk custody sign out and answers with the nonce', async () => {
    const win = framedWindow();
    const signOut = vi.spyOn(authCustody, 'signOut').mockImplementation(() => {});
    bootFramedMode(win as never);
    win.fire({ type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'n-5' });
    await vi.waitFor(() => {
      expect(win.posted).toContainEqual({
        message: { type: SIGNED_OUT_MESSAGE, nonce: 'n-5' },
        targetOrigin: window.location.origin,
      });
    });
    expect(signOut).toHaveBeenCalledTimes(1);
    signOut.mockRestore();
  });
});
