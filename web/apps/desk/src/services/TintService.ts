// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The paint tint catalogue's desk service (row CP-16): the dealer's own
// bases, colorants, colours and per size formulas, the rescale between
// container sizes, the dealer's tint charge, and the saved colours per
// customer and job.

import type { TintAmount, TintComponent, LineTint } from '../types/pos.ts';
import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (response.ok) return response;
    throw await parseApiError(response, fallback);
}

async function get<T>(path: string, fallback: string): Promise<T> {
    return (await expectOk(await fetchWithAuth(`${API_URL}${path}`), fallback)).json();
}

async function post<T>(path: string, body: unknown, fallback: string): Promise<T> {
    return (await expectOk(await fetchWithAuth(`${API_URL}${path}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    }), fallback)).json();
}

async function send<T>(method: 'PATCH', path: string, body: unknown, fallback: string): Promise<T> {
    return (await expectOk(await fetchWithAuth(`${API_URL}${path}`, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    }), fallback)).json();
}

export interface TintSystem {
    id: string;
    code: string;
    name: string;
    manufacturer: string;
    unit_denominator: number;
    unit_label: string;
}

export interface TintBase {
    id: string;
    tint_system_id: string;
    code: string;
    name: string;
    manufacturer_base_code: string;
    product_id: string | null;
    fill_volume_oz: number;
    is_deepest: boolean;
}

export interface TintColorant {
    id: string;
    tint_system_id: string;
    code: string;
    name: string;
}

export interface TintColor {
    id: string;
    tint_system_id: string;
    manufacturer_code: string | null;
    name: string;
    is_custom: boolean;
}

export interface TintFormula {
    id: string;
    color_id: string;
    base_id: string;
    container_size_code: string;
    container_volume_oz: number;
    components: TintComponent[];
}

export interface TintRescaledComponent {
    colorant_code: string;
    colorant_name: string;
    amount: TintAmount;
    rounded: boolean;
}

export interface TintRescale {
    formula: TintFormula;
    target_volume_oz: number;
    components: TintRescaledComponent[];
    any_rounded: boolean;
}

export interface TintSavedColor {
    id: string;
    customer_id: string;
    color_id: string;
    name: string;
    job_note: string;
    note: string;
}

/** The fields the base update may change; product_id null clears the link. */
export interface TintBaseUpdate {
    name?: string;
    product_id?: string | null;
    fill_volume_oz?: number;
    is_deepest?: boolean;
}

export interface TintFormulaDraft {
    color_id: string;
    base_id: string;
    container_size_code: string;
    container_volume_oz: number;
    source: string;
    components: { colorant_id: string; whole_oz: number; increments_coarse: number; increments_fine: number; unit_denominator: number }[];
}

export const tintService = {
    createSystem: (body: { code: string; name: string; manufacturer?: string; unit_denominator: number; unit_label?: string }) =>
        post<TintSystem>('/api/v1/tint/systems', body, 'Failed to create the tint system'),
    createBase: (body: { tint_system_id: string; code: string; name: string; fill_volume_oz: number; product_id?: string; is_deepest?: boolean }) =>
        post<TintBase>('/api/v1/tint/bases', body, 'Failed to create the base'),
    /** Changes a base: the link to its stocked base paint product, or its name, fill and tier. */
    updateBase: (id: string, body: TintBaseUpdate) =>
        send<TintBase>('PATCH', `/api/v1/tint/bases/${id}`, body, 'Failed to update the base'),
    colorants: (tintSystemId?: string) =>
        get<{ items: TintColorant[] }>(`/api/v1/tint/colorants${tintSystemId ? `?tint_system_id=${tintSystemId}` : ''}`,
            'Failed to read the colorants'),
    createColorant: (body: { tint_system_id: string; code: string; name: string }) =>
        post<TintColorant>('/api/v1/tint/colorants', body, 'Failed to create the colorant'),
    createColor: (body: { tint_system_id: string; manufacturer_code?: string; name: string }) =>
        post<TintColor>('/api/v1/tint/colors', body, 'Failed to create the colour'),
    createFormula: (body: TintFormulaDraft) =>
        post<TintFormula>('/api/v1/tint/formulas', body, 'Failed to create the formula'),

    systems: () => get<{ items: TintSystem[] }>('/api/v1/tint/systems', 'Failed to read the tint systems'),
    bases: (params: { product_id?: string; tint_system_id?: string } = {}) => {
        const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v) as [string, string][]);
        return get<{ items: TintBase[] }>(`/api/v1/tint/bases${q.size ? `?${q}` : ''}`, 'Failed to read the bases');
    },
    colors: (params: { tint_system_id?: string; q?: string } = {}) => {
        const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v) as [string, string][]);
        return get<{ items: TintColor[] }>(`/api/v1/tint/colors${q.size ? `?${q}` : ''}`, 'Failed to read the colours');
    },
    formulas: (params: { color_id?: string; base_id?: string; container_size_code?: string } = {}) => {
        const q = new URLSearchParams(Object.entries(params).filter(([, v]) => v) as [string, string][]);
        return get<{ items: TintFormula[] }>(`/api/v1/tint/formulas${q.size ? `?${q}` : ''}`, 'Failed to read the formulas');
    },
    rescale: (formulaId: string, targetVolumeOZ: number) =>
        post<TintRescale>(`/api/v1/tint/formulas/${formulaId}/rescale`, { target_volume_oz: targetVolumeOZ },
            'Failed to rescale the formula'),
    charge: (params: { colorant_units?: number; unit_denominator?: number; base_tier?: 'flat' | 'deep' } = {}) => {
        const q = new URLSearchParams();
        if (params.colorant_units !== undefined) q.set('colorant_units', String(params.colorant_units));
        if (params.unit_denominator !== undefined) q.set('unit_denominator', String(params.unit_denominator));
        if (params.base_tier) q.set('base_tier', params.base_tier);
        return get<{ tint_charge_cents: number }>(`/api/v1/tint/charge${q.size ? `?${q}` : ''}`, 'Failed to read the tint charge');
    },
    savedColors: (customerId?: string) => {
        const q = customerId ? `?customer_id=${customerId}` : '';
        return get<{ items: TintSavedColor[] }>(`/api/v1/tint/saved-colors${q}`, 'Failed to read the saved colours');
    },
    saveColor: (body: { customer_id: string; color_id: string; name: string; job_note?: string; note?: string }) =>
        post<TintSavedColor>('/api/v1/tint/saved-colors', body, 'Failed to save the colour'),
};

export type { LineTint };
