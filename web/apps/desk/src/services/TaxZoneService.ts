// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The tax zone wire (CP-10): bare arrays, a plain POST create, a close that
 * ends a zone's window, and the one error envelope. Every failure of those
 * routes is thrown as an ApiError.
 */
import type {
  TaxZone,
  TaxZoneCloseRequest,
  TaxZoneCreateRequest,
  TaxZoneRatePart,
} from '../types/taxZone';
import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

async function expectOk(response: Response, fallback: string): Promise<Response> {
  if (!response.ok) throw await parseApiError(response, fallback);
  return response;
}

/** The query string of a zone list; only these parameters are accepted. */
export function buildTaxZoneQuery(params: { country?: string; jurisdiction_type?: string } = {}): string {
  const q = new URLSearchParams();
  if (params.country) q.set('country', params.country.toUpperCase());
  if (params.jurisdiction_type) q.set('jurisdiction_type', params.jurisdiction_type.toUpperCase());
  const s = q.toString();
  return s ? `?${s}` : '';
}

/** The combined rate of a zone's parts, as the percent a clerk reads. */
export function combinedRatePercent(rates: TaxZoneRatePart[]): string {
  let sum = 0;
  for (const r of rates) {
    const v = Number.parseFloat(r.rate);
    if (Number.isFinite(v)) sum += v;
  }
  const pct = sum * 100;
  // Trim to at most four decimals, dropping a trailing zero fraction.
  return pct.toFixed(4).replace(/0+$/, '').replace(/\.$/, '');
}

export const TaxZoneService = {
  /** Every zone, newest first, filtered by country and jurisdiction type. */
  async list(params: { country?: string; jurisdiction_type?: string } = {}): Promise<TaxZone[]> {
    const response = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/tax/zones${buildTaxZoneQuery(params)}`),
      'Failed to load tax zones',
    );
    return (await response.json()) as TaxZone[];
  },

  /** One zone with its parts and its boundaries. */
  async get(id: string): Promise<TaxZone> {
    const response = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/tax/zones/${id}`),
      'Failed to load the tax zone',
    );
    return (await response.json()) as TaxZone;
  },

  /** Create a zone with its rate parts and its boundaries. */
  async create(body: TaxZoneCreateRequest): Promise<TaxZone> {
    const response = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/tax/zones`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      }),
      'Failed to create the tax zone',
    );
    return (await response.json()) as TaxZone;
  },

  /** End a zone's window on the named date; sales before it keep their rate. */
  async close(id: string, body: TaxZoneCloseRequest): Promise<TaxZone> {
    const response = await expectOk(
      await fetchWithAuth(`${API_URL}/api/v1/tax/zones/${id}/close`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      }),
      'Failed to close the tax zone',
    );
    return (await response.json()) as TaxZone;
  },
};
