// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';

/**
 * A confirm dialog for a lifecycle act on a sales document (void an invoice,
 * post or void a credit memo). With `require-reason` it asks for a reason and
 * disables the confirm until one is typed; the page shows the server's refusal
 * through `error` and keeps the dialog open. Events: `confirm` with
 * detail.reason, and `close`.
 */
@customElement('gable-sales-dialog')
export class GableSalesDialog extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: Boolean, attribute: 'is-open' }) isOpen = false;
    @property({ type: String }) heading = '';
    @property({ type: String }) body = '';
    @property({ type: String, attribute: 'confirm-label' }) confirmLabel = 'Confirm';
    @property({ type: Boolean, attribute: 'require-reason' }) requireReason = false;
    @property({ type: Boolean }) danger = false;
    @property({ type: Boolean }) busy = false;
    @property({ type: String }) error = '';

    @state() private reason = '';

    updated(changed: Map<string, unknown>) {
        if (changed.has('isOpen') && !this.isOpen) this.reason = '';
    }

    private close() {
        this.dispatchEvent(new CustomEvent('close', { bubbles: true, composed: true }));
    }

    private submit(e: Event) {
        e.preventDefault();
        if (this.requireReason && !this.reason.trim()) return;
        this.dispatchEvent(new CustomEvent('confirm', {
            detail: { reason: this.reason.trim() },
            bubbles: true,
            composed: true,
        }));
    }

    render() {
        if (!this.isOpen) return nothing;
        const disabled = this.busy || (this.requireReason && !this.reason.trim());
        return html`
            <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm p-4" role="dialog" aria-modal="true" aria-labelledby="sales-dialog-title">
                <form @submit=${(e: Event) => this.submit(e)} class="w-full max-w-md bg-slate-steel border border-white/10 rounded-lg shadow-2xl p-6 space-y-4">
                    <h2 id="sales-dialog-title" class="text-xl font-bold text-white">${this.heading}</h2>
                    <p class="text-sm text-zinc-400">${this.body}</p>
                    ${this.requireReason ? html`
                        <div>
                            <label class="block text-xs uppercase tracking-wide text-zinc-500 mb-1" for="sales-dialog-reason">Reason</label>
                            <textarea
                                id="sales-dialog-reason"
                                rows="3"
                                maxlength="500"
                                .value=${this.reason}
                                @input=${(e: Event) => { this.reason = (e.target as HTMLTextAreaElement).value; }}
                                class="w-full bg-black/30 border border-white/10 rounded px-3 py-2 text-white text-sm focus:border-gable-green outline-none"
                                placeholder="Why is this being done?"
                            ></textarea>
                        </div>
                    ` : nothing}
                    ${this.error ? html`
                        <div class="p-3 rounded border border-red-500/40 bg-red-500/10 text-sm text-red-300 whitespace-pre-line" role="alert" data-testid="dialog-error">${this.error}</div>
                    ` : nothing}
                    <div class="flex justify-end gap-3 pt-2">
                        <button type="button" @click=${() => this.close()} class="px-4 py-2 text-sm text-zinc-300 hover:text-white">Cancel</button>
                        <button
                            type="submit"
                            ?disabled=${disabled}
                            class="px-4 py-2 rounded text-sm font-bold whitespace-nowrap disabled:opacity-50 ${this.danger ? 'bg-red-500 text-white hover:bg-red-600' : 'bg-gable-green text-black hover:bg-gable-green/90'}"
                        >
                            ${this.busy ? 'Working...' : this.confirmLabel}
                        </button>
                    </div>
                </form>
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-sales-dialog': GableSalesDialog;
    }
}
