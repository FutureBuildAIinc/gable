// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The framed flag: how an app knows the Work shell host is framing it, so it
// shows no header band of its own (one header band, the shell's).
//
// The host adds one query parameter to the address of each app frame it opens
// (FRAMED_PARAM=FRAMED_BY_WORK_SHELL). An app reads it ONCE, at its first
// render, through framedByShell(), and the answer never changes for the life
// of the page. The flag is not a guess from window.parent: it must be present
// and exactly the one value, and it is honoured only when the page really is
// inside a frame whose parent is at the page's own origin. A page opened at
// the top level, or framed by another origin, ignores the parameter and keeps
// its own band, so a link built by an outsider enables nothing. The flag only
// hides chrome: it grants no access, no role and no data.
//
// The host and a framed app share four message pairs, every one scoped to the
// page's own origin and checked both ways:
//
//   chrome controls   the app announces which of the shell's chrome controls
//                     (Theme, Feedback) it can act on; the shell asks it to
//                     act on one.
//   chrome feed       the app posts its title and page actions up, so the
//                     shell's one header can speak for the frame; the shell
//                     posts back the id of an action the person pressed.
//   yard              the shell owns the yard control while framing; a change
//                     is told to the frame so its branch context follows.
//   sign out          the shell asks the framed app to run its OWN sign out
//                     with a nonce it just made; the app answers with the
//                     same nonce once it has. Nothing a sender can put in
//                     either message does more than sign the person out: no
//                     credential travels in them, and a forged answer only
//                     shortens a wait the shell bounds anyway.

/** The query parameter the host adds to an app frame's address. */
export const FRAMED_PARAM = 'gable-framed';

/** The one value that means "framed by the Work shell". */
export const FRAMED_BY_WORK_SHELL = 'work-shell';

/** The message type the host sends a framed app to act on one of the chrome controls it carries. */
export const CHROME_ACTION_MESSAGE = 'gable-work-shell:chrome-action';

/** The message type a framed app sends its parent: the list of the shell's controls it can act on. */
export const CHROME_CONTROLS_MESSAGE = 'gable-work-shell:chrome-controls';

/** The message type a framed app sends its parent: its title and page actions. */
export const CHROME_TITLE_MESSAGE = 'gable-work-shell:title';

/** The message type the host sends a framed app: the id of a frame action the person pressed. */
export const FRAME_ACTION_MESSAGE = 'gable-work-shell:action';

/** The message type the host sends a framed app when the yard changes. */
export const YARD_CHANGED_MESSAGE = 'gable-work-shell:yard-changed';

/** The message type a framed app sends its parent when it ended its own session inside the frame. */
export const SWITCH_ROLE_MESSAGE = 'gable-work-shell:switch-role';

/** The message type the host sends a framed app to ask it to run its own sign out. */
export const SIGN_OUT_REQUEST_MESSAGE = 'gable-work-shell:sign-out';

/** The message type a framed app sends its parent once its own sign out has run. */
export const SIGNED_OUT_MESSAGE = 'gable-work-shell:signed-out';

/** The longest nonce either sign out message may carry. */
export const MAX_SIGN_OUT_NONCE_LENGTH = 64;

const SIGN_OUT_NONCE = /^[A-Za-z0-9_-]+$/;

/** The controls the shell carries for a framed app, by the action names the apps use. */
export type ChromeAction = 'theme' | 'feedback';

const CHROME_ACTIONS: readonly string[] = ['theme', 'feedback'];

/** One action a framed app offers in the shell's header. */
export interface FrameAction {
  id: string;
  label: string;
}

/** The chrome feed a framed app posts up: its title and its page actions. */
export interface ChromeTitleFeed {
  title: string;
  actions: readonly FrameAction[];
}

/** Whether a query string carries the flag: the parameter, exactly once, with exactly the one value. */
export function framedFlagFromSearch(search: string): boolean {
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(search);
  } catch {
    return false;
  }
  const values = params.getAll(FRAMED_PARAM);
  return values.length === 1 && values[0] === FRAMED_BY_WORK_SHELL;
}

/** The slice of a window the framed decision reads. */
export interface FramedWindow {
  readonly location: { readonly search: string; readonly origin: string };
  readonly parent: unknown;
}

/**
 * The framed decision for one window: the flag is present and valid, the page
 * sits inside another window, and that parent is at this page's own origin
 * (reading a cross origin parent's location throws, which reads as not framed).
 * When the parent's path is readable it must be the shell's: another same
 * origin page framing a flagged address is not the work shell.
 */
export function readFramedByShell(win: FramedWindow): boolean {
  if (!framedFlagFromSearch(win.location.search)) return false;
  if (win.parent === (win as unknown)) return false;
  try {
    const parent = win.parent as { location: { origin: string; pathname?: string } };
    if (parent.location.origin !== win.location.origin) return false;
    const parentPath = parent.location.pathname;
    if (
      typeof parentPath === 'string' &&
      parentPath !== '/shell' &&
      !parentPath.startsWith('/shell/') &&
      parentPath !== '/field' &&
      !parentPath.startsWith('/field/')
    ) {
      return false;
    }
    return true;
  } catch {
    return false;
  }
}

let framedAtBoot: boolean | null = null;

/** The decision for this page, read once on first call and kept (false where there is no window). */
export function framedByShell(): boolean {
  if (framedAtBoot !== null) return framedAtBoot;
  if (typeof window === 'undefined' || typeof window.location?.search !== 'string') return false;
  try {
    framedAtBoot = readFramedByShell(window);
  } catch {
    framedAtBoot = false;
  }
  return framedAtBoot;
}

/**
 * The control a message asks a framed app to press, or null: only the exact
 * message type, from this window's own parent, at this page's own origin, and
 * only for a control the shell carries.
 */
export function chromeActionFrom(
  event: { readonly origin: string; readonly source: unknown; readonly data: unknown },
  win: { readonly parent: unknown; readonly location: { readonly origin: string } },
): ChromeAction | null {
  if (win.parent === (win as unknown)) return null;
  if (event.source === null || event.source !== win.parent) return null;
  if (event.origin !== win.location.origin) return null;
  const data = event.data;
  if (data === null || typeof data !== 'object') return null;
  const { type, action } = data as { type?: unknown; action?: unknown };
  if (type !== CHROME_ACTION_MESSAGE) return null;
  return typeof action === 'string' && CHROME_ACTIONS.includes(action) ? (action as ChromeAction) : null;
}

/** A framed app tells its parent which of the shell's controls it can act on, scoped to its own origin. */
export function announceChromeControls(
  win: {
    readonly parent: { postMessage(message: unknown, targetOrigin: string): void };
    readonly location: { readonly origin: string };
  },
  actions: readonly ChromeAction[],
): void {
  win.parent.postMessage({ type: CHROME_CONTROLS_MESSAGE, actions: [...actions] }, win.location.origin);
}

/**
 * The controls a frame's list names, or null: the exact type, from the host's
 * own content frame, at the host's own origin. Names the shell does not carry,
 * and repeats, are dropped.
 */
export function acceptChromeControls(
  event: { readonly origin: string; readonly source: unknown; readonly data: unknown },
  host: { readonly origin: string; readonly contentWindow: unknown },
): ChromeAction[] | null {
  if (host.contentWindow === null || host.contentWindow === undefined) return null;
  if (event.origin !== host.origin || event.source !== host.contentWindow) return null;
  const data = event.data;
  if (data === null || typeof data !== 'object') return null;
  const { type, actions } = data as { type?: unknown; actions?: unknown };
  if (type !== CHROME_CONTROLS_MESSAGE || !Array.isArray(actions)) return null;
  const kept: ChromeAction[] = [];
  for (const name of actions) {
    if (typeof name === 'string' && CHROME_ACTIONS.includes(name) && !kept.includes(name as ChromeAction)) {
      kept.push(name as ChromeAction);
    }
  }
  return kept;
}

/** The address a host gives an app frame: the app's path with the flag added. */
export function framedAddress(path: string): string {
  const hash = path.indexOf('#');
  const head = hash === -1 ? path : path.slice(0, hash);
  const tail = hash === -1 ? '' : path.slice(hash);
  const joiner = head.includes('?') ? '&' : '?';
  return `${head}${joiner}${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}${tail}`;
}

/** The feed a frame's title message carries, or null: the exact type, a real title, sane actions. */
function titleFeedOf(data: unknown): ChromeTitleFeed | null {
  if (data === null || typeof data !== 'object') return null;
  const { title, actions } = data as { title?: unknown; actions?: unknown };
  if (typeof title !== 'string') return null;
  const kept: FrameAction[] = [];
  if (Array.isArray(actions)) {
    for (const entry of actions) {
      if (entry === null || typeof entry !== 'object') continue;
      const { id, label } = entry as { id?: unknown; label?: unknown };
      if (typeof id !== 'string' || id === '' || typeof label !== 'string' || label === '') continue;
      if (kept.some((a) => a.id === id)) continue;
      kept.push({ id, label });
    }
  }
  return { title, actions: kept };
}

/** The host asks the frame to press one of the controls it carries for it (Theme, Feedback); scoped to the host's origin. */
export function announceChromeAction(
  frame: { postMessage(message: unknown, targetOrigin: string): void } | null,
  origin: string,
  action: ChromeAction,
): void {
  frame?.postMessage({ type: CHROME_ACTION_MESSAGE, action }, origin);
}

/** A framed app posts its title and page actions up, scoped to its own origin. */
export function announceChromeTitle(
  win: {
    readonly parent: { postMessage(message: unknown, targetOrigin: string): void };
    readonly location: { readonly origin: string };
  },
  feed: ChromeTitleFeed,
): void {
  win.parent.postMessage(
    { type: CHROME_TITLE_MESSAGE, title: feed.title, actions: feed.actions.map((a) => ({ ...a })) },
    win.location.origin,
  );
}

/**
 * The feed a frame's title message carries, or null: the exact type, from the
 * host's own content frame, at the host's own origin.
 */
export function acceptChromeTitle(
  event: { readonly origin: string; readonly source: unknown; readonly data: unknown },
  host: { readonly origin: string; readonly contentWindow: unknown },
): ChromeTitleFeed | null {
  if (host.contentWindow === null || host.contentWindow === undefined) return null;
  if (event.origin !== host.origin || event.source !== host.contentWindow) return null;
  const data = event.data;
  if (data === null || typeof data !== 'object') return null;
  if ((data as { type?: unknown }).type !== CHROME_TITLE_MESSAGE) return null;
  return titleFeedOf(data);
}

/** The id of a frame action message, or null: the exact type and a real id. */
function frameActionOf(data: unknown): string | null {
  if (data === null || typeof data !== 'object') return null;
  const { type, id } = data as { type?: unknown; id?: unknown };
  if (type !== FRAME_ACTION_MESSAGE || typeof id !== 'string' || id === '') return null;
  return id;
}

/** The host tells the frame which of its own actions the person pressed, scoped to the host's origin. */
export function announceFrameAction(
  frame: { postMessage(message: unknown, targetOrigin: string): void } | null,
  origin: string,
  id: string,
): void {
  frame?.postMessage({ type: FRAME_ACTION_MESSAGE, id }, origin);
}

/** The action id a frame action message carries, or null: the exact type, from this window's own parent, at its origin. */
export function frameActionFrom(
  event: { readonly origin: string; readonly source: unknown; readonly data: unknown },
  win: { readonly parent: unknown; readonly location: { readonly origin: string } },
): string | null {
  if (win.parent === (win as unknown)) return null;
  if (event.source === null || event.source !== win.parent) return null;
  if (event.origin !== win.location.origin) return null;
  return frameActionOf(event.data);
}

/** The branch id a yard message carries, or null: the exact type; null branchId means "all yards". */
function yardOf(data: unknown): string | null | undefined {
  if (data === null || typeof data !== 'object') return undefined;
  const { type, branchId } = data as { type?: unknown; branchId?: unknown };
  if (type !== YARD_CHANGED_MESSAGE) return undefined;
  if (branchId !== null && typeof branchId !== 'string') return undefined;
  return branchId as string | null;
}

/** The host tells the frame the yard changed, scoped to the host's origin. */
export function announceYardChanged(
  frame: { postMessage(message: unknown, targetOrigin: string): void } | null,
  origin: string,
  branchId: string | null,
): void {
  frame?.postMessage({ type: YARD_CHANGED_MESSAGE, branchId }, origin);
}

/** The branch id a yard message carries, or null on a wrong message (and on "all yards": tell null from undefined). */
export function yardChangedFrom(
  event: { readonly origin: string; readonly source: unknown; readonly data: unknown },
  win: { readonly parent: unknown; readonly location: { readonly origin: string } },
): string | null | undefined {
  if (win.parent === (win as unknown)) return undefined;
  if (event.source === null || event.source !== win.parent) return undefined;
  if (event.origin !== win.location.origin) return undefined;
  return yardOf(event.data);
}

/** A nonce the sign out messages accept: a short plain word, never an object, never empty, never long. */
function nonceOf(data: unknown, type: string): string | null {
  if (data === null || typeof data !== 'object') return null;
  const { type: kind, nonce } = data as { type?: unknown; nonce?: unknown };
  if (kind !== type || typeof nonce !== 'string') return null;
  if (nonce.length === 0 || nonce.length > MAX_SIGN_OUT_NONCE_LENGTH || !SIGN_OUT_NONCE.test(nonce)) return null;
  return nonce;
}

/** The host asks the frame to run its own sign out, scoped to the host's origin. */
export function announceSignOut(
  frame: { postMessage(message: unknown, targetOrigin: string): void } | null,
  origin: string,
  nonce: string,
): void {
  frame?.postMessage({ type: SIGN_OUT_REQUEST_MESSAGE, nonce }, origin);
}

/** A framed app tells its parent its own sign out has run, with the nonce of the ask, scoped to its own origin. */
export function announceSignedOut(
  win: {
    readonly parent: { postMessage(message: unknown, targetOrigin: string): void };
    readonly location: { readonly origin: string };
  },
  nonce: string,
): void {
  win.parent.postMessage({ type: SIGNED_OUT_MESSAGE, nonce }, win.location.origin);
}

/** The nonce of a sign out request, or null: the exact type, from this window's own parent, at this page's own origin. */
export function signOutRequestFrom(
  event: { readonly origin: string; readonly source: unknown; readonly data: unknown },
  win: { readonly parent: unknown; readonly location: { readonly origin: string } },
): string | null {
  if (win.parent === (win as unknown)) return null;
  if (event.source === null || event.source !== win.parent) return null;
  if (event.origin !== win.location.origin) return null;
  return nonceOf(event.data, SIGN_OUT_REQUEST_MESSAGE);
}

/** The nonce of a sign out acknowledgement, or null: the exact type, from the host's own content frame, at the host's origin. */
export function signedOutFrom(
  event: { readonly origin: string; readonly source: unknown; readonly data: unknown },
  host: { readonly origin: string; readonly contentWindow: unknown },
): string | null {
  if (host.contentWindow === null || host.contentWindow === undefined) return null;
  if (event.origin !== host.origin || event.source !== host.contentWindow) return null;
  return nonceOf(event.data, SIGNED_OUT_MESSAGE);
}

/**
 * The framed app's one hook for the host's sign out ask. The app passes its
 * OWN existing sign out (no auth code here, none added in the app); each valid
 * ask runs it, and the acknowledgement goes out after it settles, thrown or
 * not (the host must never wait on a broken app). A page that is not framed by
 * the shell listens for nothing. Returns the function that stops listening.
 */
export function listenForHostSignOut(run: () => unknown, win: FramedListenWindow = window): () => void {
  if (!readFramedByShell(win)) return () => {};
  const onMessage = async (event: MessageEvent): Promise<void> => {
    const nonce = signOutRequestFrom(event, win);
    if (nonce === null) return;
    try {
      await run();
    } catch {
      // the app's sign out failed; the host still revokes what it holds
    }
    try {
      announceSignedOut(win, nonce);
    } catch {
      // no parent left to tell
    }
  };
  win.addEventListener('message', onMessage);
  return () => win.removeEventListener('message', onMessage);
}

/** The slice of a window the sign out listener needs. */
export interface FramedListenWindow extends FramedWindow {
  readonly parent: unknown & { postMessage(message: unknown, targetOrigin: string): void };
  addEventListener(type: 'message', listener: (event: MessageEvent) => unknown): void;
  removeEventListener(type: 'message', listener: (event: MessageEvent) => unknown): void;
}

// -- the orphaned frame guard ---------------------------------------------------
//
// A framed app that ends its own walk (its own Sign out) navigates to the
// origin root, which loads the app again INSIDE the frame, standalone: the
// framed flag is gone and a second header band would paint. Such a page asks
// the host to switch role with one message, scoped to its own origin, and the
// host accepts it only from its own content frame.

/** True when this window sits inside another window. */
export function isFramedWindow(win: { readonly parent: unknown }): boolean {
  return win.parent !== (win as unknown);
}

/** Ask the parent host to switch role. The target origin is this window's own. */
export function announceSwitchRole(win: {
  readonly parent: { postMessage(message: unknown, targetOrigin: string): void };
  readonly location: { readonly origin: string };
}): void {
  win.parent.postMessage({ type: SWITCH_ROLE_MESSAGE }, win.location.origin);
}

/**
 * Whether a message event is the content frame's Switch role request: the
 * exact type, from the frame's own window, at the host's own origin.
 */
export function acceptSwitchRole(
  event: { readonly origin: string; readonly source: unknown; readonly data: unknown },
  host: { readonly origin: string; readonly contentWindow: unknown },
): boolean {
  if (host.contentWindow === null || host.contentWindow === undefined) return false;
  if (event.origin !== host.origin) return false;
  if (event.source !== host.contentWindow) return false;
  const data = event.data;
  return data !== null && typeof data === 'object' && (data as { type?: unknown }).type === SWITCH_ROLE_MESSAGE;
}

/**
 * The orphaned frame check for an app's entry: the page sits inside a frame
 * at its own origin but carries no framed flag, so it is a framed app that
 * lost its frame's flag (its own sign out navigated it). Such a page asks the
 * host to switch role and answers true; the caller mounts nothing.
 */
export function orphanedFrame(win: FramedWindow & { readonly parent: { postMessage(message: unknown, targetOrigin: string): void } }): boolean {
  if (!isFramedWindow(win)) return false;
  if (framedFlagFromSearch(win.location.search)) return false;
  try {
    const parent = win.parent as unknown as { location: { origin: string } };
    if (parent.location.origin !== win.location.origin) return false;
  } catch {
    return false;
  }
  try {
    announceSwitchRole(win);
  } catch {
    // no parent left to tell
  }
  return true;
}
