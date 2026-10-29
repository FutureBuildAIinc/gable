// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * <gable-admin-tax-zones> — admin page for Gable's own tax zone tables
 * (CP-10): the zones a delivered sale's tax is sourced to, their rate parts
 * and their boundaries. Mounted at `/admin/tax-zones` (erp layout,
 * admin-only). A zone that documents were taxed under is closed, never
 * deleted, so the sales keep the rate they were charged.
 */
import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../../lib/icons.ts';
import { MapPin, Plus, X, Ban } from 'lucide';
import { TaxZoneService, combinedRatePercent } from '../../../services/TaxZoneService.ts';
import { apiErrorMessage } from '../../../services/apiError.ts';
import { ToastService } from '../../../lib/toast-service.ts';
import '../../../components/ui/gable-select';
import type { TaxZone, TaxZoneRatePart } from '../../../types/taxZone.ts';

interface ZoneForm {
  country: string;
  jurisdiction_code: string;
  jurisdiction_type: string;
  jurisdiction_name: string;
  zone_kind: string;
  sourcing: string;
  province_code: string;
  effective_from: string;
  data_source: string;
  data_version: string;
  postal_low: string;
  postal_high: string;
  rates: TaxZoneRatePart[];
}

const EMPTY_FORM: ZoneForm = {
  country: 'US',
  jurisdiction_code: '',
  jurisdiction_type: 'CTY',
  jurisdiction_name: '',
  zone_kind: 'POSTAL',
  sourcing: 'DESTINATION',
  province_code: '',
  effective_from: new Date().toISOString().slice(0, 10),
  data_source: '',
  data_version: '',
  postal_low: '',
  postal_high: '',
  rates: [{ name: 'STATE', rate: '', effective_from: new Date().toISOString().slice(0, 10) }],
};

const PART_NAMES = ['STATE', 'COUNTY', 'CITY', 'DISTRICT', 'GOODS_TAX', 'PROVINCIAL_SALES_TAX'];
const JURISDICTION_TYPES = ['STA', 'CTY', 'CIT', 'STJ', 'PROV'];

/** The zone's boundary text, as a clerk reads it: one code or a range. */
export function boundaryText(z: TaxZone): string {
  if (z.zone_kind === 'PROVINCE') return z.province_code || '';
  return z.boundaries
    .map((b) => (b.postal_code_high && b.postal_code_high !== b.postal_code_low
      ? `${b.postal_code_low} to ${b.postal_code_high}`
      : b.postal_code_low))
    .join(', ');
}

/** The parts as a clerk reads them: name and percent each. */
export function partsText(z: TaxZone): string {
  return z.rates
    .map((r) => `${r.name} ${combinedRatePercent([r])}%`)
    .join(' + ');
}

@customElement('gable-admin-tax-zones')
export class GableAdminTaxZones extends LitElement {
  createRenderRoot() { return this; }

  @state() private _zones: TaxZone[] = [];
  @state() private _loading = true;
  @state() private _error: string | null = null;
  @state() private _country = '';
  @state() private _editorOpen = false;
  @state() private _saving = false;
  @state() private _form: ZoneForm = { ...EMPTY_FORM, rates: [...EMPTY_FORM.rates] };

  connectedCallback() {
    super.connectedCallback();
    this._load();
  }

  private async _load() {
    this._loading = true;
    this._error = null;
    try {
      this._zones = await TaxZoneService.list(this._country ? { country: this._country } : {});
    } catch (err) {
      this._error = apiErrorMessage(err, 'Failed to load tax zones');
    } finally {
      this._loading = false;
    }
  }

  private _openCreate() {
    this._form = { ...EMPTY_FORM, rates: [{ ...EMPTY_FORM.rates[0] }] };
    this._editorOpen = true;
  }

  private _closeEditor() {
    if (this._saving) return;
    this._editorOpen = false;
  }

  private _updateField<K extends keyof ZoneForm>(key: K, value: ZoneForm[K]) {
    this._form = { ...this._form, [key]: value };
  }

  private _updateRate(i: number, patch: Partial<TaxZoneRatePart>) {
    const rates = this._form.rates.map((r, j) => (j === i ? { ...r, ...patch } : r));
    this._form = { ...this._form, rates };
  }

  private async _save() {
    const f = this._form;
    if (f.country.trim().length !== 2 || !f.jurisdiction_code.trim() || !f.effective_from) {
      ToastService.show('Country (two letters), jurisdiction code and effective from are required', 'error');
      return;
    }
    if (f.zone_kind === 'POSTAL' && !f.postal_low.trim()) {
      ToastService.show('A postal zone names at least one postal code', 'error');
      return;
    }
    if (f.zone_kind === 'PROVINCE' && !f.province_code.trim()) {
      ToastService.show('A province zone names its province code', 'error');
      return;
    }
    const rates = f.rates.filter((r) => r.name && r.rate);
    if (rates.length === 0) {
      ToastService.show('Name at least one rate part', 'error');
      return;
    }
    this._saving = true;
    try {
      await TaxZoneService.create({
        country: f.country.trim().toUpperCase(),
        jurisdiction_code: f.jurisdiction_code.trim(),
        jurisdiction_type: f.jurisdiction_type,
        jurisdiction_name: f.jurisdiction_name.trim() || undefined,
        province_code: f.zone_kind === 'PROVINCE' ? f.province_code.trim().toUpperCase() : undefined,
        zone_kind: f.zone_kind,
        sourcing: f.sourcing,
        effective_from: f.effective_from,
        data_source: f.data_source.trim() || undefined,
        data_version: f.data_version.trim() || undefined,
        rates,
        boundaries: f.zone_kind === 'POSTAL'
          ? [{
              country: f.country.trim().toUpperCase(),
              postal_code_low: f.postal_low.trim(),
              postal_code_high: (f.postal_high.trim() || f.postal_low.trim()),
            }]
          : [],
      });
      ToastService.show('Tax zone created', 'success');
      this._editorOpen = false;
      await this._load();
    } catch (err) {
      ToastService.show(apiErrorMessage(err, 'Failed to create the tax zone'), 'error');
    } finally {
      this._saving = false;
    }
  }

  private async _closeZone(z: TaxZone) {
    const on = new Date().toISOString().slice(0, 10);
    try {
      await TaxZoneService.close(z.id, { effective_to: on });
      ToastService.show(`Closed ${z.jurisdiction_code}: sales before today keep its rate`, 'success');
      await this._load();
    } catch (err) {
      ToastService.show(apiErrorMessage(err, 'Failed to close the tax zone'), 'error');
    }
  }

  private _combined() {
    return combinedRatePercent(this._form.rates.filter((r) => r.rate));
  }

  render() {
    return html`
      <div class="space-y-6">
        <div class="flex items-center justify-between">
          <div>
            <h1 class="text-2xl font-bold text-white flex items-center gap-3">
              <span class="text-gable-green">${icon(MapPin, 24)}</span>
              Tax Zones
            </h1>
            <p class="text-sm text-zinc-500 mt-1">The rates a delivered sale's tax comes from, by the place it ships to</p>
          </div>
          <div class="flex items-center gap-3">
            <label class="text-xs text-zinc-400 flex items-center gap-2">
              Country
              <input
                type="text"
                .value=${this._country}
                placeholder="US"
                maxlength="2"
                @change=${(e: Event) => { this._country = (e.target as HTMLInputElement).value.trim().toUpperCase(); this._load(); }}
                class="w-16 rounded bg-deep-space/60 border border-white/10 px-2 py-1 text-sm text-white focus:outline-none focus:border-gable-green/50"
              />
            </label>
            <button
              @click=${() => this._openCreate()}
              class="flex items-center gap-2 px-4 py-2 bg-gable-green text-deep-space rounded-lg text-sm font-semibold hover:bg-gable-green/90 transition-colors shadow-glow"
            >
              ${icon(Plus, 16)} New Zone
            </button>
          </div>
        </div>

        ${this._error ? html`
          <div class="bg-safety-red/10 border border-safety-red/30 text-safety-red text-sm px-4 py-3 rounded-lg">
            ${this._error}
          </div>
        ` : nothing}

        <div class="bg-slate-steel border border-white/5 rounded-xl overflow-hidden">
          ${this._loading ? html`
            <div class="p-12 text-center text-zinc-500 text-sm">Loading...</div>
          ` : this._zones.length === 0 ? html`
            <div class="p-12 text-center text-zinc-500 text-sm">No tax zones yet. Create one so a delivered sale has a rate to come from.</div>
          ` : html`
            <table class="w-full text-sm">
              <thead class="bg-deep-space/40 text-xs uppercase tracking-wider text-zinc-500">
                <tr>
                  <th class="text-left px-4 py-3 font-semibold">Jurisdiction</th>
                  <th class="text-left px-4 py-3 font-semibold">Covers</th>
                  <th class="text-left px-4 py-3 font-semibold">Combined rate</th>
                  <th class="text-left px-4 py-3 font-semibold">Parts</th>
                  <th class="text-left px-4 py-3 font-semibold">Effective</th>
                  <th class="text-left px-4 py-3 font-semibold">Source</th>
                  <th class="text-right px-4 py-3 font-semibold">Actions</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-white/5">
                ${this._zones.map((z) => html`
                  <tr class="hover:bg-white/5 transition-colors">
                    <td class="px-4 py-3">
                      <div class="text-white font-medium">${z.jurisdiction_name || z.jurisdiction_code}</div>
                      <div class="text-xs text-zinc-500 font-mono">${z.country} ${z.jurisdiction_code} (${z.jurisdiction_type})</div>
                    </td>
                    <td class="px-4 py-3 text-zinc-400 font-mono text-xs">${boundaryText(z) || '—'}</td>
                    <td class="px-4 py-3 text-white font-medium">${combinedRatePercent(z.rates)}%</td>
                    <td class="px-4 py-3 text-zinc-400 text-xs">${partsText(z) || '—'}</td>
                    <td class="px-4 py-3 text-zinc-400 text-xs font-mono">
                      ${z.effective_from} to ${z.effective_to ?? 'now'}
                    </td>
                    <td class="px-4 py-3 text-zinc-400 text-xs">
                      ${[z.data_source, z.data_version].filter(Boolean).join(' ') || '—'}
                    </td>
                    <td class="px-4 py-3 text-right">
                      ${z.effective_to == null ? html`
                        <button
                          @click=${() => this._closeZone(z)}
                          title="Close this zone: sales before today keep its rate"
                          class="inline-flex items-center gap-1 px-2 py-1 rounded text-xs text-zinc-400 hover:bg-white/5 hover:text-safety-red transition-colors"
                        >${icon(Ban, 14)} Close</button>
                      ` : html`
                        <span class="text-xs px-2 py-0.5 rounded-full bg-zinc-700/40 text-zinc-400">Closed</span>
                      `}
                    </td>
                  </tr>
                `)}
              </tbody>
            </table>
          `}
        </div>

        ${this._editorOpen ? this._renderEditor() : nothing}
      </div>
    `;
  }

  private _renderEditor() {
    const f = this._form;
    return html`
      <div
        class="fixed inset-0 z-50 bg-deep-space/80 backdrop-blur-sm flex items-center justify-center p-4"
        @click=${() => this._closeEditor()}
      >
        <div
          class="bg-slate-steel border border-white/10 rounded-xl w-full max-w-2xl shadow-elevation-2 max-h-[90vh] overflow-y-auto"
          @click=${(e: MouseEvent) => e.stopPropagation()}
        >
          <div class="flex items-center justify-between px-6 py-4 border-b border-white/5">
            <h2 class="text-lg font-semibold text-white">New Tax Zone</h2>
            <button
              @click=${() => this._closeEditor()}
              class="p-1 rounded hover:bg-white/5 text-zinc-400 hover:text-white"
            >${icon(X, 16)}</button>
          </div>

          <div class="p-6 grid grid-cols-1 md:grid-cols-2 gap-4">
            ${this._textField('Country *', 'country', 'US')}
            ${this._textField('Jurisdiction code *', 'jurisdiction_code', 'WA-KING-001')}
            ${this._selectField('Jurisdiction type', f.jurisdiction_type,
              JURISDICTION_TYPES.map((t) => ({ value: t, label: t })),
              (v) => this._updateField('jurisdiction_type', v))}
            <div class="md:col-span-2">${this._textField('Jurisdiction name', 'jurisdiction_name', 'King County')}</div>

            ${this._selectField('Zone kind', f.zone_kind, [
                { value: 'POSTAL', label: 'Postal code zone' },
                { value: 'PROVINCE', label: 'Canadian province zone' },
              ], (v) => this._updateField('zone_kind', v))}
            ${this._selectField('Sourcing', f.sourcing, [
                { value: 'DESTINATION', label: 'Destination (deliveries)' },
                { value: 'ORIGIN', label: 'Origin (pickups from here)' },
                { value: 'BOTH', label: 'Both' },
              ], (v) => this._updateField('sourcing', v))}

            ${f.zone_kind === 'POSTAL' ? html`
              ${this._textField('Postal code from *', 'postal_low', '98100')}
              ${this._textField('Postal code to', 'postal_high', '98199 (blank: one code)')}
            ` : html`
              ${this._textField('Province code *', 'province_code', 'BC')}
            `}

            <div>${this._textField('Effective from *', 'effective_from', 'YYYY-MM-DD')}</div>
            <div class="md:col-span-2 grid grid-cols-2 gap-4">
              ${this._textField('Data source', 'data_source', 'State rate table')}
              ${this._textField('Data version', 'data_version', '2026 Q1')}
            </div>

            <div class="md:col-span-2">
              <div class="flex items-center justify-between mb-2">
                <span class="text-xs uppercase tracking-wider text-zinc-500 font-semibold">Rate parts *</span>
                <span class="text-sm text-white font-medium">Combined: ${this._combined()}%</span>
              </div>
              <div class="space-y-2">
                ${f.rates.map((r, i) => html`
                  <div class="flex items-center gap-2">
                    <gable-select
                      .value=${r.name}
                      .options=${PART_NAMES.map((n) => ({ value: n, label: n }))}
                      @change=${(e: Event) => this._updateRate(i, { name: (e.target as HTMLSelectElement).value })}
                    ></gable-select>
                    <input
                      type="text"
                      .value=${r.rate}
                      placeholder="0.06"
                      @input=${(e: Event) => this._updateRate(i, { rate: (e.target as HTMLInputElement).value })}
                      class="w-24 bg-deep-space/60 border border-white/10 rounded-md px-2 py-1.5 text-sm text-white focus:outline-none focus:border-gable-green/50"
                    />
                    <span class="text-xs text-zinc-500">from ${r.effective_from}</span>
                  </div>
                `)}
              </div>
              <button
                @click=${() => this._updateField('rates', [...f.rates, { name: 'COUNTY', rate: '', effective_from: f.effective_from }])}
                class="mt-2 text-xs text-gable-green hover:underline"
              >+ Add a part</button>
            </div>
          </div>

          <div class="px-6 py-4 border-t border-white/5 flex items-center justify-end gap-3">
            <button
              @click=${() => this._closeEditor()}
              ?disabled=${this._saving}
              class="px-4 py-2 text-sm text-zinc-300 hover:bg-white/5 rounded-lg transition-colors"
            >Cancel</button>
            <button
              @click=${() => this._save()}
              ?disabled=${this._saving}
              class="px-4 py-2 text-sm font-semibold bg-gable-green text-deep-space rounded-lg hover:bg-gable-green/90 transition-colors disabled:opacity-50"
            >${this._saving ? 'Saving...' : 'Create zone'}</button>
          </div>
        </div>
      </div>
    `;
  }

/** A select on the gable select component: the desk holds no native select (the chrome guard). */
  private _selectField(label: string, value: string, options: { value: string; label: string }[], onChange: (v: string) => void) {
    return html`
      <label class="block text-xs uppercase tracking-wider text-zinc-500 font-semibold">
        ${label}
        <gable-select
          .value=${value}
          .options=${options}
          @change=${(e: Event) => onChange((e.target as HTMLSelectElement).value)}
        ></gable-select>
      </label>
    `;
  }

  private _textField(label: string, key: keyof ZoneForm, placeholder: string) {
    const value = (this._form[key] ?? '') as string;
    return html`
      <label class="block text-xs uppercase tracking-wider text-zinc-500 font-semibold">
        ${label}
        <input
          type="text"
          .value=${value}
          placeholder=${placeholder}
          @input=${(e: Event) => this._updateField(key, (e.target as HTMLInputElement).value as never)}
          class="mt-1 w-full bg-deep-space/60 border border-white/10 rounded-md px-3 py-2 text-sm text-white font-normal normal-case tracking-normal focus:outline-none focus:border-gable-green/50"
        />
      </label>
    `;
  }
}
