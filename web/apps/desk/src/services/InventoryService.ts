// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { Inventory } from '../types/product';
import { fetchWithAuth } from './fetchClient';

export interface StockAdjustmentRequest {
    product_id: string;
    location_id?: string;
    quantity: number;
    reason: string;
    is_delta: boolean;
}

export interface StockMovementRequest {
    product_id: string;
    from_location_id: string;
    to_location_id: string;
    quantity: number;
    reason: string;
    is_delta?: boolean; // Added matching what likely exists or removing
}

const API_URL = import.meta.env.VITE_API_URL || '';

/** The most inventory rows getInventoryByProduct collects (pages of 200), so a broken cursor cannot loop forever. */
const LIST_ALL_INVENTORY_CAP = 2000;

interface InventoryPage {
    items: Inventory[];
    next_cursor?: string | null;
}

export const InventoryService = {
    async adjustStock(data: StockAdjustmentRequest): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/inventory/adjust`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data),
        });
        if (!response.ok) {
            throw new Error('Failed to adjust stock');
        }
    },

    async transferStock(data: StockMovementRequest): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/inventory/transfer`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data),
        });
        if (!response.ok) {
            throw new Error('Failed to transfer stock');
        }
    },

    async getInventoryByProduct(productId: string): Promise<Inventory[]> {
        // The levels list is the cursor envelope (ADR 0006 7.2): pages of 200,
        // a next_cursor until the last page. A product stocked in more than
        // 200 rows must not silently lose the rest from the desk, so we walk
        // every page next_cursor points at, capped at LIST_ALL_INVENTORY_CAP
        // so a broken cursor cannot hang the desk.
        const all: Inventory[] = [];
        let cursor: string | null = null;
        while (all.length < LIST_ALL_INVENTORY_CAP) {
            const params = new URLSearchParams({ product_id: productId, limit: '200' });
            if (cursor) params.set('cursor', cursor);
            const response = await fetchWithAuth(`${API_URL}/api/v1/inventory?${params.toString()}`);
            if (!response.ok) {
                throw new Error('Failed to fetch inventory');
            }
            const page = await response.json() as InventoryPage;
            all.push(...page.items);
            if (!page.next_cursor) break;
            cursor = page.next_cursor;
        }
        return all.slice(0, LIST_ALL_INVENTORY_CAP);
    }
};
