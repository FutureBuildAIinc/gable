// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The counter's payer capture dialog (CP-17a), by country: the fields the
// rule needs, the Canadian note that says the capture is not a FINTRAC
// report, the refusal message when the completion was refused, the masked
// sensitive values after capture, and Save held until the fields are filled.

import { describe, expect, it } from 'vitest';

import './PayerDialog';
import type { GablePayerDialog, PayerCapture } from './PayerDialog';
import { masked } from './PayerDialog';

const US_FIELDS = ['name', 'address', 'taxpayer_id', 'date_of_birth', 'occupation', 'id_type', 'id_number', 'third_party_payer'];
const CA_FIELDS = ['name', 'address', 'date_of_birth', 'occupation', 'id_type'];

function mount(fields: string[], dealerThreshold: boolean, refused = false, prior: PayerCapture | null = null): GablePayerDialog {
    const el = document.createElement('gable-payer-dialog') as GablePayerDialog;
    el.ask = { fields, dealerThreshold };
    el.refused = refused;
    el.prior = prior;
    document.body.appendChild(el);
    return el;
}

const fill = (el: Element, testid: string, value: string) => {
    const input = el.querySelector(`[data-testid="${testid}"]`) as HTMLInputElement;
    input.value = value;
    input.dispatchEvent(new Event('input', { bubbles: true }));
};

const choose = (el: Element, testid: string, value: string) => {
    const select = el.querySelector(`[data-testid="${testid}"]`) as HTMLInputElement;
    select.value = value;
    select.dispatchEvent(new Event('change', { bubbles: true }));
};

describe('the payer capture dialog', () => {
    it('asks the United States fields, the taxpayer identification number among them, and says nothing of FINTRAC', async () => {
        const el = mount(US_FIELDS, false);
        await el.updateComplete;
        expect(el.querySelector('[data-testid="payer-taxpayer_id"]')).not.toBeNull();
        expect(el.querySelector('[data-testid="payer-id_number"]')).not.toBeNull();
        expect(el.textContent).not.toContain('FINTRAC');
    });

    it('asks the Canadian fields with the note that it is not a FINTRAC report and asks no taxpayer identification number', async () => {
        const el = mount(CA_FIELDS, true);
        await el.updateComplete;
        expect(el.querySelector('[data-testid="payer-taxpayer_id"]')).toBeNull();
        expect(el.querySelector('[data-testid="payer-id_number"]')).toBeNull();
        expect(el.textContent).toContain("It is not a FINTRAC report.");
    });

    it('holds Save until every field the rule needs is filled, then emits the capture', async () => {
        const el = mount(US_FIELDS, false);
        await el.updateComplete;
        const save = el.querySelector('[data-testid="payer-save"]') as HTMLButtonElement;
        expect(save.disabled).toBe(true);
        fill(el, 'payer-name', 'Riverbend Builders');
        fill(el, 'payer-address', '12 Mill Road, Kelowna');
        fill(el, 'payer-date_of_birth', '1984-05-06');
        fill(el, 'payer-occupation', 'Contractor');
        choose(el, 'payer-id_type', 'drivers_license');
        fill(el, 'payer-id_number', 'DL-00-1234567');
        fill(el, 'payer-taxpayer_id', '00-1234567');
        await el.updateComplete;
        expect(save.disabled).toBe(false);
        const saved: { current: PayerCapture | null } = { current: null };
        el.addEventListener('payer-save', (e: Event) => { saved.current = (e as CustomEvent<PayerCapture>).detail; });
        save.click();
        expect(saved.current).not.toBeNull();
        expect(saved.current?.taxpayer_id).toBe('00-1234567');
    });

    it('says the refusal in words when the completion was refused without the details', async () => {
        const el = mount(US_FIELDS, false, true);
        await el.updateComplete;
        const refused = el.querySelector('[data-testid="payer-refused"]');
        expect(refused?.textContent).toContain("The payer's details are required");
    });

    it('shows a prior capture masked, never the sensitive values in cleartext', async () => {
        const prior: PayerCapture = {
            name: 'Riverbend Builders', address: '12 Mill Road', occupation: 'Contractor',
            date_of_birth: '1984-05-06', id_type: 'drivers_license',
            id_number: 'DL-00-1234567', taxpayer_id: '00-1234567', third_party_payer: false,
        };
        const el = mount(US_FIELDS, false, false, prior);
        await el.updateComplete;
        const words = el.textContent ?? '';
        expect(words).toContain('****4567');
        expect(words).not.toContain('DL-00-1234567');
        expect(words).not.toContain('00-1234567');
    });

    it('masks to the last four only', () => {
        expect(masked('DL-00-1234567')).toBe('****4567');
        expect(masked('')).toBe('');
    });
});
