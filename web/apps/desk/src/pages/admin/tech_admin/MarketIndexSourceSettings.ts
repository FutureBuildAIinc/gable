// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../../lib/icons.ts';
import { BarChart3, Shield } from 'lucide';
import { apiErrorMessage, fieldErrorMap } from '../../../services/apiError';
import { techAdminService } from '../../../services/TechAdminService';
import type { MarketIndexSourceSettings } from '../../../services/TechAdminService';

const FIELD_CLASS =
    'w-full bg-deep-space border border-white/10 rounded px-3 py-2 text-white text-sm placeholder-slate-500 focus:outline-none focus:border-gable-green';

/**
 * The Tech Admin card for the market index source (QE-07-b1): where the
 * dealer's market index values come from.
 *
 * Manual is the default and the fallback. The FB Connect feed is configured
 * here. Two of the fields are secrets and the server never answers them:
 * the feed URL (it carries the feed's identity in its path, so the address is
 * itself the secret) and the feed key's value. Both are stored sealed, shown
 * masked, and replaced whole.
 */
@customElement('gable-market-index-source-settings')
export class GableMarketIndexSourceSettings extends LitElement {
    createRenderRoot() { return this; }

    @state() private _settings: MarketIndexSourceSettings | null = null;
    @state() private _loading = true;
    @state() private _saving = false;
    @state() private _error: string | null = null;
    @state() private _fieldErrors: Record<string, string> = {};
    @state() private _success: string | null = null;
    /** The source the admin picked but has not saved yet. */
    @state() private _source: 'manual' | 'fb_connect_feed' = 'manual';
    /** The staleness threshold in days, as typed. */
    @state() private _stalenessDays = '7';
    /** The header the feed key rides in, as typed. */
    @state() private _keyHeader = '';
    /** The write only fields are shown when none is saved, or after Replace. */
    @state() private _replacingURL = false;
    @state() private _replacingKey = false;
    @state() private _newURL = '';
    @state() private _newKey = '';

    connectedCallback() {
        super.connectedCallback();
        void this._load();
    }

    private _adopt(s: MarketIndexSourceSettings) {
        this._settings = s;
        this._source = s.source;
        this._stalenessDays = String(s.staleness_days);
        this._keyHeader = s.feed_key_header;
        this._replacingURL = false;
        this._replacingKey = false;
        this._newURL = '';
        this._newKey = '';
    }

    private async _load() {
        this._loading = true;
        try {
            this._adopt(await techAdminService.getMarketIndexSource());
        } catch (err) {
            this._error = apiErrorMessage(err, 'Could not read the market index source');
        } finally {
            this._loading = false;
        }
    }

    private async _save() {
        if (!this._settings || this._saving) return;
        this._saving = true;
        this._error = null;
        this._success = null;
        this._fieldErrors = {};
        try {
            const input: Parameters<typeof techAdminService.saveMarketIndexSource>[0] = {
                source: this._source,
                staleness_days: Number(this._stalenessDays),
                feed_key_header: this._keyHeader.trim(),
            };
            // The two secrets go out only when they are typed: an untouched
            // field leaves the stored value alone.
            if (this._replacingURL) input.feed_url = this._newURL.trim();
            if (this._replacingKey) input.feed_key = this._newKey.trim();
            this._adopt(await techAdminService.saveMarketIndexSource(input));
            this._success = 'Market index source saved';
        } catch (err) {
            this._fieldErrors = fieldErrorMap(err);
            this._error = apiErrorMessage(err, 'Could not save the market index source');
        } finally {
            this._saving = false;
        }
    }

    private async _clear(field: 'feed_url' | 'feed_key') {
        if (!this._settings || this._saving) return;
        this._saving = true;
        this._error = null;
        this._success = null;
        try {
            const input: Parameters<typeof techAdminService.saveMarketIndexSource>[0] = { [field]: '' };
            if (field === 'feed_key') input.feed_key_header = '';
            this._adopt(await techAdminService.saveMarketIndexSource(input));
            this._success = 'Market index source saved';
        } catch (err) {
            this._fieldErrors = fieldErrorMap(err);
            this._error = apiErrorMessage(err, 'Could not save the market index source');
        } finally {
            this._saving = false;
        }
    }

    /**
     * One write only field: the stored value is masked and replaced whole,
     * never shown. When none is stored the field is simply empty.
     */
    private _secretField(opts: {
        name: string;
        label: string;
        placeholder: string;
        saved: boolean;
        replacing: boolean;
        value: string;
        testid: string;
        onInput: (v: string) => void;
        onReplace: () => void;
        onClear: () => void;
    }) {
        const err = this._fieldErrors[opts.name];
        return html`
            <div class="space-y-1" data-testid=${opts.testid}>
                <span class="text-xs uppercase tracking-wider text-slate-400">${opts.label}</span>
                ${opts.saved && !opts.replacing
                    ? html`<div class="flex items-center gap-3">
                        <span class="text-emerald-400 text-sm" data-testid="${opts.testid}-saved">Saved</span>
                        <span class="text-slate-500 text-xs font-mono" data-testid="${opts.testid}-masked">••••••••</span>
                        <button type="button" data-testid="${opts.testid}-replace" @click=${opts.onReplace}
                            class="px-3 py-1 border border-white/10 text-white rounded text-sm hover:bg-white/5">Replace</button>
                        <button type="button" data-testid="${opts.testid}-clear" ?disabled=${this._saving} @click=${opts.onClear}
                            class="px-3 py-1 border border-red-500/30 text-red-400 rounded text-sm hover:bg-red-500/10">Clear</button>
                    </div>`
                    : nothing}
                ${opts.replacing || !opts.saved
                    ? html`<div class="relative">
                        ${icon(Shield, 16, 'absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500')}
                        <input type="password" name=${opts.name} autocomplete="off" .value=${opts.value}
                            placeholder=${opts.placeholder}
                            @input=${(e: Event) => opts.onInput((e.target as HTMLInputElement).value)}
                            class="${FIELD_CLASS} pl-10 font-mono" />
                    </div>
                    ${err ? html`<span class="text-xs text-red-400">${err}</span>` : nothing}`
                    : nothing}
            </div>
        `;
    }

    render() {
        if (this._loading) return html`<div class="text-slate-400 p-8">Loading the market index source...</div>`;
        if (!this._settings) {
            return html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400" data-testid="error">${this._error}</div>`;
        }
        const s = this._settings;
        const lastPull = s.last_pull_at
            ? `${new Date(s.last_pull_at).toLocaleString()}${s.last_pull_ok === false ? ' (failed)' : s.last_pull_ok === true ? ' (ok)' : ''}`
            : 'No pull yet';
        return html`
            <div class="space-y-6">
                <div>
                    <h2 class="text-xl font-bold text-white flex items-center gap-2">
                        ${icon(BarChart3, 20, 'w-5 h-5 text-gable-green')}
                        Market index source
                    </h2>
                    <p class="text-slate-400 text-sm mt-1">
                        Where the dealer's market index values come from. Manual is the default and the fallback: a value
                        is typed in on the market indices screen or imported from a file.
                    </p>
                </div>

                <div class="bg-slate-steel border border-white/10 rounded-lg p-6 space-y-4">
                    <div class="space-y-1">
                        <span class="text-xs uppercase tracking-wider text-slate-400">Source</span>
                        <div class="flex gap-2">
                            <label class="flex items-center gap-2 text-sm text-white cursor-pointer">
                                <input type="radio" name="index-source" value="manual" data-testid="source-manual"
                                    .checked=${this._source === 'manual'}
                                    @change=${() => { this._source = 'manual'; }} />
                                Manual
                            </label>
                            <label class="flex items-center gap-2 text-sm text-white cursor-pointer">
                                <input type="radio" name="index-source" value="fb_connect_feed" data-testid="source-feed"
                                    .checked=${this._source === 'fb_connect_feed'}
                                    @change=${() => { this._source = 'fb_connect_feed'; }} />
                                FB Connect feed
                            </label>
                        </div>
                        ${this._fieldErrors.source ? html`<span class="text-xs text-red-400">${this._fieldErrors.source}</span>` : nothing}
                    </div>

                    ${this._secretField({
                        name: 'feed_url',
                        label: 'FB Connect feed address',
                        placeholder: 'https://connect.example.com/feed',
                        saved: s.feed_url_set,
                        replacing: this._replacingURL,
                        value: this._newURL,
                        testid: 'feed-url',
                        onInput: (v) => { this._newURL = v; },
                        onReplace: () => { this._replacingURL = true; },
                        onClear: () => { void this._clear('feed_url'); },
                    })}

                    <div class="space-y-1">
                        <span class="text-xs uppercase tracking-wider text-slate-400">Feed key header</span>
                        <input type="text" name="feed_key_header" data-testid="feed-key-header" .value=${this._keyHeader}
                            placeholder="X-FB-Connect-Key (optional)"
                            @input=${(e: Event) => { this._keyHeader = (e.target as HTMLInputElement).value; }}
                            class="${FIELD_CLASS} font-mono" />
                        <p class="text-xs text-slate-500">
                            The header the feed key rides in. Empty means the feed needs no key.
                        </p>
                        ${this._fieldErrors.feed_key_header ? html`<span class="text-xs text-red-400">${this._fieldErrors.feed_key_header}</span>` : nothing}
                    </div>

                    ${this._secretField({
                        name: 'feed_key',
                        label: 'Feed key',
                        placeholder: 'Paste the feed key',
                        saved: s.feed_key_set,
                        replacing: this._replacingKey,
                        value: this._newKey,
                        testid: 'feed-key',
                        onInput: (v) => { this._newKey = v; },
                        onReplace: () => { this._replacingKey = true; },
                        onClear: () => { void this._clear('feed_key'); },
                    })}

                    <label class="block space-y-1">
                        <span class="text-xs uppercase tracking-wider text-slate-400">Staleness threshold (days)</span>
                        <input type="number" name="staleness_days" min="1" max="365" data-testid="staleness-days"
                            .value=${this._stalenessDays}
                            @input=${(e: Event) => { this._stalenessDays = (e.target as HTMLInputElement).value; }}
                            class="${FIELD_CLASS} font-mono" />
                        <p class="text-xs text-slate-500">
                            How old an index's newest value may get before the desk is told.
                        </p>
                        ${this._fieldErrors.staleness_days ? html`<span class="text-xs text-red-400">${this._fieldErrors.staleness_days}</span>` : nothing}
                    </label>

                    <div class="space-y-1">
                        <span class="text-xs uppercase tracking-wider text-slate-400">Last pull</span>
                        <p class="text-sm text-white" data-testid="last-pull">${lastPull}</p>
                        ${s.last_pull_error ? html`<p class="text-xs text-red-400" data-testid="last-pull-error">${s.last_pull_error}</p>` : nothing}
                    </div>

                    <div class="flex justify-end">
                        <button type="button" data-testid="save" @click=${() => this._save()} ?disabled=${this._saving}
                            class="inline-flex items-center gap-2 bg-gable-green text-black font-semibold px-4 py-2 rounded disabled:opacity-50">
                            Save
                        </button>
                    </div>
                </div>

                ${this._error ? html`<div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 text-sm text-red-400" data-testid="error">${this._error}</div>` : nothing}
                ${this._success ? html`<div class="bg-emerald-500/10 border border-emerald-500/20 rounded-lg p-3 text-sm text-emerald-400" data-testid="success">${this._success}</div>` : nothing}
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-market-index-source-settings': GableMarketIndexSourceSettings;
    }
}
