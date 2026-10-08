// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * fetchWithAuth is the single chokepoint every frontend service calls, so its
 * header construction, 401 interception and retry policy are load-bearing for
 * the whole app. Its own docblock states the contract these tests hold it to:
 * "Retries on network errors (not on HTTP error status codes)".
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { fetchWithAuth, SESSION_EXPIRED_EVENT } from './fetchClient'

let fetchMock: ReturnType<typeof vi.fn>

/** Headers the client actually put on the wire for call `n`. */
function sentHeaders(n = 0): Headers {
  const init = fetchMock.mock.calls[n][1] as RequestInit
  return init.headers as Headers
}

function sentInit(n = 0): RequestInit {
  return fetchMock.mock.calls[n][1] as RequestInit
}

/**
 * The rejection a real `fetch()` produces when its signal is aborted.
 *
 * Browsers and undici reject with a DOMException named 'AbortError' that *is*
 * `instanceof Error`; jsdom's DOMException is not, which would make
 * fetchClient's `err instanceof Error ? err : new Error(String(err))` rewrap it
 * and lose the name. Modelling the two properties the client actually inspects
 * (`instanceof Error` and `.name`) keeps these tests honest about production.
 */
function abortError(): Error {
  const err = new Error('The operation was aborted.')
  err.name = 'AbortError'
  return err
}

/**
 * A fetch that never settles on its own and only rejects when its signal is
 * aborted — i.e. an endpoint that hangs until the client's timeout fires.
 */
function hangUntilAborted() {
  return (_url: string, init: RequestInit) =>
    new Promise<Response>((_resolve, reject) => {
      init.signal?.addEventListener('abort', () => reject(abortError()))
    })
}

beforeEach(() => {
  fetchMock = vi.fn().mockResolvedValue(new Response('{}', { status: 200 }))
  vi.stubGlobal('fetch', fetchMock)
  localStorage.clear()
  history.replaceState(null, '', '/login')
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('fetchWithAuth — header construction', () => {
  it('attaches the localStorage bearer token', async () => {
    localStorage.setItem('token', 'abc123')
    await fetchWithAuth('/api/v1/orders')
    expect(sentHeaders().get('Authorization')).toBe('Bearer abc123')
  })

  it('sends no Authorization header when there is no token', async () => {
    await fetchWithAuth('/api/v1/orders')
    expect(sentHeaders().has('Authorization')).toBe(false)
  })

  it('does not clobber an Authorization header the caller supplied', async () => {
    localStorage.setItem('token', 'abc123')
    await fetchWithAuth('/api/v1/orders', { headers: { Authorization: 'Bearer explicit' } })
    expect(sentHeaders().get('Authorization')).toBe('Bearer explicit')
  })

  it('attaches the selected branch for multi-branch installs', async () => {
    localStorage.setItem('gable_current_branch_id', 'branch-7')
    await fetchWithAuth('/api/v1/orders')
    expect(sentHeaders().get('X-Branch-Id')).toBe('branch-7')
  })

  it('omits X-Branch-Id when no branch is selected', async () => {
    await fetchWithAuth('/api/v1/orders')
    expect(sentHeaders().has('X-Branch-Id')).toBe(false)
  })

  it('infers application/json for a string body', async () => {
    await fetchWithAuth('/api/v1/orders', { method: 'POST', body: '{"a":1}' })
    expect(sentHeaders().get('Content-Type')).toBe('application/json')
  })

  it('leaves a caller-supplied Content-Type alone', async () => {
    await fetchWithAuth('/api/v1/import', {
      method: 'POST',
      body: 'sku,qty',
      headers: { 'Content-Type': 'text/csv' },
    })
    expect(sentHeaders().get('Content-Type')).toBe('text/csv')
  })

  it('does not invent a Content-Type for a bodyless request', async () => {
    await fetchWithAuth('/api/v1/orders')
    expect(sentHeaders().has('Content-Type')).toBe(false)
  })

  it('always sends cookies (portal auth uses an httpOnly cookie)', async () => {
    await fetchWithAuth('/api/portal/v1/orders')
    expect(sentInit().credentials).toBe('include')
  })

  it('forwards the method and body unchanged', async () => {
    await fetchWithAuth('/api/v1/orders', { method: 'PUT', body: '{"status":"OPEN"}' })
    expect(sentInit().method).toBe('PUT')
    expect(sentInit().body).toBe('{"status":"OPEN"}')
  })
})

describe('fetchWithAuth — HTTP error handling', () => {
  it('returns non-2xx responses to the caller rather than throwing', async () => {
    fetchMock.mockResolvedValue(new Response('not found', { status: 404 }))
    const res = await fetchWithAuth('/api/v1/orders/missing')
    expect(res.status).toBe(404)
    expect(res.ok).toBe(false)
  })

  it('does not retry a 500 — HTTP errors are not network errors', async () => {
    fetchMock.mockResolvedValue(new Response('boom', { status: 500 }))
    const res = await fetchWithAuth('/api/v1/orders')
    expect(res.status).toBe(500)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('clears every auth artifact and throws on 401', async () => {
    localStorage.setItem('token', 'abc')
    localStorage.setItem('portal_token', 'def')
    localStorage.setItem('portal_user', '{"id":"1"}')
    localStorage.setItem('portal_config', '{"dealer_name":"Acme"}')
    fetchMock.mockResolvedValue(new Response('', { status: 401 }))

    await expect(fetchWithAuth('/api/v1/orders', { retries: 0 })).rejects.toThrow('Session expired')

    expect(localStorage.getItem('token')).toBeNull()
    expect(localStorage.getItem('portal_token')).toBeNull()
    expect(localStorage.getItem('portal_user')).toBeNull()
    expect(localStorage.getItem('portal_config')).toBeNull()
  })

  it('leaves the branch selection intact across a 401', async () => {
    localStorage.setItem('gable_current_branch_id', 'branch-7')
    fetchMock.mockResolvedValue(new Response('', { status: 401 }))
    await expect(fetchWithAuth('/api/v1/orders', { retries: 0 })).rejects.toThrow()
    expect(localStorage.getItem('gable_current_branch_id')).toBe('branch-7')
  })

  /**
   * The ERP surfaces have no sign-in page. Authentication is an external
   * identity provider (the backend validates against JWKS_URL) and the bearer
   * token lands in localStorage out of band, so routes.ts has `/portal/login`
   * and nothing else. This interceptor used to `window.location.href = '/login'`
   * on any non-portal 401, which is not a route — an expired ERP session
   * rendered the 404 page ("Page not found") instead of anything a user could
   * act on. It now announces the expiry and lets `app.ts` render an in-place
   * session-expired panel.
   */
  it('announces an expired ERP session instead of navigating to a route that does not exist', async () => {
    history.replaceState(null, '', '/orders')
    const heard = vi.fn()
    window.addEventListener(SESSION_EXPIRED_EVENT, heard)
    fetchMock.mockResolvedValue(new Response('', { status: 401 }))

    await expect(fetchWithAuth('/api/v1/orders', { retries: 0 })).rejects.toThrow('Session expired')

    expect(heard).toHaveBeenCalledTimes(1)
    // No navigation: the path is unchanged and, in particular, is not '/login'.
    expect(window.location.pathname).toBe('/orders')
    window.removeEventListener(SESSION_EXPIRED_EVENT, heard)
  })

  it('does not announce a session expiry on the portal, which has a real login route', async () => {
    // The portal keeps the redirect: /portal/login IS in routes.ts. Assert from
    // the portal's own login page so the guard against a redirect loop keeps
    // jsdom from attempting a navigation it does not implement.
    history.replaceState(null, '', '/portal/login')
    const heard = vi.fn()
    window.addEventListener(SESSION_EXPIRED_EVENT, heard)
    fetchMock.mockResolvedValue(new Response('', { status: 401 }))

    await expect(fetchWithAuth('/api/portal/v1/orders', { retries: 0 })).rejects.toThrow(
      'Session expired',
    )

    expect(heard).not.toHaveBeenCalled()
    window.removeEventListener(SESSION_EXPIRED_EVENT, heard)
  })
})

describe('fetchWithAuth — retry policy', () => {
  it('retries once after a network error and returns the second response', async () => {
    vi.useFakeTimers()
    fetchMock
      .mockRejectedValueOnce(new TypeError('Failed to fetch'))
      .mockResolvedValueOnce(new Response('{}', { status: 200 }))

    const pending = fetchWithAuth('/api/v1/orders')
    await vi.advanceTimersByTimeAsync(2_500)
    const res = await pending

    expect(res.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('honours retries: 0 and surfaces the network error', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'))
    await expect(fetchWithAuth('/api/v1/orders', { retries: 0 })).rejects.toThrow('Failed to fetch')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('gives up after exhausting retries', async () => {
    vi.useFakeTimers()
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'))
    const pending = fetchWithAuth('/api/v1/orders', { retries: 2 })
    const assertion = expect(pending).rejects.toThrow('Failed to fetch')
    await vi.advanceTimersByTimeAsync(10_000)
    await assertion
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('stops immediately when the caller aborts', async () => {
    const controller = new AbortController()
    fetchMock.mockImplementation(hangUntilAborted())

    const pending = fetchWithAuth('/api/v1/orders', { signal: controller.signal })
    controller.abort()
    await expect(pending).rejects.toThrow(/abort/i)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('aborts a hanging request once the timeout elapses', async () => {
    // Real timers with a 20ms budget: the abort path deliberately skips the
    // retry delay, so this stays fast without fake-timer choreography.
    fetchMock.mockImplementation(hangUntilAborted())
    await expect(fetchWithAuth('/api/v1/orders', { timeout: 20, retries: 0 }))
      .rejects.toThrow(/abort/i)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  // --- Regressions on the module's stated contract ---------------------------

  // The 401 interceptor used to throw from *inside* the try block, so the generic
  // catch treated an expired session as a retryable network error. Every 401 cost
  // a 2s stall, fired a second doomed request, and ran the localStorage-clear +
  // `window.location.href = ...` redirect twice. The same wrapper carries
  // POST /workflow/plans/{id}/push, so an expired session double-wrote.
  it('does not retry a 401 — an expired session is not a network error', async () => {
    vi.useFakeTimers()
    fetchMock.mockResolvedValue(new Response('', { status: 401 }))
    const pending = fetchWithAuth('/api/v1/orders')
    const assertion = expect(pending).rejects.toThrow('Session expired')
    await vi.advanceTimersByTimeAsync(5_000)
    await assertion
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('does not re-issue a non-idempotent write after a 401', async () => {
    // The concrete cost of the old behaviour: the second attempt was a second
    // POST, so an expired session pushed the plan twice.
    vi.useFakeTimers()
    fetchMock.mockResolvedValue(new Response('', { status: 401 }))
    const pending = fetchWithAuth('/api/v1/workflow/plans/plan-1/push', {
      method: 'POST',
      body: '{}',
    })
    const assertion = expect(pending).rejects.toThrow('Session expired')
    await vi.advanceTimersByTimeAsync(5_000)
    await assertion
    expect(fetchMock.mock.calls.filter((c) => (c[1] as RequestInit).method === 'POST')).toHaveLength(1)
  })

  // The retry guard used to be `attempt < retries && lastError.name !== 'AbortError'`,
  // which only skipped the *delay* on an abort — the loop still ran another
  // attempt. The inline comment above it said "Retry on network errors, not on
  // intentional aborts from timeout". A slow endpoint got hit twice and the
  // caller waited 2x the configured timeout.
  it('does not retry after its own timeout fired', async () => {
    fetchMock.mockImplementation(hangUntilAborted())
    await expect(fetchWithAuth('/api/v1/orders', { timeout: 20, retries: 1 }))
      .rejects.toThrow(/abort/i)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
