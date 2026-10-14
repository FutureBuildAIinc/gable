// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The portal invoice page after C2-3: its own float-dollar DTO, now with the
 * document number and the computed overdue flag. OVERDUE is no longer a status;
 * WRITTEN_OFF is labelled; dollars are formatted directly, never divided.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import './PortalInvoices'
import type { PortalInvoices } from './PortalInvoices'
import { portalStatusLabel } from './PortalInvoices'
import type { PortalInvoice } from '../../types/portal'
import { mountAsync, text, jsonResponse } from '../../test/dom'

function invoice(overrides: Partial<PortalInvoice> = {}): PortalInvoice {
  return {
    id: 'aaaaaaaa-1111-2222-3333-444444444444',
    number: 'IN-000042',
    is_overdue: false,
    order_id: 'bbbbbbbb-1111-2222-3333-444444444444',
    status: 'UNPAID',
    total_amount: 846.62,
    subtotal: 777.6,
    tax_amount: 69.02,
    payment_terms: 'NET30',
    due_date: '2026-09-10T00:00:00Z',
    paid_at: null,
    created_at: '2026-08-11T14:00:00Z',
    lines: [],
    ...overrides,
  }
}

describe('gable-portal-invoices', () => {
  let el: PortalInvoices
  afterEach(() => vi.unstubAllGlobals())

  async function mountWith(invoices: PortalInvoice[]) {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(invoices)))
    el = await mountAsync<PortalInvoices>('gable-portal-invoices')
    return el
  }

  it('shows the document number, not a made-up id prefix, and dollars as they arrive', async () => {
    await mountWith([invoice()])
    const body = text(el)
    expect(body).toContain('IN-000042')
    expect(body).not.toContain('INV-AAAAAAAA')
    expect(body).toContain('$846.62')
    expect(body).not.toContain('$8.47')
  })

  it('derives the Overdue badge from is_overdue and keeps the status label', async () => {
    await mountWith([invoice({ is_overdue: true }), invoice({ id: 'x', number: 'IN-000043' })])
    expect(el.querySelectorAll('[data-testid="portal-overdue"]').length).toBe(1)
    expect(text(el)).toContain('Unpaid')
  })

  it('labels a written off invoice and never shows an OVERDUE status', async () => {
    await mountWith([invoice({ status: 'WRITTEN_OFF', number: 'IN-000044' }), invoice({ status: 'PARTIAL', number: 'IN-000045' })])
    const body = text(el)
    expect(body).toContain('Written off')
    expect(body).toContain('Partial')
    expect(body).not.toContain('OVERDUE')
    expect(portalStatusLabel('WRITTEN_OFF')).toBe('Written off')
  })
})
