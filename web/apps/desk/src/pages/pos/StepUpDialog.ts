// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { posService } from '../../services/POSService.ts';
import '../../components/ui/gable-select';
import { apiErrorMessage } from '../../services/apiError.ts';
import type { StepUpManager } from '../../types/pos.ts';

const FIELD = 'width:100%;box-sizing:border-box;padding:10px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-text));font-size:15px;outline:none';
const LABEL = 'display:block;font-size:12px;color:rgb(var(--gb-till-muted));margin:12px 0 6px';

/**
 * The manager step up at the terminal (CP-04): the manager is picked from
 * the roster and types their own counter PIN on a masked field with a
 * numeric keypad for touch. A refusal is said in the server's plain words
 * and nothing is kept; an approval hands the caller the single use token
 * and the manager's name, and the caller's act spends them.
 */
@customElement('gable-step-up-dialog')
export class StepUpDialog extends LitElement {
    createRenderRoot() { return this; }

    /** The act the approval is for. */
    @property() act: 'override' | 'pickup' | 'signoff' = 'override';

    /** The act's target: the sale, or the till session. */
    @property({ attribute: 'target-id' }) targetId = '';

    /** The pickup's amount; the approval binds it. */
    @property({ type: Number, attribute: 'amount-cents' }) amountCents?: number;

    /** The words at the top, naming what the manager is approving. */
    @property() heading = 'A manager approves';

    @state() private managers: StepUpManager[] = [];
    @state() private manager = '';
    @state() private pin = '';
    @state() private refusal = '';
    @state() private loading = true;
    @state() private busy = false;

    async connectedCallback() {
        super.connectedCallback();
        try {
            this.managers = await posService.stepUpManagers(this.act);
        } catch {
            this.managers = [];
        } finally {
            this.loading = false;
        }
    }

    private _digit(d: string) {
        if (this.pin.length >= 8) return;
        this.pin += d;
        this.refusal = '';
    }

    private _backspace() {
        this.pin = this.pin.slice(0, -1);
        this.refusal = '';
    }

    private _cancel() {
        this.dispatchEvent(new CustomEvent('step-up-cancel', { bubbles: true }));
    }

    private async _approve() {
        if (this.manager === '' || this.pin === '' || this.busy) return;
        try {
            this.busy = true;
            this.refusal = '';
            const approval = await posService.stepUp(this.act, this.manager, this.pin, this.targetId, this.amountCents);
            this.dispatchEvent(new CustomEvent('step-up-approved', {
                detail: { token: approval.token, managerName: approval.manager_name }, bubbles: true,
            }));
        } catch (err: unknown) {
            this.refusal = apiErrorMessage(err, 'Failed to ask the manager');
            // the PIN is tried again from empty: never what was refused
            this.pin = '';
        } finally {
            this.busy = false;
        }
    }

    render() {
        return html`
            <div role="dialog" aria-modal="true" aria-label=${this.heading} data-testid="step-up-dialog"
                style="position:fixed;inset:0;z-index:60;display:flex;align-items:center;justify-content:center;background:rgb(var(--gb-backdrop) / 0.6);padding:16px">
                <div style="width:100%;max-width:380px;max-height:90vh;overflow:auto;background:rgb(var(--gb-till-surface));border:1px solid rgb(var(--gb-till-edge));border-radius:14px;padding:20px;color:rgb(var(--gb-till-text))">
                    <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">${this.heading}</h2>
                    <p style="margin:0 0 6px;font-size:13px;color:rgb(var(--gb-till-muted))">The manager picks themselves and types their own counter PIN.</p>
                    ${this.loading ? html`<p style="font-size:13px;color:rgb(var(--gb-till-muted))" data-testid="step-up-loading">Loading the managers\u2026</p>` : this.managers.length === 0 ? html`
                        <p role="alert" data-testid="step-up-empty" style="margin:12px 0 0;font-size:13px;color:rgb(var(--gb-till-bad))">No manager has a counter PIN yet: an admin sets one on the staff screen.</p>
                        <div style="display:flex;margin-top:20px">
                            <button type="button" data-testid="step-up-cancel" @click=${() => this._cancel()}
                                style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Close</button>
                        </div>` : html`
                    <span style=${LABEL}>Manager</span>
                    <gable-select data-testid="step-up-manager" label="Manager" placeholder="Pick the manager"
                        .value=${this.manager}
                        .options=${this.managers.map((m) => ({ value: m.staff_no || m.id, label: m.staff_no ? `${m.full_name} (${m.staff_no})` : m.full_name }))}
                        @change=${(e: Event) => { this.manager = (e.target as HTMLSelectElement).value; this.refusal = ''; }}></gable-select>

                    <label style=${LABEL} for="step-up-pin">Counter PIN</label>
                    <input id="step-up-pin" data-testid="step-up-pin" type="password" inputmode="numeric" autocomplete="off"
                        .value=${this.pin} maxlength="8"
                        @input=${(e: Event) => { this.pin = (e.target as HTMLInputElement).value.replace(/\D/g, '').slice(0, 8); this.refusal = ''; }}
                        style="${FIELD};font-size:26px;letter-spacing:8px;text-align:center" placeholder="\u2022\u2022\u2022\u2022" />
                    <div style="display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin-top:12px" role="group" aria-label="The keypad">
                        ${['1', '2', '3', '4', '5', '6', '7', '8', '9'].map((d) => html`
                            <button type="button" data-testid="step-up-key-${d}" @click=${() => this._digit(d)}
                                style="padding:14px;border-radius:8px;border:1px solid rgb(var(--gb-till-edge));background:rgb(var(--gb-till-field));color:rgb(var(--gb-till-text));font-size:18px;font-weight:700;cursor:pointer">${d}</button>
                        `)}
                        <button type="button" data-testid="step-up-key-back" @click=${() => this._backspace()}
                            style="padding:14px;border-radius:8px;border:1px solid rgb(var(--gb-till-edge));background:transparent;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Clear</button>
                        <button type="button" data-testid="step-up-key-0" @click=${() => this._digit('0')}
                            style="padding:14px;border-radius:8px;border:1px solid rgb(var(--gb-till-edge));background:rgb(var(--gb-till-field));color:rgb(var(--gb-till-text));font-size:18px;font-weight:700;cursor:pointer">0</button>
                        <button type="button" data-testid="step-up-key-del" @click=${() => this._backspace()}
                            style="padding:14px;border-radius:8px;border:1px solid rgb(var(--gb-till-edge));background:transparent;color:rgb(var(--gb-till-muted));font-size:18px;cursor:pointer">\u232b</button>
                    </div>

                    ${this.refusal ? html`<p role="alert" data-testid="step-up-refusal" style="margin:12px 0 0;font-size:13px;color:rgb(var(--gb-till-bad))">${this.refusal}</p>` : nothing}
                    <div style="display:flex;gap:8px;margin-top:20px">
                        <button type="button" data-testid="step-up-cancel" @click=${() => this._cancel()}
                            style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Cancel</button>
                        <button type="button" data-testid="step-up-approve" @click=${() => void this._approve()} ?disabled=${this.busy}
                            style="flex:2;padding:12px;background:rgb(var(--gb-till-accent));border:none;border-radius:8px;color:rgb(var(--gb-plate));font-size:15px;font-weight:700;cursor:pointer">${this.busy ? 'Asking\u2026' : 'Approve'}</button>
                    </div>
                        `}
                </div>
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-step-up-dialog': StepUpDialog;
    }
}
