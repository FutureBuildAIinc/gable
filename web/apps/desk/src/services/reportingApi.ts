// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { fetchWithAuth } from './fetchClient';

const API_BASE = import.meta.env.VITE_API_URL || '';

export interface ReportDefinition {
  columns: ReportColumn[];
  filters: ReportFilter[];
  groupings: ReportGrouping[];
}

export interface ReportColumn {
  field: string;
  label: string;
  aggregation?: string;
}

export interface ReportFilter {
  field: string;
  operator: string;
  value: string | number | boolean | null;
}

export interface ReportGrouping {
  field: string;
}

export interface SavedReport {
  id: string;
  name: string;
  description: string;
  entity_type: string;
  definition_json: ReportDefinition;
  created_at: string;
}

export type ReportScheduleFormat = 'CSV' | 'XLSX' | 'PDF';

export interface ReportSchedule {
  id: string;
  report_id: string;
  cron_expression: string;
  recipients: string[];
  /**
   * 'ACTIVE' = a runner is attached and this schedule is registered with it.
   * 'STORED' = persisted but nothing executes it — what rows created before
   * scheduled delivery was implemented carry. Those are not started
   * retroactively; recreating the schedule writes it ACTIVE.
   */
  status: string;
  format: ReportScheduleFormat;
  last_run_at?: string;
  next_run_at?: string;
  created_at?: string;
  updated_at?: string;
}

/**
 * Whether stored schedules actually run, as reported by the backend, and what
 * "run" means there.
 *
 * This is not decoration, and it is not a constant either — the server derives
 * every field from what it actually has wired. Do not hard-code the answer in
 * the UI: an operator who configures "AR aging to the controller every Monday"
 * and sees only a success toast would reasonably assume it is happening, and
 * that assumption has to be checked against the server on every response.
 */
export interface ScheduleExecution {
  enabled: boolean;
  summary: string;
  blockers?: string[];
  /**
   * What becomes of a generated report, in the server's own words. Present only
   * when `enabled`.
   *
   * "The schedule runs" and "the report was emailed" are different claims. The
   * default deployment's email service is log-only, so a run really does query
   * the data and render a real CSV, but the message is written to the server
   * log rather than sent. Show this wherever `enabled` is shown.
   */
  delivery?: string;
  /** The cron grammar POST accepts. Six fields, seconds first. */
  cron_dialect: string;
}

export interface ReportScheduleListResponse {
  schedules: ReportSchedule[];
  execution: ScheduleExecution;
}

export interface ReportScheduleResponse {
  schedule: ReportSchedule;
  execution: ScheduleExecution;
}

export const reportingApi = {
  // Ad-hoc query preview
  previewReport: async (entityType: string, definition: ReportDefinition) => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/builder/preview`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ entity_type: entityType, definition })
    });
    if (!response.ok) throw new Error(`API Error: ${response.status}`);
    return response.json();
  },

  // Export
  exportReport: async (entityType: string, format: 'csv' | 'xlsx', definition: ReportDefinition) => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/builder/export`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ entity_type: entityType, format, definition })
    });
    if (!response.ok) throw new Error(`Export failed`);
    return response.blob();
  },

  // Saved Reports CRUD
  listSavedReports: async (): Promise<SavedReport[]> => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/saved`);
    if (!response.ok) throw new Error(`API Error: ${response.status}`);
    return response.json();
  },

  getSavedReport: async (id: string): Promise<SavedReport> => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/saved/${id}`);
    if (!response.ok) throw new Error(`API Error: ${response.status}`);
    return response.json();
  },

  saveReport: async (report: Partial<SavedReport>): Promise<SavedReport> => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/save`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(report)
    });
    if (!response.ok) throw new Error(`API Error: ${response.status}`);
    return response.json();
  },

  updateSavedReport: async (id: string, report: Partial<SavedReport>) => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/saved/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(report)
    });
    if (!response.ok) throw new Error(`API Error: ${response.status}`);
    return response.json();
  },

  deleteSavedReport: async (id: string) => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/saved/${id}`, {
      method: 'DELETE',
    });
    if (!response.ok) throw new Error(`Delete failed`);
  },

  // Run a saved report immediately and return its rows.
  runSavedReport: async (id: string): Promise<Record<string, unknown>[]> => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/saved/${id}/run`, {
      method: 'POST',
    });
    if (!response.ok) throw new Error(`API Error: ${response.status}`);
    return response.json();
  },

  // --- schedules ---------------------------------------------------------
  // These return an envelope rather than a bare list precisely so the
  // `execution` block travels with the data. Do not unwrap it away at this
  // layer: the caller needs to know whether these schedules run.

  listReportSchedules: async (): Promise<ReportScheduleListResponse> => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/schedules`);
    if (!response.ok) throw new Error(`API Error: ${response.status}`);
    return response.json();
  },

  createReportSchedule: async (
    schedule: Pick<ReportSchedule, 'report_id' | 'cron_expression' | 'recipients' | 'format'>,
  ): Promise<ReportScheduleResponse> => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/schedules`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(schedule)
    });
    if (!response.ok) throw new Error(`API Error: ${response.status}`);
    return response.json();
  },

  deleteReportSchedule: async (id: string): Promise<void> => {
    const response = await fetchWithAuth(`${API_BASE}/api/v1/reporting/schedules/${id}`, {
      method: 'DELETE',
    });
    if (!response.ok) throw new Error(`Delete failed`);
  }
};

/**
 * The server's cron engine is built with seconds enabled, so it needs SIX
 * fields. Every crontab example in the world is five, so the UI must reject
 * those before they reach the API — where the shared error envelope would
 * replace the reason with a bare "Bad Request".
 */
export function isValidCronExpression(expr: string): boolean {
  const trimmed = expr.trim();
  if (trimmed === '') return false;
  if (trimmed.startsWith('@')) return true;
  return trimmed.split(/\s+/).length === 6;
}
