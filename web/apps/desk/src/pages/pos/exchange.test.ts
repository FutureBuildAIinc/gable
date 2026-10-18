// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import { differenceOf, exchangeTotals, bpsWords, reasonLabel, returnRefusal } from './exchange';
import type { ExchangeFigures, ReturnReason } from '../../types/pos';

const figures = (over: Partial<ExchangeFigures> = {}): ExchangeFigures => ({
    returned_cents: 599, restocking_fee_cents: 0, replacement_cents: 1198, difference_cents: 599, ...over,
});

describe('what the difference asks of the cashier', () => {
    it('says what the customer pays when the replacement is worth more', () => {
        expect(differenceOf(figures())).toEqual({ kind: 'pays', cents: 599, text: 'Customer pays $5.99' });
    });
    it('says what goes back when the return is worth more', () => {
        const d = differenceOf(figures({ returned_cents: 1198, replacement_cents: 599, difference_cents: -599 }));
        expect(d).toEqual({ kind: 'refund', cents: 599, text: 'Pay the customer back $5.99' });
    });
    it('says an even exchange asks for nothing', () => {
        const d = differenceOf(figures({ returned_cents: 599, replacement_cents: 599, difference_cents: 0 }));
        expect(d.kind).toBe('even');
        expect(d.cents).toBe(0);
    });
});

describe('the totals box', () => {
    it('lists the goods back, the replacement and no fee line when there is no fee', () => {
        expect(exchangeTotals(figures())).toEqual([
            { label: 'Returned goods', cents: 599 },
            { label: 'Replacement goods', cents: 1198 },
        ]);
    });
    it('shows the restocking fee as its own negative line, the returned goods before it', () => {
        // 1198 of goods back, a 165 fee: the credit is 1033
        const rows = exchangeTotals(figures({ returned_cents: 1033, restocking_fee_cents: 165, replacement_cents: 1198, difference_cents: 165 }));
        expect(rows).toEqual([
            { label: 'Returned goods', cents: 1198 },
            { label: 'Restocking fee', cents: -165 },
            { label: 'Replacement goods', cents: 1198 },
        ]);
    });
});

describe('the reasons and their fees in words', () => {
    it('writes basis points as a percentage without float drift', () => {
        expect(bpsWords(1500)).toBe('15%');
        expect(bpsWords(125)).toBe('1.25%');
        expect(bpsWords(5)).toBe('0.05%');
        expect(bpsWords(250)).toBe('2.5%');
        expect(bpsWords(10000)).toBe('100%');
    });
    it('names the fee beside a reason that carries one', () => {
        const r = (code: ReturnReason['code'], bps: number): ReturnReason => ({ code, label: 'Changed their mind', restocking_fee_bps: bps, needs_note: false });
        expect(reasonLabel(r('changed_mind', 1500))).toBe('Changed their mind (15% restocking fee)');
        expect(reasonLabel(r('defective', 0))).toBe('Changed their mind');
    });
});

describe('when a return cannot be taken yet', () => {
    const line = (quantity: string, reason: string) => ({ quantity, reason });
    it('needs a line and a quantity', () => {
        expect(returnRefusal([line('0', '')], '')).toBe('Name at least one line and quantity to return');
    });
    it('needs a reason on each line coming back once the reasons are known', () => {
        expect(returnRefusal([line('1', '')], '', true)).toBe('Pick a reason for each line coming back');
        expect(returnRefusal([line('1', 'defective'), line('0', '')], '', true)).toBeNull();
    });
    it('needs the note when a line says other', () => {
        expect(returnRefusal([line('1', 'other')], '  ', true)).toBe('A line returned for another reason needs a note saying why');
        expect(returnRefusal([line('1', 'other')], 'kept the wrong colour', true)).toBeNull();
        expect(returnRefusal([line('1', 'defective')], '', true)).toBeNull();
    });
});
