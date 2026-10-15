// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import Chart from 'chart.js/auto';
import { onThemeChange, themeColor } from '@gable/design-system';

/**
 * The series, by token, so they follow Industrial Dark and Gable light. The
 * third (the fulfilled share, nearly the whole ring) is indigo 400 in dark and
 * the sage fill in light. In light the first three step apart in lightness
 * (deep green, a mid blue, the sage fill), so they read in greyscale; in dark
 * they are Gable green, the blueprint blue and indigo 400 exactly.
 */
const SERIES = ['chart-series-1', 'chart-series-2', 'chart-fulfilled', 'amber-400', 'rose-500', 'zinc-400'];

/** Helper to map status to readable names */
function formatStatus(status: string): string {
    return status.split('_').map(word => word.charAt(0).toUpperCase() + word.slice(1)).join(' ');
}

@customElement('gable-order-status-chart')
export class GableOrderStatusChart extends LitElement {
    createRenderRoot() { return this; }

    @property({ attribute: false }) statusBreakdown: Record<string, number> = {};
    @property({ type: Boolean }) loading = false;

    @state() private _totalOrders = 0;

    private _chart: Chart<'doughnut'> | null = null;
    private _offTheme: (() => void) | null = null;

    connectedCallback() {
        super.connectedCallback();
        this._offTheme = onThemeChange(() => { if (!this.loading) this._renderChart(); });
    }

    updated(changedProperties: Map<string, unknown>) {
        super.updated(changedProperties);
        if (changedProperties.has('statusBreakdown') || changedProperties.has('loading')) {
            if (!this.loading) {
                this._renderChart();
            }
        }
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        this._offTheme?.();
        this._offTheme = null;
        this._destroyChart();
    }

    private _destroyChart() {
        if (this._chart) {
            this._chart.destroy();
            this._chart = null;
        }
    }

    private _renderChart() {
        // Defer to next frame to ensure DOM is ready
        requestAnimationFrame(() => {
            const canvas = this.querySelector<HTMLCanvasElement>('#orderStatusCanvas');
            if (!canvas) return;

            this._destroyChart();

            const data = Object.entries(this.statusBreakdown)
                .map(([status, count]) => ({
                    name: formatStatus(status),
                    value: count,
                }))
                .filter(item => item.value > 0);

            this._totalOrders = data.reduce((acc, curr) => acc + curr.value, 0);

            if (data.length === 0) return;

            this._chart = new Chart(canvas, {
                type: 'doughnut',
                data: {
                    labels: data.map(d => d.name),
                    datasets: [{
                        data: data.map(d => d.value),
                        backgroundColor: data.map((_, i) => themeColor(SERIES[i % SERIES.length])),
                        borderWidth: 0,
                        spacing: 4,
                    }],
                },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    cutout: '65%',
                    plugins: {
                        legend: {
                            position: 'bottom',
                            labels: {
                                color: themeColor('zinc-400'),
                                usePointStyle: true,
                                pointStyle: 'circle',
                                padding: 16,
                                font: { size: 11 },
                            },
                        },
                        tooltip: {
                            backgroundColor: themeColor('slate-steel', 0.9),
                            titleColor: themeColor('zinc-400'),
                            bodyColor: themeColor('white'),
                            bodyFont: { family: 'JetBrains Mono, monospace', weight: 'bold' },
                            borderColor: themeColor('white', 0.1),
                            borderWidth: 1,
                            cornerRadius: 8,
                            padding: 8,
                        },
                    },
                },
            });
        });
    }

    render() {
        if (this.loading) {
            return html`
                <div class="rounded-xl border border-white/10 bg-slate-steel/30 backdrop-blur-sm h-[400px]">
                    <div class="p-4 border-b border-white/5">
                        <div class="h-6 w-32 bg-white/10 rounded animate-pulse"></div>
                    </div>
                    <div class="p-4 flex items-center justify-center h-[320px]">
                        <div class="h-48 w-48 rounded-full border-4 border-white/10 border-t-gable-green/50 animate-spin"></div>
                    </div>
                </div>
            `;
        }

        return html`
            <div data-testid="order-status-card" class="min-w-0 overflow-hidden rounded-xl border border-white/10 bg-slate-steel/30 backdrop-blur-sm h-[400px] flex flex-col">
                <div class="p-4 border-b border-white/5">
                    <h3 class="text-base font-semibold text-white">Order Status</h3>
                </div>
                <div data-testid="order-status-canvas-wrap" class="flex-1 min-h-0 min-w-0 relative p-4">
                    <div class="absolute inset-0 flex items-center justify-center flex-col pointer-events-none z-10">
                        <span class="text-4xl font-bold text-white font-mono">${this._totalOrders}</span>
                        <span class="text-xs text-zinc-500 uppercase tracking-widest">Orders</span>
                    </div>
                    <canvas id="orderStatusCanvas" class="w-full h-full"></canvas>
                </div>
            </div>
        `;
    }
}
