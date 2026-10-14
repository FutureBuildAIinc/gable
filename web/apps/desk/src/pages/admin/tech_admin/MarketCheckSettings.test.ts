// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The Tech Admin Market check card: the webhook id is write only (Saved, Replace, Clear; never the value). */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './MarketCheckSettings'
import type { GableMarketCheckSettings } from './MarketCheckSettings'
import { mount, update, flush, q, text, jsonResponse } from '../../../test/dom'

const BASE = {
  connect_url: 'https://connect.example.test', webhook_id_saved: true, flow_name: 'Pricing flow', store_label: '',
  store_id: '', store_zip: '', band_pct: '5', margin_floor_pct: '15', configured: true,
}

let current: Record<string, unknown>
let puts: unknown[]
let refuse: Response | null

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  current = BASE
  puts = []
  refuse = null
  vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    if ((init?.method ?? 'GET') === 'PUT') {
      puts.push(JSON.parse(String(init!.body)))
      if (refuse) return refuse
      const body = puts[puts.length - 1] as Record<string, unknown>
      current = { ...current, ...body, webhook_id_saved: body.webhook_id === '' ? false : true }
      delete (current as Record<string, unknown>).webhook_id
      return jsonResponse(current)
    }
    return jsonResponse(current)
  }))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function open(): Promise<GableMarketCheckSettings> {
  const el = await mount<GableMarketCheckSettings>('gable-market-check-settings')
  await flush()
  await update(el, {})
  return el
}

async function click(el: GableMarketCheckSettings, selector: string) {
  q<HTMLButtonElement>(el, selector).click()
  await flush()
  await update(el, {})
  await flush()
  await update(el, {})
}

describe('Market check settings card', () => {
  it('shows Saved with Replace and Clear, and no id input, when an id is saved', async () => {
    const el = await open()
    expect(text(q(el, '[data-testid="webhook-saved"]'))).toBe('Saved')
    expect(el.querySelector('input[name="webhook_id"]')).toBeNull()
    expect(text(el)).toContain('Replace')
    expect(text(el)).toContain('Clear')
    expect(el.innerHTML).not.toContain('test-hook')
  })

  it('shows a password input with autocomplete off when none is saved', async () => {
    current = { ...BASE, webhook_id_saved: false, configured: false }
    const el = await open()
    const input = q<HTMLInputElement>(el, 'input[name="webhook_id"]')
    expect(input.type).toBe('password')
    expect(input.getAttribute('autocomplete')).toBe('off')
  })

  it('Replace reveals the write only input and saves the new id', async () => {
    const el = await open()
    await click(el, '[data-testid="replace-id"]')
    const input = q<HTMLInputElement>(el, 'input[name="webhook_id"]')
    expect(input.type).toBe('password')
    expect(input.value).toBe('')
    input.value = 'test-hook-0002'
    input.dispatchEvent(new Event('input'))
    await update(el, {})
    await click(el, '[data-testid="save"]')
    expect((puts[0] as Record<string, unknown>).webhook_id).toBe('test-hook-0002')
    expect(text(q(el, '[data-testid="webhook-saved"]'))).toBe('Saved')
    expect(el.innerHTML).not.toContain('test-hook-0002')
  })

  it('a plain save leaves the id out so the server keeps it', async () => {
    const el = await open()
    await click(el, '[data-testid="save"]')
    expect(puts).toHaveLength(1)
    expect('webhook_id' in (puts[0] as object)).toBe(false)
  })

  it('Clear sends an empty id', async () => {
    const el = await open()
    await click(el, '[data-testid="clear-id"]')
    expect((puts[0] as Record<string, unknown>).webhook_id).toBe('')
    expect(el.querySelector('[data-testid="webhook-saved"]')).toBeNull()
    expect(el.querySelector('input[name="webhook_id"]')).not.toBeNull()
  })

  it('shows the answer words on a refusal', async () => {
    refuse = jsonResponse({ error: { code: 'validation_failed', message: 'the request is not valid',
      details: [{ field: 'connect_url', message: 'must start with https://' }] } }, 400)
    const el = await open()
    await click(el, '[data-testid="save"]')
    expect(text(el)).toContain('must start with https://')
    expect(text(q(el, '[data-testid="error"]'))).toContain('the request is not valid')
  })
})
