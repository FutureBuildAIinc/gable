// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import type { MillworkOption, MillworkConfiguration, EstimatedItem } from '../../types/millwork';
import { MillworkService } from '../../services/MillworkService';
import { cssVarColor } from '../../lib/theme-colors.ts';
import { ConfiguredItemHandoff, CONFIGURED_ITEM_EVENT, type ConfiguredItem } from '../../lib/configured-item.ts';
import { router } from '../../lib/router.ts';
import '../../components/configured/ConfiguredTargetPicker.ts';
import type { ConfiguredTarget } from '../../components/configured/ConfiguredTargetPicker.ts';

@customElement('gable-door-configurator')
export class DoorConfigurator extends LitElement {
  createRenderRoot() { return this; }

  @state() private _doorTypes: MillworkOption[] = [];
  @state() private _materials: MillworkOption[] = [];
  @state() private _glassOptions: MillworkOption[] = [];

  @state() private _config: MillworkConfiguration = {
    doorType: null,
    material: null,
    glass: null,
    width: 36,
    height: 80,
  };

  @state() private _loading = true;
  @state() private _error: string | null = null;
  /** The server's priced answer for the current configuration. */
  @state() private _estimate: EstimatedItem | null = null;
  @state() private _pricing = false;
  @state() private _added = false;
  /** Which document the item is headed for; null while no picker is open. */
  @state() private _target: 'quote' | 'order' | null = null;

  /**
   * True when the configurator is hosted by the document itself (the quote
   * builder opens it beside the quote): the item goes straight to that
   * document and the picker never opens.
   */
  @property({ type: Boolean, attribute: 'inline' }) inline = false;

  connectedCallback() {
    super.connectedCallback();
    this._fetchOptions();
  }

  /**
   * Moves the configuration and asks the server for the price again.
   */
  private _setConfig(patch: Partial<MillworkConfiguration>) {
    this._config = { ...this._config, ...patch };
    this._added = false;
    void this._price();
  }

  /**
   * The door's price is the server's: the client sends the choices and the
   * server prices them from the stored options. The estimate is asked for
   * whenever the configuration changes, so the summary never shows a number
   * the dealer cannot buy.
   */
  private async _price() {
    const selections: Record<string, string> = {};
    if (this._config.doorType) selections.door_type = this._config.doorType.name;
    if (this._config.material) selections.material = this._config.material.name;
    if (this._config.glass) selections.glass = this._config.glass.name;
    if (Object.keys(selections).length === 0) {
      this._estimate = null;
      return;
    }
    this._pricing = true;
    try {
      this._estimate = await MillworkService.estimate({
        product_type: 'Door',
        selections,
        width: this._config.width,
        height: this._config.height,
      });
      this._error = null;
    } catch (err) {
      this._estimate = null;
      this._error = err instanceof Error ? err.message : 'Failed to price the door';
    } finally {
      this._pricing = false;
    }
  }

  /**
   * Adds the priced door to the order: the server's price, the server's
   * product row and the configuration travel with it.
   */
  /** Opens the picker: the dealer says which quote or order it belongs on. */
  private _handleAddToQuote() {
    if (!this._estimate) return;
    if (this.inline) {
      this._addToHost();
      return;
    }
    this._target = 'quote';
  }

  private _handleAddToOrder() {
    if (!this._estimate) return;
    if (this.inline) {
      this._addToHost();
      return;
    }
    this._target = 'order';
  }

  /**
   * The item goes to the document that hosts the configurator: the quote
   * builder takes it and puts it on its line.
   */
  private _addToHost() {
    if (!this._estimate) return;
    const item: ConfiguredItem = {
      sku: this._estimate.sku,
      description: this._estimate.description,
      product_id: this._estimate.product_id,
      unit_price_ten_thousandths: this._estimate.unit_price_ten_thousandths,
      unit_cost_ten_thousandths: this._estimate.unit_cost_ten_thousandths,
      configuration_json: this._estimate.configuration_json,
    };
    ConfiguredItemHandoff.offer(item);
    this.dispatchEvent(new CustomEvent<ConfiguredItem>(CONFIGURED_ITEM_EVENT, {
      detail: item,
      bubbles: true,
      composed: true,
    }));
    this._added = true;
  }

  /**
   * The document is picked: the item is handed over and the dealer walks to
   * that document, where the item lands on its line.
   */
  private _handleTargetPicked(e: Event) {
    const target = (e as CustomEvent<ConfiguredTarget>).detail;
    const kind = this._target;
    this._target = null;
    if (!this._estimate || !kind) return;
    const item: ConfiguredItem = {
      sku: this._estimate.sku,
      description: this._estimate.description,
      product_id: this._estimate.product_id,
      unit_price_ten_thousandths: this._estimate.unit_price_ten_thousandths,
      unit_cost_ten_thousandths: this._estimate.unit_cost_ten_thousandths,
      configuration_json: this._estimate.configuration_json,
    };
    ConfiguredItemHandoff.offer(item);
    this.dispatchEvent(new CustomEvent<ConfiguredItem>(CONFIGURED_ITEM_EVENT, {
      detail: item,
      bubbles: true,
      composed: true,
    }));
    this._added = true;
    if (target.id) {
      router.navigate(kind === 'order' ? `/orders/${target.id}` : `/quotes/${target.id}/edit`);
    } else {
      router.navigate(kind === 'order' ? '/quotes' : '/quotes/new');
    }
  }

  private async _fetchOptions() {
    this._error = null;
    this._loading = true;
    try {
      const [doors, mats, glass] = await Promise.all([
        MillworkService.getOptionsByCategory('door_type'),
        MillworkService.getOptionsByCategory('material'),
        MillworkService.getOptionsByCategory('glass'),
      ]);
      this._doorTypes = doors;
      this._materials = mats;
      this._glassOptions = glass;
    } catch (err) {
      console.error("Failed to load millwork options", err);
      this._error = err instanceof Error ? err.message : 'Failed to load configurator options';
    } finally {
      this._loading = false;
    }
  }

  render() {
    if (this._loading) {
      return html`<div class="p-8 text-white">Loading Configurator...</div>`;
    }

    if (this._error) {
      return html`
        <div class="flex h-full items-center justify-center bg-deep-space text-pale">
          <div class="text-center space-y-4">
            <p class="text-red-400">${this._error}</p>
            <button
              @click=${() => this._fetchOptions()}
              class="px-4 py-2 rounded-lg bg-gable-green/10 text-gable-green border border-gable-green/20 hover:bg-gable-green/20 transition-colors text-sm font-medium"
            >
              Retry
            </button>
          </div>
        </div>
      `;
    }

    // The option adjustments are integer cents on the wire (ADR 0001
    // section 7); the server's answer is in ten-thousandths, and the two
    // formatting helpers divide exactly once, at the display.
    const cents = (n: number): string => (n / 100).toFixed(2);
    const dollars = (n: number): string => (n / 10000).toFixed(2);

    return html`
      <div class="flex h-full bg-deep-space text-pale">
        <!-- Configuration Panel -->
        <div class="w-1/3 border-r border-white/10 p-6 overflow-y-auto">
          <h2 class="text-2xl font-bold mb-6 text-gable-green">Configure Door</h2>

          <div class="space-y-8">
            <!-- Door Type -->
            <div>
              <label class="block text-sm font-medium text-gray-400 mb-2">Door Style</label>
              <div class="grid grid-cols-2 gap-3">
                ${this._doorTypes.map(opt => html`
                  <button
                    @click=${() => { this._setConfig({ doorType: opt }); }}
                    class="p-4 border rounded-lg text-left transition-all ${this._config.doorType?.id === opt.id
                      ? 'border-gable-green bg-gable-green/10 text-white'
                      : 'border-white/10 hover:border-white/30 text-gray-300'
                    }"
                  >
                    <div class="font-medium">${opt.name}</div>
                    <div class="text-sm text-gray-500">+$${cents(opt.price_adjustment_cents)}</div>
                  </button>
                `)}
              </div>
            </div>

            <!-- Material -->
            <div>
              <label class="block text-sm font-medium text-gray-400 mb-2">Material</label>
              <div class="grid grid-cols-2 gap-3">
                ${this._materials.map(opt => html`
                  <button
                    @click=${() => { this._setConfig({ material: opt }); }}
                    class="p-4 border rounded-lg text-left transition-all ${this._config.material?.id === opt.id
                      ? 'border-gable-green bg-gable-green/10 text-white'
                      : 'border-white/10 hover:border-white/30 text-gray-300'
                    }"
                  >
                    <div class="font-medium">${opt.name}</div>
                    <div class="text-sm text-gray-500">+$${cents(opt.price_adjustment_cents)}</div>
                  </button>
                `)}
              </div>
            </div>

            <!-- Dimensions -->
            <div>
              <label class="block text-sm font-medium text-gray-400 mb-2">Dimensions (Inches)</label>
              <div class="flex gap-4">
                <div>
                  <label class="text-xs text-gray-500">Width</label>
                  <input
                    type="number"
                    .value=${String(this._config.width)}
                    @input=${(e: Event) => { this._setConfig({ width: parseInt((e.target as HTMLInputElement).value) || 0 }); }}
                    class="w-full bg-slate-steel border border-white/20 rounded p-2 focus:border-gable-green outline-none"
                  />
                </div>
                <div>
                  <label class="text-xs text-gray-500">Height</label>
                  <input
                    type="number"
                    .value=${String(this._config.height)}
                    @input=${(e: Event) => { this._setConfig({ height: parseInt((e.target as HTMLInputElement).value) || 0 }); }}
                    class="w-full bg-slate-steel border border-white/20 rounded p-2 focus:border-gable-green outline-none"
                  />
                </div>
              </div>
            </div>

            <!-- Glass -->
            <div>
              <label class="block text-sm font-medium text-gray-400 mb-2">Glass Options</label>
              <div class="grid grid-cols-1 gap-2">
                <button
                  @click=${() => { this._setConfig({ glass: null }); }}
                  class="p-3 border rounded text-left ${this._config.glass === null
                    ? 'border-gable-green bg-gable-green/10'
                    : 'border-white/10'
                  }"
                >
                  No Glass
                </button>
                ${this._glassOptions.map(opt => html`
                  <button
                    @click=${() => { this._setConfig({ glass: opt }); }}
                    class="p-3 border rounded text-left flex justify-between ${this._config.glass?.id === opt.id
                      ? 'border-gable-green bg-gable-green/10 text-white'
                      : 'border-white/10 hover:border-white/30 text-gray-300'
                    }"
                  >
                    <span>${opt.name}</span>
                    <span class="text-gray-500">+$${cents(opt.price_adjustment_cents)}</span>
                  </button>
                `)}
              </div>
            </div>
          </div>
        </div>

        <!-- Visualizer / Summary -->
        <div class="flex-1 p-12 flex flex-col items-center justify-center bg-gradient-to-br from-deep-space to-slate-steel">

          <!-- Placeholder Visualizer -->
          <div
            class="border-4 bg-deep-space relative shadow-2xl mb-8 transition-all duration-500"
            style="width: ${this._config.width * 2}px; height: ${this._config.height * 2}px; border-color: ${this._config.material?.name === 'Mahogany' ? '#6D2E15' : cssVarColor('blueprint-blue')}"
          >
            <div class="absolute inset-0 flex items-center justify-center text-white/20 font-mono text-4xl">
              ${this._config.doorType?.name || "Select Style"}
            </div>
            ${this._config.glass ? html`
              <div class="absolute top-10 left-10 right-10 bottom-40 bg-blue-400/20 border border-blue-400/30 backdrop-blur-sm"></div>
            ` : nothing}
          </div>

          <!-- Price Tag -->
          <div class="bg-slate-steel border border-white/10 p-6 rounded-xl w-96 shadow-xl">
            <h3 class="text-gray-400 uppercase text-xs tracking-wider mb-4">Estimate Summary</h3>

            <div class="space-y-2 mb-4 text-sm" data-testid="estimate-breakdown">
              <div class="flex justify-between text-gray-400">
                <span>Door (${this._config.width}" x ${this._config.height}")</span>
              </div>
              ${this._estimate
                ? this._estimate.breakdown.map(line => html`
                    <div class="flex justify-between text-gable-green">
                      <span>${line.name}</span>
                      <span>$${cents(line.price_cents)}</span>
                    </div>
                  `)
                : nothing}
              ${this._pricing ? html`
                <p class="text-gray-500 text-xs">Pricing on the server...</p>
              ` : nothing}
            </div>

            <div class="border-t border-white/10 pt-4 flex justify-between items-end">
              <span class="text-gray-400">Total</span>
              <span class="text-3xl font-mono font-bold text-white" data-testid="estimate-price">
                ${this._estimate ? `$${dollars(this._estimate.unit_price_ten_thousandths)}` : '--'}
              </span>
            </div>

            ${this._estimate ? html`
              <p class="mt-2 text-center text-[11px] text-gray-500 font-mono">${this._estimate.sku}</p>
            ` : nothing}

            <div class="mt-6 flex flex-wrap gap-3">
              <button
                @click=${() => this._handleAddToQuote()}
                ?disabled=${!this._estimate || this._added}
                data-testid="add-to-quote"
                class="flex-1 ${this._estimate && !this._added
                  ? 'bg-gradient-to-r from-gable-green to-emerald-400 text-black font-bold py-3 rounded uppercase tracking-wide hover:shadow-glow-40-30 transition-all'
                  : 'border border-white/10 text-zinc-500 font-bold py-3 rounded uppercase tracking-wide opacity-50 cursor-not-allowed'}"
              >
                Add to Quote
              </button>
              <button
                @click=${() => this._handleAddToOrder()}
                ?disabled=${!this._estimate || this._added}
                data-testid="add-to-order"
                class="flex-1 ${this._estimate && !this._added
                  ? 'border border-white/20 text-white font-bold py-3 rounded uppercase tracking-wide hover:border-gable-green/60 transition-colors'
                  : 'border border-white/10 text-zinc-500 font-bold py-3 rounded uppercase tracking-wide opacity-50 cursor-not-allowed'}"
              >
                ${this._added ? 'Added' : 'Add to Order'}
              </button>
            </div>
          </div>
        </div>

        ${this._target
          ? html`<gable-configured-target-picker
              target=${this._target}
              @configured-target-picked=${(e: Event) => this._handleTargetPicked(e)}
            ></gable-configured-target-picker>`
          : nothing}
      </div>
    `;
  }
}
