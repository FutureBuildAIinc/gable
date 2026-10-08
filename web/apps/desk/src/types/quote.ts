// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The quote wire types come from the generated contract (core/api/fragments/quote.yaml
// through @gable/api-client); nothing here restates a field by hand. Money is integer
// cents (*_cents), a unit price is an integer in ten thousandths of a dollar, and a
// quantity is a decimal string. Exposure states on a quote are lowercase; the exposure
// routes keep their own uppercase vocabulary (types/exposure.ts).

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type Quote = Schemas['Quote'];
export type QuoteSummary = Schemas['QuoteSummary'];
export type QuoteLine = Schemas['QuoteLine'];
export type QuoteStatus = Schemas['QuoteStatus'];
export type QuoteDeliveryType = Schemas['QuoteDeliveryType'];
export type QuoteExposureStatus = Schemas['QuoteExposureStatus'];
export type QuoteRequest = Schemas['QuoteRequest'];
export type QuoteLineRequest = Schemas['QuoteLineRequest'];
export type QuoteUom = Schemas['QuoteUom'];
export type QuoteTransitionRequest = Schemas['QuoteTransitionRequest'];
export type QuotePage = Schemas['QuotePage'];
export type QuoteAnalytics = Schemas['QuoteAnalytics'];
export type QuoteAnalyticsTrend = Schemas['QuoteAnalyticsTrend'];
export type QuoteOrderPayload = Schemas['QuoteOrderPayload'];

export interface ParseMapItem {
    raw_text: string;
    matched_product: {
        product_id: string;
        sku: string;
        description: string;
        uom: string;
        base_price: number;
    } | null;
    quantity: number;
    uom: string;
    confidence: number;
    is_special_order: boolean;
    alternatives: {
        product_id: string;
        sku: string;
        description: string;
        uom: string;
        base_price: number;
    }[];
}

/**
 * Every unit code the quote wire accepts, in the order a picker shows them.
 * The Record keys must match the contract's QuoteUom exactly, so a unit the
 * contract adds or drops fails the type check here until this list follows.
 */
const UOM_CODE_SET: Record<QuoteUom, true> = {
    PCS: true, EA: true, LF: true, SF: true, BF: true, MBF: true, SQ: true, BOX: true,
    CTN: true, RL: true, GAL: true, LBS: true, BAG: true, BUNDLE: true, PAIR: true, SET: true,
};
export const QUOTE_UOM_CODES = Object.keys(UOM_CODE_SET) as QuoteUom[];
