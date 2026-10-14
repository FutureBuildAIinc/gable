// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { PricingService } from '../services/pricing.service';
import type { CalculatedPrice } from '../types/pricing';

export interface LinePrice {
  /** The exact scale 4 unit price the line carries. */
  unitPriceTenThousandths: number;
  /** The resolved price, set only when it is the price used. */
  details: CalculatedPrice | null;
  /** True when the pricing call failed and the base price stands in. */
  failed: boolean;
}

/**
 * The price a line takes for a customer. With a customer named it is the price
 * `PricingService.calculatePrice` answers when that price is in the line's own
 * unit, the base price otherwise (a price in another unit needs the line's
 * conversion pair). With no customer it is the base price. A failed call falls
 * back to the base price and says so, so the caller can speak once.
 */
export async function resolveLinePrice(
  customerId: string | undefined,
  productId: string,
  lineUom: string,
  baseTenThousandths: number,
): Promise<LinePrice> {
  const base: LinePrice = { unitPriceTenThousandths: Math.round(baseTenThousandths), details: null, failed: false };
  if (!customerId) return base;
  try {
    const pricing = await PricingService.calculatePrice(customerId, productId);
    if (pricing.price_uom.toUpperCase() === lineUom.toUpperCase()) {
      return { unitPriceTenThousandths: Math.round(pricing.unit_price_ten_thousandths), details: pricing, failed: false };
    }
    return base;
  } catch (err) {
    console.error('Failed to fetch price', err);
    return { ...base, failed: true };
  }
}
