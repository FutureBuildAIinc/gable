// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The counter specs' catalogue pick: a product the register's own yard can
 * serve, not one the whole dealer happens to hold somewhere.
 */
import type { APIRequestContext } from '@playwright/test';

/** The branch the counter's register belongs to, the yard the whole run works in. */
export async function registerBranch(request: APIRequestContext): Promise<string> {
  const registers = (await (await request.get('/api/v1/pos/registers')).json()) as {
    items: { id: string; branch_id: string }[];
  };
  const reg = registers.items.find((r) => r.id === 'REG-01');
  if (!reg) throw new Error('the seed holds no REG-01 register for the counter run');
  return reg.branch_id;
}

/**
 * A product the register's own yard holds free, for the item search, with its
 * base price so money checks can be measured against the sale.
 *
 * The CI run seeds with the multi yard switch off, which makes the branch
 * header inert: every stock read then lists every yard. The sale itself is
 * issued at the register's yard only, and the yard is the branch with every
 * location under it, not the one row the yard is named for: the completion
 * draws its free stock from all of them together. So this helper rebuilds the
 * yard's free stock (quantity minus allocated, the sale's own measure) by
 * pairing each inventory row with the branch its location names, and picks
 * against the register's yard with room to spare. Earlier specs in the same
 * run spend and allocate the same catalogue pick, so a figure that counts
 * other yards, or one named row alone, can read healthy while the register's
 * yard holds nothing free.
 */
export async function stockedProduct(
  request: APIRequestContext,
  branchId?: string,
): Promise<{ id: string; sku: string; base_price_ten_thousandths: number }> {
  const yard = branchId ?? (await registerBranch(request));

  const locations = (await (await request.get('/api/v1/locations?limit=200')).json()) as {
    items: { id: string; branch_id: string }[];
  };
  const branchOf = new Map(locations.items.map((l) => [l.id, l.branch_id]));

  const levels: { product_id: string; location_id: string | null; available: string }[] = [];
  let cursor = '';
  for (;;) {
    const page = (await (
      await request.get(`/api/v1/inventory?limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`)
    ).json()) as {
      items: { product_id: string; location_id: string | null; available: string }[];
      next_cursor: string | null;
    };
    levels.push(...page.items);
    if (!page.next_cursor) break;
    cursor = page.next_cursor;
  }
  const freeAtYard = new Map<string, number>();
  for (const lv of levels) {
    if (!lv.location_id || branchOf.get(lv.location_id) !== yard) continue;
    freeAtYard.set(lv.product_id, (freeAtYard.get(lv.product_id) ?? 0) + (Number(lv.available) || 0));
  }

  const products = (await (await request.get('/api/v1/products?limit=200')).json()) as {
    items: { id: string; sku: string; base_price_ten_thousandths: number }[];
  };
  // The busiest counter sale in these specs rings a handful of pieces; ask for
  // a yard's free stock comfortably above that.
  const FLOOR = 50;
  for (const p of products.items.filter((p) => !p.sku.startsWith('E2E-')).reverse()) {
    if ((freeAtYard.get(p.id) ?? 0) >= FLOOR) return p;
  }
  throw new Error('no stocked product for the counter run');
}
