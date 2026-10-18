// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { TabKey } from './capabilities.ts'

export const NAVIGATE_EVENT = 'gable:pro-navigate'

export function navigate(path: string): void {
  window.history.pushState({}, '', path)
  window.dispatchEvent(new CustomEvent(NAVIGATE_EVENT, { detail: { path } }))
}

export function replacePath(path: string): void {
  window.history.replaceState({}, '', path)
}

export function tabFromPath(pathname: string): TabKey {
  const first = pathname.split('/').filter(Boolean)[0]
  if (first === 'order' || first === 'schedule' || first === 'pay' || first === 'quotes') return first
  return 'today'
}

export function orderIdFromPath(pathname: string): string | null {
  const match = /^\/schedule\/orders\/([^/]+)/.exec(pathname)
  return match?.[1] ?? null
}

export function quoteIdFromPath(pathname: string): string | null {
  const match = /^\/quotes\/([^/]+)/.exec(pathname)
  return match?.[1] ?? null
}
