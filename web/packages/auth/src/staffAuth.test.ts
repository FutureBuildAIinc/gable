// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The staff sign in client: it posts to the core with the CSRF header, holds
 * display data only, shows the core's fixed refusal in plain words, and
 * clears custody on sign out even when the core cannot be reached.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { authCustody } from './custody'
import { restoreStaffSession, signInStaff, signOutStaff } from './staffAuth'

const SUB = '3f2b8c1e-0d5a-5b7e-9c41-6a1d2e3f4a5b'

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } })
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  sessionStorage.clear()
  authCustody.signOut()
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
  authCustody.signOut()
})

describe('signInStaff', () => {
  it('posts the form to the core with the CSRF header and holds display data only', async () => {
    fetchMock.mockResolvedValue(json(200, { sub: SUB, roles: ['cashier'] }))
    const out = await signInStaff('till@example.test', 'a-password')
    expect(out).toEqual({ ok: true })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/auth/signin')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('same-origin')
    expect((init.headers as Record<string, string>)['X-Gable-CSRF']).toBe('1')
    expect(JSON.parse(init.body as string)).toEqual({ email: 'till@example.test', password: 'a-password' })

    expect(authCustody.subject).toBe(SUB)
    expect(authCustody.roles).toEqual(['cashier'])
    expect(authCustody.displayName).toBe('till@example.test')
    expect(authCustody.token).toBeNull()
    // The password is never kept anywhere a script can read.
    expect(JSON.stringify(sessionStorage)).not.toContain('a-password')
    expect(JSON.stringify(localStorage)).not.toContain('a-password')
  })

  it('shows the fixed 401 in plain words and holds nothing', async () => {
    fetchMock.mockResolvedValue(json(401, { error: 'sign in failed' }))
    const out = await signInStaff('till@example.test', 'wrong')
    expect(out.ok).toBe(false)
    expect(out.ok === false && out.message).toMatch(/not accepted/)
    expect(authCustody.session).toBeNull()
  })

  it('says how long to wait on a 429', async () => {
    fetchMock.mockResolvedValue(json(429, { error: 'sign in failed' }, { 'Retry-After': '42' }))
    const out = await signInStaff('till@example.test', 'x')
    expect(out).toEqual({ ok: false, message: 'Too many attempts. Try again in 42 seconds.' })
  })

  it('says sign in is unavailable on a 503', async () => {
    fetchMock.mockResolvedValue(json(503, { error: 'sign in is unavailable' }))
    const out = await signInStaff('till@example.test', 'x')
    expect(out.ok === false && out.message).toMatch(/unavailable/)
  })

  it('says the server could not be reached on a network failure', async () => {
    fetchMock.mockRejectedValue(new TypeError('network'))
    const out = await signInStaff('till@example.test', 'x')
    expect(out).toEqual({ ok: false, message: 'The server could not be reached.' })
  })

  it('refuses an answer that is not a subject and roles', async () => {
    fetchMock.mockResolvedValue(json(200, { hello: 'world' }))
    const out = await signInStaff('till@example.test', 'x')
    expect(out.ok).toBe(false)
    expect(authCustody.session).toBeNull()
  })
})

describe('signOutStaff', () => {
  it('posts the sign out with the CSRF header, then clears custody', async () => {
    authCustody.signInStaff({ sub: SUB, roles: ['owner'] })
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await signOutStaff()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/auth/signout')
    expect(init.method).toBe('POST')
    expect((init.headers as Record<string, string>)['X-Gable-CSRF']).toBe('1')
    expect(authCustody.session).toBeNull()
    expect(sessionStorage.length).toBe(0)
  })

  it('clears custody even when the core cannot be reached', async () => {
    authCustody.signInStaff({ sub: SUB, roles: ['owner'] })
    fetchMock.mockRejectedValue(new TypeError('network'))
    await signOutStaff()
    expect(authCustody.session).toBeNull()
  })
})

describe('restoreStaffSession', () => {
  it('adopts the person the cookie belongs to on a fresh tab', async () => {
    fetchMock.mockResolvedValue(json(200, { sub: SUB, roles: ['admin'] }))
    expect(await restoreStaffSession()).toBe(true)
    expect(authCustody.roles).toEqual(['admin'])
    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/auth/session')
  })

  it('leaves custody empty on a 401', async () => {
    fetchMock.mockResolvedValue(json(401, { error: 'no session' }))
    expect(await restoreStaffSession()).toBe(false)
    expect(authCustody.session).toBeNull()
  })

  it('does not ask when custody already holds a session', async () => {
    authCustody.signInStaff({ sub: SUB, roles: ['admin'] })
    expect(await restoreStaffSession()).toBe(true)
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
