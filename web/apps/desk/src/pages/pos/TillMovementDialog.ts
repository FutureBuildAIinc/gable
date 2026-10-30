// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import '../../components/ui/gable-select';
import { posService, dollarsToCents } from '../../services/POSService';
import './StepUpDialog';
import type { MovementKind, TillMovement } from '../../types/pos';
import {
    MOVEMENT_KINDS,
    MOVEMENT_REASONS,
    isPaidKind,
    kindLabel,
    planMovement,
    type MovementForm,
} from './movements.ts';

const fmt = (cents: number) => `$${(cents / 100).toFixed(2)}`;

const FIELD = 'width:100%;box-sizing:border-box;padding:10px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-text));font-size:15px;outline:none';
const LABEL = 'display:block;font-size:12px;color:rgb(var(--gb-till-muted));margin:12px 0 6px';

/**
 * The drawer's cash movements: a safe drop, a pickup by a second person, cash
 * paid in or paid out with a reason from the closed list. It sits in the
 * counter's till dialog frame (the terminal wraps it) and speaks the counter's
 * colours. It records one movement against the open session and tells the
 * terminal, which keeps the drawer.
 */
@customElement('gable-till-movement-dialog')
export class TillMovementDialog extends LitElement {
    createRenderRoot() { return this; }

    /** The open till session the movement lands in. */
    @property({ attribute: 'session-id' }) sessionId = '';

    @state() private form: MovementForm = { kind: 'safe_drop', amount: '', reason: '', note: '', takenBy: '' };
    @state() private refusal: { field: string; message: string } | null = null;
    @state() private failure = '';
    @state() private busy = false;
    // A pickup's taker may be a manager stepping up with their own counter
    // PIN at this terminal (CP-04): the approval names the taker.
    @state() private asking = false;

    private choose(kind: MovementKind) {
        this.form = { kind, amount: this.form.amount, reason: '', note: '', takenBy: '' };
        this.refusal = null;
        this.failure = '';
    }

    private set(patch: Partial<MovementForm>) {
        this.form = { ...this.form, ...patch };
        this.refusal = null;
        this.failure = '';
    }

    private async record(approvalToken?: string) {
        // a pickup spent on a step up approval names no taker: the manager is
        // named by the approval, never typed, so that one plan refusal is
        // lifted on the token path and taken_by is not sent
        const withToken = approvalToken !== undefined;
        const plan = planMovement(withToken && this.form.kind === 'pickup' ? { ...this.form, takenBy: 'by approval' } : this.form)
        if (!plan.ok) {
            this.refusal = { field: plan.field, message: plan.message };
            await this.updateComplete;
            (this.querySelector(`[data-testid="movement-${plan.field}"]`) as HTMLElement | null)?.focus();
            return;
        }
        const request = !withToken
            ? plan.request
            : { kind: plan.request.kind, amount_cents: plan.request.amount_cents, note: plan.request.note, approval_token: approvalToken };
        try {
            this.busy = true;
            const movement = await posService.recordMovement(this.sessionId, request);
            this.dispatchEvent(new CustomEvent<TillMovement>('movement-recorded', { detail: movement, bubbles: true }));
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to record the movement';
        } finally {
            this.busy = false;
        }
    }

    /** Opens the manager step up for this pickup's amount; the token records it. */
    private _askTaker() {
        const cents = dollarsToCents(this.form.amount);
        if (isNaN(cents) || cents <= 0) {
            this.refusal = { field: 'amount', message: 'Enter the amount first' };
            return;
        }
        this.form = { ...this.form, takenBy: '' };
        this.refusal = null;
        this.failure = '';
        this.asking = true;
    }

    /** The Record button's words: the kind, and the amount once it parses. */
    private recordLabel(): string {
        const cents = dollarsToCents(this.form.amount);
        const name = kindLabel(this.form.kind).toLowerCase();
        return !isNaN(cents) && cents > 0 ? `Record ${name} ${fmt(cents)}` : `Record ${name}`;
    }

    private cancel() {
        this.dispatchEvent(new CustomEvent('movement-cancel', { bubbles: true }));
    }

    private error(field: string) {
        return this.refusal?.field === field
            ? html`<p role="alert" data-testid="movement-${field}-error" style="margin:6px 0 0;font-size:13px;color:rgb(var(--gb-till-bad))">${this.refusal.message}</p>`
            : nothing;
    }

    render() {
        const { kind } = this.form;
        const hint = MOVEMENT_KINDS.find((k) => k.kind === kind)?.hint ?? '';
        return html`
            <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Drawer Cash</h2>
            <p style="margin:0 0 14px;font-size:13px;color:rgb(var(--gb-till-muted))">${hint}. The drawer's expected cash moves with it.</p>
            <div style="display:grid;grid-template-columns:1fr 1fr;gap:8px" role="group" aria-label="Kind of movement">
                ${MOVEMENT_KINDS.map((k) => html`
                    <button type="button" data-testid="movement-kind-${k.kind}" aria-pressed=${kind === k.kind ? 'true' : 'false'}
                        @click=${() => this.choose(k.kind)}
                        style="padding:10px;border-radius:8px;font-size:14px;font-weight:600;cursor:pointer;border:1px solid ${kind === k.kind ? 'rgb(var(--gb-till-accent))' : 'rgb(var(--gb-till-edge))'};background:${kind === k.kind ? 'rgb(var(--gb-till-field))' : 'transparent'};color:${kind === k.kind ? 'rgb(var(--gb-till-text))' : 'rgb(var(--gb-till-muted))'}">${k.label}</button>
                `)}
            </div>

            <label style=${LABEL} for="movement-amount">Amount ($)</label>
            <input id="movement-amount" type="number" step="0.01" min="0" data-testid="movement-amount" .value=${this.form.amount}
                @input=${(e: Event) => this.set({ amount: (e.target as HTMLInputElement).value })}
                class="placeholder:font-normal placeholder:text-[rgb(var(--gb-till-muted))]"
                style="${FIELD};font-size:22px;font-weight:700;text-align:center" placeholder="Type the amount" />
            ${this.error('amount')}

            ${isPaidKind(kind) ? html`
                <span style=${LABEL}>Reason</span>
                <gable-select id="movement-reason" data-testid="movement-reason" label="Reason" placeholder="Choose a reason"
                    .value=${this.form.reason} .options=${MOVEMENT_REASONS[kind]}
                    @change=${(e: Event) => this.set({ reason: (e.target as HTMLSelectElement).value })}></gable-select>
                ${this.error('reason')}
                <label style=${LABEL} for="movement-note">${this.form.reason === 'other' ? 'What for' : 'Note (optional)'}</label>
                <input id="movement-note" type="text" data-testid="movement-note" .value=${this.form.note}
                    @input=${(e: Event) => this.set({ note: (e.target as HTMLInputElement).value })} style=${FIELD} />
                ${this.error('note')}
            ` : nothing}

            ${kind === 'pickup' ? html`
                <label style=${LABEL} for="movement-taken-by">Taken by (the second person's login)</label>
                <input id="movement-taken-by" type="text" data-testid="movement-taken_by" .value=${this.form.takenBy}
                    @input=${(e: Event) => this.set({ takenBy: (e.target as HTMLInputElement).value })} style=${FIELD} />
                ${this.error('taken_by')}
                <p style="margin:8px 0 0;font-size:13px;color:rgb(var(--gb-till-muted))">Or the manager taking it steps up with their own counter PIN:</p>
                <button type="button" data-testid="movement-ask-manager" @click=${() => this._askTaker()}
                    style="margin-top:8px;width:100%;padding:12px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-accent));border-radius:8px;color:rgb(var(--gb-till-text));font-size:14px;font-weight:600;cursor:pointer">Ask the manager taking it</button>
            ` : nothing}

            ${this.failure ? html`<p role="alert" data-testid="movement-failure" style="margin:12px 0 0;font-size:13px;color:rgb(var(--gb-till-bad))">${this.failure}</p>` : nothing}
            ${this.asking && kind === 'pickup' ? html`
                <gable-step-up-dialog act="pickup" target-id=${this.sessionId}
                    .amountCents=${dollarsToCents(this.form.amount)}
                    heading="A manager takes the pickup"
                    .detail=${`Take ${fmt(dollarsToCents(this.form.amount))} from the drawer`}
                    @step-up-cancel=${() => { this.asking = false; }}
                    @step-up-approved=${async (e: CustomEvent<{ token: string }>) => { this.asking = false; await this.record(e.detail.token); }}></gable-step-up-dialog>
            ` : nothing}
            <div style="display:flex;gap:8px;margin-top:20px">
                <button type="button" data-testid="movement-cancel" @click=${() => this.cancel()}
                    style="flex:1;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Cancel</button>
                <button type="button" data-testid="movement-record" @click=${() => this.record()} ?disabled=${this.busy}
                    style="flex:2;padding:12px;background:rgb(var(--gb-till-accent));border:none;border-radius:8px;color:rgb(var(--gb-deep-space));font-size:15px;font-weight:700;cursor:pointer">${this.busy ? 'Recording\u2026' : this.recordLabel()}</button>
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-till-movement-dialog': TillMovementDialog;
    }
}
