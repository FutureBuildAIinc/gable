// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest'
import { OFFLINE_WRITE_REASON, OfflineReadStore, writesDisabled } from './offline.ts'

class MemoryStorage {
  private readonly values = new Map<string, string>()
  get length() { return this.values.size }
  key(index: number) { return Array.from(this.values.keys())[index] ?? null }
  getItem(key: string) { return this.values.get(key) ?? null }
  setItem(key: string, value: string) { this.values.set(key, value) }
  removeItem(key: string) { this.values.delete(key) }
}

describe('offline reads and writes', () => {
  it('round trips a read with its change feed validators', () => {
    const store = new OfflineReadStore(new MemoryStorage())
    store.write('today', { value: { count: 3 }, etag: 'validator', latestChange: 'cursor' })
    expect(store.read<{ count: number }>('today')).toEqual({
      value: { count: 3 },
      etag: 'validator',
      latestChange: 'cursor',
    })
  })

  it('drops malformed cached data instead of inventing a read', () => {
    const storage = new MemoryStorage()
    storage.setItem('gable.pro.read.today', '{broken')
    expect(new OfflineReadStore(storage).read('today')).toBeNull()
  })

  it('disables writes offline and never describes a queue', () => {
    expect(writesDisabled(true)).toBe(true)
    expect(writesDisabled(false)).toBe(false)
    expect(OFFLINE_WRITE_REASON).toContain('Nothing is queued')
  })

  it('isolates cached entries across account and user scopes and clears all prefixed keys', () => {
    const storage = new MemoryStorage()
    storage.setItem('unrelated.key', 'keep-me')
    const storeA = new OfflineReadStore(storage, 'gable.pro.read.', { accountId: 'acct-1', userId: 'user-owner' })
    const storeB = new OfflineReadStore(storage, 'gable.pro.read.', { accountId: 'acct-1', userId: 'user-super' })
    const storeC = new OfflineReadStore(storage, 'gable.pro.read.', { accountId: 'acct-2', userId: 'user-owner' })

    storeA.write('today', { value: { secret_cents: 99900 }, etag: 'e1', latestChange: 'c1' })
    storeB.write('orders', { value: [{ id: 'ord-1' }], etag: 'e2', latestChange: 'c2' })

    expect(storeA.read('today')?.value).toEqual({ secret_cents: 99900 })
    expect(storeB.read('today')).toBeNull()
    expect(storeC.read('today')).toBeNull()
    expect(storeA.read('orders')).toBeNull()
    expect(storeB.read('orders')?.value).toEqual([{ id: 'ord-1' }])

    storeA.clear()
    expect(storeA.read('today')).toBeNull()
    expect(storeB.read('orders')).toBeNull()
    expect(storage.getItem('unrelated.key')).toBe('keep-me')
  })

  it('clears the offline read store on signOut and renders configured sign_in_methods including OIDC', async () => {
    const { GableProShell } = await import('../app-shell.ts')
    const { readStore } = await import('./api.ts')
    const { meFixture, todayFixture } = await import('../test/fixtures.ts')

    localStorage.clear()
    readStore.setScope(meFixture.account.id, meFixture.user.id)
    readStore.write('today', { value: todayFixture, etag: 'e1', latestChange: 'c1' })
    expect(readStore.read('today')).not.toBeNull()

    const shell = new GableProShell()
    ;(shell as unknown as { phase: string; me: unknown; config: unknown }).me = structuredClone(meFixture)
    ;(shell as unknown as { phase: string; me: unknown; config: unknown }).phase = 'ready'
    document.body.append(shell)
    await shell.updateComplete

    await (shell as unknown as { signOut: () => Promise<void> }).signOut()
    expect(readStore.read('today')).toBeNull()

    // Default when sign_in_methods is absent: shows password + magic link, no oidc
    ;(shell as unknown as { config: unknown }).config = {
      id: '55555555-5555-4555-8555-555555555555',
      dealer_name: 'Example Supply',
      logo_url: '',
      primary_color: '',
      support_email: 'support@example.com',
      support_phone: '',
      locale: 'en-US',
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }
    shell.requestUpdate()
    await shell.updateComplete
    expect(shell.querySelector('input[name="password"]')).not.toBeNull()
    expect(shell.querySelector('[data-testid="pro-signin-magic-link"]')).not.toBeNull()
    expect(shell.querySelector('[data-testid="pro-signin-oidc"]')).toBeNull()

    // Explicit sign_in_methods with oidc only: hides password and magic_link, shows pro-signin-oidc
    ;(shell as unknown as { config: unknown }).config = {
      id: '55555555-5555-4555-8555-555555555555',
      dealer_name: 'Example Supply',
      logo_url: '',
      primary_color: '',
      support_email: 'support@example.com',
      support_phone: '',
      locale: 'en-US',
      sign_in_methods: ['oidc'],
      oidc: { enabled: true, label: 'Sign in with SSO' },
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }
    shell.requestUpdate()
    await shell.updateComplete
    expect(shell.querySelector('input[name="password"]')).toBeNull()
    expect(shell.querySelector('[data-testid="pro-signin-magic-link"]')).toBeNull()
    expect(shell.querySelector('[data-testid="pro-signin-oidc"]')).not.toBeNull()
    shell.remove()
  })
})


