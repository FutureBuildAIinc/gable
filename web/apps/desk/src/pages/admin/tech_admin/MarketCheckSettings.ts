// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../../lib/icons.ts';
import { Radar, Shield } from 'lucide';
import { apiErrorMessage, fieldErrorMap } from '../../../services/apiError';
import { marketCheckService } from '../../../services/MarketCheckService';
import type { MarketCheckSettings, MarketCheckSettingsInput } from '../../../services/MarketCheckService';

interface Draft {
    connect_url: string;
    flow_name: string;
    store_label: string;
    store_id: string;
    store_zip: string;
    band_pct: string;
    margin_floor_pct: string;
}

const FIELD_CLASS =
    'w-full bg-deep-space border border-white/10 rounded px-3 py-2 text-white text-sm placeholder-slate-500 focus:outline-none focus:border-gable-green';

/** The Tech Admin card for the Market check: the FB Connect address and the flow's webhook id. */
@customElement('gable-market-check-settings')
export class GableMarketCheckSettings extends LitElement {
    createRenderRoot() { return this; }

    @state() private _settings: MarketCheckSettings | null = null;
    @state() private _draft: Draft | null = null;
    @state() private _loading = true;
    @state() private _saving = false;
    @state() private _error: string | null = null;
    @state() private _fieldErrors: Record<string, string> = {};
    @state() private _success: string | null = null;
    /** The write only id field is shown when none is saved, or after Replace. */
    @state() private _replacing = false;
    @state() private _newId = '';

    connectedCallback() {
        super.connectedCallback();
        void this._load();
    }

    private _adopt(s: MarketCheckSettings) {
        this._settings = s;
        this._draft = {
            connect_url: s.connect_url,
            flow_name: s.flow_name,
            store_label: s.store_label,
            store_id: s.store_id,
            store_zip: s.store_zip,
            band_pct: s.band_pct,
            margin_floor_pct: s.margin_floor_pct,
        };
        this._replacing = false;
        this._newId = '';
    }

    private async _load() {
        this._loading = true;
        try {
            this._adopt(await marketCheckService.getSettings());
        } catch (err) {
            this._error = apiErrorMessage(err, 'Could not read the Market check settings');
        } finally {
            this._loading = false;
        }
    }

    private async _save(extra: MarketCheckSettingsInput = {}) {
        if (!this._draft || this._saving) return;
        this._saving = true;
        this._error = null;
        this._success = null;
        this._fieldErrors = {};
        try {
            const input: MarketCheckSettingsInput = { ...this._draft, ...extra };
            if (this._newId.trim() && extra.webhook_id === undefined) input.webhook_id = this._newId.trim();
            this._adopt(await marketCheckService.saveSettings(input));
            this._success = 'Market check settings saved';
        } catch (err) {
            this._fieldErrors = fieldErrorMap(err);
            this._error = apiErrorMessage(err, 'Could not save the Market check settings');
        } finally {
            this._saving = false;
        }
    }

    private _field(key: keyof Draft, label: string, placeholder = '', mono = false) {
        const d = this._draft!;
        const err = this._fieldErrors[key];
        return html`
            <label class="block space-y-1">
                <span class="text-xs uppercase tracking-wider text-slate-400">${label}</span>
                <input type="text" name=${key} .value=${d[key]} placeholder=${placeholder}
                    @input=${(e: Event) => { // the current draft, not the one captured at render: several fields set before a re-render (autofill) all stand
                    this._draft = { ...this._draft!, [key]: (e.target as HTMLInputElement).value }; }}
                    class="${FIELD_CLASS} ${mono ? 'font-mono' : ''}" />
                ${err ? html`<span class="text-xs text-red-400">${err}</span>` : nothing}
            </label>
        `;
    }

    render() {
        if (this._loading) return html`<div class="text-slate-400 p-8">Loading Market check settings...</div>`;
        if (!this._settings || !this._draft) {
            return html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400">${this._error}</div>`;
        }
        const saved = this._settings.webhook_id_saved;
        const showInput = !saved || this._replacing;
        return html`
            <div class="space-y-6">
                <div>
                    <h2 class="text-xl font-bold text-white flex items-center gap-2">
                        ${icon(Radar, 20, 'w-5 h-5 text-gable-green')}
                        Market check
                    </h2>
                    <p class="text-slate-400 text-sm mt-1">
                        Reads our prices against a live competitor store through an FB Connect pricing flow. The webhook id
                        is a secret: anyone holding it can run the flow, so it is stored sealed and never shown again.
                    </p>
                </div>

                <div class="bg-slate-steel border border-white/10 rounded-lg p-6 space-y-4">
                    ${this._field('connect_url', 'FB Connect address', 'https://connect.example.com', true)}

                    <div class="space-y-1" data-testid="webhook-id">
                        <span class="text-xs uppercase tracking-wider text-slate-400">Flow webhook id</span>
                        ${saved && !this._replacing
                            ? html`<div class="flex items-center gap-3">
                                <span class="text-emerald-400 text-sm" data-testid="webhook-saved">Saved</span>
                                <button type="button" data-testid="replace-id" @click=${() => { this._replacing = true; }}
                                    class="px-3 py-1 border border-white/10 text-white rounded text-sm hover:bg-white/5">Replace</button>
                                <button type="button" data-testid="clear-id" ?disabled=${this._saving}
                                    @click=${() => this._save({ webhook_id: '' })}
                                    class="px-3 py-1 border border-red-500/30 text-red-400 rounded text-sm hover:bg-red-500/10">Clear</button>
                            </div>`
                            : nothing}
                        ${this._settings.notice
                            ? html`<p class="text-sm text-amber-400" data-testid="notice">${this._settings.notice}</p>`
                            : nothing}
                        ${showInput
                            ? html`<div class="relative">
                                ${icon(Shield, 16, 'absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500')}
                                <input type="password" name="webhook_id" autocomplete="off" .value=${this._newId}
                                    placeholder="Paste the flow's webhook id"
                                    @input=${(e: Event) => { this._newId = (e.target as HTMLInputElement).value; }}
                                    class="${FIELD_CLASS} pl-10 font-mono" />
                            </div>
                            ${this._fieldErrors.webhook_id ? html`<span class="text-xs text-red-400">${this._fieldErrors.webhook_id}</span>` : nothing}`
                            : nothing}
                    </div>

                    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                        ${this._field('flow_name', 'Flow name', 'FB Connect pricing flow')}
                        ${this._field('store_label', 'Store label', 'Home Depot, Spokane WA: nearest store with live prices')}
                        ${this._field('store_id', 'Store id', '', true)}
                        ${this._field('store_zip', 'Store ZIP', '', true)}
                        ${this._field('band_pct', 'Band percent', '5', true)}
                        ${this._field('margin_floor_pct', 'Margin floor percent', '15', true)}
                    </div>

                    <div class="flex justify-end">
                        <button type="button" data-testid="save" @click=${() => this._save()} ?disabled=${this._saving}
                            class="inline-flex items-center gap-2 bg-gable-green text-black font-semibold px-4 py-2 rounded disabled:opacity-50">
                            Save
                        </button>
                    </div>
                </div>

                ${this._error ? html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400" data-testid="error">${this._error}</div>` : nothing}
                ${this._success ? html`<div class="bg-emerald-500/10 border border-emerald-500/20 rounded-lg p-3 text-sm text-emerald-400">${this._success}</div>` : nothing}
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-market-check-settings': GableMarketCheckSettings;
    }
}
