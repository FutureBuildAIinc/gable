// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The price read comes from the generated contract (core/api/fragments/pricing.yaml through
// @gable/api-client): unit_price_ten_thousandths is the exact scale 4 price per price_uom,
// line_total_cents is the one rounding, and price_basis is lowercase (ADR 0006 section 7.3).
// The market index and escalation shapes below belong to routes that are not converted.

import type { components } from '@gable/api-client';

type Schemas = components['schemas'];

export type PricingSource = Schemas['PricingSource'];
export type CalculatedPrice = Schemas['PricingCalculatedPrice'];

// --- Escalator Pricing Types ---

export type EscalationType = "PERCENTAGE" | "INDEX_DELTA";

export interface MarketIndex {
    id: string;
    name: string;
    source: string;
    current_value: number;
    previous_value: number | null;
    unit: string;
    last_updated_at: string;
    created_at: string;
}

export interface EscalationRequest {
    base_price: number;
    escalation_type: EscalationType;
    escalation_rate: number;
    effective_date: string;
    target_date: string;
    market_index_id?: string;
}

export interface EscalationResult {
    base_price: number;
    future_price: number;
    price_delta: number;
    delta_percent: number;
    months_out: number;
    is_stale: boolean;
    stale_delta_pct: number;
    current_index: number | null;
    base_index: number | null;
    escalation_type: string;
    expiration_date: string;
    is_expired: boolean;
}

export interface QuoteLineEscalator {
    enabled: boolean;
    escalation_type: EscalationType;
    escalation_rate: number;
    effective_date: string;
    target_date: string;
    market_index_id?: string;
    result?: EscalationResult;
}
