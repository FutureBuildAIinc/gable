// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { fineUnits, formatAmount, priceWithTintCharge, tintPayload, tintSummary } from './tint';

describe('fineUnits', () => {
    it('carries the coarse count through the denominator it belongs to', () => {
        expect(fineUnits({ whole_oz: 1, increments_coarse: 12, increments_fine: 0, unit_denominator: 384 })).toBe(528);
        expect(fineUnits({ whole_oz: 0, increments_coarse: 1, increments_fine: 0, unit_denominator: 64 })).toBe(2);
    });
    it('drops the coarse count in a grid it does not divide (a shot system)', () => {
        expect(fineUnits({ whole_oz: 0, increments_coarse: 1, increments_fine: 3, unit_denominator: 48 })).toBe(3);
    });
});

describe('formatAmount', () => {
    it('prints the guide layout, skipping zeros', () => {
        expect(formatAmount({ whole_oz: 1, increments_coarse: 12, increments_fine: 0, unit_denominator: 384 })).toBe('1 OZ + 12/32');
        expect(formatAmount({ whole_oz: 0, increments_coarse: 0, increments_fine: 7, unit_denominator: 384 })).toBe('7/384');
        expect(formatAmount({ whole_oz: 2, increments_coarse: 0, increments_fine: 0, unit_denominator: 48 })).toBe('2 OZ');
        expect(formatAmount({ whole_oz: 0, increments_coarse: 0, increments_fine: 0, unit_denominator: 32 })).toBe('0');
    });
});

describe('priceWithTintCharge', () => {
    it('adds the flat charge to the base price, in ten thousandths', () => {
        expect(priceWithTintCharge(55000, 250)).toBe(80000);
        expect(priceWithTintCharge(55000, 0)).toBe(55000);
    });
});

describe('tintPayload', () => {
    it('carries the identity and the edited amounts', () => {
        const p = tintPayload({
            formula_id: 'f1', color_code: 'HC-188', color_name: 'Essex Green', base_code: 'DEEP',
            container_size_code: 'GAL', rescaled: true, tint_charge_cents: 250,
            components: [{ colorant_code: 'B1', colorant_name: 'Black', amount: { whole_oz: 1, increments_coarse: 12, increments_fine: 0, unit_denominator: 384 } }],
        });
        expect(p.rescaled).toBe(true);
        expect(p.components[0].amount.unit_denominator).toBe(384);
        expect(p.components[0].amount).toEqual({ whole_oz: 1, increments_coarse: 12, increments_fine: 0, unit_denominator: 384 });
    });
});

describe('tintSummary', () => {
    it('gives the one line the cart shows under a tinted line', () => {
        expect(tintSummary([
            { colorant_code: 'B1', colorant_name: 'Black', amount: { whole_oz: 1, increments_coarse: 0, increments_fine: 0, unit_denominator: 384 } },
            { colorant_code: 'W1', colorant_name: 'White', amount: { whole_oz: 0, increments_coarse: 0, increments_fine: 2, unit_denominator: 384 } },
        ])).toBe('B1 1 OZ, W1 2/384');
    });
});
