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
