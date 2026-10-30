// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The Tech Admin tax setup panel (CP-14 A1): the GST/HST number, the small supplier switch, and the standing warning. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './TaxSetup'
import type { GableTaxSetup } from './TaxSetup'
import { mount, update, flush, q, text, jsonResponse } from '../../../test/dom'

const PROFILE = {
  legal_name: 'Gable Building Supply Ltd', trading_name: 'Gable Building Supply', phone: '', email: '', website: '',
  remit_to: '', locale: 'en-CA', gst_hst_number: '', gst_hst_not_registered: false, logo_url: null, revision: 4,
  updated_at: '2026-01-01T00:00:00Z',
}

let profile: Record<string, unknown>
let setup: Record<string, unknown>
let puts: { body: Record<string, unknown>; ifMatch: string | null }[]
let refuse: Response | null

beforeEach(() => {
  localStorage.setItem('token', 'test-token')
  profile = { ...PROFILE }
  setup = { canadian: true, gst_hst_number_missing: true, unzoned_provinces: [] }
  puts = []
  refuse = null
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.includes('/api/v1/dealer-profile') && (init?.method ?? 'GET') === 'PUT') {
      const body = JSON.parse(String(init!.body)) as Record<string, unknown>
      puts.push({ body, ifMatch: new Headers(init!.headers).get('If-Match') })
      if (refuse) return refuse
      profile = { ...profile, ...body, gst_hst_number: String(body.gst_hst_number).replace(/[\s-]/g, '').toUpperCase(), revision: Number(profile.revision) + 1 }
      setup = { ...setup, gst_hst_number_missing: profile.gst_hst_number === '' && !profile.gst_hst_not_registered }
      return jsonResponse(profile)
    }
    if (url.includes('/api/v1/dealer-profile')) return jsonResponse(profile)
    if (url.includes('/api/v1/tax/setup')) return jsonResponse(setup)
    return jsonResponse({}, 404)
  }))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function open(): Promise<GableTaxSetup> {
  const el = await mount<GableTaxSetup>('gable-tax-setup')
  await flush()
  await update(el, {})
  return el
}

async function settle(el: GableTaxSetup) {
  await flush()
  await update(el, {})
  await flush()
  await update(el, {})
}

describe('Tax setup panel', () => {
  it('names the missing GST/HST number as a standing warning when the dealer is registered and none is on file', async () => {
    const el = await open()
    expect(text(q(el, '[data-testid="setup-warning-gst-hst"]'))).toContain('GST/HST number')
  })

  it('shows no warning once a number is on file, printed the way CRA writes it', async () => {
    profile = { ...PROFILE, gst_hst_number: '123456789RT0001' }
    setup = { ...setup, gst_hst_number_missing: false }
    const el = await open()
    expect(el.querySelector('[data-testid="setup-warning-gst-hst"]')).toBeNull()
    expect(text(q(el, '[data-testid="gst-hst-on-file"]'))).toContain('123456789 RT0001')
  })

  it('shows no warning when the dealer is marked not registered', async () => {
    profile = { ...PROFILE, gst_hst_not_registered: true }
    setup = { ...setup, gst_hst_number_missing: false }
    const el = await open()
    expect(el.querySelector('[data-testid="setup-warning-gst-hst"]')).toBeNull()
  })

  it('names each province of Canadian ship-tos with no tax zone, beside the number warning (item 365)', async () => {
    setup = { ...setup, unzoned_provinces: [{ province: 'AB', name: 'Alberta', ship_tos: 3 }] }
    const el = await open()
    const words = text(q(el, '[data-testid="setup-warning-zone-AB"]'))
    expect(words).toContain('No tax zone covers Alberta (3 ship-tos)')
    expect(words).toContain('a delivery there is refused')
    expect(el.querySelector('[data-testid="setup-warning-gst-hst"]')).not.toBeNull()
  })

  it('shows no warnings for a dealer with no Canadian branch', async () => {
    setup = { canadian: false, gst_hst_number_missing: false, unzoned_provinces: [] }
    const el = await open()
    expect(el.querySelector('[data-testid="setup-warnings"]')).toBeNull()
  })

  it('saves the typed number with the profile revision and the rest of the profile unchanged', async () => {
    const el = await open()
    const input = q<HTMLInputElement>(el, 'input[name="gst_hst_number"]')
    input.value = '123 456 789 RT0001'
    input.dispatchEvent(new Event('input'))
    q<HTMLButtonElement>(el, '[data-testid="save-gst-hst"]').click()
    await settle(el)
    expect(puts).toHaveLength(1)
    expect(puts[0].ifMatch).toBe('"4"')
    expect(puts[0].body).toMatchObject({ legal_name: 'Gable Building Supply Ltd', locale: 'en-CA', gst_hst_number: '123 456 789 RT0001', gst_hst_not_registered: false })
    expect(el.querySelector('[data-testid="setup-warning-gst-hst"]')).toBeNull()
  })

  it('shows the server refusal beside the number field', async () => {
    refuse = jsonResponse({ error: { code: 'VALIDATION_FAILED', message: 'one or more fields failed validation',
      details: [{ field: 'gst_hst_number', message: 'must be a GST/HST number: nine digits, RT, four digits, for example 123456789 RT0001' }] } }, 400)
    const el = await open()
    const input = q<HTMLInputElement>(el, 'input[name="gst_hst_number"]')
    input.value = '12345678RT0001'
    input.dispatchEvent(new Event('input'))
    q<HTMLButtonElement>(el, '[data-testid="save-gst-hst"]').click()
    await settle(el)
    expect(text(q(el, '[data-testid="gst-hst-error"]'))).toContain('nine digits, RT, four digits')
  })

  it('marking the dealer not registered clears the number and hides the field', async () => {
    profile = { ...PROFILE }
    const el = await open()
    q<HTMLInputElement>(el, 'input[name="gst_hst_not_registered"]').click()
    await settle(el)
    expect(el.querySelector('input[name="gst_hst_number"]')).toBeNull()
    q<HTMLButtonElement>(el, '[data-testid="save-gst-hst"]').click()
    await settle(el)
    expect(puts[0].body).toMatchObject({ gst_hst_number: '', gst_hst_not_registered: true })
  })
})
