// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './StepUpDialog';
import type { StepUpDialog } from './StepUpDialog';
import type { GableSelect } from '../../components/ui/gable-select';
import { flush, jsonResponse, mount, q, update } from '../../test/dom';

const managers = {
    items: [
        { id: '11111111-1111-4111-8111-111111111111', full_name: 'Dana Ramirez', staff_no: 'STF-001', role: 'admin' },
        { id: '22222222-2222-4222-8222-222222222222', full_name: 'Yuki Tan', staff_no: 'STF-002', role: 'owner' },
    ],
};

const approval = { token: 'tok-1', manager_name: 'Dana Ramirez', expires_at: new Date(0).toISOString() };

let calls: { url: string; method?: string; body: Record<string, unknown> }[] = [];

beforeEach(() => {
    calls = [];
    localStorage.setItem('token', 'test-token');
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url, method: init?.method, body: init?.body ? JSON.parse(String(init.body)) : {} });
        if (String(url).includes('/step-up/managers')) return jsonResponse(managers, 200);
        return jsonResponse(approval, 201);
    }));
});

afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = '';
});

async function pickManager(el: StepUpDialog) {
    const select = q<GableSelect>(el, '[data-testid="step-up-manager"]');
    select.value = 'STF-001';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await update(el, {});
}

describe('the manager step up dialog', () => {
    it('lists the managers the act admits', async () => {
        const el = await mount<StepUpDialog>('gable-step-up-dialog', { act: 'override', targetId: 's-1' });
        await flush();
        const options = q<GableSelect>(el, '[data-testid="step-up-manager"]').options;
        expect(options.map((o) => o.value)).toEqual(['STF-001', 'STF-002']);
        expect(options[0].label).toContain('Dana Ramirez');
        expect(calls[0].url).toContain('/api/v1/pos/step-up/managers?act=override');
    });

    it('masks the PIN and types only digits from the keypad', async () => {
        const el = await mount<StepUpDialog>('gable-step-up-dialog', { act: 'override', targetId: 's-1' });
        await flush();
        const pin = q<HTMLInputElement>(el, '[data-testid="step-up-pin"]');
        expect(pin.type).toBe('password');
        for (const d of ['7', '3', '6', '1']) {
            q<HTMLButtonElement>(el, `[data-testid="step-up-key-${d}"]`).click();
            await update(el, {});
        }
        expect(q<HTMLInputElement>(el, '[data-testid="step-up-pin"]').value).toBe('7361');
        // a typed letter never lands
        pin.value = '12a';
        pin.dispatchEvent(new Event('input', { bubbles: true }));
        await update(el, {});
        expect(q<HTMLInputElement>(el, '[data-testid="step-up-pin"]').value).toBe('12');
        // and the backspace takes one digit
        q<HTMLButtonElement>(el, '[data-testid="step-up-key-del"]').click();
        await update(el, {});
        expect(q<HTMLInputElement>(el, '[data-testid="step-up-pin"]').value).toBe('1');
        // the clear empties it
        q<HTMLButtonElement>(el, '[data-testid="step-up-key-back"]').click();
        await update(el, {});
        expect(q<HTMLInputElement>(el, '[data-testid="step-up-pin"]').value).toBe('');
    });

    it('sends the manager, the pin and the target, and hands back the approval', async () => {
        const el = await mount<StepUpDialog>('gable-step-up-dialog', { act: 'pickup', targetId: 'till-1', amountCents: 1000 });
        await flush();
        await pickManager(el);
        for (const d of ['1', '7', '3', '9']) {
            q<HTMLButtonElement>(el, `[data-testid="step-up-key-${d}"]`).click();
            await update(el, {});
        }
        let heard: { token: string; managerName: string } | null = null;
        el.addEventListener('step-up-approved', (e: Event) => { heard = (e as CustomEvent).detail; });
        q<HTMLButtonElement>(el, '[data-testid="step-up-approve"]').click();
        await flush();
        expect(calls[1].url).toContain('/api/v1/pos/step-up');
        expect(calls[1].method).toBe('POST');
        expect(calls[1].body).toEqual({ act: 'pickup', manager: 'STF-001', pin: '1739', target_id: 'till-1', amount_cents: 1000 });
        expect(heard).toEqual({ token: 'tok-1', managerName: 'Dana Ramirez' });
    });

    it('says the server refusal in its own words and empties the PIN', async () => {
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            if (String(url).includes('/step-up/managers')) return jsonResponse(managers, 200);
            return jsonResponse({ error: { code: 'step_up_refused', message: 'That PIN is not right' } }, 403);
        }));
        const el = await mount<StepUpDialog>('gable-step-up-dialog', { act: 'override', targetId: 's-1' });
        await flush();
        await pickManager(el);
        for (const d of ['0', '0', '0', '0']) {
            q<HTMLButtonElement>(el, `[data-testid="step-up-key-${d}"]`).click();
            await update(el, {});
        }
        q<HTMLButtonElement>(el, '[data-testid="step-up-approve"]').click();
        await flush();
        await update(el, {});
        expect(q(el, '[data-testid="step-up-refusal"]').textContent).toContain('That PIN is not right');
        expect(q<HTMLInputElement>(el, '[data-testid="step-up-pin"]').value).toBe('');
    });

    it('sends nothing without a manager and a PIN', async () => {
        const el = await mount<StepUpDialog>('gable-step-up-dialog', { act: 'override', targetId: 's-1' });
        await flush();
        q<HTMLButtonElement>(el, '[data-testid="step-up-approve"]').click();
        await flush();
        expect(calls).toHaveLength(1); // only the managers list
    });

    it('says it plainly when no manager has a PIN', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({ items: [] }, 200)));
        const el = await mount<StepUpDialog>('gable-step-up-dialog', { act: 'override', targetId: 's-1' });
        await flush();
        expect(q(el, '[data-testid="step-up-empty"]').textContent)
            .toContain('No manager has a counter PIN yet: an admin sets one on the staff screen');
    });
});
