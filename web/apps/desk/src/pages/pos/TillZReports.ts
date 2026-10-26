// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { posService } from '../../services/POSService';
import type { TillReport, ZReport } from '../../types/pos';
import { formatDateTime } from '../../lib/dates.ts';
import '../../components/ui/gable-date-field.ts';
import type { GableDateField } from '../../components/ui/gable-date-field.ts';
import './TillReportView.ts';

const fmt = (cents: number) => `$${(cents / 100).toFixed(2)}`;

/**
 * The register's stored Z reports (CP-08): the frozen close of each past
 * drawer of the register on a business day (today unless another day is
 * picked). One opens read only, printable, and never changes. Sits in the
 * counter's till dialog frame.
 */
@customElement('gable-till-z-reports')
export class TillZReports extends LitElement {
    createRenderRoot() { return this; }

    /** The register whose Z reports list. */
    @property({ attribute: 'register-id' }) registerId = '';

    /** The business day the list reads, YYYY-MM-DD; empty is today. */
    @state() private day = '';
    @state() private list: ZReport[] | null = null;
    @state() private openReport: TillReport | null = null;
    @state() private failure = '';
    @state() private busy = false;

    connectedCallback() {
        super.connectedCallback();
        void this.load();
    }

    private async load() {
        try {
            this.busy = true;
            this.failure = '';
            this.list = await posService.zReports(this.registerId, this.day || undefined);
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to load the Z reports';
            this.list = [];
        } finally {
            this.busy = false;
        }
    }

    private async open(z: ZReport) {
        try {
            this.busy = true;
            this.failure = '';
            const frozen = await posService.zReport(z.till_session_id);
            this.openReport = frozen.payload ?? null;
            if (this.openReport === null) this.failure = 'This Z report carries no frozen report';
        } catch (err: unknown) {
            this.failure = err instanceof Error ? err.message : 'Failed to load the Z report';
        } finally {
            this.busy = false;
        }
    }

    private close() {
        this.dispatchEvent(new CustomEvent('z-close', { bubbles: true }));
    }

    render() {
        if (this.openReport) {
            return html`
                <div style="margin-bottom:12px">
                    <button type="button" data-testid="z-back" @click=${() => { this.openReport = null; }}
                        style="padding:8px 12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:13px;cursor:pointer">Back to the list</button>
                </div>
                <gable-till-report-view kind="z" .report=${this.openReport}
                    @report-close=${() => this.close()}></gable-till-report-view>
                ${this.failure ? html`<p role="alert" data-testid="z-failure" style="margin:12px 0 0;font-size:13px;color:rgb(var(--gb-till-bad))">${this.failure}</p>` : nothing}
            `;
        }
        const list = this.list;
        return html`
            <h2 style="margin:0 0 4px;font-size:18px;font-weight:700">Z Reports</h2>
            <p style="margin:0 0 14px;font-size:13px;color:rgb(var(--gb-till-muted))">The frozen close of each past drawer of register <span class="font-mono">${this.registerId}</span>. One opens read only.</p>
            <label style="display:block;font-size:12px;color:rgb(var(--gb-till-muted));margin-bottom:6px">Business day (empty is today)</label>
            <gable-date-field label="Business day" data-testid="z-day" .value=${this.day}
                @change=${(e: Event) => { this.day = (e.target as GableDateField).value; void this.load(); }}></gable-date-field>
            ${this.failure ? html`<p role="alert" data-testid="z-failure" style="margin:12px 0 0;font-size:13px;color:rgb(var(--gb-till-bad))">${this.failure}</p>` : nothing}
            <div style="margin-top:14px" data-testid="z-list">
                ${list === null ? html`<p style="font-size:13px;color:rgb(var(--gb-till-muted))">Reading the Z reports…</p>`
                    : list.length === 0 ? html`<p style="font-size:13px;color:rgb(var(--gb-till-muted))" data-testid="z-empty">No Z reports stored for this register on this day.</p>`
                    : list.map((z) => html`
                        <button type="button" data-testid="z-report-row" @click=${() => this.open(z)} ?disabled=${this.busy}
                            style="display:flex;justify-content:space-between;gap:10px;width:100%;padding:10px;margin-bottom:8px;background:rgb(var(--gb-till-field));border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-text));font-size:13px;cursor:pointer;text-align:left">
                            <span>Closed drawer of ${formatDateTime(z.generated_at)}</span>
                            <span class="font-mono" style="color:${z.over_short_cents === 0 ? 'rgb(var(--gb-till-muted))' : z.over_short_cents < 0 ? 'rgb(var(--gb-till-bad))' : 'rgb(var(--gb-till-warn))'}">${z.over_short_cents === 0 ? 'Balanced' : `${z.over_short_cents < 0 ? 'Short' : 'Over'} ${fmt(Math.abs(z.over_short_cents))}`}</span>
                        </button>`)}
            </div>
            <button type="button" data-testid="z-close" @click=${() => this.close()}
                style="width:100%;margin-top:14px;padding:12px;background:transparent;border:1px solid rgb(var(--gb-till-edge));border-radius:8px;color:rgb(var(--gb-till-muted));font-size:14px;cursor:pointer">Close</button>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-till-z-reports': TillZReports;
    }
}
