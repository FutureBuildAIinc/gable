// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Registration and webview fallback for the `/pos` precache service worker
 * (ADR 0011 section 5.2).
 *
 * The worker is registered ONLY when the desk runs inside the work shell's
 * frame (`gable-framed=work-shell`, checked via `readFramedByShell` from
 * `@gable/design-system`). A desk open in a browser tab installs nothing.
 * Where a platform webview refuses service workers on the configured origin,
 * registration falls back to keeping `/pos` mounted and surfaces the
 * `"Do not reload while offline"` warning on the document root for the
 * offline strip.
 */

import { readFramedByShell } from '@gable/design-system';

export const POS_SW_SCRIPT_URL = '/pos-sw.js';
export const POS_SW_SCOPE = '/pos';
export const POS_SW_RELOAD_WARNING = 'Do not reload while offline';
export const POS_SW_WARNING_ATTR = 'data-pos-offline-reload-warning';

export type PosServiceWorkerState = 'not-framed' | 'registered' | 'fallback-no-reload';

export interface PosServiceWorkerOutcome {
  state: PosServiceWorkerState;
  warning: string | null;
}

export interface ServiceWorkerContainerLike {
  register: (scriptURL: string, options?: { scope?: string }) => Promise<unknown>;
}

export interface NavigatorWithServiceWorker {
  serviceWorker?: ServiceWorkerContainerLike;
}

/**
 * Returns true only for paths that the `/pos` service worker may cache:
 * the `/pos` route itself and static desk bundle assets (`/assets/*`,
 * `/theme-boot.js`, `/favicon.svg`). Never caches `/api/*` or non-POS routes.
 */
export function isPosPrecacheEligiblePath(pathname: string): boolean {
  if (pathname.startsWith('/api/') || pathname === '/api') {
    return false;
  }
  if (pathname === '/pos' || pathname === '/pos/') {
    return true;
  }
  if (pathname.startsWith('/assets/')) {
    return true;
  }
  return pathname === '/theme-boot.js' || pathname === '/favicon.svg';
}

export async function registerPosServiceWorker(
  win: Window = window,
  nav: NavigatorWithServiceWorker = navigator,
  doc: Document = document
): Promise<PosServiceWorkerOutcome> {
  if (!readFramedByShell(win)) {
    return {
      state: 'not-framed',
      warning: null,
    };
  }

  const sw = nav?.serviceWorker;
  if (!sw || typeof sw.register !== 'function') {
    doc.documentElement.setAttribute(POS_SW_WARNING_ATTR, POS_SW_RELOAD_WARNING);
    return {
      state: 'fallback-no-reload',
      warning: POS_SW_RELOAD_WARNING,
    };
  }

  try {
    await sw.register(POS_SW_SCRIPT_URL, { scope: POS_SW_SCOPE });
    doc.documentElement.removeAttribute(POS_SW_WARNING_ATTR);
    return {
      state: 'registered',
      warning: null,
    };
  } catch {
    doc.documentElement.setAttribute(POS_SW_WARNING_ATTR, POS_SW_RELOAD_WARNING);
    return {
      state: 'fallback-no-reload',
      warning: POS_SW_RELOAD_WARNING,
    };
  }
}
