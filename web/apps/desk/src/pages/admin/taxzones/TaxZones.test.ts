// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The tax zone admin page on the wire contract (CP-10): the bare array the
 * zone list answers, the combined rate summed from the parts, the boundary
 * text a clerk reads, and the close action a used zone takes instead of a
 * delete.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './TaxZones'
import type { GableAdminTaxZones } from './TaxZones'
import { boundaryText, partsText } from './TaxZones'
import { combinedRatePercent, buildTaxZoneQuery } from '../../../services/TaxZoneService'
import { mountAsync, text, flush } from '../../../test/dom'
import type { TaxZone } from '../../../types/taxZone'

function zone(over: Partial<TaxZone> = {}): TaxZone {
  return {
    id: 'z1',
    country: 'US',
    jurisdiction_code: 'WA-KING-001',
    jurisdiction_type: 'CTY',
    jurisdiction_name: 'King County',
    zone_kind: 'POSTAL',
    sourcing: 'DESTINATION',
    effective_from: '2020-01-01',
    effective_to: null,
    data_source: 'State rate table',
    data_version: '2026 Q1',
    rates: [
      { name: 'STATE', rate: '0.065000' },
      { name: 'CITY', rate: '0.020000' },
    ].map((r) => ({ effective_from: '2020-01-01', ...r })) as TaxZone['rates'],
    boundaries: [{ country: 'US', postal_code_low: '98100', postal_code_high: '98199' }],
    ...over,
  }
}

describe('the tax zone helpers', () => {
  it('sums the parts into the combined percent', () => {
    expect(combinedRatePercent(zone().rates)).toBe('8.5')
    expect(combinedRatePercent([{ name: 'STATE', rate: '0.060000', effective_from: '2020-01-01' }])).toBe('6')
    expect(combinedRatePercent([])).toBe('0')
  })

  it('reads a range or a single code out of the boundaries', () => {
    expect(boundaryText(zone())).toBe('98100 to 98199')
    expect(boundaryText(zone({ boundaries: [{ country: 'US', postal_code_low: '98109', postal_code_high: '98109' }] }))).toBe('98109')
    expect(boundaryText(zone({ zone_kind: 'PROVINCE', province_code: 'BC', boundaries: [] }))).toBe('BC')
  })

  it('names each part with its own percent', () => {
    expect(partsText(zone())).toBe('STATE 6.5% + CITY 2%')
  })

  it('builds the strict zone list query', () => {
    expect(buildTaxZoneQuery()).toBe('')
    expect(buildTaxZoneQuery({ country: 'us' })).toBe('?country=US')
    expect(buildTaxZoneQuery({ country: 'US', jurisdiction_type: 'cty' })).toBe('?country=US&jurisdiction_type=CTY')
  })
})

describe('gable-admin-tax-zones', () => {
  let el: GableAdminTaxZones

  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async () =>
      new Response(JSON.stringify([zone()]), { status: 200, headers: { 'Content-Type': 'application/json' } })))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('lists the zones with their combined rate, parts and window', async () => {
    el = await mountAsync<GableAdminTaxZones>('gable-admin-tax-zones')
    await flush()
    const body = text(el)
    expect(body).toContain('King County')
    expect(body).toContain('8.5%')
    expect(body).toContain('STATE 6.5% + CITY 2%')
    expect(body).toContain('98100 to 98199')
    expect(body).toContain('2020-01-01 to now')
    expect(body).toContain('State rate table 2026 Q1')
  })

  it('marks a closed zone instead of offering the close action', async () => {
    vi.stubGlobal('fetch', vi.fn(async () =>
      new Response(JSON.stringify([zone({ effective_to: '2026-06-30' })]), { status: 200 })))
    el = await mountAsync<GableAdminTaxZones>('gable-admin-tax-zones')
    await flush()
    const body = text(el)
    expect(body).toContain('Closed')
    expect(body).toContain('2020-01-01') // effective_from, still shown
    expect(body).toContain('2026-06-30')
  })
})
