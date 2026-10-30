// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Cash rounding without pennies (CP-18): the same table the Go core carries
 * (core/internal/pos/rounding.go), so the change the cashier sees on the
 * counter and the change the books carry are one number.
 *
 * The Government of Canada elected symmetric rounding to the nearest five
 * cents for cash: a total ending in 1, 2, 6 or 7 cents goes down, one ending
 * in 3, 4, 8 or 9 cents goes up, and one ending in 0 or 5 cents does not move.
 * The rounding touches the cash leg only: a card, a cheque, an account and
 * store credit settle to the exact cent.
 */

/** A branch's own cash rounding rule. */
export type CashRoundingMode = 'OFF' | 'NEAREST_5_CENTS';

/** The rule off: the money is taken exactly as it was tendered. */
export const CASH_ROUNDING_OFF: CashRoundingMode = 'OFF';

/** The rule that rounds the cash leg to the nearest five cents. */
export const CASH_ROUNDING_NEAREST_5: CashRoundingMode = 'NEAREST_5_CENTS';

/** The rounded amount in cents and the difference from the input, in cents. */
export interface CashRounding {
    rounded: number;
    diff: number;
}

/**
 * Rounds a cash amount in cents to the nearest five cents, symmetrically.
 * The difference is what the drawer receives over the exact figure: positive
 * on a round-up, negative on a round-down. A negative amount mirrors the
 * positive one and its difference follows the amount.
 */
export function roundCash(cents: number): CashRounding {
    const magnitude = Math.abs(cents);
    const rest = magnitude % 5;
    let roundedMagnitude = magnitude;
    if (rest === 1) roundedMagnitude = magnitude - 1;
    else if (rest === 2) roundedMagnitude = magnitude - 2;
    else if (rest === 3) roundedMagnitude = magnitude + 2;
    else if (rest === 4) roundedMagnitude = magnitude + 1;
    const rounded = cents < 0 ? -roundedMagnitude : roundedMagnitude;
    return { rounded, diff: rounded - cents };
}

/**
 * Rounds a cash amount under a branch's rule. The rule off takes the amount
 * exactly as it is, and no difference is carried.
 */
export function roundCashFor(mode: CashRoundingMode | null | undefined, cents: number): CashRounding {
    if (mode !== CASH_ROUNDING_NEAREST_5) return { rounded: cents, diff: 0 };
    return roundCash(cents);
}
