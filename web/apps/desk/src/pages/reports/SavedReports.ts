// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import '../../components/ui/gable-select.ts';
import { customElement, state } from 'lit/decorators.js';
import { ToastService } from '../../lib/toast-service.ts';
import { reportingApi, isValidCronExpression } from '../../services/reportingApi';
import { confirmDialog } from '../../components/ui/app-dialog.ts';
import type {
    SavedReport,
    ReportSchedule,
    ReportScheduleFormat,
    ScheduleExecution,
} from '../../services/reportingApi';

type Frequency = 'daily' | 'weekly' | 'monthly' | 'custom';

/**
 * Six-field expressions, seconds first — the dialect the server's cron engine
 * accepts. These are NOT the five-field crontab strings the same schedules are
 * usually written as elsewhere.
 */
const FREQUENCY_PRESETS: Record<Exclude<Frequency, 'custom'>, string> = {
    daily: '0 0 9 * * *',
    weekly: '0 0 9 * * 0',
    monthly: '0 0 9 1 * *',
};

@customElement('gable-saved-reports')
export class SavedReports extends LitElement {
    createRenderRoot() { return this; }

    @state() private reports: SavedReport[] = [];
    @state() private loading = true;
    @state() private error: string | null = null;

    // Schedule modal
    @state() private scheduleFor: SavedReport | null = null;
    @state() private schedules: ReportSchedule[] = [];
    @state() private loadingSchedules = false;
    @state() private savingSchedule = false;
    @state() private execution: ScheduleExecution | null = null;

    @state() private newRecipients = '';
    @state() private newFormat: ReportScheduleFormat = 'CSV';
    @state() private newFrequency: Frequency = 'daily';
    @state() private newCron = FREQUENCY_PRESETS.daily;

    connectedCallback() {
        super.connectedCallback();
        this._loadReports();
    }

    private async _loadReports() {
        try {
            this.loading = true;
            const data = await reportingApi.listSavedReports();
            this.reports = data || [];
            this.error = null;
        } catch (err: unknown) {
            this.error = err instanceof Error ? err.message : 'Failed to load saved reports';
        } finally {
            this.loading = false;
        }
    }

    private async _handleDelete(id: string) {
        if (!await confirmDialog({
            title: 'Delete this report?',
            body: 'This cannot be undone.',
            confirmLabel: 'Delete',
            tone: 'danger',
        })) return;
        try {
            await reportingApi.deleteSavedReport(id);
            this._loadReports();
        } catch (err: unknown) {
            ToastService.show('Failed to delete report: ' + (err instanceof Error ? err.message : 'Unknown error'), 'error');
        }
    }

    private async _handleRun(id: string) {
        try {
            const rows = await reportingApi.runSavedReport(id);
            ToastService.show(`Report returned ${rows.length} row${rows.length === 1 ? '' : 's'}`, 'success');
        } catch (err: unknown) {
            ToastService.show('Failed to run report: ' + (err instanceof Error ? err.message : 'Unknown error'), 'error');
        }
    }

    // --- schedules --------------------------------------------------------

    private async _openScheduleModal(report: SavedReport) {
        this.scheduleFor = report;
        this.newRecipients = '';
        this.newFormat = 'CSV';
        this.newFrequency = 'daily';
        this.newCron = FREQUENCY_PRESETS.daily;
        await this._loadSchedules();
    }

    private _closeScheduleModal() {
        this.scheduleFor = null;
        this.schedules = [];
    }

    private async _loadSchedules() {
        if (!this.scheduleFor) return;
        try {
            this.loadingSchedules = true;
            const res = await reportingApi.listReportSchedules();
            this.execution = res.execution;
            this.schedules = (res.schedules || []).filter((s) => s.report_id === this.scheduleFor?.id);
        } catch (err: unknown) {
            ToastService.show('Failed to load schedules: ' + (err instanceof Error ? err.message : 'Unknown error'), 'error');
        } finally {
            this.loadingSchedules = false;
        }
    }

    private _handleFrequencyChange(e: Event) {
        const value = (e.target as HTMLSelectElement).value as Frequency;
        this.newFrequency = value;
        if (value !== 'custom') this.newCron = FREQUENCY_PRESETS[value];
    }

    private async _handleAddSchedule(e: Event) {
        e.preventDefault();
        if (!this.scheduleFor) return;

        const recipients = this.newRecipients
            .split(',')
            .map((email) => email.trim())
            .filter((email) => email.length > 0);

        if (recipients.length === 0) {
            ToastService.show('Provide at least one recipient email address', 'error');
            return;
        }

        // Validated here because the server's 4xx envelope replaces the reason
        // with a bare "Bad Request", so a five-field crontab expression would
        // otherwise be rejected with no explanation.
        if (!isValidCronExpression(this.newCron)) {
            ToastService.show(
                'Invalid cron expression. This scheduler uses six fields with a leading seconds field, e.g. "0 0 9 * * *" for 09:00 daily.',
                'error',
            );
            return;
        }

        try {
            this.savingSchedule = true;
            const res = await reportingApi.createReportSchedule({
                report_id: this.scheduleFor.id,
                cron_expression: this.newCron.trim(),
                recipients,
                format: this.newFormat,
            });
            this.execution = res.execution;

            // Do NOT say "scheduled" when nothing runs it, and do not say
            // "delivered" when the server only logs the send. The server tells
            // us which of the two happened and what its delivery actually is;
            // repeat it rather than assuming. The full delivery sentence stays
            // in the banner below, which is on screen for as long as the modal
            // is, rather than in a toast that disappears.
            ToastService.show(
                res.execution.enabled
                    ? 'Schedule created and registered — it will run on the server'
                    : 'Schedule saved — but scheduled delivery is not enabled, so it will not run',
                res.execution.enabled ? 'success' : 'info',
            );

            this.newRecipients = '';
            await this._loadSchedules();
        } catch (err: unknown) {
            ToastService.show('Failed to add schedule: ' + (err instanceof Error ? err.message : 'Unknown error'), 'error');
        } finally {
            this.savingSchedule = false;
        }
    }

    private async _handleDeleteSchedule(id: string) {
        if (!await confirmDialog({
            title: 'Delete this schedule?',
            body: 'This cannot be undone.',
            confirmLabel: 'Delete',
            tone: 'danger',
        })) return;
        try {
            await reportingApi.deleteReportSchedule(id);
            await this._loadSchedules();
        } catch (err: unknown) {
            ToastService.show('Failed to delete schedule: ' + (err instanceof Error ? err.message : 'Unknown error'), 'error');
        }
    }

    private _describeCron(expr: string): string {
        const known: Record<string, string> = {
            '0 0 9 * * *': 'Daily at 9:00 AM',
            '0 0 9 * * 0': 'Weekly on Sundays at 9:00 AM',
            '0 0 9 1 * *': 'Monthly on the 1st at 9:00 AM',
        };
        return known[expr.trim()] ?? `Custom (${expr})`;
    }

    /**
     * The disclosure banner, rendered whenever the server reports that nothing
     * executes stored schedules. Without it this modal reads as a working
     * automation feature, and an operator would have no way to discover that
     * their weekly financial report is never sent.
     *
     * Both this and _renderDeliveryNotice below are driven entirely by the
     * server's `execution` block — never by a local assumption about which
     * state we are in — so whichever one is true is the one that renders.
     */
    private _renderExecutionNotice() {
        if (!this.execution || this.execution.enabled) return nothing;
        return html`
            <div
                class="bg-amber-500/10 border border-amber-500/30 text-amber-400 rounded p-4 mb-4"
                data-testid="schedule-execution-notice"
                role="status"
            >
                <p class="font-semibold">${this.execution.summary}</p>
                ${this.execution.blockers?.length ? html`
                    <ul class="list-disc list-inside mt-2 text-sm space-y-1">
                        ${this.execution.blockers.map((b) => html`<li>${b}</li>`)}
                    </ul>
                ` : nothing}
                <p class="text-sm mt-2">
                    Schedules below are saved for when delivery is enabled. Use <strong>Run</strong>
                    to produce a report now.
                </p>
            </div>
        `;
    }

    /**
     * What actually happens to a report once it has run.
     *
     * "The schedule runs" and "the report reached the controller's inbox" are
     * two different claims, and the server makes only the first on its own. The
     * default deployment's email service is log-only: the run genuinely queries
     * the data and produces a real CSV, and the send is recorded in the server
     * log rather than transmitted. Showing execution.enabled without this would
     * reintroduce the same false confidence one step further along.
     */
    private _renderDeliveryNotice() {
        if (!this.execution?.enabled || !this.execution.delivery) return nothing;
        return html`
            <div
                class="bg-blue-500/10 border border-blue-500/30 text-blue-400 rounded p-4 mb-4"
                data-testid="schedule-delivery-notice"
                role="status"
            >
                <p class="font-semibold">${this.execution.summary}</p>
                <p class="text-sm mt-2">${this.execution.delivery}</p>
            </div>
        `;
    }

    private _renderScheduleModal() {
        if (!this.scheduleFor) return nothing;
        return html`
            <div class="fixed inset-0 bg-backdrop/50 flex items-center justify-center p-4 z-50">
                <div class="bg-slate-steel rounded shadow-lg w-full max-w-2xl max-h-[85vh] overflow-y-auto">
                    <div class="flex justify-between items-center px-6 py-4 border-b">
                        <div>
                            <h3 class="text-lg font-bold">Schedule delivery</h3>
                            <p class="text-sm text-zinc-400">${this.scheduleFor.name}</p>
                        </div>
                        <button
                            @click=${this._closeScheduleModal}
                            class="text-zinc-400 hover:text-white px-2"
                            aria-label="Close"
                        >&times;</button>
                    </div>

                    <div class="p-6 space-y-6">
                        ${this._renderExecutionNotice()}
                        ${this._renderDeliveryNotice()}

                        <div>
                            <h4 class="text-xs font-semibold text-zinc-400 uppercase tracking-wider mb-3">
                                Existing schedules
                            </h4>
                            ${this.loadingSchedules ? html`
                                <p class="text-zinc-400 text-sm">Loading schedules...</p>
                            ` : this.schedules.length === 0 ? html`
                                <p class="text-zinc-400 text-sm border border-dashed rounded p-4 text-center">
                                    No schedules configured for this report.
                                </p>
                            ` : html`
                                <ul class="space-y-2" data-testid="schedule-list">
                                    ${this.schedules.map((s) => html`
                                        <li class="flex items-center justify-between border rounded p-3">
                                            <div>
                                                <div class="font-medium text-sm">
                                                    ${this._describeCron(s.cron_expression)}
                                                    <span class="ml-2 text-xs font-mono bg-white/10 px-2 py-0.5 rounded">${s.format}</span>
                                                    <span class="ml-2 text-xs px-2 py-0.5 rounded bg-white/10 text-zinc-400">${s.status}</span>
                                                </div>
                                                <div class="text-xs text-zinc-400 mt-1">${s.recipients.join(', ')}</div>
                                            </div>
                                            <button
                                                @click=${() => this._handleDeleteSchedule(s.id)}
                                                class="text-red-400 hover:text-red-400 text-sm"
                                            >Delete</button>
                                        </li>
                                    `)}
                                </ul>
                            `}
                        </div>

                        <form @submit=${this._handleAddSchedule} class="space-y-4 border-t pt-6">
                            <h4 class="text-xs font-semibold text-zinc-400 uppercase tracking-wider">
                                Add a schedule
                            </h4>

                            <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                                <div>
                                    <label class="block text-sm text-zinc-300 mb-1" for="schedule-format">Output format</label>
                                    <gable-select
                                        id="schedule-format"
                                        label="Output format"
                                        .value=${this.newFormat}
                                        .options=${[
                                            { value: 'CSV', label: 'CSV' },
                                            { value: 'XLSX', label: 'Excel (XLSX)' },
                                            { value: 'PDF', label: 'PDF' },
                                        ]}
                                        @change=${(e: Event) => this.newFormat = (e.target as HTMLSelectElement).value as ReportScheduleFormat}
                                    ></gable-select>
                                </div>
                                <div>
                                    <label class="block text-sm text-zinc-300 mb-1" for="schedule-frequency">Frequency</label>
                                    <gable-select
                                        id="schedule-frequency"
                                        label="Frequency"
                                        .value=${this.newFrequency}
                                        .options=${[
                                            { value: 'daily', label: 'Daily at 9:00 AM' },
                                            { value: 'weekly', label: 'Weekly on Sunday at 9:00 AM' },
                                            { value: 'monthly', label: 'Monthly on the 1st at 9:00 AM' },
                                            { value: 'custom', label: 'Custom cron expression' },
                                        ]}
                                        @change=${this._handleFrequencyChange}
                                    ></gable-select>
                                </div>
                            </div>

                            <div>
                                <label class="block text-sm text-zinc-300 mb-1" for="schedule-cron">Cron expression</label>
                                <input
                                    id="schedule-cron"
                                    type="text"
                                    aria-label="Cron expression"
                                    .value=${this.newCron}
                                    ?readonly=${this.newFrequency !== 'custom'}
                                    @input=${(e: Event) => this.newCron = (e.target as HTMLInputElement).value}
                                    class="w-full border bg-zinc-800 text-white rounded p-2 font-mono text-sm ${this.newFrequency !== 'custom' ? 'bg-white/10' : ''}"
                                    placeholder="0 0 9 * * *"
                                />
                                <p class="text-xs text-zinc-400 mt-1">
                                    ${this.execution?.cron_dialect ??
                                        'Six fields, seconds first (e.g. "0 0 9 * * *"). Five-field crontab expressions are rejected.'}
                                </p>
                            </div>

                            <div>
                                <label class="block text-sm text-zinc-300 mb-1" for="schedule-recipients">Recipients</label>
                                <input
                                    id="schedule-recipients"
                                    type="text"
                                    aria-label="Recipients"
                                    .value=${this.newRecipients}
                                    @input=${(e: Event) => this.newRecipients = (e.target as HTMLInputElement).value}
                                    class="w-full border bg-zinc-800 text-white rounded p-2"
                                    placeholder="controller@example.com, gm@example.com"
                                    required
                                />
                                <p class="text-xs text-zinc-400 mt-1">Separate multiple addresses with a comma.</p>
                            </div>

                            <div class="flex justify-end gap-2">
                                <button
                                    type="button"
                                    @click=${this._closeScheduleModal}
                                    class="px-4 py-2 border rounded text-zinc-300 hover:bg-white/5"
                                >Cancel</button>
                                <button
                                    type="submit"
                                    ?disabled=${this.savingSchedule}
                                    class="px-4 py-2 bg-blue-600 text-plate rounded hover:bg-blue-700 disabled:opacity-50"
                                >${this.savingSchedule ? 'Saving...' : 'Add schedule'}</button>
                            </div>
                        </form>
                    </div>
                </div>
            </div>
        `;
    }

    render() {
        if (this.loading) {
            return html`<div class="p-8">Loading reports...</div>`;
        }

        return html`
            <div class="p-8">
                <div class="flex justify-between items-center mb-6">
                    <h1 class="text-2xl font-bold">Saved Reports</h1>
                    <a
                        href="/reports/builder"
                        class="bg-blue-600 text-plate px-4 py-2 rounded shadow hover:bg-blue-700"
                    >
                        Create New Report
                    </a>
                </div>

                ${this.error ? html`
                    <div class="bg-red-500/10 text-red-400 p-4 rounded mb-6">
                        ${this.error}
                    </div>
                ` : nothing}

                ${this.reports.length === 0 ? html`
                    <div class="text-center text-zinc-400 py-12 bg-slate-steel rounded shadow">
                        <p>No reports saved yet.</p>
                        <a href="/reports/builder" class="text-blue-400 hover:underline mt-2 inline-block">
                            Build your first report
                        </a>
                    </div>
                ` : html`
                    <div class="bg-slate-steel shadow rounded overflow-hidden">
                        <table class="min-w-full divide-y divide-white/10">
                            <thead class="bg-white/5">
                                <tr>
                                    <th class="px-6 py-3 text-left text-xs font-medium text-zinc-400 uppercase tracking-wider">Name</th>
                                    <th class="px-6 py-3 text-left text-xs font-medium text-zinc-400 uppercase tracking-wider">Entity Type</th>
                                    <th class="px-6 py-3 text-left text-xs font-medium text-zinc-400 uppercase tracking-wider">Description</th>
                                    <th class="px-6 py-3 text-right text-xs font-medium text-zinc-400 uppercase tracking-wider">Actions</th>
                                </tr>
                            </thead>
                            <tbody class="bg-slate-steel divide-y divide-white/10">
                                ${this.reports.map((report) => html`
                                    <tr>
                                        <td class="px-6 py-4 whitespace-nowrap font-medium text-white">${report.name}</td>
                                        <td class="px-6 py-4 whitespace-nowrap text-sm text-zinc-400 capitalize">${report.entity_type}</td>
                                        <td class="px-6 py-4 text-sm text-zinc-400 truncate max-w-xs">${report.description}</td>
                                        <td class="px-6 py-4 whitespace-nowrap text-right text-sm font-medium">
                                            <button
                                                class="text-indigo-400 hover:text-indigo-400 mr-4"
                                                @click=${() => this._handleRun(report.id)}
                                            >
                                                Run
                                            </button>
                                            <button
                                                class="text-indigo-400 hover:text-indigo-400 mr-4"
                                                @click=${() => this._openScheduleModal(report)}
                                            >
                                                Schedule
                                            </button>
                                            <a href="/reports/builder?id=${report.id}" class="text-blue-400 hover:text-blue-400 mr-4">
                                                Edit
                                            </a>
                                            <button
                                                @click=${() => this._handleDelete(report.id)}
                                                class="text-red-400 hover:text-red-400"
                                            >
                                                Delete
                                            </button>
                                        </td>
                                    </tr>
                                `)}
                            </tbody>
                        </table>
                    </div>
                `}
            </div>
            ${this._renderScheduleModal()}
        `;
    }
}

export default SavedReports;
