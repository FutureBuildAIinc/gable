// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The deposit dialog (CP-07): dollars in, integer cents posted against the
 * order with no applications, and the refusals in the same words the server
 * uses: nothing below a cent, and never past the order's balance due after
 * deposits.
 */
import { describe, it, expect, vi } from 'vitest'
import './DepositModal'
import type { GableDepositModal } from './DepositModal'
import { mount, update, q, chooseOption } from '../../test/dom'
import type { CreatePaymentRequest } from '../../types/payment'

function open(balanceDueCents: number) {
  return mount<GableDepositModal>('gable-deposit-modal', {
    isOpen: true,
    orderId: 'ord-1',
    customerId: 'cust-1',
    branchId: 'branch-1',
    balanceDueCents,
  })
}

function amountInput(el: GableDepositModal): HTMLInputElement {
  return q<HTMLInputElement>(el, 'input[name="deposit-amount"]')
}

async function typeAmount(el: GableDepositModal, dollars: string) {
  const input = amountInput(el)
  input.value = dollars
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await update(el, {})
}

function submit(el: GableDepositModal): CreatePaymentRequest | null {
  const saved = vi.fn()
  el.addEventListener('save', (e) => saved((e as CustomEvent<CreatePaymentRequest>).detail))
  q<HTMLFormElement>(el, 'form').dispatchEvent(
    new Event('submit', { bubbles: true, cancelable: true }),
  )
  return saved.mock.calls.length ? saved.mock.calls[0][0] as CreatePaymentRequest : null
}

describe('gable-deposit-modal — cents out', () => {
  it('posts integer cents with the order named, the customer and branch, and no applications', async () => {
    const el = await open(84_662)
    await typeAmount(el, '400')
    const saved = submit(el)
    expect(saved).toEqual({
      customer_id: 'cust-1',
      branch_id: 'branch-1',
      amount_cents: 40_000,
      method: 'cash',
      order_id: 'ord-1',
    })
  })

  it('carries the reference and notes when given', async () => {
    const el = await open(84_662)
    await typeAmount(el, '10')
    const reference = el.querySelector('input[type="text"]') as HTMLInputElement
    reference.value = 'CHK-4412'
    reference.dispatchEvent(new Event('input', { bubbles: true }))
    const notes = el.querySelector('textarea') as HTMLTextAreaElement
    notes.value = 'down payment on the delivery'
    notes.dispatchEvent(new Event('input', { bubbles: true }))
    await update(el, {})
    const saved = submit(el)
    expect(saved?.reference).toBe('CHK-4412')
    expect(saved?.notes).toBe('down payment on the delivery')
  })

  it('posts the chosen method, not only cash', async () => {
    const el = await open(84_662)
    await typeAmount(el, '25')
    await chooseOption(q(el, 'gable-select'), 'check')
    await update(el, {})
    const saved = submit(el)
    expect(saved?.method).toBe('check')
  })
})

describe('gable-deposit-modal — the refusals', () => {
  it('refuses an amount under a cent in words', async () => {
    const el = await open(84_662)
    await typeAmount(el, '0')
    expect(submit(el)).toBeNull()
    await update(el, {})
    expect(q(el, '[data-testid="deposit-error"]').textContent).toContain('one cent or more')
  })

  it('refuses a deposit past the balance due after deposits in the server\'s words', async () => {
    const el = await open(5988)
    await typeAmount(el, '59.89')
    expect(submit(el)).toBeNull()
    await update(el, {})
    expect(q(el, '[data-testid="deposit-error"]').textContent).toContain('larger than the order\'s balance due after deposits')
  })

  it('takes a deposit of exactly the balance due', async () => {
    const el = await open(5988)
    await typeAmount(el, '59.88')
    expect(submit(el)?.amount_cents).toBe(5988)
  })

  it('shows the balance the deposit may not pass', async () => {
    const el = await open(84_662)
    expect(q(el, '[data-testid="deposit-balance-due"]').textContent).toBe('846.62')
  })
})
