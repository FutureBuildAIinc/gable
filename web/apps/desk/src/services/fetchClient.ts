// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Shared fetch wrapper with auth headers, timeout, and retry logic.
 * Drop-in replacement for fetch() — all frontend services should use this.
 */

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
}

/**
 * Fetch wrapper that automatically:
 * - Injects Bearer auth token from localStorage
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
    ...fetchOpts
  } = options;

  const headers = new Headers(customHeaders);

  // Inject auth token if not already present (ERP/OIDC flows using localStorage)
  if (!headers.has('Authorization')) {
    const token = localStorage.getItem('token');
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }
  }

  // Inject the currently selected branch for multi-branch installs.
  // We read straight from localStorage to avoid a circular import with the
  // BranchContext singleton, which itself uses fetchWithAuth at init time.
  if (!headers.has('X-Branch-Id')) {
    const branchId = localStorage.getItem('gable_current_branch_id');
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
        credentials: 'include',
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
