// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The dealer's mark and name for the top bar, from the dealer profile the
// core serves (GET /api/v1/dealer-profile): the trading name, else the legal
// name, and the logo's URL when one is set. No Gable mark competes with the
// dealer's: a profile that never answers leaves the brand corner empty.

import { fetchWithAuth } from '@gable/auth';

export interface DealerBrand {
  name: string;
  markUrl: string;
}

/** The dealer's brand for the top bar. A failure answers the empty brand. */
export async function loadDealerBrand(fetcher: typeof fetchWithAuth = fetchWithAuth): Promise<DealerBrand> {
  try {
    const response = await fetcher('/api/v1/dealer-profile');
    if (!response.ok) return { name: '', markUrl: '' };
    const body = (await response.json()) as unknown;
    if (body === null || typeof body !== 'object') return { name: '', markUrl: '' };
    const { trading_name, legal_name, logo_url } = body as {
      trading_name?: unknown;
      legal_name?: unknown;
      logo_url?: unknown;
    };
    const trading = typeof trading_name === 'string' ? trading_name.trim() : '';
    const legal = typeof legal_name === 'string' ? legal_name.trim() : '';
    return {
      name: trading !== '' ? trading : legal,
      markUrl: typeof logo_url === 'string' ? logo_url : '',
    };
  } catch {
    return { name: '', markUrl: '' };
  }
}
