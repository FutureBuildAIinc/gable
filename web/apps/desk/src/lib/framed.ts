// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The desk's framed mode (the desk half of the single header protocol in
// web/packages/design-system/src/framed.ts). When the work shell frames the
// desk, the desk hides its own header band (app-shell.ts reads
// framedByShell()), tells the shell which chrome controls it can act on
// (Theme), posts its title up on every route change, follows the shell's
// yard picks (a yard the person was not granted is refused), and runs its own
// sign out when the shell asks, answering with the ask's nonce. Standalone,
// nothing here listens and the desk is exactly as it was.

import {
  announceChromeControls,
  announceChromeTitle,
  chromeActionFrom,
  frameActionFrom,
  listenForHostSignOut,
  orphanedFrame,
  readFramedByShell,
  toggleTheme,
  yardChangedFrom,
  type FramedListenWindow,
} from '@gable/design-system';
import { authCustody } from '@gable/auth';
import { router } from './router.ts';
import { branchContext } from '../services/BranchContext.ts';

/** The event a page listens to for its own actions coming back down from the shell's header. */
export const FRAME_ACTION_EVENT = 'gable-work-shell-action';

/** The desk's page titles by route pattern; the first segment in words is the fallback. */
const TITLES: Record<string, string> = {
  '/': 'Home',
  '/home': 'Home',
  '/dashboard': 'Dashboard',
  '/orders': 'Orders',
  '/quotes': 'Quotes',
  '/inventory': 'Inventory',
  '/invoices': 'Invoices',
  '/credit-memos': 'Credit memos',
  '/pos': 'Point of Sale',
  '/dispatch': 'Dispatch',
  '/fleet': 'Fleet',
  '/purchasing': 'Purchasing',
  '/reports/daily-till': 'Daily till',
  '/admin': 'Tech admin',
  '/portal': 'Portal',
  '/driver': 'Driver',
  '/yard': 'Yard',
};

export function titleForPath(path: string): string {
  const known = TITLES[path];
  if (known !== undefined) return known;
  const first = path.split('/').filter(Boolean)[0];
  return first === undefined ? 'Home' : first[0]!.toUpperCase() + first.slice(1);
}

/**
 * The orphaned frame check for the desk's entry: the desk sits inside a frame
 * at its own origin but carries no framed flag, so it is a framed desk whose
 * own sign out navigated it to the origin root. It asks the host to switch
 * role and the caller mounts nothing. True when orphaned.
 */
export function orphanedAtBoot(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    return orphanedFrame(window);
  } catch {
    return false;
  }
}

/**
 * Boot the framed mode. Standalone this is a no-op: no listeners, no
 * messages. The window is a parameter so the framed branch is testable; the
 * app always passes the real one.
 */
export function bootFramedMode(win: FramedListenWindow = window): void {
  if (!readFramedByShell(win)) return;

  // The shell's chrome controls the desk can act on: Theme. There is no
  // feedback surface in the desk, so none is announced.
  try {
    announceChromeControls(win, ['theme']);
  } catch {
    // no parent left to tell
  }

  // The chrome feed: the desk's title goes up on every route change.
  router.addEventListener('route-changed', (e) => {
    const match = (e as CustomEvent).detail;
    const path = match?.route?.path ?? '/';
    try {
      announceChromeTitle(win, { title: titleForPath(path), actions: [] });
    } catch {
      // no parent left to tell
    }
  });

  // The shell's asks: chrome actions, yard picks, page actions, and the sign
  // out handshake. Every reader refuses a message whose source is not the
  // parent (and a top level window outright).
  win.addEventListener('message', (event: MessageEvent) => {
    const action = chromeActionFrom(event, win);
    if (action === 'theme') toggleTheme();
    const yard = yardChangedFrom(event, win);
    if (yard !== undefined) {
      // null is "all yards"; any other id must be one the person was granted.
      if (yard === null || branchContext.branches.some((b) => b.id === yard)) {
        branchContext.setCurrent(yard);
      }
    }
    const frameAction = frameActionFrom(event, win);
    if (frameAction !== null) {
      window.dispatchEvent(new CustomEvent(FRAME_ACTION_EVENT, { detail: { id: frameAction } }));
    }
  });

  // The host's sign out ask runs the desk's OWN sign out (custody and the per
  // person stores are the desk's to end) and answers with the ask's nonce.
  listenForHostSignOut(() => {
    authCustody.signOut();
  }, win);
}
