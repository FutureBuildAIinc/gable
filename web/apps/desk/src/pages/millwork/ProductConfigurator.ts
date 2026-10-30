// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { CONFIGURATOR_STEPS } from '../../types/configurator';
import type { AvailableOption, ValidateConfigResponse, BuildSKUResponse } from '../../types/configurator';
import { ConfiguratorService } from '../../services/ConfiguratorService';
import { icon } from '../../lib/icons.ts';
import { ChevronRight, ChevronLeft, Check, AlertTriangle, Package, TreePine, Gauge, Ruler, Eye, Sparkles, X } from 'lucide';
import { ToastService } from '../../lib/toast-service.ts';
import { cssVarColor } from '../../lib/theme-colors.ts';
import { ConfiguredItemHandoff, CONFIGURED_ITEM_EVENT, type ConfiguredItem } from '../../lib/configured-item.ts';
import { router } from '../../lib/router.ts';
import '../../components/configured/ConfiguredTargetPicker.ts';
import type { ConfiguredTarget } from '../../components/configured/ConfiguredTargetPicker.ts';

const STEP_ICONS = [Package, TreePine, Gauge, Ruler, Eye];

const DIMENSION_OPTIONS = [
  '2x4', '2x6', '2x8', '2x10', '2x12',
  '4x4', '4x6', '6x6',
  '1x4', '1x6', '1x8', '1x10', '1x12',
];
const LENGTH_OPTIONS = ['8', '10', '12', '14', '16', '20'];

/**
 * The server's money, in ten-thousandths of the major unit, as the dealer
 * reads it: two decimals of dollars, never a float the client computed.
 */
function formatTenThousandths(value: number): string {
  const cents = Math.round(value / 100);
  return `$${(cents / 100).toFixed(2)}`;
}

function getSpeciesColor(species: string): string {
  const colors: Record<string, string> = {
    'SYP': 'rgba(184, 134, 61, 0.2)',
    'Douglas Fir': 'rgba(139, 90, 43, 0.2)',
    'Cedar': 'rgba(180, 83, 55, 0.2)',
    'Hem-Fir': 'rgba(160, 120, 70, 0.2)',
    'SPF': 'rgba(200, 180, 140, 0.2)',
  };
  return colors[species] || 'rgba(100, 100, 100, 0.1)';
}

@customElement('gable-product-configurator')
export class ProductConfigurator extends LitElement {
  createRenderRoot() { return this; }

  @state() private _currentStep = 0;
  @state() private _selections: Record<string, string> = {
    ProductType: '',
    Species: '',
    Grade: '',
    Treatment: 'None',
    Dimensions: '',
  };
  @state() private _availableOptions: AvailableOption[] = [];
  @state() private _treatmentOptions: AvailableOption[] = [];
  @state() private _validation: ValidateConfigResponse | null = null;
  @state() private _skuResult: BuildSKUResponse | null = null;
  @state() private _added = false;
  /** Which document the item is headed for; null while no picker is open. */
  @state() private _target: 'quote' | 'order' | null = null;

  /**
   * True when the configurator is hosted by the document itself (the quote
   * builder opens it beside the quote): the item goes straight to that
   * document and the picker never opens.
   */
  @property({ type: Boolean, attribute: 'inline' }) inline = false;
  @state() private _loading = false;
  @state() private _error: string | null = null;
  @state() private _dimensionSize = '';
  @state() private _dimensionLength = '';

  private get _step() { return CONFIGURATOR_STEPS[this._currentStep]; }

  connectedCallback() {
    super.connectedCallback();
    this._fetchOptions();
  }

  updated(changed: Map<string, unknown>) {
    if (changed.has('_currentStep') || changed.has('_selections')) {
      this._fetchOptions();
    }
    if (changed.has('_currentStep') && this._currentStep === 4) {
      this._validateConfig();
    }
  }

  private async _fetchOptions() {
    if (this._currentStep === 4) return; // Review step
    if (this._currentStep === 3) return; // Dimensions are hardcoded

    this._loading = true;
    this._error = null;
    try {
      const opts = await ConfiguratorService.getAvailableOptions(
        this._step.attributeType,
        this._selections
      );
      this._availableOptions = opts;

      // Also fetch treatment options for the Grade step
      if (this._currentStep === 2 && this._selections.Species) {
        const treatOpts = await ConfiguratorService.getAvailableOptions('Treatment', this._selections);
        this._treatmentOptions = treatOpts;
      }
    } catch (err) {
      console.error('Failed to fetch options:', err);
      // Fall back to defaults for ProductType
      if (this._currentStep === 0) {
        this._availableOptions = [
          { value: 'Lumber', allowed: true, message: null },
          { value: 'Door', allowed: true, message: null },
          { value: 'Trim', allowed: true, message: null },
          { value: 'Panel', allowed: true, message: null },
        ];
      } else {
        this._error = 'Failed to load options. Please try again.';
      }
    } finally {
      this._loading = false;
    }
  }

  private async _validateConfig() {
    try {
      const result = await ConfiguratorService.validateConfig(this._selections);
      this._validation = result;
    } catch {
      ToastService.error('Failed to validate configuration');
    }
  }

  private _selectOption(key: string, value: string) {
    this._selections = { ...this._selections, [key]: value };
  }

  private _canProceed(): boolean {
    switch (this._currentStep) {
      case 0: return !!this._selections.ProductType;
      case 1: return !!this._selections.Species;
      case 2: return !!this._selections.Grade;
      case 3: return !!this._dimensionSize && !!this._dimensionLength;
      case 4: return this._validation?.valid === true;
      default: return false;
    }
  }

  private _handleNext() {
    if (this._currentStep === 3) {
      this._selections = { ...this._selections, Dimensions: `${this._dimensionSize}-${this._dimensionLength}` };
    }
    if (this._currentStep < CONFIGURATOR_STEPS.length - 1) {
      this._currentStep++;
    }
  }

  private _handleBack() {
    if (this._currentStep > 0) {
      this._currentStep--;
      this._validation = null;
      this._skuResult = null;
    }
  }

  private async _handleBuildSKU() {
    this._error = null;
    this._added = false;
    try {
      // The server prices the configuration from the stored options and
      // builds the product row; the client sends the choices and reads the
      // money.
      const result = await ConfiguratorService.buildSKU(this._selections.ProductType, this._selections);
      this._skuResult = result;
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to build SKU';
      this._error = message;
    }
  }

  /**
   * Adds the configured item to the quote: the server's price, the server's
   * product row and the configuration travel with it, so the line the
   * document carries is priced by the server and shows what the item is.
   */
  /** Opens the picker: the dealer says which quote or order it belongs on. */
  private _handleAddToQuote() {
    if (!this._skuResult) return;
    if (this.inline) {
      this._addToHost();
      return;
    }
    this._target = 'quote';
  }

  private _handleAddToOrder() {
    if (!this._skuResult) return;
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
    if (!this._skuResult) return;
    const item: ConfiguredItem = {
      sku: this._skuResult.sku,
      description: this._skuResult.description,
      product_id: this._skuResult.product_id,
      unit_price_ten_thousandths: this._skuResult.unit_price_ten_thousandths,
      unit_cost_ten_thousandths: this._skuResult.unit_cost_ten_thousandths,
      configuration_json: this._skuResult.configuration_json,
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
    if (!this._skuResult || !kind) return;
    const item: ConfiguredItem = {
      sku: this._skuResult.sku,
      description: this._skuResult.description,
      product_id: this._skuResult.product_id,
      unit_price_ten_thousandths: this._skuResult.unit_price_ten_thousandths,
      unit_cost_ten_thousandths: this._skuResult.unit_cost_ten_thousandths,
      configuration_json: this._skuResult.configuration_json,
    };
    ConfiguredItemHandoff.offer(item);
    this.dispatchEvent(new CustomEvent<ConfiguredItem>(CONFIGURED_ITEM_EVENT, {
      detail: item,
      bubbles: true,
      composed: true,
    }));
    this._added = true;
    ToastService.success('Added to the quote');
    if (target.id) {
      router.navigate(kind === 'order' ? `/orders/${target.id}` : `/quotes/${target.id}/edit`);
    } else {
      router.navigate(kind === 'order' ? '/quotes' : '/quotes/new');
    }
  }

  /* ---- Render helpers ---- */

  private _renderOptionGrid(key: string, options: AvailableOption[]) {
    return html`
      <div class="grid grid-cols-2 lg:grid-cols-3 gap-4">
        ${options.map(opt => html`
          <button
            @click=${() => opt.allowed && this._selectOption(key, opt.value)}
            ?disabled=${!opt.allowed}
            class="relative p-6 rounded-xl border-2 text-left transition-all duration-300 group ${this._selections[key] === opt.value
              ? 'border-gable-green bg-gable-green/10 shadow-glow-30-15'
              : opt.allowed
                ? 'border-white/10 hover:border-white/30 hover:bg-white/5'
                : 'border-white/5 opacity-40 cursor-not-allowed'
            }"
          >
            ${this._selections[key] === opt.value ? html`
              <div class="absolute top-3 right-3">
                ${icon(Check, 16, 'text-gable-green')}
              </div>
            ` : nothing}
            <div class="font-semibold text-lg">${opt.value}</div>
            ${!opt.allowed && opt.message ? html`
              <div class="text-xs text-red-400 mt-2 flex items-start gap-1">
                ${icon(AlertTriangle, 12, 'mt-0.5 shrink-0')}
                <span>${opt.message}</span>
              </div>
            ` : nothing}
          </button>
        `)}
      </div>
    `;
  }

  private _renderGradeAndTreatment() {
    return html`
      <div class="space-y-8">
        <div>
          <h4 class="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-4">Grade</h4>
          <div class="grid grid-cols-2 lg:grid-cols-3 gap-3">
            ${this._availableOptions.map(opt => html`
              <button
                @click=${() => opt.allowed && this._selectOption('Grade', opt.value)}
                ?disabled=${!opt.allowed}
                class="p-4 rounded-xl border-2 text-left transition-all duration-300 ${this._selections.Grade === opt.value
                  ? 'border-gable-green bg-gable-green/10 shadow-glow-20-10'
                  : opt.allowed
                    ? 'border-white/10 hover:border-white/30'
                    : 'border-white/5 opacity-40 cursor-not-allowed'
                }"
              >
                <div class="font-medium">${opt.value}</div>
                ${!opt.allowed && opt.message ? html`
                  <div class="text-xs text-red-400 mt-1">${opt.message}</div>
                ` : nothing}
              </button>
            `)}
          </div>
        </div>

        <div class="border-t border-white/10 pt-6">
          <h4 class="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-4">Treatment</h4>
          <div class="grid grid-cols-2 lg:grid-cols-3 gap-3">
            <button
              @click=${() => this._selectOption('Treatment', 'None')}
              class="p-4 rounded-xl border-2 text-left transition-all ${this._selections.Treatment === 'None'
                ? 'border-gable-green bg-gable-green/10'
                : 'border-white/10 hover:border-white/30'
              }"
            >
              <div class="font-medium">None</div>
              <div class="text-xs text-gray-500 mt-1">Untreated</div>
            </button>
            ${this._treatmentOptions.map(opt => html`
              <button
                @click=${() => opt.allowed && this._selectOption('Treatment', opt.value)}
                ?disabled=${!opt.allowed}
                class="p-4 rounded-xl border-2 text-left transition-all ${this._selections.Treatment === opt.value
                  ? 'border-gable-green bg-gable-green/10'
                  : opt.allowed
                    ? 'border-white/10 hover:border-white/30'
                    : 'border-white/5 opacity-40 cursor-not-allowed'
                }"
              >
                <div class="font-medium">${opt.value}</div>
                ${!opt.allowed ? html`
                  <div class="text-xs text-red-400 mt-1 flex items-center gap-1">
                    ${icon(X, 10)} Not available
                  </div>
                ` : nothing}
              </button>
            `)}
          </div>
        </div>
      </div>
    `;
  }

  private _renderDimensions() {
    return html`
      <div class="space-y-8">
        <div>
          <h4 class="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-4">Cross Section</h4>
          <div class="grid grid-cols-3 lg:grid-cols-5 gap-3">
            ${DIMENSION_OPTIONS.map(dim => html`
              <button
                @click=${() => { this._dimensionSize = dim; }}
                class="p-4 rounded-xl border-2 text-center font-mono text-lg transition-all ${this._dimensionSize === dim
                  ? 'border-gable-green bg-gable-green/10 text-gable-green shadow-glow-20-10'
                  : 'border-white/10 hover:border-white/30 text-gray-300'
                }"
              >
                ${dim}
              </button>
            `)}
          </div>
        </div>

        <div class="border-t border-white/10 pt-6">
          <h4 class="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-4">Length (feet)</h4>
          <div class="grid grid-cols-3 lg:grid-cols-6 gap-3">
            ${LENGTH_OPTIONS.map(len => html`
              <button
                @click=${() => { this._dimensionLength = len; }}
                class="p-4 rounded-xl border-2 text-center font-mono text-lg transition-all ${this._dimensionLength === len
                  ? 'border-gable-green bg-gable-green/10 text-gable-green'
                  : 'border-white/10 hover:border-white/30 text-gray-300'
                }"
              >
                ${len}'
              </button>
            `)}
          </div>
        </div>

        ${this._dimensionSize && this._dimensionLength ? html`
          <div class="bg-slate-steel border border-white/10 rounded-xl p-4 text-center">
            <span class="text-gray-400">Selected: </span>
            <span class="text-gable-green font-mono text-xl font-bold">
              ${this._dimensionSize} \u00d7 ${this._dimensionLength}'
            </span>
          </div>
        ` : nothing}
      </div>
    `;
  }

  private _renderReview() {
    return html`
      <div class="space-y-6">
        <!-- Configuration Summary -->
        <div class="bg-slate-steel border border-white/10 rounded-xl p-6">
          <h4 class="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-4">Configuration Summary</h4>
          <div class="space-y-3">
            ${Object.entries(this._selections).map(([key, value]) =>
              value && value !== 'None' ? html`
                <div class="flex justify-between items-center py-2 border-b border-white/5 last:border-0">
                  <span class="text-gray-400">${key}</span>
                  <span class="font-medium text-white">${value}</span>
                </div>
              ` : nothing
            )}
            ${this._selections.Treatment === 'None' ? html`
              <div class="flex justify-between items-center py-2 border-b border-white/5">
                <span class="text-gray-400">Treatment</span>
                <span class="font-medium text-gray-500">None</span>
              </div>
            ` : nothing}
          </div>
        </div>

        <!-- Validation Result -->
        ${this._validation ? html`
          <div class="border rounded-xl p-6 ${this._validation.valid
            ? 'bg-emerald-500/5 border-emerald-500/30'
            : 'bg-red-500/5 border-red-500/30'
          }">
            <div class="flex items-center gap-3 mb-3">
              ${this._validation.valid ? html`
                ${icon(Check, 20, 'text-emerald-400')}
                <span class="font-semibold text-emerald-400">Configuration Valid</span>
              ` : html`
                ${icon(AlertTriangle, 20, 'text-red-400')}
                <span class="font-semibold text-red-400">
                  ${this._validation.conflicts?.length} Conflict${(this._validation.conflicts?.length || 0) > 1 ? 's' : ''} Found
                </span>
              `}
            </div>
            ${this._validation.conflicts?.map(conflict => html`
              <div class="bg-red-500/10 border border-red-500/20 rounded-lg p-3 mt-2 text-sm text-red-300">
                ${conflict.message}
              </div>
            `)}
          </div>
        ` : nothing}

        <!-- Build SKU Action -->
        ${this._validation?.valid && !this._skuResult ? html`
          <button
            @click=${() => this._handleBuildSKU()}
            class="w-full bg-gradient-to-r from-gable-green to-emerald-400 text-black font-bold py-4 rounded-xl hover:shadow-glow-40-30 transition-all duration-300 flex items-center justify-center gap-2"
          >
            ${icon(Sparkles, 20)}
            Generate Non-Stock SKU
          </button>
        ` : nothing}

        <!-- SKU Result -->
        ${this._skuResult ? html`
          <div class="bg-gradient-to-br from-gable-green/10 to-emerald-500/5 border-2 border-gable-green/40 rounded-xl p-6">
            <div class="text-sm text-gray-400 uppercase tracking-wider mb-2">Generated SKU</div>
            <div class="text-2xl font-mono font-bold text-gable-green mb-3 tracking-wide">
              ${this._skuResult.sku}
            </div>
            <div class="text-sm text-gray-300">${this._skuResult.description}</div>
            <!--
              The server's money: the price and the cost it computed from the
              stored options, never a price the client chose.
            -->
            <dl class="mt-4 grid grid-cols-2 gap-3 text-sm">
              <div class="rounded-lg bg-black/30 px-3 py-2">
                <dt class="text-[11px] uppercase tracking-wider text-gray-500">Unit price</dt>
                <dd class="font-mono font-bold text-gable-green" data-testid="configured-price">
                  ${formatTenThousandths(this._skuResult.unit_price_ten_thousandths)}
                </dd>
              </div>
              <div class="rounded-lg bg-black/30 px-3 py-2">
                <dt class="text-[11px] uppercase tracking-wider text-gray-500">Unit cost</dt>
                <dd class="font-mono font-bold text-gray-200">
                  ${formatTenThousandths(this._skuResult.unit_cost_ten_thousandths)}
                </dd>
              </div>
            </dl>
            <div class="mt-4 flex flex-wrap gap-3">
              <button
                @click=${() => this._handleAddToQuote()}
                ?disabled=${this._added}
                data-testid="add-to-quote"
                class="flex-1 ${this._added
                  ? 'border border-gable-green/40 text-gable-green font-bold py-3 px-6 rounded-lg cursor-default'
                  : 'bg-gradient-to-r from-gable-green to-emerald-400 text-black font-bold py-3 px-6 rounded-lg hover:shadow-glow-40-30 transition-all'}"
              >
                ${this._added ? 'Added' : 'Add to Quote'}
              </button>
              <button
                @click=${() => this._handleAddToOrder()}
                ?disabled=${this._added}
                data-testid="add-to-order"
                class="flex-1 border border-white/20 text-white font-bold py-3 px-6 rounded-lg hover:border-gable-green/60 transition-colors"
              >
                Add to Order
              </button>
            </div>
          </div>
        ` : nothing}
      </div>
    `;
  }

  private _renderStepContent() {
    if (this._loading) {
      return html`
        <div class="flex items-center justify-center h-64">
          <div class="w-8 h-8 border-2 border-gable-green border-t-transparent rounded-full animate-spin"></div>
        </div>
      `;
    }

    if (this._error) {
      return html`
        <div class="bg-red-500/10 border border-red-500/30 rounded-xl p-6 text-center">
          ${icon(AlertTriangle, 24, 'text-red-400 mx-auto mb-2')}
          <p class="text-red-300 text-sm">${this._error}</p>
          <button @click=${() => this._fetchOptions()} class="mt-3 text-xs text-gable-green hover:text-white">
            Retry
          </button>
        </div>
      `;
    }

    switch (this._currentStep) {
      case 0: return this._renderOptionGrid('ProductType', this._availableOptions);
      case 1: return this._renderOptionGrid('Species', this._availableOptions);
      case 2: return this._renderGradeAndTreatment();
      case 3: return this._renderDimensions();
      case 4: return this._renderReview();
      default: return nothing;
    }
  }

  render() {
    const step = this._step;

    return html`
      <div class="min-h-[calc(100vh-6rem)]">
        <!-- Header -->
        <div class="mb-8">
          <h1 class="text-3xl font-bold text-white">Product Configurator</h1>
          <p class="text-gray-400 mt-1">Configure custom lumber, millwork, and building materials</p>
        </div>

        <div class="flex gap-8">
          <!-- Stepper Sidebar -->
          <div class="w-72 shrink-0">
            <div class="bg-slate-steel border border-white/10 rounded-xl p-6 sticky top-24">
              <div class="space-y-1">
                ${CONFIGURATOR_STEPS.map((s, index) => {
                  const stepIcon = STEP_ICONS[index];
                  const isActive = index === this._currentStep;
                  const isComplete = index < this._currentStep;
                  const isDisabled = index > this._currentStep;

                  return html`
                    <button
                      @click=${() => index < this._currentStep && (this._currentStep = index)}
                      ?disabled=${isDisabled}
                      class="w-full flex items-center gap-3 p-3 rounded-lg transition-all text-left ${isActive
                        ? 'bg-gable-green/10 text-gable-green'
                        : isComplete
                          ? 'text-emerald-400 hover:bg-white/5 cursor-pointer'
                          : 'text-gray-600 cursor-not-allowed'
                      }"
                    >
                      <div class="w-8 h-8 rounded-full flex items-center justify-center shrink-0 border-2 transition-all ${isActive
                        ? 'border-gable-green bg-gable-green/20'
                        : isComplete
                          ? 'border-emerald-500 bg-emerald-500/20'
                          : 'border-gray-700 bg-gray-800'
                      }">
                        ${isComplete ? icon(Check, 14) : icon(stepIcon, 14)}
                      </div>
                      <div>
                        <div class="text-sm font-medium">${s.label}</div>
                        ${isActive ? html`
                          <div class="text-xs text-gray-500">${s.description}</div>
                        ` : nothing}
                      </div>
                    </button>
                  `;
                })}
              </div>

              <!-- Live Preview -->
              ${(this._selections.ProductType || this._selections.Species) ? html`
                <div class="mt-6 pt-6 border-t border-white/10">
                  <div class="text-xs text-gray-500 uppercase tracking-wider mb-3">Live Preview</div>
                  <div class="bg-deep-space rounded-lg p-4 border border-white/5">
                    <div class="w-full aspect-square relative flex items-center justify-center">
                      <div
                        class="border-2 rounded-sm transition-all duration-500"
                        style="width: 80%; height: 60%; border-color: ${this._selections.Treatment === 'Treatable' ? cssVarColor('green-500') : cssVarColor('blueprint-blue')}; background-color: ${getSpeciesColor(this._selections.Species)}"
                      >
                        <div class="absolute inset-0 flex flex-col items-center justify-center text-center p-2">
                          <div class="text-xs text-white/40 font-mono">
                            ${this._selections.ProductType || '\u2014'}
                          </div>
                          <div class="text-sm text-white/60 font-medium mt-1">
                            ${this._selections.Species || '\u2014'}
                          </div>
                          ${this._selections.Dimensions ? html`
                            <div class="text-xs text-gable-green font-mono mt-1">
                              ${this._selections.Dimensions}
                            </div>
                          ` : nothing}
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              ` : nothing}
            </div>
          </div>

          <!-- Main Content -->
          <div class="flex-1 min-w-0">
            <div class="bg-slate-steel border border-white/10 rounded-xl p-8">
              <!-- Step Header -->
              <div class="mb-8">
                <div class="text-xs text-gable-green font-semibold uppercase tracking-wider mb-1">
                  Step ${this._currentStep + 1} of ${CONFIGURATOR_STEPS.length}
                </div>
                <h2 class="text-2xl font-bold text-white">${step.label}</h2>
                <p class="text-gray-400 text-sm mt-1">${step.description}</p>
              </div>

              <!-- Step Content -->
              <div>
                ${this._renderStepContent()}
              </div>

              <!-- Navigation -->
              <div class="flex justify-between mt-8 pt-6 border-t border-white/10">
                <button
                  @click=${() => this._handleBack()}
                  ?disabled=${this._currentStep === 0}
                  class="flex items-center gap-2 px-6 py-3 rounded-lg font-medium transition-all ${this._currentStep === 0
                    ? 'text-gray-600 cursor-not-allowed'
                    : 'text-gray-300 hover:text-white hover:bg-white/5 border border-white/10'
                  }"
                >
                  ${icon(ChevronLeft, 18)}
                  Back
                </button>

                ${this._currentStep < CONFIGURATOR_STEPS.length - 1 ? html`
                  <button
                    @click=${() => this._handleNext()}
                    ?disabled=${!this._canProceed()}
                    class="flex items-center gap-2 px-6 py-3 rounded-lg font-medium transition-all ${this._canProceed()
                      ? 'bg-gable-green text-black hover:shadow-glow-20-30'
                      : 'bg-gray-800 text-gray-600 cursor-not-allowed'
                    }"
                  >
                    Next
                    ${icon(ChevronRight, 18)}
                  </button>
                ` : nothing}
              </div>
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
