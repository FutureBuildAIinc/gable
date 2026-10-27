// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import {
    chargeRefusal, money, standingOf, holdStanding,
    verdictLine, waysForward, wayLabel, buyerRefusal, checkRefusal, storeCreditOffer, tenderMethods, idRefusal, idRequired, idRequiredWords,
} from './standing';
import type { CounterCustomer, CounterStanding, WayForward } from '../../types/pos';

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

// --- Can this sale go on account? The server's verdict, in words and actions ---

const way = (kind: WayForward['kind'], available: boolean, extra: Partial<WayForward> = {}): WayForward =>
    ({ kind, available, message: `${kind} message`, ...extra });

const allowed: CounterStanding = {
    customer_id: 'c1', name: 'Smith Builders', currency: 'USD', credit_limit_cents: 500000,
    open_receivable_cents: 120050, open_credit_memo_cents: 0, available_credit_cents: 379950, on_hold: false,
    terms_kind: 'net_days', named_buyer_required: false,
    buyers: [{ id: 'b1', name: 'Ada Buyer', cap_cents: 5000 }, { id: 'b2', name: 'Bo Buyer', cap_cents: null }],
    check_limit_cents: 20000, payment_options: ['cash', 'check', 'card', 'account'],
    on_account: { verdict: 'allowed', reason: '', message: '', over_cents: null, ways_forward: [] },
};
const blocked: CounterStanding = {
    ...allowed,
    on_account: {
        verdict: 'blocked', reason: 'credit_limit', over_cents: 395,
        message: 'This sale would put the account 3.95 USD over its credit limit of 30.00 USD.',
        ways_forward: [way('pay_down', true, { amount_cents: 395 }), way('manager_override', true), way('switch_tender', true)],
    },
};

describe('the account verdict in words', () => {
    it('says a sale can go on the account when it is allowed', () => {
        expect(verdictLine(allowed)).toEqual({ tone: 'ok', text: 'This sale can go on the account' });
    });
    it('says why it cannot, in the server’s words', () => {
        expect(verdictLine(blocked)).toEqual({ tone: 'stop', text: blocked.on_account.message });
    });
    it('says a manager made it allowed', () => {
        const approved = { ...allowed, on_account: { ...allowed.on_account, reason: 'manager_override' as const, message: 'A manager approved this sale on account up to 12.00 USD.' } };
        expect(verdictLine(approved)).toEqual({ tone: 'warn', text: 'A manager approved this sale on account up to 12.00 USD.' });
    });
});

describe('the ways forward from a blocked verdict', () => {
    it('offers the three ways as actions, with the amount that clears it', () => {
        const ways = waysForward(blocked);
        expect(ways.map((w) => w.kind)).toEqual(['pay_down', 'manager_override', 'switch_tender']);
        expect(wayLabel(ways[0])).toBe('Take $3.95 on the account');
        expect(wayLabel(ways[1])).toBe('Ask a manager to approve');
        expect(wayLabel(ways[2])).toBe('Pay another way');
    });
    it('offers only the ways that are available, and none when allowed', () => {
        const held: CounterStanding = {
            ...blocked,
            on_account: { ...blocked.on_account, reason: 'cash_only_terms', ways_forward: [way('pay_down', false), way('manager_override', false), way('switch_tender', true)] },
        };
        expect(waysForward(held).map((w) => w.kind)).toEqual(['switch_tender']);
        expect(waysForward(allowed)).toEqual([]);
        expect(waysForward(null)).toEqual([]);
    });
});

describe('the buyer roster', () => {
    it('asks for a buyer when the account requires one', () => {
        const need = { ...allowed, named_buyer_required: true };
        expect(buyerRefusal(need, '', 1000)).toBe('Smith Builders asks for a named buyer: pick one from the account\'s roster');
        expect(buyerRefusal(allowed, '', 1000)).toBeNull();
        expect(buyerRefusal(null, '', 1000)).toBeNull();
    });
    it('holds a named buyer to the roster and to the buyer’s own cap', () => {
        expect(buyerRefusal(allowed, 'nobody', 100)).toMatch(/not on Smith Builders'?s? roster|not on Smith Builders/);
        expect(buyerRefusal(allowed, 'b1', 5000)).toBeNull();
        expect(buyerRefusal(allowed, 'b1', 5001)).toBe('Ada Buyer may put at most $50.00 on the account in one sale');
        expect(buyerRefusal(allowed, 'b2', 99999999)).toBeNull();
    });
});

describe('check limits, store credit and the tenders offered', () => {
    it('refuses a check over the limit and says by what limit', () => {
        expect(checkRefusal(allowed, null, 20000)).toBeNull();
        expect(checkRefusal(allowed, null, 20001)).toBe('A check of $200.01 is over the check limit of $200.00: ask a manager to approve it');
    });
    it('falls back to the limit it was given when the account states none, and to no limit', () => {
        const none = { ...allowed, check_limit_cents: null };
        expect(checkRefusal(none, 1000, 1001)).toMatch(/over the check limit of \$10.00/);
        expect(checkRefusal(none, null, 99999999)).toBeNull();
        expect(checkRefusal(null, null, 5)).toBeNull();
    });
    it('offers the store credit an account holds, up to what is owed', () => {
        const credit = { ...allowed, open_credit_memo_cents: 599 };
        expect(storeCreditOffer(credit, 1198)).toBe(599);
        expect(storeCreditOffer(credit, 300)).toBe(300);
        expect(storeCreditOffer(allowed, 1198)).toBe(0);
        expect(storeCreditOffer(null, 1198)).toBe(0);
        expect(storeCreditOffer(credit, 0)).toBe(0);
    });
    it('shows store credit as a tender only while the account holds any', () => {
        expect(tenderMethods(null)).toEqual(['cash', 'check', 'card', 'account']);
        expect(tenderMethods(allowed)).toEqual(['cash', 'check', 'card', 'account']);
        expect(tenderMethods({ ...allowed, open_credit_memo_cents: 1 })).toEqual(['cash', 'check', 'card', 'account', 'store_credit']);
    });
    it('asks for the kind of ID once ID is checked', () => {
        expect(idRefusal(true, '')).toBe('Pick the kind of ID that was checked');
        expect(idRefusal(true, 'passport')).toBeNull();
        expect(idRefusal(false, '')).toBeNull();
    });
    it('waives the credit words, never the purchase order, once a manager approved the sale', () => {
        const over = { ...base, standing: 'over_limit' as const, available_credit_cents: -100, po_required: true };
        expect(chargeRefusal(over, 100, 'PO-1', true)).toBeNull();
        expect(chargeRefusal(over, 100, '', true)).toMatch(/purchase order/);
        expect(chargeRefusal(over, 100, 'PO-1')).toMatch(/Over the credit limit/);
    });
});

describe('the dealer check ID rule, as the counter reads it', () => {
    const never = { mode: 'never' as const, over_cents: null };
    const every = { mode: 'every_check' as const, over_cents: null };
    const over50 = { mode: 'over_amount' as const, over_cents: 5000 };

    it('never asks when no rule is set or the rule is never', () => {
        expect(idRequired(null, 100000)).toBe(false);
        expect(idRequired(never, 100000)).toBe(false);
        expect(idRequiredWords(null, 100000)).toBeNull();
    });

    it('asks on every check when the rule is every check', () => {
        expect(idRequired(every, 1)).toBe(true);
        expect(idRequiredWords(every, 1))
            .toBe(`This check needs the customer's ID: check it and pick the ID type`);
    });

    it('asks only on checks over the figure, not at it', () => {
        expect(idRequired(over50, 5000)).toBe(false);
        expect(idRequired(over50, 5001)).toBe(true);
        expect(idRequiredWords(over50, 6000))
            .toBe(`A check over $50.00 needs the customer's ID: check it and pick the ID type`);
    });
});

describe('an account held by a credit hold record', () => {
    const holdWays = [way('pay_down', false, { message: 'Paying the balance does not lift a hold; accounts receivable releases it.' }), way('manager_override', true), way('switch_tender', true)];
    const heldRecord: CounterStanding = {
        ...allowed,
        on_hold: true,
        payment_options: ['cash', 'check', 'card'],
        on_account: {
            verdict: 'blocked', reason: 'account_hold', over_cents: null,
            message: 'This account is on credit hold: past due 60 days. Nothing can be put on the account.',
            ways_forward: holdWays,
        },
    };
    it('states the hold in its own words on the standing line, the same blocked treatment the zero limit gets', () => {
        expect(holdStanding(heldRecord)).toEqual({ tone: 'stop', text: 'On credit hold' });
    });
    it('carries the hold’s reason in the verdict line, and the ways forward never offer a pay down', () => {
        expect(verdictLine(heldRecord)).toEqual({ tone: 'stop', text: heldRecord.on_account.message });
        expect(waysForward(heldRecord).map((w) => w.kind)).toEqual(['manager_override', 'switch_tender']);
        expect(holdWays[0].message).toMatch(/accounts receivable releases it/);
    });
    it('is not a held record when the verdict is another reason, allowed, or nothing was read', () => {
        expect(holdStanding(blocked)).toBeNull();
        expect(holdStanding(allowed)).toBeNull();
        expect(holdStanding(null)).toBeNull();
    });
    it('is not a held record when only the sale is over the cap of a live override window: the on_hold flag says the account is not held', () => {
        const overCap: CounterStanding = {
            ...heldRecord,
            on_hold: false,
            on_account: {
                verdict: 'blocked', reason: 'account_hold', over_cents: null,
                message: 'The hold on this account is overridden up to 5.99 USD a sale: this sale asks 6.00 USD.',
                ways_forward: holdWays,
            },
        };
        expect(holdStanding(overCap)).toBeNull();
        expect(verdictLine(overCap).text).toContain('overridden up to');
    });
    it('offers no account tender while the hold stands', () => {
        expect(heldRecord.payment_options).not.toContain('account');
    });
});
