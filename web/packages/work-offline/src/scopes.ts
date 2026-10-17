// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The money scope fence. The scopes listed here carry money or a decision
 * about money, and money never queues: nothing of them waits in an offline
 * queue, whatever a caller or a stored row says. The list is code, not
 * configuration, so no manifest edit can open it.
 */

/** The scopes whose writes resolve online, always. */
export const MONEY_SCOPES: readonly string[] = [
  'aging',
  'approvals',
  'invoices',
  'payment_methods',
  'payments',
  'pos_transactions',
  'price_matrix',
]

/** Why a write is refused. */
export interface WriteRefusal {
  reason: 'money_scope'
  message: string
}

/**
 * The fence: a money scope in any spelling (case, spaces) is money and is
 * refused; anything else passes. Run it where a write is offered, and run it
 * again on anything a stored row would replay, because a row written by an
 * older build must not slip past a fence that has grown since.
 */
export function scopeFence(scope: unknown): WriteRefusal | null {
  const plain = typeof scope === 'string' ? scope.trim().toLowerCase() : ''
  if (MONEY_SCOPES.includes(plain)) {
    return { reason: 'money_scope', message: `${String(scope)} is a money scope: money never queues, resolve it online` }
  }
  return null
}
