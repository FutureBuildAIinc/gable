// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * cn — merge conditional class names with Tailwind-aware conflict
 * resolution. MOVED from web/apps/desk/src/lib/utils.ts when web/ became
 * one workspace; the desk re-exports it from there so existing imports
 * keep working, and every shared component (and the front door) uses this
 * copy.
 */
import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
    return twMerge(clsx(inputs))
}
