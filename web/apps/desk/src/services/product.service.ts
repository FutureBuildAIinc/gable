// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { Product } from '../types/product';
import { fetchWithAuth } from './fetchClient';

const API_URL = import.meta.env.VITE_API_URL || '';

/**
 * The mutable slice of a product's canonical parametric geometry.
 *
 * `null` is a value, not an absence: it means "no geometry recorded". It has to
 * be sent explicitly rather than dropped from the payload, because the backend
 * clears the column to SQL NULL for a null field and AI_LM distinguishes NULL
 * (fall back to its own defaults) from 0 (a real zero-size box). Never
 * substitute 0 or false for "the operator left this blank".
 */
export interface ProductGeometry {
    length_in: number | null;
    width_in: number | null;
    height_in: number | null;
    stackable: boolean | null;
    geometry_source?: string | null;
}

export const ProductService = {
    async getProducts(): Promise<Product[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/products`);
        if (!response.ok) {
            throw new Error('Failed to fetch products');
        }
        const payload = await response.json();
        // Backend returns a paged envelope: { data: Product[], total, limit, offset }.
        // Older endpoints returned a bare array; accept both shapes.
        return Array.isArray(payload) ? payload : (payload?.data ?? []);
    },

    async createProduct(product: Omit<Product, 'id' | 'created_at' | 'updated_at'>): Promise<Product> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/products`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify(product),
        });

        if (!response.ok) {
            const errorText = await response.text();
            throw new Error(errorText || 'Failed to create product');
        }

        return response.json();
    },

    async getProduct(id: string): Promise<Product> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/products/${id}`);
        if (!response.ok) {
            throw new Error('Failed to fetch product');
        }
        return response.json();
    },

    /**
     * Writes the PIM's canonical parametric geometry for a product.
     *
     * The payload is serialized as-is so that a `null` field reaches the wire
     * as JSON `null` and clears the column. Do not add `omitempty`-style
     * pruning here: a dropped field and a `0` are both wrong, and both are
     * indistinguishable from real data once they reach the load planner.
     */
    async updateDimensions(id: string, geometry: ProductGeometry): Promise<ProductGeometry> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/products/${id}/dimensions`, {
            method: 'PATCH',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({
                length_in: geometry.length_in,
                width_in: geometry.width_in,
                height_in: geometry.height_in,
                stackable: geometry.stackable,
                geometry_source: geometry.geometry_source ?? null,
            }),
        });

        if (!response.ok) {
            const errorText = await response.text();
            throw new Error(errorText || 'Failed to update dimensions');
        }

        return response.json();
    },
};
