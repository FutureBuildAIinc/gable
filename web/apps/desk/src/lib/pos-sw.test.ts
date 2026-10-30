// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it, vi } from 'vitest';
import {
  POS_SW_RELOAD_WARNING,
  POS_SW_SCOPE,
  POS_SW_SCRIPT_URL,
   isPosPrecacheEligiblePath,
  registerPosServiceWorker,
} from './pos-sw.ts';

function fakeWindow(options: {
  framed: boolean;
  search?: string;
  parentPathname?: string;
  selfOrigin?: string;
  parentOrigin?: string;
}): Window {
  const selfOrigin = options.selfOrigin ?? 'https://gable.yard.internal';
  const parentOrigin = options.parentOrigin ?? selfOrigin;
  const selfWindow: Record<string, unknown> = {
    location: {
      origin: selfOrigin,
      search: options.search ?? (options.framed ? '?gable-framed=work-shell' : ''),
      pathname: '/pos',
    },
  };
  if (!options.framed) {
    selfWindow.parent = selfWindow;
    return selfWindow as unknown as Window;
  }
  selfWindow.parent = {
    location: {
      get origin(): string {
        if (parentOrigin !== selfOrigin) {
          throw new DOMException('Blocked a frame from accessing a cross-origin frame.', 'SecurityError');
        }
        return parentOrigin;
      },
      pathname: options.parentPathname ?? '/shell/',
    },
  };
  return selfWindow as unknown as Window;
}

describe('POS service worker registration and fallback (ADR 0011 section 5.2)', () => {
  // RULE (ADR 0011 section 5.2 and section 7): the /pos precache worker
  // registers only when `gable-framed=work-shell` is present (`readFramedByShell`),
  // and a desk open in a browser tab installs nothing.
  it('registers /pos-sw.js with scope /pos when framed by the work shell', async () => {
    const register = vi.fn().mockResolvedValue({ scope: 'https://gable.yard.internal/pos' });
    const win = fakeWindow({
      framed: true,
      search: '?gable-framed=work-shell',
      parentPathname: '/shell/',
    });
    const doc = document.implementation.createHTMLDocument('pos');

    const outcome = await registerPosServiceWorker(win, { serviceWorker: { register } }, doc);

    expect(register).toHaveBeenCalledTimes(1);
    expect(register).toHaveBeenCalledWith(POS_SW_SCRIPT_URL, { scope: POS_SW_SCOPE });
    expect(outcome).toEqual({
      state: 'registered',
      warning: null,
    });
    expect(doc.documentElement.hasAttribute('data-pos-offline-reload-warning')).toBe(false);
  });

  it('installs nothing when the desk is open in a browser tab', async () => {
    const register = vi.fn().mockResolvedValue({});
    const topWindow = fakeWindow({ framed: false, search: '?gable-framed=work-shell' });
    const doc = document.implementation.createHTMLDocument('tab');

    const outcome = await registerPosServiceWorker(topWindow, { serviceWorker: { register } }, doc);

    expect(register).not.toHaveBeenCalled();
    expect(outcome).toEqual({
      state: 'not-framed',
      warning: null,
    });
  });

  it('installs nothing when framed without gable-framed=work-shell or across origins', async () => {
    const register = vi.fn().mockResolvedValue({});
    const otherFrame = fakeWindow({
      framed: true,
      search: '?gable-framed=other',
      parentPathname: '/shell/',
    });
    const crossOriginFrame = fakeWindow({
      framed: true,
      search: '?gable-framed=work-shell',
      selfOrigin: 'https://gable.yard.internal',
      parentOrigin: 'https://evil.example',
    });

    const out1 = await registerPosServiceWorker(otherFrame, { serviceWorker: { register } });
    const out2 = await registerPosServiceWorker(crossOriginFrame, { serviceWorker: { register } });

    expect(register).not.toHaveBeenCalled();
    expect(out1.state).toBe('not-framed');
    expect(out2.state).toBe('not-framed');
  });

  it('surfaces the "Do not reload while offline" fallback warning when the webview refuses service workers', async () => {
    const register = vi.fn().mockRejectedValue(new Error('ServiceWorker registration refused by webview'));
    const win = fakeWindow({
      framed: true,
      search: '?gable-framed=work-shell',
      parentPathname: '/shell/',
    });
    const doc = document.implementation.createHTMLDocument('pos');

    const refusedOutcome = await registerPosServiceWorker(win, { serviceWorker: { register } }, doc);
    expect(refusedOutcome).toEqual({
      state: 'fallback-no-reload',
      warning: POS_SW_RELOAD_WARNING,
    });
    expect(doc.documentElement.getAttribute('data-pos-offline-reload-warning')).toBe(
      'Do not reload while offline'
    );

    const missingSwOutcome = await registerPosServiceWorker(win, {}, doc);
    expect(missingSwOutcome).toEqual({
      state: 'fallback-no-reload',
      warning: POS_SW_RELOAD_WARNING,
    });
  });

  it('allows precaching only for /pos and static desk assets and never caches /api/* responses', () => {
    expect(isPosPrecacheEligiblePath('/pos')).toBe(true);
    expect(isPosPrecacheEligiblePath('/pos/')).toBe(true);
    expect(isPosPrecacheEligiblePath('/assets/index-abc123.js')).toBe(true);
    expect(isPosPrecacheEligiblePath('/assets/index-abc123.css')).toBe(true);
    expect(isPosPrecacheEligiblePath('/theme-boot.js')).toBe(true);

    expect(isPosPrecacheEligiblePath('/api/v1/pos/offline/snapshot')).toBe(false);
    expect(isPosPrecacheEligiblePath('/api/v1/pos/transactions')).toBe(false);
    expect(isPosPrecacheEligiblePath('/orders')).toBe(false);
    expect(isPosPrecacheEligiblePath('/quotes')).toBe(false);
  });
});
