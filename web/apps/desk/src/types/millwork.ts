// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { ConfiguredConfiguration } from './configurator';

export interface MillworkOption {
    id: string;
    category: string;
    name: string;
    /** Integer cents (ADR 0001 section 7); a negative adjustment discounts. */
    price_adjustment_cents: number;
    /**
     * What the option contributes to a configured item's price when it is the
     * one naming the item (a door's door type, a board's species). Zero on an
     * option that only adjusts.
     */
    base_price_cents: number;
    /** The same contribution to the item's cost. */
    base_cost_cents: number;
    /** What the option adds to the configured item's cost on top of the base. */
    cost_adjustment_cents: number;
    attributes: Record<string, unknown> | null;
    revision: number;
    created_at: string;
    updated_at: string;
}

export interface CreateOptionRequest {
    category: string;
    name: string;
    price_adjustment_cents: number;
    base_price_cents?: number;
    base_cost_cents?: number;
    cost_adjustment_cents?: number;
    attributes?: Record<string, unknown>;
}

export interface UpdateOptionRequest {
    category: string;
    name: string;
    price_adjustment_cents: number;
    base_price_cents?: number;
    base_cost_cents?: number;
    cost_adjustment_cents?: number;
    attributes?: Record<string, unknown>;
    /** The revision the caller read, when the body carries it. */
    revision?: number;
}

/** One option's contribution to a configured item's price. */
export interface MillworkPriceLine {
    category: string;
    name: string;
    price_cents: number;
    cost_cents: number;
}

/**
 * The priced configured item the server answers. The money is in
 * ten-thousandths of the major unit, the scale the documents carry their
 * prices in.
 */
export interface EstimatedItem {
    /** The deterministic NS- SKU. */
    sku: string;
    /** The short description the documents carry. */
    description: string;
    /** The product row the configured item became. */
    product_id: string;
    unit_price_ten_thousandths: number;
    unit_cost_ten_thousandths: number;
    /** The canonical configuration the price came from. */
    configuration_json: ConfiguredConfiguration;
    breakdown: MillworkPriceLine[];
}

/** The body of the estimate route: the configuration to price. */
export interface EstimateRequest {
    product_type: string;
    selections: Record<string, string>;
    width?: number;
    height?: number;
}

/** One page of the options list envelope. */
export interface MillworkOptionPage {
    items: MillworkOption[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}

export interface MillworkConfiguration {
    doorType: MillworkOption | null;
    material: MillworkOption | null;
    glass: MillworkOption | null;
    width: number;
    height: number;
}
