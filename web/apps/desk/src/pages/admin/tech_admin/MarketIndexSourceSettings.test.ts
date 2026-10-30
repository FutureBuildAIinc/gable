// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The Tech Admin market index source card (QE-07-b1): the feed URL and the
 * feed key are secrets the server never answers, so the card masks them and
 * replaces them whole. The header the key rides in is configuration, so it
 * is shown.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './MarketIndexSourceSettings'
import type { GableMarketIndexSourceSettings } from './MarketIndexSourceSettings'
import { mount, update, flush, q, text, jsonResponse } from '../../../test/dom'

const BASE = {
  source: 'manual' as const,
  feed_url_set: true,
  feed_key_header: '',
  feed_key_set: true,
  staleness_days: 7,
  last_pull_at: null,
  last_pull_ok: null,
  last_pull_error: null,
  revision: 1,
}

let current: Record<string, unknown>
let puts: unknown[]
let refuse: Response | null

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  current = { ...BASE }
  puts = []
  refuse = null
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    if ((init?.method ?? 'GET') === 'PUT') {
      puts.push(JSON.parse(String(init!.body)))
      if (refuse) return refuse
      const body = puts[puts.length - 1] as Record<string, unknown>
      current = {
        ...current,
        ...body,
        feed_url_set: body.feed_url === undefined ? current.feed_url_set : body.feed_url !== '',
        feed_key_set: body.feed_key === undefined ? current.feed_key_set : body.feed_key !== '',
      }
      delete (current as Record<string, unknown>).feed_url
      delete (current as Record<string, unknown>).feed_key
      return jsonResponse(current)
    }
    return jsonResponse(current)
  }))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function open(): Promise<GableMarketIndexSourceSettings> {
  const el = await mount<GableMarketIndexSourceSettings>('gable-market-index-source-settings')
  await flush()
  await update(el, {})
  return el
}

async function click(el: GableMarketIndexSourceSettings, selector: string) {
  q<HTMLButtonElement>(el, selector).click()
  await flush()
  await update(el, {})
  await flush()
  await update(el, {})
}

describe('market index source card', () => {
  it('shows Manual and the seven day threshold for a fresh deployment', async () => {
    current = { ...BASE, source: 'manual', feed_url_set: false, feed_key_set: false }
    const el = await open()
    expect(text(q(el, '[data-testid="source-manual"]'))).toBe('')
    expect((q<HTMLInputElement>(el, '[data-testid="source-manual"]')).checked).toBe(true)
    expect(q<HTMLInputElement>(el, '[data-testid="staleness-days"]').value).toBe('7')
  })

  it('masks the feed URL and the key: Saved, Replace and Clear, and no value', async () => {
    const el = await open()
    expect(text(q(el, '[data-testid="feed-url-saved"]'))).toBe('Saved')
    expect(text(q(el, '[data-testid="feed-key-saved"]'))).toBe('Saved')
    expect(text(q(el, '[data-testid="feed-url-masked"]'))).not.toBe('')
    expect(text(q(el, '[data-testid="feed-key-masked"]'))).not.toBe('')
    expect(el.querySelector('input[name="feed_url"]')).toBeNull()
    expect(el.querySelector('input[name="feed_key"]')).toBeNull()
  })

  it('Replace reveals a password input and saves the new value', async () => {
    const el = await open()
    await click(el, '[data-testid="feed-key-replace"]')
    const input = q<HTMLInputElement>(el, 'input[name="feed_key"]')
    expect(input.type).toBe('password')
    expect(input.getAttribute('autocomplete')).toBe('off')
    expect(input.value).toBe('')
    input.value = 'fk_test_0002'
    input.dispatchEvent(new Event('input'))
    await update(el, {})
    await click(el, '[data-testid="save"]')
    expect((puts[0] as Record<string, unknown>).feed_key).toBe('fk_test_0002')
    expect(el.innerHTML).not.toContain('fk_test_0002')
  })

  it('a plain save leaves the two secrets out so the server keeps them', async () => {
    const el = await open()
    await click(el, '[data-testid="save"]')
    expect(puts).toHaveLength(1)
    const body = puts[0] as Record<string, unknown>
    expect('feed_key' in body).toBe(false)
    expect('feed_url' in body).toBe(false)
  })

  it('Clear sends an empty value', async () => {
    const el = await open()
    await click(el, '[data-testid="feed-key-clear"]')
    expect((puts[0] as Record<string, unknown>).feed_key).toBe('')
    expect(el.querySelector('[data-testid="feed-key-saved"]')).toBeNull()
  })

  it('shows the header the key rides in, and saves it', async () => {
    current = { ...BASE, feed_key_header: 'X-FB-Connect-Key' }
    const el = await open()
    expect(q<HTMLInputElement>(el, '[data-testid="feed-key-header"]').value).toBe('X-FB-Connect-Key')
    await click(el, '[data-testid="save"]')
    expect((puts[0] as Record<string, unknown>).feed_key_header).toBe('X-FB-Connect-Key')
  })

  it('shows the answer words on a refusal', async () => {
    refuse = jsonResponse({ error: { code: 'validation_failed', message: 'the request is not valid',
      details: [{ field: 'feed_url', message: 'feed URL must use https' }] } }, 400)
    const el = await open()
    await click(el, '[data-testid="save"]')
    expect(text(q(el, '[data-testid="error"]'))).toContain('the request is not valid')
  })

  it('shows no pull until one exists', async () => {
    const el = await open()
    expect(text(q(el, '[data-testid="last-pull"]'))).toContain('No pull yet')
  })
})
