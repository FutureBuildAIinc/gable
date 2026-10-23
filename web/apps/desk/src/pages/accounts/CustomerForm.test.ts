// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The customer form's check limit field: shown beside the credit limit, empty meaning the counter rule. */
import { describe, it, expect, afterEach } from 'vitest'
import './CustomerForm'
import type { GableCustomerForm } from './CustomerForm'
import type { Customer, CustomerRequest } from '../../types/customer'
import { update, q, text } from '../../test/dom'

const CUSTOMER: Customer = {
  id: 'c-1',
  account_number: 'A-100',
  name: 'Smith Builders',
  email: null,
  phone: null,
  address: null,
  is_active: true,
  primary_branch_id: 'b-1',
  branch_ids: ['b-1'],
  price_profile_id: null,
  salesperson_id: null,
  salesperson_name: null,
  credit_limit_cents: null,
  check_limit_cents: 20050,
  balance_cents: 0,
  currency: null,
  effective_currency: 'USD',
  payment_terms_id: 't-1',
  payment_terms: { id: 't-1', code: 'NET30', name: 'Net 30' },
  po_required: false,
  named_buyer_required: false,
  revision: 4,
  created_at: '2026-01-01T00:00:00.000000Z',
  updated_at: '2026-01-01T00:00:00.000000Z',
} as unknown as Customer

afterEach(() => { document.body.innerHTML = '' })

async function open(customer: Customer | null): Promise<GableCustomerForm> {
  const el = document.createElement('gable-customer-form') as GableCustomerForm;
  el.customer = customer;
  document.body.appendChild(el);
  await update(el, {})
  await update(el, {})
  return el
}

function submit(el: GableCustomerForm): CustomerRequest | null {
  let detail: CustomerRequest | null = null
  el.addEventListener('customer-submit', (e: Event) => { detail = (e as CustomEvent<CustomerRequest>).detail })
  const form = el.querySelector('form')!
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  return detail
}

describe('the customer form check limit field', () => {
  it('shows the stored check limit in dollars beside the credit limit', async () => {
    const el = await open(CUSTOMER)
    const field = q<HTMLInputElement>(el, 'input[id$="-checklimit"]')
    expect(field.value).toBe('200.50')
  })

  it('sends the typed check limit in cents, empty meaning the counter rule', async () => {
    const el = await open(CUSTOMER)
    const field = q<HTMLInputElement>(el, 'input[id$="-checklimit"]')
    field.value = '500'
    field.dispatchEvent(new Event('input'))
    await update(el, {})
    expect(submit(el)).toMatchObject({ check_limit_cents: 50000 })

    field.value = ''
    field.dispatchEvent(new Event('input'))
    await update(el, {})
    expect(submit(el)).toMatchObject({ check_limit_cents: null })
  })

  it('refuses a check limit that is not a dollar amount', async () => {
    const el = await open(CUSTOMER)
    const field = q<HTMLInputElement>(el, 'input[id$="-checklimit"]')
    field.value = 'lots'
    field.dispatchEvent(new Event('input'))
    await update(el, {})
    expect(submit(el)).toBeNull()
    await update(el, {})
    expect(text(el)).toContain('Enter an amount in dollars')
  })
})
