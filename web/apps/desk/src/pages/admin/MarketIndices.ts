// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { ExposureService } from '../../services/ExposureService';
import { promptDialog } from '../../components/ui/app-dialog.ts';
import '../../components/quotes/index-staleness-banner.ts';
import type { MarketIndex, MarketIndexImportPreview, MarketIndexImportResult } from '../../types/exposure';
import { Activity, FileUp, RefreshCw } from 'lucide';

@customElement('gable-market-indices')
export class MarketIndicesPage extends LitElement {
  createRenderRoot() { return this; }

  @state() private indices: MarketIndex[] = [];
  @state() private loading = true;
  @state() private busyIndex = '';

  /** The file chosen for the import. */
  @state() private importFile: File | null = null;
  /** What the chosen file would do; null until it is previewed. */
  @state() private preview: MarketIndexImportPreview | null = null;
  /** What the last apply did. */
  @state() private importResult: MarketIndexImportResult | null = null;
  @state() private importing = false;
  /** Bumped when an index value moves, so the staleness banner reads again. */
  @state() private stalenessToken = 0;

  connectedCallback() {
    super.connectedCallback();
    this._load();
  }

  private async _load() {
    this.loading = true;
    try {
      this.indices = await ExposureService.listMarketIndices();
    } catch (err) {
      console.error(err);
      ToastService.show('Failed to load market indices', 'error');
    } finally {
      this.loading = false;
    }
  }

  private _fmt(v: number | undefined) {
    if (v == null) return '—';
    return v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  }

  private async _refresh(idx: MarketIndex) {
    const raw = await promptDialog({
      title: `Apply a new value for ${idx.name} (${idx.index_code || '—'})`,
      body: `Current: ${this._fmt(idx.current_value)} ${idx.unit || ''}`,
      label: 'New index value',
      required: true,
      confirmLabel: 'Apply',
      initialValue: idx.current_value != null ? String(idx.current_value) : '',
    });
    if (raw == null) return;
    const newValue = Number(raw);
    if (!Number.isFinite(newValue) || newValue <= 0) {
      ToastService.show('Value must be a number greater than 0', 'error');
      return;
    }
    this.busyIndex = idx.id;
    try {
      await ExposureService.refreshIndex(idx.id, { new_value: newValue, source: 'MANUAL' });
      ToastService.show('Index updated — exposure rescanned', 'success');
      await this._load();
      this.stalenessToken++;
    } catch (err) {
      console.error(err);
      ToastService.show('Failed to refresh index', 'error');
    } finally {
      this.busyIndex = '';
    }
  }

  /** The file changed: the old preview says nothing about the new file. */
  private _chooseFile(e: Event) {
    const input = e.target as HTMLInputElement;
    this.importFile = input.files && input.files.length > 0 ? input.files[0] : null;
    this.preview = null;
    this.importResult = null;
  }

  private async _preview() {
    if (!this.importFile || this.importing) return;
    this.importing = true;
    try {
      this.preview = await ExposureService.previewIndexImport(this.importFile);
      this.importResult = null;
    } catch (err) {
      console.error(err);
      this.preview = null;
      ToastService.show('Failed to read the import file', 'error');
    } finally {
      this.importing = false;
    }
  }

  private async _apply() {
    if (!this.importFile || !this.preview || this.importing) return;
    this.importing = true;
    try {
      this.importResult = await ExposureService.applyIndexImport(this.importFile);
      this.preview = null;
      ToastService.show(
        `Imported ${this.importResult.applied_count} row(s); ${this.importResult.changed_indices} index value(s) moved`,
        'success',
      );
      await this._load();
      this.stalenessToken++;
    } catch (err) {
      console.error(err);
      ToastService.show('Failed to apply the import', 'error');
    } finally {
      this.importing = false;
    }
  }

  private _renderImport() {
    const p = this.preview;
    const r = this.importResult;
    return html`
      <div class="backdrop-blur-md bg-white/5 border border-white/10 rounded-xl p-6 space-y-4">
        <div>
          <h2 class="text-lg font-bold text-white flex items-center gap-2">
            ${icon(FileUp, 18, 'w-4.5 h-4.5 text-gable-green')}
            Import index values
          </h2>
          <p class="text-zinc-500 text-sm mt-1">
            A CSV whose header names the columns: <span class="font-mono text-zinc-400">index_code</span>,
            <span class="font-mono text-zinc-400">value</span> and
            <span class="font-mono text-zinc-400">as_of_date</span> are required,
            <span class="font-mono text-zinc-400">source</span> and
            <span class="font-mono text-zinc-400">source_url</span> are optional. Each row is matched to an index
            and the newest day an index carries becomes its current value.
          </p>
        </div>

        <div class="flex flex-wrap items-center gap-3">
          <input type="file" accept=".csv,text/csv" data-testid="import-file" @change=${this._chooseFile}
            class="text-sm text-zinc-300 file:mr-3 file:rounded file:border-0 file:bg-white/10 file:px-3 file:py-1.5 file:text-white hover:file:bg-white/20" />
          <button type="button" data-testid="import-preview" @click=${() => this._preview()}
            ?disabled=${!this.importFile || this.importing}
            class="inline-flex items-center gap-2 px-3 py-1.5 text-xs rounded-md bg-white/10 text-white hover:bg-white/20 transition-colors disabled:opacity-50">
            Preview
          </button>
          <button type="button" data-testid="import-apply" @click=${() => this._apply()}
            ?disabled=${!p || p.accepted_count === 0 || this.importing}
            class="inline-flex items-center gap-2 px-3 py-1.5 text-xs rounded-md bg-gable-green text-black font-semibold hover:opacity-90 transition-colors disabled:opacity-50">
            Apply
          </button>
          ${this.importing ? html`<span class="text-xs text-zinc-400">Working...</span>` : nothing}
        </div>

        ${p ? html`
          <div class="space-y-2" data-testid="import-preview">
            <p class="text-sm text-zinc-300" data-testid="import-summary">
              ${p.accepted_count} row(s) will be written, ${p.unchanged_count} already stored,
              ${p.refused_count} refused. ${p.indices_affected} index value(s) will move.
            </p>
            <div class="overflow-x-auto">
              <table class="w-full text-sm text-left" data-testid="import-preview-table">
                <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                  <tr>
                    <th class="px-4 py-2">Line</th>
                    <th class="px-4 py-2">Index</th>
                    <th class="px-4 py-2 text-right">Value</th>
                    <th class="px-4 py-2">As of</th>
                    <th class="px-4 py-2">Fate</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-white/5">
                  ${p.rows.map((row) => html`
                    <tr data-testid="import-row" data-status=${row.status}>
                      <td class="px-4 py-2 font-mono text-zinc-500">${row.line}</td>
                      <td class="px-4 py-2 font-mono text-blueprint-blue">${row.index_code || '—'}</td>
                      <td class="px-4 py-2 text-right font-mono text-white">${row.value || '—'}</td>
                      <td class="px-4 py-2 font-mono text-zinc-300">${row.as_of_date || '—'}</td>
                      <td class="px-4 py-2">
                        <span class="text-xs ${row.status === 'accepted' ? 'text-emerald-400' : row.status === 'unchanged' ? 'text-zinc-400' : 'text-rose-400'}">
                          ${row.status === 'accepted' ? 'Will be written' : row.status === 'unchanged' ? 'Already stored' : 'Refused'}
                        </span>
                        ${row.reason ? html`<div class="text-xs text-rose-400">${row.reason}</div>` : nothing}
                      </td>
                    </tr>
                  `)}
                </tbody>
              </table>
            </div>
          </div>
        ` : nothing}

        ${r ? html`
          <p class="text-sm text-emerald-400" data-testid="import-result">
            ${r.applied_count} row(s) written, ${r.changed_indices} index value(s) moved,
            ${r.scans} exposure scan(s) ran, ${r.unchanged_count} already stored, ${r.refused_count} refused.
          </p>
        ` : nothing}
      </div>
    `;
  }

  render() {
    return html`
      <div class="mb-8">
        <h1 class="text-3xl font-bold text-white flex items-center gap-3">
          ${icon(Activity, 32, 'w-8 h-8 text-gable-green')}
          Market Indices
        </h1>
        <p class="text-zinc-500 mt-1">Lumber commodity indices driving quote price-exposure detection</p>
      </div>

      <div class="space-y-6">
        <gable-index-staleness-banner .refreshToken=${this.stalenessToken}></gable-index-staleness-banner>

        ${this._renderImport()}

        <div class="backdrop-blur-md bg-white/5 border border-white/10 rounded-xl">
          ${this.loading ? html`
            <div class="p-12 flex justify-center">
              <div class="animate-spin rounded-full h-8 w-8 border-b-2 border-gable-green"></div>
            </div>
          ` : this.indices.length === 0 ? html`
            <div class="p-12 text-center text-zinc-500">No market indices configured</div>
          ` : html`
            <table class="w-full text-sm text-left">
              <thead class="bg-white/5 text-zinc-400 uppercase tracking-wider text-xs font-semibold">
                <tr>
                  <th class="px-6 py-4">Index</th>
                  <th class="px-6 py-4">Code</th>
                  <th class="px-6 py-4">Commodity</th>
                  <th class="px-6 py-4 text-right">Current Value</th>
                  <th class="px-6 py-4">Unit</th>
                  <th class="px-6 py-4 text-center">Active</th>
                  <th class="px-6 py-4 text-right">Actions</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-white/5">
                ${this.indices.map((idx) => {
                  const busy = this.busyIndex === idx.id;
                  return html`
                    <tr class="hover:bg-white/5 transition-colors">
                      <td class="px-6 py-4 text-white font-medium">
                        ${idx.name}
                        ${idx.description ? html`<div class="text-xs text-zinc-500 font-normal">${idx.description}</div>` : nothing}
                      </td>
                      <td class="px-6 py-4 font-mono text-blueprint-blue">${idx.index_code || '—'}</td>
                      <td class="px-6 py-4 text-zinc-300">${idx.commodity_kind || '—'}</td>
                      <td class="px-6 py-4 text-right font-mono text-white font-bold">${this._fmt(idx.current_value)}</td>
                      <td class="px-6 py-4 font-mono text-zinc-400">${idx.unit || '—'}</td>
                      <td class="px-6 py-4 text-center">
                        <span class="px-2 py-1 rounded-md text-xs font-medium ${idx.is_active ? 'text-emerald-400 bg-emerald-500/10' : 'text-zinc-500 bg-white/5'}">
                          ${idx.is_active ? 'Active' : 'Inactive'}
                        </span>
                      </td>
                      <td class="px-6 py-4 text-right">
                        <button
                          ?disabled=${busy}
                          @click=${() => this._refresh(idx)}
                          class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs rounded-md bg-gable-green/10 text-gable-green hover:bg-gable-green/20 transition-colors disabled:opacity-50">
                          ${icon(RefreshCw, 14, busy ? 'w-3.5 h-3.5 animate-spin' : 'w-3.5 h-3.5')}
                          Refresh
                        </button>
                      </td>
                    </tr>
                  `;
                })}
              </tbody>
            </table>
          `}
        </div>
      </div>
    `;
  }
}
