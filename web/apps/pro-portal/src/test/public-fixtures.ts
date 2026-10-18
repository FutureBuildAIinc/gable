// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { PublicQuote } from '../public/public-view.ts'
import { dateTime } from './fixtures.ts'

export const publicToken = 'g'.repeat(43)
export const publicIds = {
  group: '12121212-1212-4121-8121-121212121212',
  lineA: '23232323-2323-4232-8232-232323232323',
  lineB: '34343434-3434-4343-8434-343434343434',
  lineC: '45454545-4545-4545-8454-454545454545',
  extra: '56565656-5656-4565-8565-565656565656',
}

export const publicQuoteFixture = {
  reference: 'CQ-1001',
  title: 'Deck and railing package',
  status: 'open',
  contractor: { name: 'Summit Frame Works', has_logo: false },
  customer_name: 'Homeowner',
  job_site_address: 'North gate, lot 4',
  valid_until: dateTime(4),
  note: 'Choose one decking option and approve the whole package when ready.',
  payment_terms: 'Deposit on acceptance, balance on completion.',
  hide_line_prices: false,
  groups: [{ id: publicIds.group, label: 'Decking option' }],
  lines: [
    { id: publicIds.lineA, group_id: publicIds.group, description: 'Cedar decking', quantity: '92', uom: 'EA', note: 'Within allowance', price_cents: 12000, selection: 'approved', cost_cents: 111111, markup: { kind: 'markup', bps: 3333 } },
    { id: publicIds.lineB, group_id: publicIds.group, description: 'Composite decking', quantity: '92', uom: 'EA', note: 'Upgrade', price_cents: 18000, selection: 'pending', dealer_cost_cents: 222222, applied: { tier: 'line', kind: 'margin', bps: 3333 } },
    { id: publicIds.lineC, description: 'Framing labour', quantity: '1', uom: 'JOB', price_cents: 30000, selection: 'pending', margin_bps: 3333 },
    { id: '67676767-6767-4676-8676-676767676767', description: 'Secret internal labour', quantity: '1', uom: 'JOB', price_cents: 444444, selection: 'pending', contractor_only: true },
  ],
  extras: [{ id: publicIds.extra, kind: 'overhead', description: 'Site overhead', price_cents: 10000, margin_cents: 555555 }],
  totals: { subtotal_cents: 60000, tax_cents: 6000, total_cents: 66000, deposit_cents: 19800, tax_mode: 'site_rate', tax_rate_bps: 1000, tax_note: 'Site tax', material_cost_cents: 666666 },
  requires_code: true,
  can_decide: true,
  selections_version: 1,
  dealer_cost_cents: 777777,
  margin_bps: 3333,
} as unknown as PublicQuote & Record<string, unknown>

export const signedPublicQuoteFixture: PublicQuote = {
  ...publicQuoteFixture,
  status: 'approved',
  lines: publicQuoteFixture.lines.filter((line) => line.id !== publicIds.lineC && line.description !== 'Secret internal labour').map((line) => ({ ...line, selection: line.id === publicIds.lineB ? 'declined' : 'approved' })),
  can_decide: false,
  approval: { decision: 'approved', signed_by: 'Customer Signoff', at: dateTime() },
  fulfilment: 'ordered',
}
