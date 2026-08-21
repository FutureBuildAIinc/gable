// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** Types for AI Material List parsing */

export interface MatchedProduct {
    product_id: string;
    sku: string;
    description: string;
    uom: string;
    base_price: number;
}

export interface ParsedItem {
    raw_text: string;
    matched_product: MatchedProduct | null;
    quantity: number;
    uom: string;
    confidence: number;
    is_special_order: boolean;
    alternatives: MatchedProduct[];
}

export interface ParseResponse {
    items: ParsedItem[];
    source_image: string;
    parse_time_ms: number;
    item_count: number;

    /**
     * True when `items` did NOT come from the uploaded document.
     *
     * The extractor needs an AI provider. When one is not configured — which is
     * the default — the backend returns a canned material list rather than an
     * error, so the upload flow stays usable offline. That list has nothing to
     * do with the file the user submitted.
     *
     * This panel shows the user's real upload next to the item table, so
     * without this flag a canned list reads as a successful parse of their
     * document. Never present a synthetic result as extracted.
     */
    synthetic?: boolean;

    /** Why the result is synthetic, for display. Set only when `synthetic`. */
    synthetic_reason?: string;
}
