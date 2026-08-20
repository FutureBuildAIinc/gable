// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

export type UOM =
    | 'PCS'
    | 'EA'
    | 'LF'
    | 'SF'
    | 'BF'
    | 'MBF'
    | 'SQ'
    | 'BOX'
    | 'CTN'
    | 'RL'
    | 'GAL'
    | 'LBS'
    | 'BAG'
    | 'BUNDLE'
    | 'PAIR'
    | 'SET';

export interface Product {
    id: string;
    sku: string;
    description: string;
    uom_primary: UOM;
    base_price: number;
    vendor?: string;         // Display name (denormalized)
    vendor_id?: string;      // Canonical FK -> vendors.id
    upc?: string;
    weight_lbs?: number;
    // Canonical parametric geometry (inches). `null` means "no geometry
    // recorded for this SKU" and is NOT the same as 0 — AI_LM's load planner
    // falls back to its own defaults only for null, and would treat a 0 as a
    // real zero-volume box. Keep these nullable; see migration 080.
    length_in?: number | null;
    width_in?: number | null;
    height_in?: number | null;
    stackable?: boolean | null;       // null = unknown
    geometry_source?: string | null;  // 'parametric' (future: 'mesh')
    reorder_point?: number;
    reorder_qty?: number;
    total_quantity?: number;
    total_allocated?: number;
    average_unit_cost: number;
    target_margin: number;
    commission_rate: number;
    created_at: string;
    updated_at: string;
}

export interface ReorderAlert {
    product_id: string;
    sku: string;
    description: string;
    vendor?: string;
    vendor_id?: string;
    reorder_point: number;
    reorder_qty: number;
    current_stock: number;
    deficit: number;
}

export interface Inventory {
    id: string;
    product_id: string;
    location: string; // Deprecated? Or just path?
    location_id?: string;
    location_name?: string;
    quantity: number;
    allocated?: number;
    updated_at: string;
}
