// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Shared fetch wrapper with auth headers, timeout, and retry logic.
 * Drop-in replacement for fetch() — all frontend services should use this.
 *
 * Moved from web/apps/desk/src/services when web/ became one workspace.
 * A staff session is the HttpOnly cookie the core sets at sign in (sent with
 * credentials 'same-origin'); no script holds it. An in-memory bearer from
 * custody, or the legacy out-of-band `token` key in localStorage, is still
 * read for the JWKS path until ADR 0012 slice 8 retires it. Custody never
 * WRITES to localStorage; this read only adopts what an operator or the
 * identity provider put there out of band.
 */

import { NO_SIGNAL_ERROR } from '@gable/work-offline'
import { authCustody } from './custody.ts'
import { BRANCH_STORAGE_KEY } from './branch.ts'
import { CSRF_HEADER } from './staffAuth.ts'

const DEFAULT_TIMEOUT = 10_000; // 10 seconds
const DEFAULT_RETRIES = 1;
const RETRY_DELAY = 2_000; // 2 seconds

/**
 * Fired on `window` when an ERP-surface request comes back 401. The ERP has no
 * in-app login route to redirect to, so the app shell renders a session-expired
 * panel in place instead. The portal keeps its own /portal/login redirect.
 */
export const SESSION_EXPIRED_EVENT = 'gable:session-expired';

export interface FetchWithAuthOptions extends Omit<RequestInit, 'signal'> {
  timeout?: number;
  retries?: number;
  signal?: AbortSignal | null;
  /**
   * Whether the device has a connection right now, read when a write is
   * offered. Defaults to the platform's own `navigator.onLine`; tests and a
   * harness that cuts the network on purpose pass their own.
   */
  onlineSignal?: () => boolean;
}

/** The verbs a read may take: they proceed without a signal, riding the cache. */
const READ_VERBS = new Set(['GET', 'HEAD', 'OPTIONS']);

/**
 * Fetch wrapper that automatically:
 * - Refuses a write verb with no signal before anything dials (money never
 *   queues: the call is not sent, not queued and not retried)
 * - Injects the Bearer token from in-memory custody, falling back to the
 *   legacy out-of-band localStorage token
 * - Applies request timeout via AbortController
 * - Retries on network errors (not on HTTP error status codes, not on a 401,
 *   and not on an abort — whether the caller's or this wrapper's own timeout)
 */
export async function fetchWithAuth(
  url: string,
  options: FetchWithAuthOptions = {}
): Promise<Response> {
  const {
    timeout = DEFAULT_TIMEOUT,
    retries = DEFAULT_RETRIES,
    headers: customHeaders,
    signal: externalSignal,
    onlineSignal,
    ...fetchOpts
  } = options;

  // The no signal guard (money never queues): a write with no signal is
  // refused before the first attempt, so a refused write is never stored,
  // never queued and never replayed. A read is untouched: cached reads keep
  // working offline.
  const method = (fetchOpts.method ?? 'GET').toUpperCase();
  if (!READ_VERBS.has(method) && !(onlineSignal ?? (() => navigator.onLine !== false))()) {
    const refusal = new Error(NO_SIGNAL_ERROR);
    refusal.name = 'NoSignalError';
    throw refusal;
  }

  const headers = new Headers(customHeaders);

  // Inject auth token if not already present. Custody (in-memory, fed by the
  // front door's session handoff) wins; the legacy localStorage token is the
  // out-of-band path the desk has always supported.
  if (!headers.has('Authorization')) {
    const token = authCustody.token ?? localStorage.getItem('token');
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }
  }

  // A write the session cookie authenticates must carry the custom header a
  // cross site form cannot set (ADR 0012 section 3). The core checks it only
  // on the cookie path, so a bearer or machine key request is unaffected.
  if (!READ_VERBS.has(method) && !headers.has(CSRF_HEADER)) {
    headers.set(CSRF_HEADER, '1');
  }

  // Inject the currently selected branch for multi-branch installs.
  // We read straight from localStorage to avoid a circular import with the
  // BranchContext singleton, which itself uses fetchWithAuth at init time.
  if (!headers.has('X-Branch-Id')) {
    const branchId = localStorage.getItem(BRANCH_STORAGE_KEY);
    if (branchId) {
      headers.set('X-Branch-Id', branchId);
    }
  }

  // Ensure Content-Type is set for JSON requests
  if (!headers.has('Content-Type') && fetchOpts.body && typeof fetchOpts.body === 'string') {
    headers.set('Content-Type', 'application/json');
  }

  let lastError: Error | null = null;

  for (let attempt = 0; attempt <= retries; attempt++) {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), timeout);

    // If an external signal is provided, abort when it fires
    if (externalSignal != null) {
      externalSignal.addEventListener('abort', () => controller.abort(), { once: true });
    }

    // Only the transport call belongs in the try: the retry policy below is for
    // network failures, so anything this wrapper throws *about a response it
    // successfully received* (the 401 interceptor) has to be raised after the
    // catch, or the generic handler treats an expired session as transient and
    // re-issues the request.
    let response: Response;
    try {
      response = await fetch(url, {
        ...fetchOpts,
        headers,
        credentials: 'same-origin',
        signal: controller.signal,
      });
      clearTimeout(timeoutId);
    } catch (err) {
      clearTimeout(timeoutId);
      lastError = err instanceof Error ? err : new Error(String(err));

      // Don't retry if the caller explicitly aborted
      if (externalSignal?.aborted) {
        throw lastError;
      }

      // Don't retry an abort we raised ourselves: the request already had its
      // full timeout budget, so a second attempt just doubles the caller's wait
      // and hits an endpoint we already know is not answering in time.
      if (lastError.name === 'AbortError') {
        throw lastError;
      }

      // Retry on network errors only.
      if (attempt < retries) {
        await new Promise((resolve) => setTimeout(resolve, RETRY_DELAY));
      }
      continue;
    }

    // 401 Interceptor: clear auth state, surface the expiry, and throw. Outside
    // the try on purpose — an expired session is a terminal answer from the
    // server, not a transient failure, and retrying it would run the
    // localStorage-clear + redirect twice and double-write any non-idempotent
    // request (e.g. POST /workflow/plans/{id}/push) the caller wrapped.
    if (response.status === 401) {
      // The server rejected the credential: drop custody's copy (memory +
      // session handoff), then the legacy artifacts, before surfacing.
      authCustody.onUnauthorized();
      localStorage.removeItem('token');
      localStorage.removeItem('portal_token');
      localStorage.removeItem('portal_user');
      localStorage.removeItem('portal_config');

      const path = window.location.pathname;
      if (path.startsWith('/portal')) {
        // The portal owns its own sign-in page, and /portal/login is in
        // routes.ts, so a hard navigation lands somewhere real.
        if (!path.endsWith('/login')) {
          window.location.href = '/portal/login';
        }
      } else {
        // The ERP surfaces have NO sign-in page: authentication is an external
        // identity provider (backend JWKS_URL) and the bearer token arrives in
        // localStorage out of band. This used to `window.location.href =
        // '/login'`, a path that is not in routes.ts, so an expired ERP session
        // rendered "Page not found" instead of anything actionable. Announce it
        // instead and let the app shell render an in-place session-expired
        // state (app.ts listens for this).
        window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
      }

      throw new Error('Session expired');
    }

    return response;
  }

  throw lastError!;
}
