// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../../lib/icons.ts';
import { Receipt, TriangleAlert } from 'lucide';
import { apiErrorMessage, fieldErrorMap } from '../../../services/apiError';
import { dealerProfileService, formatGSTHSTNumber } from '../../../services/DealerProfileService';
import type { DealerProfile } from '../../../services/DealerProfileService';

const FIELD_CLASS =
    'w-full bg-deep-space border border-white/10 rounded px-3 py-2 text-white text-sm placeholder-slate-500 focus:outline-none focus:border-gable-green font-mono';

/**
 * The Tech Admin tax setup panel (CP-14 A1): the dealer's GST/HST number, the
 * small supplier switch, and the standing setup warnings a Canadian document
 * needs answered. A missing number never stops a sale; it is named here until
 * it is set, and documents print a plain notice in its place.
 */
@customElement('gable-tax-setup')
export class GableTaxSetup extends LitElement {
    createRenderRoot() { return this; }

    @state() private _profile: DealerProfile | null = null;
    @state() private _number = '';
    @state() private _notRegistered = false;
    @state() private _loading = true;
    @state() private _saving = false;
    @state() private _error: string | null = null;
    @state() private _fieldErrors: Record<string, string> = {};
    @state() private _success: string | null = null;

    connectedCallback() {
        super.connectedCallback();
        void this._load();
    }

    private _adopt(p: DealerProfile) {
        this._profile = p;
        this._number = p.gst_hst_number;
        this._notRegistered = p.gst_hst_not_registered;
    }

    private async _load() {
        this._loading = true;
        try {
            this._adopt(await dealerProfileService.get());
        } catch (err) {
            this._error = apiErrorMessage(err, 'Could not read the dealer profile');
        } finally {
            this._loading = false;
        }
    }

    private async _save() {
        if (!this._profile || this._saving) return;
        this._saving = true;
        this._error = null;
        this._success = null;
        this._fieldErrors = {};
        try {
            const number = this._notRegistered ? '' : this._number.trim();
            this._adopt(await dealerProfileService.saveGSTHST(this._profile, number, this._notRegistered));
            this._success = 'GST/HST registration saved';
        } catch (err) {
            this._fieldErrors = fieldErrorMap(err);
            this._error = apiErrorMessage(err, 'Could not save the GST/HST registration');
        } finally {
            this._saving = false;
        }
    }

    /** The standing warnings, from what is saved (not the draft): each names what is missing. */
    private _warnings() {
        const p = this._profile!;
        const out: { id: string; words: string }[] = [];
        if (!p.gst_hst_not_registered && p.gst_hst_number === '') {
            out.push({ id: 'gst-hst', words: 'No GST/HST number is on file. Canadian invoices and receipts print a notice in its place until it is set, or mark the dealer not registered for GST/HST.' });
        }
        return out;
    }

    render() {
        if (this._loading) return html`<div class="text-slate-400 p-8">Loading tax setup...</div>`;
        if (!this._profile) {
            return html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400">${this._error}</div>`;
        }
        const warnings = this._warnings();
        const numErr = this._fieldErrors['gst_hst_number'] ?? this._fieldErrors['gst_hst_not_registered'];
        return html`
            <div class="space-y-6">
                <div>
                    <h2 class="text-xl font-bold text-white flex items-center gap-2">
                        ${icon(Receipt, 20, 'w-5 h-5 text-gable-green')}
                        Tax setup
                    </h2>
                    <p class="text-slate-400 text-sm mt-1">
                        What every Canadian invoice and receipt prints: GST and PST as their own lines, and the dealer's GST/HST number.
                    </p>
                </div>

                ${warnings.length > 0 ? html`
                    <div class="bg-red-500/10 border border-red-500/30 rounded-lg p-4 space-y-2" data-testid="setup-warnings">
                        ${warnings.map((w) => html`
                            <p class="text-sm text-red-300 flex gap-2" data-testid="setup-warning-${w.id}">
                                ${icon(TriangleAlert, 16, 'w-4 h-4 mt-0.5 shrink-0')} ${w.words}
                            </p>`)}
                    </div>` : nothing}

                <div class="bg-slate-steel border border-white/10 rounded-lg p-6 space-y-4">
                    <label class="flex items-center gap-2 text-sm text-white">
                        <input type="checkbox" name="gst_hst_not_registered" .checked=${this._notRegistered}
                            @change=${(e: Event) => { this._notRegistered = (e.target as HTMLInputElement).checked; }} />
                        Not registered for GST/HST (small supplier): no GST is charged or printed
                    </label>
                    ${this._notRegistered ? nothing : html`
                        <label class="block space-y-1">
                            <span class="text-xs uppercase tracking-wider text-slate-400">GST/HST number</span>
                            <input type="text" name="gst_hst_number" .value=${this._number} placeholder="123456789 RT0001"
                                @input=${(e: Event) => { this._number = (e.target as HTMLInputElement).value; }}
                                class=${FIELD_CLASS} />
                            ${this._profile.gst_hst_number
                                ? html`<span class="text-xs text-slate-400" data-testid="gst-hst-on-file">On file: ${formatGSTHSTNumber(this._profile.gst_hst_number)}</span>`
                                : nothing}
                        </label>`}
                    ${numErr ? html`<span class="block text-xs text-red-400" data-testid="gst-hst-error">${numErr}</span>` : nothing}
                    ${this._error && !numErr ? html`<div class="text-sm text-red-400">${this._error}</div>` : nothing}
                    ${this._success ? html`<div class="text-sm text-emerald-400">${this._success}</div>` : nothing}
                    <button type="button" data-testid="save-gst-hst" ?disabled=${this._saving} @click=${() => this._save()}
                        class="px-4 py-2 bg-gable-green text-deep-space rounded font-semibold text-sm disabled:opacity-50">
                        ${this._saving ? 'Saving...' : 'Save'}
                    </button>
                </div>
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-tax-setup': GableTaxSetup;
    }
}
