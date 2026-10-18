// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The no signal guard. Money never queues: every tender, payment, approval,
 * price change and any other money moving action resolves online, and with no
 * signal it answers one honest state and does nothing else. Nothing is stored
 * for later: a refused write is never queued, never retried and never
 * replayed.
 *
 * The guard sits in front of the transport, so an app's whole write surface is
 * covered by one rule: a write verb with no signal is refused before anything
 * dials, a read is untouched.
 */

/** The words the glass shows, whichever surface and whichever action. */
export const NO_SIGNAL_ERROR =
  'No signal. This needs the server and does not wait in a queue: try again when you are back online.'

/** Whether the device has a connection right now. */
export interface SignalSource {
  online(): boolean
}

/** The platform's own word, read each time it is asked. */
export function browserSignal(): SignalSource {
  return { online: () => (globalThis as { navigator?: { onLine?: boolean } }).navigator?.onLine !== false }
}

/** A signal the caller sets: the tests', and a harness that cuts the network on purpose. */
export function fixedSignal(initial = true): SignalSource & { set(online: boolean): void } {
  let current = initial
  return {
    online: () => current,
    set(online: boolean) {
      current = online
    },
  }
}

type WebFetch = (input: string, init?: RequestInit) => Promise<Response>

const platformWebFetch: WebFetch = (input, init) => globalThis.fetch(input, init)

const READ_VERBS = new Set(['GET', 'HEAD', 'OPTIONS'])

/** The refusal body: one code and one message, in both envelopes' places. */
function refusalBody(): string {
  return JSON.stringify({
    error: { code: 'no_signal', message: NO_SIGNAL_ERROR },
    message: NO_SIGNAL_ERROR,
  })
}

/**
 * The same guard for the platform's fetch. A write verb with no signal is
 * answered before any network call is attempted: HTTP 503, the JSON body
 * above, and the `X-Gable-Host-Refusal` header a caller can use to speak the
 * refusal's words instead of a bare status. A read verb is untouched.
 */
export function noSignalWebFetch(base: WebFetch | undefined, signal: SignalSource): WebFetch {
  const inner = base ?? platformWebFetch
  return async (input: string, init?: RequestInit): Promise<Response> => {
    const method = (init?.method ?? 'GET').toUpperCase()
    if (!READ_VERBS.has(method) && !signal.online()) {
      return new Response(refusalBody(), {
        status: 503,
        headers: { 'Content-Type': 'application/json', 'X-Gable-Host-Refusal': '1' },
      })
    }
    return inner(input, init)
  }
}
