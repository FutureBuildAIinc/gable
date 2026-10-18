// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { PortalMe, PortalOrder, PortalOrderTimeline, PortalScheduleHold, PortalScheduleSlot, PortalToday } from '../types.ts'

export function iso(): string {
  return new Date().toISOString()
}

export function day(offset = 0): string {
  const value = new Date()
  value.setUTCDate(value.getUTCDate() + offset)
  return value.toISOString().slice(0, 10)
}

export function dateTime(offset = 0, hour = 12): string {
  const value = new Date()
  value.setUTCDate(value.getUTCDate() + offset)
  value.setUTCHours(hour, 0, 0, 0)
  return value.toISOString()
}

export const testIds = {
  user: '11111111-1111-4111-8111-111111111111',
  customer: '22222222-2222-4222-8222-222222222222',
  yardA: '33333333-3333-4333-8333-333333333333',
  yardB: '44444444-4444-4444-8444-444444444444',
  order: '66666666-6666-4666-8666-666666666666',
  delivery: '77777777-7777-4777-8777-777777777777',
  windowA: '88888888-8888-4888-8888-888888888888',
  windowB: '99999999-9999-4999-8999-999999999999',
  hold: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa',
  invoice: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb',
  quote: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc',
  product: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd',
}

export const meFixture: PortalMe = {
  user: {
    id: testIds.user,
    customer_id: testIds.customer,
    email: 'project@example.com',
    name: 'Project Lead',
    role: 'owner',
    status: 'Active',
    created_at: iso(),
    updated_at: iso(),
  },
  account: {
    id: testIds.customer,
    name: 'Summit Frame Works',
    account_number: 'PRO-100',
    primary_branch_id: testIds.yardA,
  },
  yards: [
    { id: testIds.yardA, code: 'MAIN', name: 'Main Yard', timezone: 'UTC', is_primary: true },
    { id: testIds.yardB, code: 'NORTH', name: 'North Yard', timezone: 'UTC', is_primary: false },
  ],
  capabilities: {
    manage_team: true,
    see_money: true,
    pay: true,
    order: true,
    request_quotes: true,
    approve_quotes: true,
    schedule: true,
    edit_delivery_instructions: true,
  },
}

export const todayFixture: PortalToday = {
  quotes_awaiting_pro: {
    count: 1,
    first: [{ id: testIds.quote, total_cents: 7388, sent_at: iso(), expires_at: dateTime(2), updated_at: iso() }],
  },
  deliveries_today: {
    count: 1,
    first: [{ delivery_id: testIds.delivery, order_id: testIds.order, status: 'OUT_FOR_DELIVERY', scheduled_for: dateTime(0, 14), yard_code: 'MAIN', is_pickup: false, updated_at: iso() }],
  },
  next_deliveries: {
    count: 1,
    first: [{ delivery_id: testIds.delivery, order_id: testIds.order, status: 'SCHEDULED', scheduled_for: dateTime(1, 15), yard_code: 'NORTH', is_pickup: true, updated_at: iso() }],
  },
  invoices_due: {
    count: 1,
    first: [{ id: testIds.invoice, number: 'IN-1001', total_cents: 125050, due_date: dateTime(-1), is_overdue: true, updated_at: iso() }],
  },
  open_orders: {
    count: 1,
    first: [{ id: testIds.order, status: 'CONFIRMED', total_cents: 742118, updated_at: iso() }],
  },
}

export const holdFixture: PortalScheduleHold = {
  id: testIds.hold,
  window_id: testIds.windowA,
  yard_id: testIds.yardA,
  kind: 'delivery',
  date: day(1),
  start_utc: dateTime(1, 7),
  end_utc: dateTime(1, 11),
  order_id: testIds.order,
  status: 'held',
  created_at: iso(),
  updated_at: iso(),
}

export const orderFixture: PortalOrder = {
  id: testIds.order,
  status: 'CONFIRMED',
  total_amount: 7421.18,
  created_at: iso(),
  updated_at: iso(),
  project_id: null,
  project_name: 'Glenview Lofts',
  site_delivery_instructions: 'Use the north gate and call the site lead.',
  schedule_hold: holdFixture,
  lines: [{
    product_id: testIds.product,
    product_sku: 'LUM-248',
    product_name: '2x4x8 SPF Premium',
    quantity: 48,
    price_each: 4.88,
  }],
}

export const timelineFixture: PortalOrderTimeline = {
  order_id: testIds.order,
  status: 'OUT_FOR_DELIVERY',
  delivery_type: 'DELIVERY',
  is_cancelled: false,
  steps: [
    { key: 'confirmed', label: 'Confirmed', state: 'done', at: iso() },
    { key: 'preparing', label: 'Preparing', state: 'done', at: iso() },
    { key: 'loaded', label: 'Loaded', state: 'done', at: iso() },
    { key: 'out_for_delivery', label: 'Out for delivery', state: 'current', at: iso() },
    { key: 'delivered', label: 'Delivered', state: 'upcoming', at: null },
  ],
  pod: null,
}

export const deliveredTimelineFixture: PortalOrderTimeline = {
  ...timelineFixture,
  status: 'DELIVERED',
  steps: timelineFixture.steps.map((step, index) => ({ ...step, state: index === 4 ? 'current' : 'done', at: iso() })),
  pod: {
    signed_by: 'Site Receiver',
    photo_url: 'https://example.com/proof.jpg',
    place: 'North gate, lot 4',
    timestamp: iso(),
  },
}

export const slotsFixture: PortalScheduleSlot[] = [
  {
    window_id: testIds.windowA,
    yard_id: testIds.yardA,
    yard_code: 'MAIN',
    kind: 'delivery',
    date: day(1),
    start_utc: dateTime(1, 7),
    end_utc: dateTime(1, 11),
    start_local: '07:00',
    end_local: '11:00',
    capacity: 4,
    remaining: 2,
    cutoff_minutes: 60,
  },
  {
    window_id: testIds.windowB,
    yard_id: testIds.yardB,
    yard_code: 'NORTH',
    kind: 'pickup',
    date: day(2),
    start_utc: dateTime(2, 13),
    end_utc: dateTime(2, 16),
    start_local: '13:00',
    end_local: '16:00',
    capacity: 3,
    remaining: 1,
    cutoff_minutes: 60,
  },
]
