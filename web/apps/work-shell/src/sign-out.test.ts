// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it, vi } from 'vitest';
import { SIGNED_OUT_MESSAGE, SIGN_OUT_REQUEST_MESSAGE } from '@gable/design-system';
import { requestFrameSignOut } from './sign-out.ts';

const ORIGIN = 'http://localhost';

function rig() {
  const posted: { message: unknown; targetOrigin: string }[] = [];
  const listeners = new Set<(event: MessageEvent) => unknown>();
  const timers = new Map<number, () => void>();
  let timerSeq = 0;
  const contentWindow = {
    postMessage(message: unknown, targetOrigin: string) {
      posted.push({ message, targetOrigin });
    },
  };
  const win = {
    addEventListener: (_t: 'message', l: (event: MessageEvent) => unknown) => listeners.add(l),
    removeEventListener: (_t: 'message', l: (event: MessageEvent) => unknown) => listeners.delete(l),
  };
  const deps = {
    frame: { contentWindow },
    origin: ORIGIN,
    win,
    nonce: () => 'nonce-1',
    setTimer: (cb: () => void) => {
      timerSeq += 1;
      timers.set(timerSeq, cb);
      return timerSeq;
    },
    clearTimer: (t: unknown) => {
      timers.delete(t as number);
    },
    waitMs: 4000,
  };
  const answer = (data: unknown, origin = ORIGIN, source: unknown = contentWindow) => {
    for (const l of [...listeners]) l({ data, origin, source } as MessageEvent);
  };
  const runTimer = () => {
    const cb = timers.get(1);
    if (cb) cb();
  };
  return { deps, posted, answer, runTimer, listeners };
}

describe('requestFrameSignOut', () => {
  it('asks the frame with a nonce, scoped to the host origin', async () => {
    const { deps, posted, answer } = rig();
    const p = requestFrameSignOut(deps);
    expect(posted).toEqual([{ message: { type: SIGN_OUT_REQUEST_MESSAGE, nonce: 'nonce-1' }, targetOrigin: ORIGIN }]);
    answer({ type: SIGNED_OUT_MESSAGE, nonce: 'nonce-1' });
    await expect(p).resolves.toBe('acknowledged');
  });

  it('a wrong nonce, a wrong origin and a wrong source never end the wait; the timer does', async () => {
    const { deps, answer, runTimer, listeners } = rig();
    const p = requestFrameSignOut(deps);
    answer({ type: SIGNED_OUT_MESSAGE, nonce: 'nonce-2' });
    answer({ type: SIGNED_OUT_MESSAGE, nonce: 'nonce-1' }, 'http://evil.example');
    answer({ type: SIGNED_OUT_MESSAGE, nonce: 'nonce-1' }, ORIGIN, {});
    runTimer();
    await expect(p).resolves.toBe('timed-out');
    expect(listeners.size).toBe(0);
  });

  it('no frame answers no-frame at once', async () => {
    const { deps } = rig();
    await expect(requestFrameSignOut({ ...deps, frame: null })).resolves.toBe('no-frame');
    await expect(requestFrameSignOut({ ...deps, frame: { contentWindow: null } })).resolves.toBe('no-frame');
  });

  it('a frame that cannot be messaged still bounds the wait', async () => {
    const { deps, runTimer } = rig();
    const broken = {
      contentWindow: {
        postMessage() {
          throw new Error('gone');
        },
      },
    };
    const p = requestFrameSignOut({ ...deps, frame: broken });
    runTimer();
    await expect(p).resolves.toBe('timed-out');
  });

  it('defaults to a random nonce', async () => {
    const posted: unknown[] = [];
    const listeners = new Set<(event: MessageEvent) => unknown>();
    const contentWindow = { postMessage: (m: unknown) => posted.push(m) };
    vi.useFakeTimers();
    try {
      const p = requestFrameSignOut({
        frame: { contentWindow },
        origin: ORIGIN,
        win: {
          addEventListener: (_t: 'message', l: (event: MessageEvent) => unknown) => listeners.add(l),
          removeEventListener: (_t: 'message', l: (event: MessageEvent) => unknown) => listeners.delete(l),
        },
      });
      const nonce = (posted[0] as { nonce: string }).nonce;
      expect(nonce).toMatch(/^[0-9a-f]{32}$/);
      await vi.advanceTimersByTimeAsync(5000);
      await expect(p).resolves.toBe('timed-out');
    } finally {
      vi.useRealTimers();
    }
  });
});
