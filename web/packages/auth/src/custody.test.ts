// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * JWT custody: the token lives in memory, the only persistence is the
 * sessionStorage handoff (so the front door can hand a session to the desk
 * across a full page navigation), and every record is validated field by
 * field on adoption and dropped when it does not parse.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import {
  AuthCustody,
  SESSION_HANDOFF_KEY,
  DevModeDisabledError,
  InvalidTokenError,
} from './custody'
import { authConfig } from './config'
import { decodeTokenClaims, isExpired } from './jwt'

/** A throw-away in-memory storage double. */
function memoryStorage(initial: Record<string, string> = {}) {
  const mem = new Map<string, string>(Object.entries(initial))
  return {
    getItem: (k: string) => (mem.has(k) ? (mem.get(k) as string) : null),
    setItem: (k: string, v: string) => void mem.set(k, v),
    removeItem: (k: string) => void mem.delete(k),
    written: mem,
  }
}

/** b64url-encode a string (the browser alphabet, no padding). */
function b64url(s: string): string {
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** A structurally valid JWT with the given payload claims. */
function jwt(claims: Record<string, unknown>): string {
  const header = b64url(JSON.stringify({ alg: 'RS256', typ: 'JWT' }))
  const payload = b64url(JSON.stringify(claims))
  return `${header}.${payload}.sig`
}

const FUTURE = Math.floor(Date.now() / 1000) + 3600
const PAST = Math.floor(Date.now() / 1000) - 3600

const TOKEN = jwt({ sub: 'user-1', email: 'dev@gable.test', roles: ['admin', 'sales'], exp: FUTURE })

beforeEach(() => {
  sessionStorage.clear()
})

afterEach(() => {
  // devMode is the build's, not a per-instance knob; tests that need it on
  // restore it here.
  authConfig.devMode = false
})

describe('custody — in-memory rule', () => {
  it('holds nothing before any sign-in', () => {
    const c = new AuthCustody(memoryStorage())
    expect(c.session).toBeNull()
    expect(c.token).toBeNull()
    expect(c.displayName).toBeNull()
    expect(c.roles).toEqual([])
  })

  it('holds a signed-in token in memory and exposes its claims', () => {
    const c = new AuthCustody(memoryStorage())
    const s = c.signInWithToken(TOKEN)
    expect(s.kind).toBe('token')
    expect(c.token).toBe(TOKEN)
    expect(c.displayName).toBe('dev@gable.test')
    expect(c.roles).toEqual(['admin', 'sales'])
  })

  it('refuses a non-JWT as data', () => {
    const c = new AuthCustody(memoryStorage())
    expect(() => c.signInWithToken('not-a-jwt')).toThrow(InvalidTokenError)
    expect(() => c.signInWithToken('')).toThrow(InvalidTokenError)
  })

  it('refuses a token whose own exp claim is in the past', () => {
    const c = new AuthCustody(memoryStorage())
    expect(() => c.signInWithToken(jwt({ sub: 'u', exp: PAST }))).toThrow(InvalidTokenError)
    expect(c.session).toBeNull()
  })
})

describe('custody — the sessionStorage handoff', () => {
  it('writes the handoff at sign-in (token included) and never localStorage', () => {
    const store = memoryStorage()
    const localSpy = memoryStorage()
    const c = new AuthCustody(store)
    c.signInWithToken(TOKEN)

    const raw = store.written.get(SESSION_HANDOFF_KEY)
    expect(raw).toBeDefined()
    const rec = JSON.parse(raw as string)
    expect(rec).toEqual({ v: 1, kind: 'token', token: TOKEN })
    expect(localSpy.written.size).toBe(0)
  })

  it('a fresh custody (the desk booting after the door navigated) adopts the handoff', () => {
    const store = memoryStorage()
    new AuthCustody(store).signInWithToken(TOKEN)

    const desk = new AuthCustody(store)
    expect(desk.token).toBe(TOKEN)
    expect(desk.roles).toEqual(['admin', 'sales'])
  })

  it('drops a handoff that does not parse, leaving nothing adopted', () => {
    const store = memoryStorage({ [SESSION_HANDOFF_KEY]: '{not json' })
    const c = new AuthCustody(store)
    expect(c.session).toBeNull()
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(false)
  })

  it('drops a handoff with an unknown payload version', () => {
    const store = memoryStorage({ [SESSION_HANDOFF_KEY]: JSON.stringify({ v: 2, kind: 'token', token: TOKEN }) })
    const c = new AuthCustody(store)
    expect(c.session).toBeNull()
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(false)
  })

  it('drops a handoff whose token has expired since it was written', () => {
    const stale = jwt({ sub: 'u', exp: PAST })
    const store = memoryStorage({ [SESSION_HANDOFF_KEY]: JSON.stringify({ v: 1, kind: 'token', token: stale }) })
    const c = new AuthCustody(store)
    expect(c.session).toBeNull()
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(false)
  })

  it('drops a dev handoff in a build without dev mode', () => {
    const store = memoryStorage({ [SESSION_HANDOFF_KEY]: JSON.stringify({ v: 1, kind: 'dev', name: 'Local' }) })
    const c = new AuthCustody(store)
    expect(c.session).toBeNull()
  })

  it('sign-out clears the memory and the handoff', () => {
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInWithToken(TOKEN)
    c.signOut()
    expect(c.session).toBeNull()
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(false)
  })
})

describe('custody — the handoff value is removed', () => {
  it('on sign out', () => {
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInWithToken(TOKEN)
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(true)
    c.signOut()
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(false)
  })

  it('on a 401 (onUnauthorized)', () => {
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInWithToken(TOKEN)
    c.onUnauthorized()
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(false)
    expect(c.session).toBeNull()
  })

  it('on a 401 through the real fetch client, from the real sessionStorage', async () => {
    const { fetchWithAuth } = await import('./fetchClient')
    const { authCustody } = await import('./custody')
    authCustody.signInWithToken(TOKEN)
    expect(sessionStorage.getItem(SESSION_HANDOFF_KEY)).not.toBeNull()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })))
    await expect(fetchWithAuth('/api/v1/quotes')).rejects.toThrow()
    vi.unstubAllGlobals()
    expect(sessionStorage.getItem(SESSION_HANDOFF_KEY)).toBeNull()
  })

  it('and no other module can read the key: the package does not export it', async () => {
    const pkg = await import('./index')
    expect('SESSION_HANDOFF_KEY' in pkg).toBe(false)
  })
})

describe('custody — dev sign-in', () => {
  it('is refused in a build without VITE_AUTH_DEV_MODE', () => {
    authConfig.devMode = false
    const c = new AuthCustody(memoryStorage())
    expect(() => c.signInDev('Local')).toThrow(DevModeDisabledError)
  })

  it('holds a display name with no credential when dev mode is on', () => {
    authConfig.devMode = true
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInDev('  Local Developer  ')
    expect(c.session).toEqual({ kind: 'dev', name: 'Local Developer' })
    expect(c.token).toBeNull()
    expect(c.displayName).toBe('Local Developer')
    // The handoff carries the dev record so the desk shows the same user.
    const rec = JSON.parse(store.written.get(SESSION_HANDOFF_KEY) as string)
    expect(rec).toEqual({ v: 1, kind: 'dev', name: 'Local Developer' })
  })

  it('refuses an empty display name', () => {
    authConfig.devMode = true
    const c = new AuthCustody(memoryStorage())
    expect(() => c.signInDev('   ')).toThrow(InvalidTokenError)
  })
})

describe('custody — hostile storage degrades to memory', () => {
  it('sign-in still works when the storage accessor throws', () => {
    const hostile = {
      getItem: () => null,
      setItem: () => { throw new Error('denied') },
      removeItem: () => { throw new Error('denied') },
    }
    const c = new AuthCustody(hostile)
    c.signInWithToken(TOKEN)
    expect(c.token).toBe(TOKEN)
    c.signOut() // must not throw
    expect(c.session).toBeNull()
  })
})

describe('jwt — payload decode', () => {
  it('decodes the claim set the frontends render', () => {
    expect(decodeTokenClaims(TOKEN)).toEqual({
      sub: 'user-1',
      email: 'dev@gable.test',
      roles: ['admin', 'sales'],
      exp: FUTURE,
    })
  })

  it('refuses anything that is not three segments', () => {
    expect(decodeTokenClaims('')).toBeNull()
    expect(decodeTokenClaims('a.b')).toBeNull()
    expect(decodeTokenClaims('a.b.c.d')).toBeNull()
    expect(decodeTokenClaims('..')).toBeNull()
  })

  it('refuses a payload that is not a JSON object', () => {
    const arr = `${b64url(JSON.stringify({ alg: 'none' }))}.${b64url('[1,2]')}.s`
    expect(decodeTokenClaims(arr)).toBeNull()
    const str = `${b64url(JSON.stringify({ alg: 'none' }))}.${b64url('"x"')}.s`
    expect(decodeTokenClaims(str)).toBeNull()
  })

  it('treats exp as a past/future boundary', () => {
    expect(isExpired({ exp: PAST })).toBe(true)
    expect(isExpired({ exp: FUTURE })).toBe(false)
    expect(isExpired({})).toBe(false) // no claim: not known to be expired
  })
})
