// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

const currency = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

export const NO_FIGURE = 'n/a'

export function formatCents(cents: number | null | undefined): string {
  return typeof cents === 'number' && Number.isFinite(cents) ? currency.format(cents / 100) : NO_FIGURE
}

export function formatDollars(dollars: number | null | undefined): string {
  return typeof dollars === 'number' && Number.isFinite(dollars) ? currency.format(dollars) : NO_FIGURE
}

export function formatBps(bps: number | null | undefined): string {
  if (typeof bps !== 'number' || !Number.isFinite(bps)) return NO_FIGURE
  return `${(bps / 100).toLocaleString('en-US', { maximumFractionDigits: 2 })}%`
}

export function quantityWithUnit(quantity: string | number, unit: string): string {
  const qty = String(quantity).trim()
  return unit.trim() === '' ? qty : `${qty} ${unit.trim()}`
}

export function shortDate(value: string | null | undefined, timeZone?: string): string {
  if (!value) return NO_FIGURE
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return NO_FIGURE
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone }).format(date)
}

export function timeOfDay(value: string | null | undefined, timeZone?: string): string {
  if (!value) return NO_FIGURE
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return NO_FIGURE
  return new Intl.DateTimeFormat('en-US', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone }).format(date)
}

export function statusLabel(status: string): string {
  return status.toLowerCase().replaceAll('_', ' ').replace(/^\w/, (letter) => letter.toUpperCase())
}
