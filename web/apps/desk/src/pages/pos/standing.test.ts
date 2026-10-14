// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import { chargeRefusal, money, standingOf } from './standing';
import type { CounterCustomer } from '../../types/pos';

const base: CounterCustomer = {
    id: 'c1',
    account_number: 'A-100',
    name: 'Smith Builders',
    po_required: false,
    credit_limit_cents: 500000,
    open_receivable_cents: 120050,
    available_credit_cents: 379950,
    standing: 'good',
    jobs: [],
};

describe('money', () => {
    it('writes integer cents with grouping and no float drift', () => {
        expect(money(0)).toBe('$0.00');
        expect(money(5)).toBe('$0.05');
        expect(money(123456789)).toBe('$1,234,567.89');
        expect(money(-1999)).toBe('-$19.99');
    });
});

describe('standingOf', () => {
    it('states available credit against the limit', () => {
        expect(standingOf(base)).toEqual({ tone: 'ok', text: 'Credit available $3,799.50 of $5,000.00' });
    });
    it('says on hold when the account has no credit', () => {
        const c = { ...base, credit_limit_cents: 0, available_credit_cents: 0, standing: 'no_credit' as const };
        expect(standingOf(c)).toEqual({ tone: 'stop', text: 'On hold: this account has no credit' });
    });
    it('says by how much the account is over the limit', () => {
        const c = { ...base, available_credit_cents: -2500, standing: 'over_limit' as const };
        expect(standingOf(c)).toEqual({ tone: 'stop', text: 'Over the credit limit by $25.00' });
    });
    it('says there is no limit', () => {
        const c = { ...base, credit_limit_cents: null, available_credit_cents: null, standing: 'no_limit' as const };
        expect(standingOf(c).text).toBe('No credit limit on this account');
    });
});

describe('chargeRefusal', () => {
    it('refuses a walk-in sale', () => {
        expect(chargeRefusal(null, 1000, '')).toMatch(/Name a customer/);
    });
    it('refuses a held or over limit account', () => {
        expect(chargeRefusal({ ...base, standing: 'no_credit' }, 100, '')).toMatch(/On hold/);
        expect(chargeRefusal({ ...base, standing: 'over_limit', available_credit_cents: -1 }, 100, '')).toMatch(/Over the credit limit/);
    });
    it('refuses a sale larger than the available credit, and says by how much', () => {
        expect(chargeRefusal(base, 380000, '')).toBe('This sale is $0.50 over the available credit');
        expect(chargeRefusal(base, 379950, '')).toBeNull();
    });
    it('asks for the purchase order the account requires', () => {
        const c = { ...base, po_required: true };
        expect(chargeRefusal(c, 1000, '  ')).toMatch(/purchase order/);
        expect(chargeRefusal(c, 1000, 'PO-9')).toBeNull();
    });
    it('offers the charge to an account with no limit', () => {
        const c = { ...base, credit_limit_cents: null, available_credit_cents: null, standing: 'no_limit' as const };
        expect(chargeRefusal(c, 99999999, '')).toBeNull();
    });
});
