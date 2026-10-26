// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The branch's cash rounding rule (CP-18): symmetric rounding to the nearest
// five cents of the cash leg only. The table is the rule, and the server
// carries the same one.

import { describe, expect, it } from 'vitest';
import {
  CASH_ROUNDING_NEAREST_5,
  CASH_ROUNDING_OFF,
  roundCash,
  roundCashFor,
} from './cashRounding';

describe('roundCash', () => {
  // One, two, six and seven cents fall to the nickel below; three, four,
  // eight and nine rise to the nickel above; five and zero stand still.
  const table: [number, number][] = [
    [0, 0],
    [1, 0],
    [2, 0],
    [3, 5],
    [4, 5],
    [5, 5],
    [6, 5],
    [7, 5],
    [8, 10],
    [9, 10],
  ];

  it('rounds every ending the way the rule says', () => {
    for (const [cents, want] of table) {
      expect(roundCash(cents).rounded).toBe(want);
      expect(roundCash(1000 + cents).rounded).toBe(1000 + want);
      expect(roundCash(2000 + cents).rounded).toBe(2000 + want);
    }
  });

  it('reports the difference the rounding made', () => {
    expect(roundCash(103).rounded).toBe(105);
    expect(roundCash(103).diff).toBe(2);
    expect(roundCash(107).rounded).toBe(105);
    expect(roundCash(107).diff).toBe(-2);
    expect(roundCash(105).diff).toBe(0);
  });

  it('mirrors a negative amount, so a refund rounds the same way', () => {
    expect(roundCash(-103).rounded).toBe(-105);
    expect(roundCash(-103).diff).toBe(-2);
    expect(roundCash(-107).rounded).toBe(-105);
    expect(roundCash(-107).diff).toBe(2);
  });
});

describe('roundCashFor', () => {
  it("takes the money exactly when the branch's rule is off", () => {
    expect(roundCashFor(CASH_ROUNDING_OFF, 103).rounded).toBe(103);
    expect(roundCashFor(CASH_ROUNDING_OFF, 103).diff).toBe(0);
    expect(roundCashFor(null, 103).rounded).toBe(103);
    expect(roundCashFor(undefined, 103).rounded).toBe(103);
  });

  it("rounds the cash leg when the branch's rule is on", () => {
    expect(roundCashFor(CASH_ROUNDING_NEAREST_5, 103).rounded).toBe(105);
    expect(roundCashFor(CASH_ROUNDING_NEAREST_5, 103).diff).toBe(2);
    expect(roundCashFor(CASH_ROUNDING_NEAREST_5, 107).rounded).toBe(105);
    expect(roundCashFor(CASH_ROUNDING_NEAREST_5, 107).diff).toBe(-2);
  });

  it('takes money exactly when there is no cash leg to round', () => {
    expect(roundCashFor(CASH_ROUNDING_NEAREST_5, 0).rounded).toBe(0);
    expect(roundCashFor(CASH_ROUNDING_NEAREST_5, 0).diff).toBe(0);
  });
});
