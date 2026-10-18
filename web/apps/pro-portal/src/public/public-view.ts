// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { components } from '@gable/api-client'

export type PublicQuote = components['schemas']['CustomerQuotePublicView']
export type PublicQuoteChoices = components['schemas']['CustomerQuoteChoices']
export type PublicQuoteAccept = components['schemas']['CustomerQuoteAcceptRequest']
export type PublicQuoteDecline = components['schemas']['CustomerQuoteDeclineRequest']

export class PublicViewError extends Error {
  constructor() {
    super('This quote is not in a readable shape.')
    this.name = 'PublicViewError'
  }
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw new PublicViewError()
  return value as Record<string, unknown>
}

function text(value: unknown, fallback = ''): string {
  return typeof value === 'string' ? value : fallback
}

function bool(value: unknown): boolean {
  return value === true
}

function cents(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : undefined
}

function selection(value: unknown): 'pending' | 'approved' | 'declined' {
  return value === 'approved' || value === 'declined' ? value : 'pending'
}

function status(value: unknown): PublicQuote['status'] {
  return ['open', 'expired', 'approved', 'declined', 'confirmed', 'withdrawn'].includes(text(value))
    ? value as PublicQuote['status']
    : 'withdrawn'
}

function array(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

export function sanitizePublicQuote(value: unknown): PublicQuote {
  const root = record(value)
  const hidePrices = bool(root['hide_line_prices'])
  const totals = record(root['totals'])
  const taxMode = ['site_rate', 'included', 'at_checkout'].includes(text(totals['tax_mode']))
    ? totals['tax_mode'] as PublicQuote['totals']['tax_mode']
    : 'at_checkout'
  const groups = array(root['groups']).map((item) => {
    const group = record(item)
    return { id: text(group['id']), label: text(group['label']) }
  })
  const lines = array(root['lines']).filter((item) => record(item)['contractor_only'] !== true).map((item) => {
    const line = record(item)
    const price = hidePrices ? undefined : cents(line['price_cents'])
    return {
      id: text(line['id']),
      ...(line['group_id'] !== undefined ? { group_id: text(line['group_id']) } : {}),
      description: text(line['description']),
      quantity: text(line['quantity']),
      uom: text(line['uom']),
      ...(line['note'] !== undefined ? { note: text(line['note']) } : {}),
      ...(price !== undefined ? { price_cents: price } : {}),
      selection: selection(line['selection']),
    }
  })
  const extras = array(root['extras']).filter((item) => record(item)['contractor_only'] !== true).map((item) => {
    const extra = record(item)
    const price = hidePrices ? undefined : cents(extra['price_cents'])
    return {
      id: text(extra['id']),
      kind: extra['kind'] === 'overhead' ? 'overhead' as const : 'labour' as const,
      description: text(extra['description']),
      ...(price !== undefined ? { price_cents: price } : {}),
    }
  })
  const approvalValue = root['approval']
  const approval = typeof approvalValue === 'object' && approvalValue !== null ? record(approvalValue) : null
  const fulfilment = ['ordered', 'being_prepared', 'fulfilled', 'cancelled'].includes(text(root['fulfilment']))
    ? root['fulfilment'] as PublicQuote['fulfilment']
    : undefined
  return {
    reference: text(root['reference']),
    title: text(root['title']),
    status: status(root['status']),
    contractor: {
      name: text(record(root['contractor'])['name']),
      has_logo: bool(record(root['contractor'])['has_logo']),
    },
    customer_name: text(root['customer_name']),
    job_site_address: text(root['job_site_address']),
    valid_until: text(root['valid_until']),
    note: text(root['note']),
    payment_terms: text(root['payment_terms']),
    hide_line_prices: hidePrices,
    groups,
    lines,
    extras,
    totals: {
      subtotal_cents: cents(totals['subtotal_cents']) ?? 0,
      tax_cents: cents(totals['tax_cents']) ?? 0,
      total_cents: cents(totals['total_cents']) ?? 0,
      deposit_cents: cents(totals['deposit_cents']) ?? 0,
      tax_mode: taxMode,
      ...(cents(totals['tax_rate_bps']) !== undefined ? { tax_rate_bps: cents(totals['tax_rate_bps']) } : {}),
      tax_note: text(totals['tax_note']),
    },
    requires_code: bool(root['requires_code']),
    can_decide: bool(root['can_decide']),
    selections_version: Math.max(1, cents(root['selections_version']) ?? 1),
    ...(typeof root['notice'] === 'string' && root['notice'] !== '' ? { notice: root['notice'] } : {}),
    ...(approval !== null ? {
      approval: {
        decision: approval['decision'] === 'declined' ? 'declined' as const : 'approved' as const,
        signed_by: text(approval['signed_by']),
        at: text(approval['at']),
      },
    } : {}),
    ...(fulfilment !== undefined ? { fulfilment } : {}),
  }
}
