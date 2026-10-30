// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { intentIdOfRefusal, cardWaitWords, lostKindWords, lateWords, needsReference, cardOverWords, CARD_CANCEL_WORDS, CARD_DECLINED_WORDS } from './terminal';
import type { TerminalIntent, TerminalLostItem } from '../../types/pos';

const INTENT_ID = '9c8b7a6d-5e4f-4c3b-2a19-080715d4e3f2';

const intent = (state: TerminalIntent['state']): TerminalIntent => ({
  id: INTENT_ID, transaction_id: 's1', leg_key: 'card-0', kind: 'sale', state,
  amount_cents: 8650, provider_ref: '', answer_verdict: state === 'authorized' ? 'authorized' : state === 'declined' ? 'declined' : '',
});

describe('the waiting refusal names its intent', () => {
  it('reads the intent id out of the waiting refusal', () => {
    expect(intentIdOfRefusal(`the terminal has the card leg and has not answered (intent ${INTENT_ID}): the sale is not completed`)).toBe(INTENT_ID);
  });

  it('answers null for every other refusal', () => {
    expect(intentIdOfRefusal('the sale needs 8650 cents and the tenders carry 0')).toBeNull();
    expect(intentIdOfRefusal('the card was declined at the terminal')).toBeNull();
    expect(intentIdOfRefusal('')).toBeNull();
  });
});

describe('the card leg\'s outcomes in words', () => {
  it('waits in plain words while the terminal holds the card', () => {
    for (const state of ['claimed', 'pending_at_device'] as const) {
      const words = cardWaitWords(intent(state));
      expect(words.title).toBe('Card at the terminal');
      expect(words.body).not.toContain('$');
    }
    expect(cardWaitWords(intent('pending_at_device')).body).toContain('Pay again once the terminal answers');
  });

  it('says the approval, the decline and the no answer in words', () => {
    const approved = cardWaitWords(intent('authorized'));
    expect(approved.title).toBe('Card approved');
    // the wait ran out before the approval came: the sale is not complete, and the words say how to finish it
    expect(approved.body).toContain('Pay again with the same card tender');
    expect(approved.body).toContain('not charged twice');
    const declined = cardWaitWords(intent('declined'));
    expect(declined.title).toBe('Card declined');
    expect(declined.body).toBe(CARD_DECLINED_WORDS);
    expect(CARD_DECLINED_WORDS).toContain('The sale is still open');
    const unreached = cardWaitWords(intent('lost'));
    expect(unreached.title).toBe('The terminal did not answer');
    expect(unreached.body).toContain('terminal was never reached');
    expect(unreached.body).not.toContain('manager');
    const lost = cardWaitWords({ ...intent('lost'), provider_ref: 'devref-1' });
    expect(lost.title).toBe('The terminal did not answer');
    expect(lost.body).toContain('manager');
  });

  it('never claims the cancel stopped the charge', () => {
    expect(CARD_CANCEL_WORDS).toContain('does not stop the terminal');
    expect(CARD_CANCEL_WORDS).toContain('may still move');
  });
});

describe('the manager\'s kind words', () => {
  it('names each kind of lost answer', () => {
    expect(lostKindWords('sale')).toBe('a card charge');
    expect(lostKindWords('refund')).toBe('a card refund');
    expect(lostKindWords('void')).toBe('a card void');
  });
});

const lost = (over: Partial<TerminalLostItem> = {}): TerminalLostItem => ({
  id: 'li1', intent_id: 'in1', branch_id: 'b', register_id: 'REG-07', transaction_id: 'tx1', kind: 'sale',
  amount_cents: 12000, device_id: 'REG-07', provider_ref: '', state: 'open', late_verdict: '', late_provider_ref: '', ...over,
});

describe('the device\'s late word on a lost answer', () => {
  it('says nothing when the device sent no late answer', () => {
    expect(lateWords(lost())).toBeNull();
  });

  it('says a late approval and a late decline, with the reference', () => {
    expect(lateWords(lost({ late_verdict: 'authorized', late_provider_ref: 'devref-7' })))
      .toBe('The terminal answered late: it approved this (reference devref-7).');
    expect(lateWords(lost({ late_verdict: 'declined' }))).toBe('The terminal answered late: it declined this.');
  });

  it('asks for a reference only when an approval has none from the device', () => {
    expect(needsReference(lost(), 'authorized')).toBe(true);
    expect(needsReference(lost({ provider_ref: 'devref-1' }), 'authorized')).toBe(false);
    expect(needsReference(lost({ late_provider_ref: 'devref-2' }), 'authorized')).toBe(false);
    expect(needsReference(lost(), 'declined')).toBe(false);
  });
});

describe('a card is held to what is owed on it', () => {
  it('lets the remainder and anything under it through', () => {
    expect(cardOverWords(500, 599, '$5.99')).toBeNull();
    expect(cardOverWords(599, 599, '$5.99')).toBeNull();
  });

  it('refuses more than the remainder in the counter\'s words', () => {
    expect(cardOverWords(1599, 599, '$5.99')).toBe('The card can be charged at most $5.99: the rest is cash change.');
  });
});
