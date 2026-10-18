// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it, vi } from 'vitest';
import {
  CHROME_ACTION_MESSAGE,
  CHROME_CONTROLS_MESSAGE,
  CHROME_TITLE_MESSAGE,
  FRAME_ACTION_MESSAGE,
  FRAMED_BY_WORK_SHELL,
  FRAMED_PARAM,
  MAX_SIGN_OUT_NONCE_LENGTH,
  SIGNED_OUT_MESSAGE,
  SIGN_OUT_REQUEST_MESSAGE,
  YARD_CHANGED_MESSAGE,
  acceptChromeControls,
  acceptChromeTitle,
  announceChromeControls,
  announceChromeTitle,
  announceFrameAction,
  announceSignOut,
  announceSignedOut,
  announceYardChanged,
  chromeActionFrom,
  frameActionFrom,
  framedAddress,
  framedByShell,
  framedFlagFromSearch,
  listenForHostSignOut,
  readFramedByShell,
  signOutRequestFrom,
  signedOutFrom,
  SWITCH_ROLE_MESSAGE,
  acceptSwitchRole,
  orphanedFrame,
  yardChangedFrom,
  type FramedListenWindow,
} from './framed.ts';

const ORIGIN = 'http://localhost';

function framedWindow() {
  const posted: { message: unknown; targetOrigin: string }[] = [];
  const listeners = new Set<(event: MessageEvent) => unknown>();
  const parent = {
    location: { origin: ORIGIN },
    postMessage(message: unknown, targetOrigin: string) {
      posted.push({ message, targetOrigin });
    },
  };
  const win: FramedListenWindow & { posted: typeof posted; fire(event: Partial<MessageEvent>): void } = {
    location: { search: `?${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`, origin: ORIGIN },
    parent,
    posted,
    addEventListener: (_type, listener) => listeners.add(listener),
    removeEventListener: (_type, listener) => listeners.delete(listener),
    fire(event) {
      for (const listener of [...listeners]) void listener(event as MessageEvent);
    },
  };
  return { win, parent, posted };
}

describe('framedFlagFromSearch', () => {
  it('is true only for the parameter, exactly once, with the one value', () => {
    expect(framedFlagFromSearch(`?${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`)).toBe(true);
    expect(framedFlagFromSearch(`?x=1&${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`)).toBe(true);
    expect(framedFlagFromSearch('')).toBe(false);
    expect(framedFlagFromSearch(`?${FRAMED_PARAM}=other`)).toBe(false);
    expect(framedFlagFromSearch(`?${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}&${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`)).toBe(false);
    expect(framedFlagFromSearch(`?${FRAMED_PARAM}=`)).toBe(false);
  });
});

describe('readFramedByShell', () => {
  it('is true when flagged, framed, and the parent is same origin', () => {
    const { win } = framedWindow();
    expect(readFramedByShell(win)).toBe(true);
  });

  it('is true when the parent is a shell path (/shell/ or /field/), false when the parent is another same origin page', () => {
    const { win } = framedWindow();
    const shellParent = { location: { origin: ORIGIN, pathname: '/shell/' }, postMessage: () => {} };
    expect(readFramedByShell({ ...win, parent: shellParent })).toBe(true);
    const fieldParent = { location: { origin: ORIGIN, pathname: '/field/' }, postMessage: () => {} };
    expect(readFramedByShell({ ...win, parent: fieldParent })).toBe(true);
    const fieldBareParent = { location: { origin: ORIGIN, pathname: '/field' }, postMessage: () => {} };
    expect(readFramedByShell({ ...win, parent: fieldBareParent })).toBe(true);
    const otherParent = { location: { origin: ORIGIN, pathname: '/evil' }, postMessage: () => {} };
    expect(readFramedByShell({ ...win, parent: otherParent })).toBe(false);
  });

  it('is false at the top level even with the flag', () => {
    const win = {
      location: { search: `?${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`, origin: ORIGIN },
      parent: undefined as unknown,
    };
    win.parent = win;
    expect(readFramedByShell(win)).toBe(false);
  });

  it('is false without the flag even when framed same origin', () => {
    const { win } = framedWindow();
    const unflagged = { ...win, location: { search: '', origin: ORIGIN } };
    expect(readFramedByShell(unflagged)).toBe(false);
  });

  it('is false when the parent is cross origin (the read throws)', () => {
    const parent = {};
    Object.defineProperty(parent, 'location', {
      get() {
        throw new Error('cross origin');
      },
    });
    const win = {
      location: { search: `?${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`, origin: ORIGIN },
      parent,
    };
    expect(readFramedByShell(win)).toBe(false);
  });
});

describe('framedByShell', () => {
  it('is false for this top level test window', () => {
    expect(framedByShell()).toBe(false);
  });
});

describe('framedAddress', () => {
  it('adds the flag, keeping an existing query and a hash', () => {
    expect(framedAddress('/home')).toBe(`/home?${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`);
    expect(framedAddress('/home?x=1')).toBe(`/home?x=1&${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`);
    expect(framedAddress('/home#top')).toBe(`/home?${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}#top`);
  });
});

describe('chrome actions and controls', () => {
  it('chromeActionFrom accepts only the parent at the own origin with a known control', () => {
    const { win, parent } = framedWindow();
    const good = { origin: ORIGIN, source: parent, data: { type: CHROME_ACTION_MESSAGE, action: 'theme' } };
    expect(chromeActionFrom(good, win)).toBe('theme');
    expect(chromeActionFrom({ ...good, origin: 'http://evil.example' }, win)).toBeNull();
    expect(chromeActionFrom({ ...good, source: {} }, win)).toBeNull();
    expect(chromeActionFrom({ ...good, source: null }, win)).toBeNull();
    expect(chromeActionFrom({ ...good, data: { type: CHROME_ACTION_MESSAGE, action: 'nuke' } }, win)).toBeNull();
    expect(chromeActionFrom({ ...good, data: { type: 'gable-work-shell:other', action: 'theme' } }, win)).toBeNull();
    expect(chromeActionFrom({ ...good, data: 'theme' }, win)).toBeNull();
  });

  it('announceChromeControls posts the list to the parent at the own origin', () => {
    const { win, posted } = framedWindow();
    announceChromeControls(win, ['theme']);
    expect(posted).toEqual([{ message: { type: CHROME_CONTROLS_MESSAGE, actions: ['theme'] }, targetOrigin: ORIGIN }]);
  });

  it('acceptChromeControls keeps known names once, and refuses other sources', () => {
    const contentWindow = {};
    const host = { origin: ORIGIN, contentWindow };
    const good = {
      origin: ORIGIN,
      source: contentWindow,
      data: { type: CHROME_CONTROLS_MESSAGE, actions: ['theme', 'feedback', 'theme', 'bogus', 7] },
    };
    expect(acceptChromeControls(good, host)).toEqual(['theme', 'feedback']);
    expect(acceptChromeControls({ ...good, origin: 'http://evil.example' }, host)).toBeNull();
    expect(acceptChromeControls({ ...good, source: {} }, host)).toBeNull();
    expect(acceptChromeControls(good, { origin: ORIGIN, contentWindow: null })).toBeNull();
    expect(acceptChromeControls({ ...good, data: { type: CHROME_CONTROLS_MESSAGE, actions: 'theme' } }, host)).toBeNull();
  });
});

describe('the frame boundary readers refuse a top level window', () => {
  // window.parent === window at the top level, so a same window postMessage
  // would satisfy source === parent: every frame side reader refuses it.
  function topLevel() {
    const win: { location: { origin: string; search: string }; parent: unknown } = {
      location: { origin: ORIGIN, search: '' },
      parent: undefined,
    };
    win.parent = win;
    return win;
  }

  it('chromeActionFrom, frameActionFrom, yardChangedFrom and signOutRequestFrom answer null at the top level', () => {
    const win = topLevel();
    const fromSelf = { origin: ORIGIN, source: win };
    expect(chromeActionFrom({ ...fromSelf, data: { type: CHROME_ACTION_MESSAGE, action: 'theme' } }, win)).toBeNull();
    expect(frameActionFrom({ ...fromSelf, data: { type: FRAME_ACTION_MESSAGE, id: 'new' } }, win)).toBeNull();
    expect(yardChangedFrom({ ...fromSelf, data: { type: YARD_CHANGED_MESSAGE, branchId: 'b-1' } }, win)).toBeUndefined();
    expect(signOutRequestFrom({ ...fromSelf, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'n-1' } }, win)).toBeNull();
  });
});

describe('the chrome feed (title and actions)', () => {
  const contentWindow = {};
  const host = { origin: ORIGIN, contentWindow };

  it('round trips a title with actions, dropping malformed or repeated actions', () => {
    const { win, posted } = framedWindow();
    announceChromeTitle(win, {
      title: 'Orders',
      actions: [
        { id: 'new', label: 'New order' },
        { id: 'new', label: 'Duplicate' },
        { id: '', label: 'No id' },
        { id: 'x' } as never,
      ],
    });
    expect(posted[0]?.targetOrigin).toBe(ORIGIN);
    const accepted = acceptChromeTitle(
      { origin: ORIGIN, source: contentWindow, data: (posted[0] as { message: unknown }).message },
      host,
    );
    expect(accepted).toEqual({ title: 'Orders', actions: [{ id: 'new', label: 'New order' }] });
  });

  it('acceptChromeTitle refuses other origins, sources, types and titles', () => {
    const good = { origin: ORIGIN, source: contentWindow, data: { type: CHROME_TITLE_MESSAGE, title: 'Quotes' } };
    expect(acceptChromeTitle(good, host)).toEqual({ title: 'Quotes', actions: [] });
    expect(acceptChromeTitle({ ...good, origin: 'http://evil.example' }, host)).toBeNull();
    expect(acceptChromeTitle({ ...good, source: {} }, host)).toBeNull();
    expect(acceptChromeTitle({ ...good, data: { type: CHROME_TITLE_MESSAGE } }, host)).toBeNull();
    expect(acceptChromeTitle({ ...good, data: { type: 'gable-work-shell:other', title: 'Quotes' } }, host)).toBeNull();
    expect(acceptChromeTitle(good, { origin: ORIGIN, contentWindow: undefined })).toBeNull();
  });

  it('frame actions go shell to frame with the same checks', () => {
    const { win, parent } = framedWindow();
    const sent: { message: unknown; targetOrigin: string }[] = [];
    const frame = { postMessage: (m: unknown, o: string) => sent.push({ message: m, targetOrigin: o }) };
    announceFrameAction(frame, ORIGIN, 'new');
    expect(sent).toEqual([{ message: { type: FRAME_ACTION_MESSAGE, id: 'new' }, targetOrigin: ORIGIN }]);
    const good = { origin: ORIGIN, source: parent, data: { type: FRAME_ACTION_MESSAGE, id: 'new' } };
    expect(frameActionFrom(good, win)).toBe('new');
    expect(frameActionFrom({ ...good, origin: 'http://evil.example' }, win)).toBeNull();
    expect(frameActionFrom({ ...good, source: {} }, win)).toBeNull();
    expect(frameActionFrom({ ...good, data: { type: FRAME_ACTION_MESSAGE, id: '' } }, win)).toBeNull();
  });
});

describe('the yard message', () => {
  it('carries a branch id or null, and refuses everything else', () => {
    const { win, parent } = framedWindow();
    const sent: { message: unknown; targetOrigin: string }[] = [];
    const frame = { postMessage: (m: unknown, o: string) => sent.push({ message: m, targetOrigin: o }) };
    announceYardChanged(frame, ORIGIN, 'b-1');
    expect(sent).toEqual([{ message: { type: YARD_CHANGED_MESSAGE, branchId: 'b-1' }, targetOrigin: ORIGIN }]);
    const good = { origin: ORIGIN, source: parent, data: { type: YARD_CHANGED_MESSAGE, branchId: 'b-1' } };
    expect(yardChangedFrom(good, win)).toBe('b-1');
    expect(yardChangedFrom({ ...good, data: { type: YARD_CHANGED_MESSAGE, branchId: null } }, win)).toBeNull();
    expect(yardChangedFrom({ ...good, data: { type: YARD_CHANGED_MESSAGE, branchId: 4 } }, win)).toBeUndefined();
    expect(yardChangedFrom({ ...good, origin: 'http://evil.example' }, win)).toBeUndefined();
    expect(yardChangedFrom({ ...good, source: {} }, win)).toBeUndefined();
    expect(yardChangedFrom({ ...good, data: { type: 'gable-work-shell:other', branchId: 'b-1' } }, win)).toBeUndefined();
  });
});

describe('the switch role guard', () => {
  it('acceptSwitchRole takes only the frame at the host origin with the exact type', () => {
    const contentWindow = {};
    const host = { origin: ORIGIN, contentWindow };
    const good = { origin: ORIGIN, source: contentWindow, data: { type: SWITCH_ROLE_MESSAGE } };
    expect(acceptSwitchRole(good, host)).toBe(true);
    expect(acceptSwitchRole({ ...good, origin: 'http://evil.example' }, host)).toBe(false);
    expect(acceptSwitchRole({ ...good, source: {} }, host)).toBe(false);
    expect(acceptSwitchRole({ ...good, data: { type: 'gable-work-shell:other' } }, host)).toBe(false);
    expect(acceptSwitchRole({ ...good, data: SWITCH_ROLE_MESSAGE }, host)).toBe(false);
    expect(acceptSwitchRole(good, { origin: ORIGIN, contentWindow: null })).toBe(false);
  });

  it('an orphaned frame (framed, same origin, no flag) asks the host to switch role', () => {
    const posted: { message: unknown; targetOrigin: string }[] = [];
    const win = {
      location: { search: '', origin: ORIGIN },
      parent: {
        location: { origin: ORIGIN },
        postMessage: (m: unknown, o: string) => posted.push({ message: m, targetOrigin: o }),
      },
    };
    expect(orphanedFrame(win)).toBe(true);
    expect(posted).toEqual([{ message: { type: SWITCH_ROLE_MESSAGE }, targetOrigin: ORIGIN }]);
  });

  it('a flagged frame, a top level page, and a cross origin parent are not orphaned', () => {
    const posted: unknown[] = [];
    const parent = {
      location: { origin: ORIGIN },
      postMessage: (m: unknown) => posted.push(m),
    };
    const flagged = {
      location: { search: `?${FRAMED_PARAM}=${FRAMED_BY_WORK_SHELL}`, origin: ORIGIN },
      parent,
    };
    expect(orphanedFrame(flagged)).toBe(false);
    const top = { location: { search: '', origin: ORIGIN }, parent: undefined as unknown };
    top.parent = top;
    expect(orphanedFrame(top as never)).toBe(false);
    const foreign = {
      location: { search: '', origin: ORIGIN },
      parent: {
        get location(): never {
          throw new Error('cross origin');
        },
        postMessage: () => {},
      },
    };
    expect(orphanedFrame(foreign as never)).toBe(false);
    expect(posted).toEqual([]);
  });
});

describe('the sign out handshake', () => {
  it('signOutRequestFrom takes only a good nonce from the parent at the own origin', () => {
    const { win, parent } = framedWindow();
    const good = { origin: ORIGIN, source: parent, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'abc-123_X' } };
    expect(signOutRequestFrom(good, win)).toBe('abc-123_X');
    expect(signOutRequestFrom({ ...good, origin: 'http://evil.example' }, win)).toBeNull();
    expect(signOutRequestFrom({ ...good, source: {} }, win)).toBeNull();
    expect(signOutRequestFrom({ ...good, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: '' } }, win)).toBeNull();
    expect(signOutRequestFrom({ ...good, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'x'.repeat(MAX_SIGN_OUT_NONCE_LENGTH + 1) } }, win)).toBeNull();
    expect(signOutRequestFrom({ ...good, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'has spaces' } }, win)).toBeNull();
    expect(signOutRequestFrom({ ...good, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 42 } }, win)).toBeNull();
    expect(signOutRequestFrom({ ...good, data: { type: SIGNED_OUT_MESSAGE, nonce: 'abc' } }, win)).toBeNull();
  });

  it('signedOutFrom takes only the frame at the host origin with a good nonce', () => {
    const contentWindow = {};
    const host = { origin: ORIGIN, contentWindow };
    const good = { origin: ORIGIN, source: contentWindow, data: { type: SIGNED_OUT_MESSAGE, nonce: 'n-1' } };
    expect(signedOutFrom(good, host)).toBe('n-1');
    expect(signedOutFrom({ ...good, origin: 'http://evil.example' }, host)).toBeNull();
    expect(signedOutFrom({ ...good, source: {} }, host)).toBeNull();
    expect(signedOutFrom(good, { origin: ORIGIN, contentWindow: null })).toBeNull();
    expect(signedOutFrom({ ...good, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'n-1' } }, host)).toBeNull();
  });

  it('announceSignOut and announceSignedOut post scoped to their own origins', () => {
    const { win, posted } = framedWindow();
    const sent: { message: unknown; targetOrigin: string }[] = [];
    announceSignOut({ postMessage: (m: unknown, o: string) => sent.push({ message: m, targetOrigin: o }) }, ORIGIN, 'n-1');
    expect(sent).toEqual([{ message: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'n-1' }, targetOrigin: ORIGIN }]);
    announceSignedOut(win, 'n-1');
    expect(posted).toEqual([{ message: { type: SIGNED_OUT_MESSAGE, nonce: 'n-1' }, targetOrigin: ORIGIN }]);
    expect(() => announceSignOut(null, ORIGIN, 'n-1')).not.toThrow();
  });

  it('listenForHostSignOut runs the app sign out on a valid ask and answers with the nonce', async () => {
    const { win, parent, posted } = framedWindow();
    const run = vi.fn();
    const stop = listenForHostSignOut(run, win);
    win.fire({ origin: ORIGIN, source: parent, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'n-9' } });
    await vi.waitFor(() => {
      expect(posted).toEqual([{ message: { type: SIGNED_OUT_MESSAGE, nonce: 'n-9' }, targetOrigin: ORIGIN }]);
    });
    expect(run).toHaveBeenCalledTimes(1);
    stop();
  });

  it('listenForHostSignOut ignores wrong origins, sources and nonces', async () => {
    const { win, parent, posted } = framedWindow();
    const run = vi.fn();
    listenForHostSignOut(run, win);
    win.fire({ origin: 'http://evil.example', source: parent, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'n-1' } });
    win.fire({ origin: ORIGIN, source: {}, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'n-1' } });
    win.fire({ origin: ORIGIN, source: parent, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'not a nonce!' } });
    await new Promise((r) => setTimeout(r, 10));
    expect(run).not.toHaveBeenCalled();
    expect(posted).toEqual([]);
  });

  it('listenForHostSignOut answers even when the app sign out throws', async () => {
    const { win, parent, posted } = framedWindow();
    const stop = listenForHostSignOut(() => {
      throw new Error('broken');
    }, win);
    win.fire({ origin: ORIGIN, source: parent, data: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'n-2' } });
    await vi.waitFor(() => {
      expect(posted).toEqual([{ message: { type: SIGNED_OUT_MESSAGE, nonce: 'n-2' }, targetOrigin: ORIGIN }]);
    });
    stop();
  });

  it('listenForHostSignOut listens to nothing when the page is not framed', () => {
    const win = {
      location: { search: '', origin: ORIGIN },
      parent: { postMessage: vi.fn() },
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    } as unknown as FramedListenWindow;
    (win as { parent: unknown }).parent = win;
    const stop = listenForHostSignOut(vi.fn(), win);
    expect(win.addEventListener).not.toHaveBeenCalled();
    expect(stop).toBeInstanceOf(Function);
  });
});
