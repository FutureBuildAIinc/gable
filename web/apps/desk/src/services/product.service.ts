// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The product wire (ADR 0001, ADR 0006 sections 6 and 7.1): cursor lists, a quoted If-Match on
 * every PATCH, integer ten thousandths prices, decimal string quantities and the one error
 * envelope. Every failure is thrown as an ApiError.
 */
import type { Product, ProductCreate, ProductPage } from '../types/product';
import { fetchWithAuth } from './fetchClient';
import { ifMatch, parseApiError } from './apiError';

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

export interface ListProductsParams {
    /** 1 to 200; the server default applies when left out. */
    limit?: number;
    cursor?: string | null;
    /** Ask for the total row count (include=total). */
    includeTotal?: boolean;
}

/** The query string of a product list: only cursor, limit and include are accepted (anything else is a 400). */
export function buildProductQuery(params: ListProductsParams = {}): string {
    const q = new URLSearchParams();
    if (params.limit) q.set('limit', String(params.limit));
    if (params.cursor) q.set('cursor', params.cursor);
    if (params.includeTotal) q.set('include', 'total');
    const s = q.toString();
    return s ? `?${s}` : '';
}

/** The most products listAllProducts collects (pages of 200), so a huge catalog never hangs a picker. */
export const LIST_ALL_PRODUCTS_CAP = 2000;

/**
 * The products whose SKU, description, UPC or vendor contain the text, case blind. The list route has
 * no search parameter (an unknown query parameter is a 400), so pickers and the yard lookup filter
 * the catalog they loaded with listAllProducts.
 */
export function matchProducts(products: Product[], query: string): Product[] {
    const term = query.trim().toLowerCase();
    if (term === '') return products;
    return products.filter(p =>
        p.sku.toLowerCase().includes(term) ||
        p.description.toLowerCase().includes(term) ||
        (p.upc ?? '').toLowerCase().includes(term) ||
        (p.vendor ?? '').toLowerCase().includes(term),
    );
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (!response.ok) throw await parseApiError(response, fallback);
    return response;
}

async function patch<T>(id: string, path: string, body: T, revision: number, fallback: string): Promise<Product> {
    const response = await expectOk(
        await fetchWithAuth(`${API_URL}/api/v1/products/${id}/${path}`, {
            method: 'PATCH',
            headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
            body: JSON.stringify(body),
        }),
        fallback,
    );
    return response.json();
}

export const ProductService = {
    async listProducts(params: ListProductsParams = {}): Promise<ProductPage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/products${buildProductQuery(params)}`),
            'Failed to fetch products',
        );
        return response.json();
    },

    /**
     * Pages by cursor until the last page or LIST_ALL_PRODUCTS_CAP products, for the pickers, the
     * omnibar and the inventory table, which filter the whole catalog on the client.
     */
    async listAllProducts(): Promise<Product[]> {
        const all: Product[] = [];
        let cursor: string | null = null;
        while (all.length < LIST_ALL_PRODUCTS_CAP) {
            const page: ProductPage = await ProductService.listProducts({ limit: 200, cursor });
            all.push(...page.items);
            if (!page.next_cursor) break;
            cursor = page.next_cursor;
        }
        return all.slice(0, LIST_ALL_PRODUCTS_CAP);
    },

    async createProduct(product: ProductCreate): Promise<Product> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/products`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(product),
            }),
            'Failed to create product',
        );
        return response.json();
    },

    async getProduct(id: string): Promise<Product> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/products/${id}`),
            'Failed to fetch product',
        );
        return response.json();
    },

    /** Writes the margin rules on the revision the page loaded; answers the whole product at its new revision. */
    async updateMargins(id: string, margins: { target_margin: number; commission_rate: number }, revision: number): Promise<Product> {
        return patch(id, 'margins', margins, revision, 'Failed to update margins');
    },

    /** Publishes (days) or clears (null) the lead time on the revision the page loaded. */
    async updateLeadTime(id: string, leadTimeDays: number | null, revision: number): Promise<Product> {
        return patch(id, 'lead-time', { lead_time_days: leadTimeDays }, revision, 'Failed to update lead time');
    },

    /**
     * Writes the PIM's canonical parametric geometry for a product on the revision the page loaded.
     *
     * The payload is serialized as-is so that a `null` field reaches the wire
     * as JSON `null` and clears the column. Do not add `omitempty`-style
     * pruning here: a dropped field and a `0` are both wrong, and both are
     * indistinguishable from real data once they reach the load planner.
     * Answers the whole product at its new revision.
     */
    async updateDimensions(id: string, geometry: ProductGeometry, revision: number): Promise<Product> {
        return patch(
            id,
            'dimensions',
            {
                length_in: geometry.length_in,
                width_in: geometry.width_in,
                height_in: geometry.height_in,
                stackable: geometry.stackable,
                geometry_source: geometry.geometry_source ?? null,
            },
            revision,
            'Failed to update dimensions',
        );
    },
};
