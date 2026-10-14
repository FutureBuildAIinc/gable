// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

export interface MillworkOption {
    id: string;
    category: string;
    name: string;
    /** Integer cents (ADR 0001 section 7); a negative adjustment discounts. */
    price_adjustment_cents: number;
    attributes: Record<string, unknown> | null;
    revision: number;
    created_at: string;
    updated_at: string;
}

export interface CreateOptionRequest {
    category: string;
    name: string;
    price_adjustment_cents: number;
    attributes?: Record<string, unknown>;
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
