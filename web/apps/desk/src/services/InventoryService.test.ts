// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The inventory levels list is the cursor envelope (ADR 0001, ADR 0006 7.2);
 * a product stocked in more than 200 rows must not silently lose the rest
 * from the desk. The desk's getInventoryByProduct follows next_cursor with a
 * bounded walk, capped so a broken cursor cannot loop forever.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { InventoryService } from './InventoryService'
import type { Inventory } from '../types/product'

let fetchMock: ReturnType<typeof vi.fn>

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function urlOf(call: number): URL {
    return new URL(fetchMock.mock.calls[call][0] as string, 'http://localhost')
}

function row(id: string): Inventory {
    return {
        id,
        product_id: 'p-1',
        location_id: null,
        location_name: 'wl-inv-' + id,
        quantity: '1',
        allocated: '0',
        available: '1',
        uom: 'PCS',
        updated_at: '2026-01-01T00:00:00.000000Z',
    }
}

beforeEach(() => {
    fetchMock = vi.fn(async () => jsonResponse({}))
    vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => vi.unstubAllGlobals())

describe('InventoryService.getInventoryByProduct', () => {
    it('walks the cursor past the first page so a product stocked in many rows is read whole', async () => {
        fetchMock
            .mockImplementationOnce(async () => jsonResponse({
                items: [row('r-1'), row('r-2')],
                next_cursor: 'c2',
                limit: 200,
            }))
            .mockImplementationOnce(async () => jsonResponse({
                items: [row('r-3')],
                next_cursor: null,
                limit: 200,
            }))
        const all = await InventoryService.getInventoryByProduct('p-1')
        expect(all.map(r => r.id)).toEqual(['r-1', 'r-2', 'r-3'])
        // The second call rides the cursor the server offered.
        expect(urlOf(1).searchParams.get('cursor')).toBe('c2')
        expect(urlOf(0).searchParams.get('cursor')).toBeNull()
    })

    it('stops at the cap when the server keeps offering a fresh cursor', async () => {
        // The walk is bounded: a server that keeps minting next_cursor cannot
        // hang the desk past the cap (2000 rows in pages of 200: ten calls).
        let n = 0
        fetchMock.mockImplementation(async () => {
            n++
            return jsonResponse({
                items: Array.from({ length: 200 }, (_, i) => row(`p${n}-${i}`)),
                next_cursor: `c${n}`,
                limit: 200,
            })
        })
        const all = await InventoryService.getInventoryByProduct('p-1')
        expect(all.length).toBe(2000)
        expect(fetchMock).toHaveBeenCalledTimes(10)
    })

    it('stops on an empty page that still carries a cursor', async () => {
        fetchMock.mockImplementation(async () => jsonResponse({ items: [], next_cursor: 'again', limit: 200 }))
        const all = await InventoryService.getInventoryByProduct('p-1')
        expect(all).toEqual([])
        expect(fetchMock).toHaveBeenCalledTimes(1)
    })

    it('stops when the server hands back the cursor it was just sent', async () => {
        fetchMock
            .mockImplementationOnce(async () => jsonResponse({ items: [row('r-1')], next_cursor: 'same', limit: 200 }))
            .mockImplementation(async () => jsonResponse({ items: [row('r-2')], next_cursor: 'same', limit: 200 }))
        const all = await InventoryService.getInventoryByProduct('p-1')
        expect(all.map(r => r.id)).toEqual(['r-1', 'r-2'])
        expect(fetchMock).toHaveBeenCalledTimes(2)
    })

    it('answers an empty list when the first page is empty', async () => {
        fetchMock.mockImplementationOnce(async () => jsonResponse({ items: [], next_cursor: null, limit: 200 }))
        expect(await InventoryService.getInventoryByProduct('p-1')).toEqual([])
    })

    it('throws when a later page fails, rather than answering a partial list', async () => {
        fetchMock
            .mockImplementationOnce(async () => jsonResponse({ items: [row('r-1')], next_cursor: 'c2', limit: 200 }))
            .mockImplementationOnce(async () => jsonResponse({ error: { code: 'internal_error' } }, 500))
        await expect(InventoryService.getInventoryByProduct('p-1')).rejects.toThrow('Failed to fetch inventory')
    })

    it('sends the product_id filter on every page of the walk', async () => {
        fetchMock
            .mockImplementationOnce(async () => jsonResponse({
                items: [row('r-1')],
                next_cursor: 'c2',
                limit: 200,
            }))
            .mockImplementationOnce(async () => jsonResponse({
                items: [],
                next_cursor: null,
                limit: 200,
            }))
        await InventoryService.getInventoryByProduct('p-42')
        for (let i = 0; i < fetchMock.mock.calls.length; i++) {
            expect(urlOf(i).searchParams.get('product_id')).toBe('p-42')
        }
    })
})
