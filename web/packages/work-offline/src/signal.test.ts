// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The no signal guard and the money scope fence, pinned as the rule reads:
 * a write verb with no signal is refused before anything dials, a read is
 * untouched, and a money scope in any spelling is money.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { NO_SIGNAL_ERROR, browserSignal, fixedSignal, noSignalWebFetch } from './signal'
import { MONEY_SCOPES, scopeFence } from './scopes'

const inner = vi.fn(async (_input: string, _init?: RequestInit) => new Response('{}', { status: 200 }))

afterEach(() => {
  inner.mockClear()
})

describe('fixedSignal', () => {
  it('starts online by default and follows a later set', () => {
    const signal = fixedSignal()
    expect(signal.online()).toBe(true)
    signal.set(false)
    expect(signal.online()).toBe(false)
  })

  it('can start offline', () => {
    expect(fixedSignal(false).online()).toBe(false)
  })
})

describe('browserSignal', () => {
  const original = Object.getOwnPropertyDescriptor(Navigator.prototype, 'onLine')

  afterEach(() => {
    if (original) Object.defineProperty(Navigator.prototype, 'onLine', original)
  })

  function setOnLine(value: boolean) {
    Object.defineProperty(Navigator.prototype, 'onLine', { ...original, get: () => value })
  }

  it('reads the platform word fresh on every ask', () => {
    setOnLine(true)
    expect(browserSignal().online()).toBe(true)
    setOnLine(false)
    expect(browserSignal().online()).toBe(false)
  })
})

describe('noSignalWebFetch', () => {
  it('sends a GET through while offline, for the cached reads', async () => {
    await noSignalWebFetch(inner, fixedSignal(false))('/api/v1/pos/catalog')
    expect(inner).toHaveBeenCalledTimes(1)
  })

  it.each(['GET', 'HEAD', 'OPTIONS'])('sends a %s through while offline', async (method) => {
    await noSignalWebFetch(inner, fixedSignal(false))('/api/v1/pos/catalog', { method })
    expect(inner).toHaveBeenCalledTimes(1)
  })

  it.each(['POST', 'PUT', 'PATCH', 'DELETE'])(
    'refuses a %s while offline before any network call',
    async (method) => {
      const res = await noSignalWebFetch(inner, fixedSignal(false))('/api/v1/payments', {
        method,
        body: '{}',
      })
      expect(res.status).toBe(503)
      expect(res.headers.get('Content-Type')).toBe('application/json')
      expect(res.headers.get('X-Gable-Host-Refusal')).toBe('1')
      expect(await res.json()).toEqual({
        error: { code: 'no_signal', message: NO_SIGNAL_ERROR },
        message: NO_SIGNAL_ERROR,
      })
      expect(inner).not.toHaveBeenCalled()
    },
  )

  it.each(['POST', 'PUT', 'PATCH', 'DELETE'])('sends a %s through while online', async (method) => {
    await noSignalWebFetch(inner, fixedSignal(true))('/api/v1/payments', { method, body: '{}' })
    expect(inner).toHaveBeenCalledTimes(1)
  })

  it('reads an implicit GET when the init carries no method', async () => {
    await noSignalWebFetch(inner, fixedSignal(false))('/api/v1/pos/catalog')
    expect(inner).toHaveBeenCalledTimes(1)
  })

  it('is case insensitive about the method', async () => {
    const res = await noSignalWebFetch(inner, fixedSignal(false))('/api/v1/payments', { method: 'post' })
    expect(res.status).toBe(503)
    expect(inner).not.toHaveBeenCalled()
  })
})

describe('scopeFence', () => {
  it('refuses every money scope by its exact name', () => {
    for (const scope of MONEY_SCOPES) {
      const refusal = scopeFence(scope)
      expect(refusal).not.toBeNull()
      expect(refusal?.reason).toBe('money_scope')
      expect(refusal?.message).toBe(`${scope} is a money scope: money never queues, resolve it online`)
    }
  })

  it('refuses a money scope whatever the casing or the whitespace around it', () => {
    for (const scope of MONEY_SCOPES) {
      expect(scopeFence(scope.toUpperCase())?.reason).toBe('money_scope')
      expect(scopeFence(`  ${scope} `)?.reason).toBe('money_scope')
      expect(scopeFence(` ${scope.toUpperCase()}  `)?.reason).toBe('money_scope')
    }
  })

  it('keeps the spelling the caller used in the words of the refusal', () => {
    expect(scopeFence('  Invoices ')?.message).toBe(
      '  Invoices  is a money scope: money never queues, resolve it online',
    )
  })

  it('lets a scope that carries no money through', () => {
    expect(scopeFence('customers')).toBeNull()
    expect(scopeFence('products')).toBeNull()
  })

  it('answers nothing for a value that is no scope at all', () => {
    expect(scopeFence(null)).toBeNull()
    expect(scopeFence(42)).toBeNull()
  })
})
