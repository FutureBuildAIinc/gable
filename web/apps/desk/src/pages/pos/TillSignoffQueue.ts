// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { posService } from '../../services/POSService.ts';
import './StepUpDialog';
import { ApiError } from '../../services/apiError.ts';
import { onBranchChanged } from '../../lib/branch-listener.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { formatCents } from '../../lib/utils.ts';
import type { TillSession } from '../../types/pos.ts';

/**
 * The drawers waiting for a manager (CP-08), on the Daily Till page: each
 * counted drawer whose over or short passed the branch's variance threshold,
 * with the reason a manager gives to sign it off. The queue is the managers':
 * where the server refuses it, the section does not show.
 */
@customElement('gable-till-signoff-queue')
export class GableTillSignoffQueue extends LitElement {
    createRenderRoot() { return this; }

    @state() private items: TillSession[] = [];
    @state() private allowed = true;
    @state() private reasons: Record<string, string> = {};
    @state() private failures: Record<string, string> = {};
    @state() private busy = '';
    // The drawer whose variance a manager is stepping up for with their own
    // counter PIN (CP-04): the approval signs it off.
    @state() private asking: TillSession | null = null;

    private unsubscribe: (() => void) | null = null;

    connectedCallback() {
        super.connectedCallback();
        this.unsubscribe = onBranchChanged(() => { void this.load(); });
        void this.load();
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        this.unsubscribe?.();
    }

    private async load() {
        try {
            this.items = await posService.pendingSignoffs();
            this.allowed = true;
        } catch (err: unknown) {
            if (err instanceof ApiError && err.status === 403) {
                this.allowed = false;
            }
            this.items = [];
        }
    }

    /** The Sign off button: a reason is asked first, then the manager steps up. */
    private signOff(s: TillSession) {
        const reason = (this.reasons[s.id] ?? '').trim();
        if (reason === '') {
            this.failures = { ...this.failures, [s.id]: 'Say why the variance is accepted.' };
            return;
        }
        this.asking = s;
    }

    private async _signOff(s: TillSession, approvalToken?: string, managerName?: string) {
        const reason = (this.reasons[s.id] ?? '').trim();
        try {
            this.busy = s.id;
            this.failures = { ...this.failures, [s.id]: '' };
            await posService.signOffTill(s.id, reason, approvalToken);
            this.items = this.items.filter((i) => i.id !== s.id);
            ToastService.show(managerName ? `Register ${s.register_id} signed off by ${managerName}` : `Register ${s.register_id} signed off`, 'success');
        } catch (err: unknown) {
            this.failures = { ...this.failures, [s.id]: err instanceof Error ? err.message : 'Failed to sign off the till' };
        } finally {
            this.busy = '';
        }
    }

    render() {
        if (!this.allowed) return nothing;
        return html`
            <div class="bg-zinc-900 border border-zinc-800 rounded-lg p-6" data-testid="signoff-queue">
                <h2 class="text-xl font-bold text-amber-500 mb-6">Drawers waiting for sign off</h2>
                ${this.items.length === 0 ? html`
                    <p class="text-zinc-500" data-testid="signoff-empty">No counted drawer waits for a manager.</p>` : html`
                    <div class="space-y-4">
                        ${this.items.map((s) => {
                            const os = s.over_short_cents ?? 0;
                            return html`
                                <div class="p-4 bg-black/20 rounded border border-white/5 space-y-3" data-testid="signoff-${s.id}">
                                    <div class="flex items-center justify-between gap-4">
                                        <span class="font-bold text-zinc-200">Register ${s.register_id}
                                            <span class="font-normal text-zinc-500">closed by ${s.closed_by ?? 'unknown'}</span></span>
                                        <span class="font-mono text-xl ${os < 0 ? 'text-red-400' : 'text-amber-500'}" data-testid="signoff-variance">
                                            ${os < 0 ? 'Short' : 'Over'} ${formatCents(Math.abs(os))}
                                        </span>
                                    </div>
                                    <p class="text-xs text-zinc-500">
                                        Limit ${formatCents(s.variance_threshold_cents ?? 0)}.
                                        Expected ${formatCents(s.expected_by_method?.CASH ?? 0)}, counted ${formatCents(s.counted_by_method?.CASH ?? 0)} in cash.
                                    </p>
                                    <div class="flex flex-wrap items-center gap-3">
                                        <input type="text" placeholder="Why the variance is accepted" data-testid="signoff-reason-${s.id}"
                                            .value=${this.reasons[s.id] ?? ''}
                                            @input=${(e: Event) => { this.reasons = { ...this.reasons, [s.id]: (e.target as HTMLInputElement).value }; }}
                                            class="flex-1 min-w-[16rem] px-3 py-2 bg-black/20 border border-zinc-700 rounded text-white" />
                                        <button data-testid="signoff-button-${s.id}" @click=${() => this.signOff(s)} ?disabled=${this.busy === s.id}
                                            class="px-4 py-2 rounded bg-emerald-500 text-black text-sm font-bold">${this.busy === s.id ? 'Signing off...' : 'Sign off'}</button>
                                    </div>
                                    ${this.failures[s.id] ? html`<p class="text-red-400 text-sm" role="alert" data-testid="signoff-error-${s.id}">${this.failures[s.id]}</p>` : nothing}
                                </div>`;
                        })}
                    </div>`}
            </div>
            ${this.asking !== null ? html`
                <gable-step-up-dialog act="signoff" target-id=${this.asking.id}
                    heading="A manager signs off the variance"
                    @step-up-cancel=${() => { this.asking = null; }}
                    @step-up-approved=${async (e: CustomEvent<{ token: string; managerName: string }>) => {
                        const s = this.asking;
                        this.asking = null;
                        if (s) await this._signOff(s, e.detail.token, e.detail.managerName);
                    }}></gable-step-up-dialog>
            ` : nothing}
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-till-signoff-queue': GableTillSignoffQueue;
    }
}
