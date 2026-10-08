// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

export interface DailyTillReport {
    date: string;
    total_collected: number;
    by_method: Record<string, number>;
    transaction_count: number;
}

export interface SalesSummaryReport {
    start_date: string;
    end_date: string;
    total_invoiced: number;
    total_collected: number;
    outstanding_ar: number;
    invoice_count: number;
}
