// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** QE-01-b: the version beside the number, the Revise button's states, and the revision history panel. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './QuoteDetail'
import type { GableQuoteDetail } from './QuoteDetail'
import { mount, flush, update, q, text, jsonResponse } from '../../test/dom'
import { router } from '../../lib/router.ts'
import { versionLabel, canRevise, changedLines } from './quoteRevisions'
import type { QuoteRevision } from '../../types/quote'

const line = (sku: string, quantity: string, price: number) => ({
    id: `${sku}-1`, quote_id: 'q1', product_id: null, sku, description: `${sku} stud`, customer_note: null,
    quantity, uom: 'PCS', price_uom: 'PCS', uom_qty: '1', price_uom_qty: '1',
    unit_price_ten_thousandths: price, line_total_cents: price, stock_uom: null, stock_quantity: null,
    tally: null, unit_cost_ten_thousandths: 0, created_at: '2026-01-01T00:00:00Z',
})

function quote(status: string, version: number) {
    return {
        id: 'q1', number: 'Q-000123', status, revision: 4, version_number: version, source: 'manual',
        customer_id: 'c0000000-0000-4000-8000-000000000001', customer_name: 'Test Builder Co',
        created_at: '2026-01-01T00:00:00Z', lines: [line('2X4-8', '10', 55000)],
        total_cents: 55000, freight_cents: 0, delivery_type: 'pickup',
    }
}

function revision(version: number, quantity: string, price = 55000): QuoteRevision {
    return {
        id: `r${version}`, quote_id: 'q1', version_number: version, created_by: '', reason: '',
        created_at: `2026-01-0${version}T00:00:00Z`,
        snapshot: { ...quote('sent', version), lines: [line('2X4-8', quantity, price)], total_cents: price },
    } as unknown as QuoteRevision
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
    localStorage.setItem('token', 'test-token')
})
afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    document.body.innerHTML = ''
})

describe('version words', () => {
    it('carries the version beside the number the moment it moves past the first', () => {
        expect(versionLabel('Q-001042', 1)).toBe('Q-001042')
        expect(versionLabel('Q-001042', 2)).toBe('Q-001042 v2')
        expect(versionLabel('Q-001042', 7)).toBe('Q-001042 v7')
        expect(versionLabel('Q-001042', undefined)).toBe('Q-001042')
    })

    it('lets a dealer revise a sent, rejected or expired quote, never a draft or an accepted one', () => {
        expect(canRevise('sent')).toBe(true)
        expect(canRevise('rejected')).toBe(true)
        expect(canRevise('expired')).toBe(true)
        expect(canRevise('draft')).toBe(false)
        expect(canRevise('accepted')).toBe(false)
    })
})

describe('changedLines', () => {
    it('names the lines that changed from the version before, matched by SKU', () => {
        const changes = changedLines(revision(1, '10'), revision(2, '20'))
        expect(changes).toHaveLength(1)
        expect(changes[0]).toMatchObject({ sku: '2X4-8', change: 'quantity', fromQuantity: '10', toQuantity: '20' })
    })

    it('says added and removed lines, and a price move on its own', () => {
        const withOsb = (v: number) => ({
            ...revision(v, '10'),
            snapshot: {
                ...revision(v, '10').snapshot!,
                lines: v === 1 ? [line('2X4-8', '10', 55000), line('OSB-48', '5', 20000)] : [line('2X4-8', '10', 60000)],
            },
        } as QuoteRevision)
        const changes = changedLines(withOsb(1), withOsb(2))
        const price = changes.find(c => c.sku === '2X4-8')!
        expect(price.change).toBe('price')
        expect(price.fromPrice).toBe(55000)
        expect(price.toPrice).toBe(60000)
        const removed = changes.find(c => c.sku === 'OSB-48')!
        expect(removed.change).toBe('removed')
        expect(removed.fromQuantity).toBe('5')
    })

    it('names a quantity and price move together, and nothing on the first version', () => {
        expect(changedLines(undefined, revision(1, '10'))).toEqual([])
        const both = changedLines(revision(1, '10', 55000), revision(2, '20', 60000))
        expect(both[0].change).toBe('quantity and price')
    })
})

describe('QuoteDetail version and Revise', () => {
    function routes(status: string, version: number, revisions: QuoteRevision[]) {
        fetchMock = vi.fn(async (url: string) => {
            const path = new URL(url, 'http://localhost').pathname
            if (path === '/api/v1/quotes/q1/revisions') {
                return jsonResponse({ items: [...revisions].reverse(), next_cursor: null, limit: 50 })
            }
            if (path === '/api/v1/quotes/q1/revise') {
                return jsonResponse(quote('draft', version + 1))
            }
            if (path === '/api/v1/quotes/q1') return jsonResponse(quote(status, version))
            return jsonResponse({ message: 'none' }, 404)
        })
        vi.stubGlobal('fetch', fetchMock)
    }

    async function detail() {
        const el = await mount<GableQuoteDetail>('gable-quote-detail', { routeId: 'q1' } as Partial<GableQuoteDetail>)
        await flush(); await flush(); await flush()
        await update(el, {})
        return el
    }

    const reviseButton = (el: HTMLElement) => el.querySelector<HTMLButtonElement>('[data-testid="quote-revise"]')

    it('shows the number with its version and the sent versions with their changes', async () => {
        routes('sent', 2, [revision(1, '10'), revision(2, '20')])
        const el = await detail()
        expect(text(q(el, '[data-testid="quote-number-version"]'))).toBe('Quote Q-000123 v2')
        const history = q(el, '[data-testid="quote-revision-history"]')
        expect(text(history)).toContain('Revision history')
        const rows = el.querySelectorAll('[data-testid="quote-revision-row"]')
        expect(rows.length).toBe(2)
        expect(text(rows[0])).toContain('v1')
        expect(text(rows[0])).toContain('The first version')
        expect(text(rows[1])).toContain('v2')
        expect(text(rows[1])).toContain('Quantity 10 to 20')
        expect(text(rows[1])).toContain('$550.00')
    })

    it('offers Revise on a sent, rejected or expired quote, never on a draft or an accepted one', async () => {
        for (const [status, want] of [['sent', true], ['rejected', true], ['expired', true], ['draft', false], ['accepted', false]] as const) {
            routes(status, 1, [revision(1, '10')])
            const el = await detail()
            if (want) {
                expect(reviseButton(el), `Revise on a ${status} quote`).toBeTruthy()
            } else {
                expect(reviseButton(el), `no Revise on a ${status} quote`).toBeNull()
            }
            document.body.innerHTML = ''
        }
    })

    it('revise posts the loaded revision and opens the builder', async () => {
        routes('sent', 1, [])
        const el = await detail()
        const pushes: string[] = []
        const spy = vi.spyOn(router, 'navigate').mockImplementation((p: string) => { pushes.push(p) })
        try {
            reviseButton(el)!.click()
            await flush(); await flush(); await flush()
            const call = fetchMock.mock.calls.find(([u]) => String(u).endsWith('/api/v1/quotes/q1/revise'))
            expect(call, 'the revise call was made').toBeDefined()
            expect((call![1] as RequestInit).method).toBe('POST')
            expect(new Headers((call![1] as RequestInit).headers).get('If-Match')).toBe('"4"')
            expect(pushes[0]).toBe('/quotes/q1/edit')
        } finally {
            spy.mockRestore()
        }
    })
})
