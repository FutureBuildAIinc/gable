// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The ask half of Sign out and Switch role: the host asks the framed app to
// run its OWN sign out (its custody and its per person stores are the app's
// to end; the host cannot reach them), and waits for the app's
// acknowledgement for a bounded time. The answer is judged by source, origin
// and nonce, so a frame other than the one asked, or a page guessing, cannot
// end the wait. Whatever comes back, the caller then clears what the HOST
// holds (its own custody): this wait decides nothing about that. A wrong or
// late nonce never signs anything out silently: the host clears its own
// custody and says so.

import { announceSignOut, signedOutFrom } from '@gable/design-system';
import { SIGN_OUT_WAIT_MS } from './config.ts';

export type FrameSignOutOutcome = 'acknowledged' | 'timed-out' | 'no-frame';

export interface FrameSignOutDeps {
  /** The content frame, or null when none is mounted. */
  frame: { contentWindow: { postMessage(message: unknown, targetOrigin: string): void } | null } | null;
  /** The host's own origin: where the ask is addressed and what the answer must carry. */
  origin: string;
  /** The window the answer arrives on. */
  win: {
    addEventListener(type: 'message', listener: (event: MessageEvent) => unknown): void;
    removeEventListener(type: 'message', listener: (event: MessageEvent) => unknown): void;
  };
  /** A fresh nonce per ask; random when omitted. */
  nonce?: () => string;
  setTimer?: (callback: () => void, ms: number) => unknown;
  clearTimer?: (timer: unknown) => void;
  waitMs?: number;
}

function randomNonce(): string {
  const bytes = new Uint8Array(16);
  globalThis.crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

/** Ask the frame to sign out and wait for its answer, at most the bounded time. Never rejects. */
export function requestFrameSignOut(deps: FrameSignOutDeps): Promise<FrameSignOutOutcome> {
  const target = deps.frame?.contentWindow ?? null;
  if (target === null) return Promise.resolve('no-frame');
  const nonce = (deps.nonce ?? randomNonce)();
  const setTimer = deps.setTimer ?? ((callback, ms) => globalThis.setTimeout(callback, ms));
  const clearTimer = deps.clearTimer ?? ((timer) => globalThis.clearTimeout(timer as ReturnType<typeof setTimeout>));
  return new Promise((resolve) => {
    let timer: unknown = null;
    const finish = (outcome: FrameSignOutOutcome): void => {
      deps.win.removeEventListener('message', onMessage);
      if (timer !== null) clearTimer(timer);
      resolve(outcome);
    };
    const onMessage = (event: MessageEvent): void => {
      if (signedOutFrom(event, { origin: deps.origin, contentWindow: target }) === nonce) finish('acknowledged');
    };
    deps.win.addEventListener('message', onMessage);
    timer = setTimer(() => finish('timed-out'), deps.waitMs ?? SIGN_OUT_WAIT_MS);
    try {
      announceSignOut(target, deps.origin, nonce);
    } catch {
      // a frame that cannot be messaged cannot answer: the bounded wait runs out and the host revokes alone
    }
  });
}
