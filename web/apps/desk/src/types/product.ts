// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The product wire types come from the generated contract (core/api/fragments/product.yaml
// through @gable/api-client); nothing here restates a field by hand. A price is an integer
// in ten thousandths of a dollar per stocking unit (*_ten_thousandths), a quantity is a decimal
// string in the stocking unit, and every product carries the revision a write must name (If-Match).
// Geometry fields stay nullable on purpose: null means "no geometry recorded for this SKU" and is
// NOT the same as 0 (AI_LM's load planner falls back to its own defaults only for null).

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type UOM = Schemas['UOM'];
export type Product = Schemas['ProductView'];
export type ProductCreate = Schemas['ProductCreate'];
export type ProductPage = Schemas['ProductPage'];
export type ProductMarginUpdate = Schemas['ProductMarginUpdate'];
export type ProductDimensionsUpdate = Schemas['ProductDimensionsUpdate'];
export type ProductLeadTimeUpdate = Schemas['ProductLeadTimeUpdate'];
export type ReorderAlert = Schemas['ReorderAlert'];
export type ReorderAlertList = Schemas['ReorderAlertList'];

/** Every unit the wire accepts, in the order a picker shows them. The Record keys must match the contract's UOM exactly. */
const UOM_SET: Record<UOM, true> = {
    PCS: true, EA: true, LF: true, SF: true, BF: true, MBF: true, SQ: true, BOX: true,
    CTN: true, RL: true, GAL: true, LBS: true, BAG: true, BUNDLE: true, PAIR: true, SET: true,
};
export const UOM_OPTIONS = Object.keys(UOM_SET) as UOM[];

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
