// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Session custody: a staff session is display data only (the cookie is the
 * credential and no script holds it) held in memory only, the one thing in
 * sessionStorage is the dev handoff, a bearer token is memory only, and every
 * record is validated field by field on adoption and dropped when it does
 * not parse.
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import {
  AuthCustody,
  SESSION_HANDOFF_KEY,
  DevModeDisabledError,
  InvalidTokenError,
} from './custody'
import { authConfig } from './config'
import { BRANCH_STORAGE_KEY } from './branch'
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

describe('custody, subject', () => {
  it('is null signed out, the sub claim for a token, dev:<name> for a dev session', () => {
    const c = new AuthCustody()
    expect(c.subject).toBeNull()
    c.signInWithToken(TOKEN)
    expect(c.subject).toBe('user-1')
    authConfig.devMode = true
    c.signInDev('Local Developer')
    expect(c.subject).toBe('dev:Local Developer')
    c.signOut()
    expect(c.subject).toBeNull()
  })
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

  it('merges the single role claim into roles (core admits it too)', () => {
    const only = new AuthCustody(memoryStorage())
    only.signInWithToken(jwt({ sub: 'u', role: 'owner', exp: FUTURE }))
    expect(only.roles).toEqual(['owner'])

    const both = new AuthCustody(memoryStorage())
    both.signInWithToken(jwt({ sub: 'u', role: 'admin', roles: ['sales', 'admin'], exp: FUTURE }))
    expect(both.roles).toEqual(['sales', 'admin'])

    const junk = new AuthCustody(memoryStorage())
    junk.signInWithToken(jwt({ sub: 'u', role: 7, roles: 'sales', exp: FUTURE }))
    expect(junk.roles).toEqual([])
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
  it('writes nothing at a staff sign in: display data lives in memory only', () => {
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInStaff({ sub: 'sub-1', roles: ['owner'], name: 'dev@gable.test' })

    expect(store.written.size).toBe(0)
    expect(sessionStorage.length).toBe(0)
    expect(localStorage.length).toBe(0)
    expect(c.session?.kind).toBe('staff')
  })

  it('a bearer token is held in memory and written to no storage', () => {
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInWithToken(TOKEN)
    expect(c.token).toBe(TOKEN)
    expect(store.written.size).toBe(0)
    expect(sessionStorage.length).toBe(0)
  })

  it('a fresh custody holds no staff session until the core is asked', () => {
    const store = memoryStorage()
    new AuthCustody(store).signInStaff({ sub: 'sub-1', roles: ['admin', 'sales'], name: 'dev@gable.test' })

    const desk = new AuthCustody(store)
    expect(desk.session).toBeNull()
  })

  it('drops a staff record an older build left in storage', () => {
    const store = memoryStorage({
      [SESSION_HANDOFF_KEY]: JSON.stringify({ v: 1, kind: 'staff', sub: 'sub-1', roles: ['owner'] }),
    })
    expect(new AuthCustody(store).session).toBeNull()
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(false)
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

  it('drops an old token handoff outright: a token is never adopted from storage', () => {
    const store = memoryStorage({ [SESSION_HANDOFF_KEY]: JSON.stringify({ v: 1, kind: 'token', token: TOKEN }) })
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

describe('custody — the counter\'s remembered sale keys', () => {
  it('sign out removes them and leaves other keys', () => {
    localStorage.setItem('gable.pos.sale.user-1.REG-01', 's1')
    localStorage.setItem('gable.pos.register.main', 'REG-01')
    const c = new AuthCustody(memoryStorage())
    c.signInWithToken(TOKEN)
    c.signOut()
    expect(localStorage.getItem('gable.pos.sale.user-1.REG-01')).toBeNull()
    expect(localStorage.getItem('gable.pos.register.main')).toBe('REG-01')
    localStorage.removeItem('gable.pos.register.main')
  })
})

describe('custody: the desk\'s open apps', () => {
  it('sign out closes them, so the next person starts on their own home', () => {
    localStorage.setItem('gable_workspace', JSON.stringify({ tabs: [{ key: 'reporting', path: '/reports/ar-aging' }], active: 'reporting' }))
    localStorage.setItem('gable.pos.register.main', 'REG-01')
    const c = new AuthCustody(memoryStorage())
    c.signInWithToken(TOKEN)
    c.signOut()
    expect(localStorage.getItem('gable_workspace')).toBeNull()
    expect(localStorage.getItem('gable.pos.register.main')).toBe('REG-01')
    localStorage.removeItem('gable.pos.register.main')
  })
})

describe('custody — the handoff value is removed', () => {
  it('on sign out', () => {
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInStaff({ sub: 'sub-1', roles: ['owner'] })
    expect(c.session).not.toBeNull()
    c.signOut()
    expect(c.session).toBeNull()
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
    authCustody.signInStaff({ sub: 'sub-1', roles: ['owner'] })
    expect(authCustody.session).not.toBeNull()
    expect(sessionStorage.getItem(SESSION_HANDOFF_KEY)).toBeNull()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })))
    await expect(fetchWithAuth('/api/v1/quotes')).rejects.toThrow()
    vi.unstubAllGlobals()
    expect(authCustody.session).toBeNull()
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

describe('custody: dev sign-in with a persona', () => {
  it('stores the persona on the session and exposes it through the getters', () => {
    authConfig.devMode = true
    const store = memoryStorage()
    const c = new AuthCustody(store)
    const s = c.signInDev('Jordan Pike', { role: 'cashier', homeYardCode: 'KEL-MAIN' })
    expect(s).toEqual({ kind: 'dev', name: 'Jordan Pike', role: 'cashier', homeYardCode: 'KEL-MAIN' })
    expect(c.displayName).toBe('Jordan Pike')
    expect(c.personaRole).toBe('cashier')
    expect(c.personaHomeYardCode).toBe('KEL-MAIN')
    const rec = JSON.parse(store.written.get(SESSION_HANDOFF_KEY) as string)
    expect(rec).toEqual({
      v: 1, kind: 'dev', name: 'Jordan Pike', role: 'cashier', homeYardCode: 'KEL-MAIN',
    })
  })

  it('reads the persona back through the handoff on a fresh custody (the desk booting)', () => {
    authConfig.devMode = true
    const store = memoryStorage()
    new AuthCustody(store).signInDev('Avery Brooks', { role: 'sales', homeYardCode: 'KEL-MAIN' })

    const desk = new AuthCustody(store)
    expect(desk.personaRole).toBe('sales')
    expect(desk.personaHomeYardCode).toBe('KEL-MAIN')
  })

  it('drops the stored yard when a persona signs in, and keeps it for a sign in by name', () => {
    authConfig.devMode = true
    const c = new AuthCustody(memoryStorage())
    localStorage.setItem(BRANCH_STORAGE_KEY, 'b-kel-main')
    c.signInDev('Local')
    expect(localStorage.getItem(BRANCH_STORAGE_KEY)).toBe('b-kel-main')
    c.signInDev('Drew Patel', { role: 'cashier', homeYardCode: 'LK' })
    expect(localStorage.getItem(BRANCH_STORAGE_KEY)).toBeNull()
  })

  it('treats an absent persona as no role and no home yard', () => {
    authConfig.devMode = true
    const c = new AuthCustody(memoryStorage())
    c.signInDev('Local')
    expect(c.personaRole).toBeNull()
    expect(c.personaHomeYardCode).toBeNull()
  })

  it('drops an empty persona field on the way through the handoff', () => {
    authConfig.devMode = true
    const store = memoryStorage({
      [SESSION_HANDOFF_KEY]: JSON.stringify({
        v: 1, kind: 'dev', name: 'Avery Brooks', role: '', homeYardCode: '',
      }),
    })
    const c = new AuthCustody(store)
    expect(c.personaRole).toBeNull()
    expect(c.personaHomeYardCode).toBeNull()
  })

  it('drops a non-string persona field rather than trust the caller', () => {
    authConfig.devMode = true
    const store = memoryStorage({
      [SESSION_HANDOFF_KEY]: JSON.stringify({
        v: 1, kind: 'dev', name: 'Avery Brooks', role: 7, homeYardCode: null,
      }),
    })
    const c = new AuthCustody(store)
    expect(c.personaRole).toBeNull()
    expect(c.personaHomeYardCode).toBeNull()
  })

  it('clears the persona when the user signs out', () => {
    authConfig.devMode = true
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInDev('Jordan Pike', { role: 'cashier', homeYardCode: 'KEL-MAIN' })
    c.signOut()
    expect(c.personaRole).toBeNull()
    expect(c.personaHomeYardCode).toBeNull()
    expect(store.written.has(SESSION_HANDOFF_KEY)).toBe(false)
  })

  it('clears the persona on a 401 (onUnauthorized)', () => {
    authConfig.devMode = true
    const store = memoryStorage()
    const c = new AuthCustody(store)
    c.signInDev('Jordan Pike', { role: 'cashier', homeYardCode: 'KEL-MAIN' })
    c.onUnauthorized()
    expect(c.personaRole).toBeNull()
    expect(c.personaHomeYardCode).toBeNull()
  })

  it('refuses a persona sign-in in a non-dev build', () => {
    authConfig.devMode = false
    const c = new AuthCustody(memoryStorage())
    expect(() => c.signInDev('Jordan Pike', { role: 'cashier', homeYardCode: 'KEL-MAIN' }))
      .toThrow(DevModeDisabledError)
  })

  it('refuses an empty display name even when a persona is given', () => {
    authConfig.devMode = true
    const c = new AuthCustody(memoryStorage())
    expect(() => c.signInDev('   ', { role: 'cashier' })).toThrow(InvalidTokenError)
  })

  it('returns no persona role or yard for a token session', () => {
    const c = new AuthCustody(memoryStorage())
    c.signInWithToken(TOKEN)
    expect(c.personaRole).toBeNull()
    expect(c.personaHomeYardCode).toBeNull()
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
