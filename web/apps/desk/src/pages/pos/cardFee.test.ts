// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import { expectedFeeCents, ratePercentText, cardFeeNotice } from './cardFee';

describe('expectedFeeCents', () => {
    it('is the rate times the card amount, half up, in integer cents', () => {
        expect(expectedFeeCents(10000, 300)).toBe(300);
        expect(expectedFeeCents(1073, 300)).toBe(32); // 32.19 down
        expect(expectedFeeCents(1050, 300)).toBe(32); // 31.5 rounds up
        expect(expectedFeeCents(1, 300)).toBe(0);
        expect(expectedFeeCents(17, 300)).toBe(1); // 0.51 rounds up
    });
    it('is zero for no rate or no amount', () => {
        expect(expectedFeeCents(10000, 0)).toBe(0);
        expect(expectedFeeCents(0, 300)).toBe(0);
        expect(expectedFeeCents(-5, 300)).toBe(0);
    });
});

describe('ratePercentText', () => {
    it('writes basis points as percent', () => {
        expect(ratePercentText(300)).toBe('3');
        expect(ratePercentText(250)).toBe('2.5');
        expect(ratePercentText(5)).toBe('0.05');
        expect(ratePercentText(0)).toBe('0');
    });
});

describe('cardFeeNotice', () => {
    it('says nothing while the branch does not charge a card fee', () => {
        expect(cardFeeNotice(null, 10000)).toBeNull();
        expect(cardFeeNotice(undefined, 10000)).toBeNull();
        expect(cardFeeNotice({ branch_id: 'b', enabled: false, rate_bps: 300 }, 10000)).toBeNull();
        expect(cardFeeNotice({ branch_id: 'b', enabled: true, rate_bps: 0 }, 10000)).toBeNull();
    });
    it('names the rate, the expected fee as a notice and the cash price', () => {
        const n = cardFeeNotice({ branch_id: 'b', enabled: true, rate_bps: 300 }, 10000);
        expect(n?.fee).toContain('Card fee 3%');
        expect(n?.fee).toContain('$3.00');
        expect(n?.fee).toContain('final figure');
        expect(n?.cash).toBe('Cash price $100.00, no fee.');
    });
});
