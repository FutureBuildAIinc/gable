// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The Staff tab of Tech Admin — the UI that hands out AI_LM access.
 *
 * Every control here writes a row the backend's
 * `POST /api/integration/validate-staff` reads back, and that endpoint decides
 * whether someone can log into AI_LM at all. Its rule is:
 *
 *     entitled = staff.active AND an `ai_lm` grant exists AND `modules.ai_lm.enabled`
 *
 * Three things about that make this surface easy to get subtly wrong:
 *
 *  - **The checkbox is a grant, not an entitlement.** It must stay checked while
 *    the module is globally disabled; the kill switch suspends access without
 *    deleting grants, so flipping it back on restores the same roster. A UI that
 *    cleared the boxes would tell an operator their grants were gone.
 *  - **Grant and revoke are different verbs on different URLs.** Sending POST
 *    where DELETE was meant silently grants access instead of removing it.
 *  - **State comes from the server, never optimistically.** A failed grant must
 *    leave the box where the server says it is, not where the click put it.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import './TechAdminPage'
import type { TechAdminPage } from './TechAdminPage'
import type { StaffMember, ModuleInfo } from '../../../services/TechAdminService'
import { mountAsync, update, flush, text, jsonResponse } from '../../../test/dom'

const DANA = '11111111-1111-4111-8111-111111111111'
const YUKI = '22222222-2222-4222-8222-222222222222'

function roster(danaHasAILM = true): StaffMember[] {
  return [
    {
      id: DANA,
      email: 'dispatcher@gable.com',
      full_name: 'Dana Ramirez',
      staff_no: 'STF-001',
      role: 'dispatcher',
      active: true,
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      modules: danaHasAILM ? ['ai_lm'] : [],
    },
    {
      id: YUKI,
      email: 'yard@gable.com',
      full_name: 'Yuki Tan',
      role: 'yard',
      active: false,
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
      modules: [],
    },
  ]
}

interface Recorded {
  url: string
  method: string
  body: string | null
}

/** Server state the fake backend serves and mutates, as the real one would. */
interface Backend {
  staff: StaffMember[]
  modules: ModuleInfo[]
  /** URLs (as `METHOD url`) that should answer with a 500 instead. */
  fail: Set<string>
  calls: Recorded[]
}

let backend: Backend

/**
 * Route the whole Tech Admin page's fetches. The page loads keys, AI settings,
 * routing settings and EDI partners on connect as well, so those get empty
 * stubs — only the staff/module calls carry behaviour.
 */
function serve(initial: Partial<Backend> = {}) {
  backend = {
    staff: initial.staff ?? roster(),
    modules: initial.modules ?? [{ id: 'ai_lm', name: 'AI_LM', enabled: true }],
    fail: initial.fail ?? new Set<string>(),
    calls: [],
  }

  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = (init?.method ?? 'GET').toUpperCase()
      const body = typeof init?.body === 'string' ? init.body : null
      backend.calls.push({ url, method, body })

      const key = `${method} ${url}`
      if (backend.fail.has(key)) {
        return Promise.resolve(jsonResponse({ error: 'nope' }, 500))
      }

      if (method === 'GET' && url.endsWith('/api/v1/admin/staff')) {
        return Promise.resolve(jsonResponse(backend.staff))
      }
      if (method === 'GET' && url.endsWith('/api/v1/admin/modules')) {
        return Promise.resolve(jsonResponse(backend.modules))
      }
      if (method === 'PUT' && url.includes('/api/v1/admin/modules/')) {
        const id = url.split('/api/v1/admin/modules/')[1]
        const enabled = JSON.parse(body ?? '{}').enabled === true
        backend.modules = backend.modules.map((m) => (m.id === id ? { ...m, enabled } : m))
        return Promise.resolve(jsonResponse({ id, enabled }))
      }
      if (method === 'POST' && url.includes('/modules')) {
        const staffId = url.split('/api/v1/admin/staff/')[1].split('/')[0]
        const moduleId = JSON.parse(body ?? '{}').module_id
        backend.staff = backend.staff.map((s) =>
          s.id === staffId && !s.modules.includes(moduleId)
            ? { ...s, modules: [...s.modules, moduleId] }
            : s,
        )
        return Promise.resolve(jsonResponse(backend.staff.find((s) => s.id === staffId)))
      }
      if (method === 'DELETE' && url.includes('/modules/')) {
        const [staffId, , moduleId] = url.split('/api/v1/admin/staff/')[1].split('/')
        backend.staff = backend.staff.map((s) =>
          s.id === staffId ? { ...s, modules: s.modules.filter((m) => m !== moduleId) } : s,
        )
        return Promise.resolve(jsonResponse(backend.staff.find((s) => s.id === staffId)))
      }

      // Everything else the page loads on connect.
      return Promise.resolve(jsonResponse([]))
    }),
  )
}

/** Mount the page and switch to the Staff tab. */
async function openStaffTab(): Promise<TechAdminPage> {
  const el = await mountAsync<TechAdminPage>('gable-tech-admin')
  const tab = Array.from(el.querySelectorAll('button')).find((b) => text(b) === 'Staff')
  if (!tab) throw new Error('no Staff tab button')
  tab.click()
  await flush()
  await update(el, {})
  return el
}

/** The AI_LM grant checkbox on the row whose text contains `name`. */
function grantBox(el: TechAdminPage, name: string): HTMLInputElement {
  const row = Array.from(el.querySelectorAll('tbody tr')).find((tr) => text(tr).includes(name))
  if (!row) throw new Error(`no roster row for ${name}`)
  const box = row.querySelector<HTMLInputElement>('input[type="checkbox"]')
  if (!box) throw new Error(`no grant checkbox for ${name}`)
  return box
}

function globalToggle(el: TechAdminPage): HTMLButtonElement {
  const btn = el.querySelector<HTMLButtonElement>('button[role="switch"]')
  if (!btn) throw new Error('no global module toggle')
  return btn
}

async function toggle(el: TechAdminPage, box: HTMLInputElement, checked: boolean) {
  box.checked = checked
  box.dispatchEvent(new Event('change'))
  await flush()
  await update(el, {})
}

function callsTo(method: string, fragment: string): Recorded[] {
  return backend.calls.filter((c) => c.method === method && c.url.includes(fragment))
}

beforeEach(() => serve())
afterEach(() => vi.unstubAllGlobals())

describe('Staff tab — roster', () => {
  it('renders each staff member with their grant state and active flag', async () => {
    const el = await openStaffTab()

    expect(grantBox(el, 'Dana Ramirez').checked).toBe(true)
    expect(grantBox(el, 'Yuki Tan').checked).toBe(false)

    const dana = Array.from(el.querySelectorAll('tbody tr')).find((tr) =>
      text(tr).includes('Dana Ramirez'),
    )!
    expect(text(dana)).toContain('dispatcher@gable.com')
    expect(text(dana)).toContain('STF-001')
    expect(text(dana)).toContain('Active')

    // Yuki is deactivated: never entitled, whatever her grants say.
    const yuki = Array.from(el.querySelectorAll('tbody tr')).find((tr) =>
      text(tr).includes('Yuki Tan'),
    )!
    expect(text(yuki)).toContain('Inactive')
  })

  it('says so when the roster is empty rather than rendering a blank table', async () => {
    serve({ staff: [] })
    const el = await openStaffTab()
    expect(text(el.querySelector('tbody'))).toContain('No staff members yet')
  })
})

describe('Staff tab — granting and revoking', () => {
  it('grants with POST .../modules carrying module_id, then re-reads from the server', async () => {
    const el = await openStaffTab()
    const before = callsTo('GET', '/api/v1/admin/staff').length

    await toggle(el, grantBox(el, 'Yuki Tan'), true)

    const grants = callsTo('POST', `/api/v1/admin/staff/${YUKI}/modules`)
    expect(grants).toHaveLength(1)
    expect(JSON.parse(grants[0].body!)).toEqual({ module_id: 'ai_lm' })
    // Refetched, so the box reflects what the server stored, not the click.
    expect(callsTo('GET', '/api/v1/admin/staff').length).toBe(before + 1)
    expect(grantBox(el, 'Yuki Tan').checked).toBe(true)
  })

  it('revokes with DELETE .../modules/ai_lm — never a POST', async () => {
    const el = await openStaffTab()

    await toggle(el, grantBox(el, 'Dana Ramirez'), false)

    expect(callsTo('DELETE', `/api/v1/admin/staff/${DANA}/modules/ai_lm`)).toHaveLength(1)
    expect(callsTo('POST', `/api/v1/admin/staff/${DANA}/modules`)).toHaveLength(0)
    expect(grantBox(el, 'Dana Ramirez').checked).toBe(false)
    expect(backend.staff.find((s) => s.id === DANA)!.modules).toEqual([])
  })

  it('surfaces the error and grants nothing when the server rejects a grant', async () => {
    serve({ fail: new Set([`POST /api/v1/admin/staff/${YUKI}/modules`]) })
    const el = await openStaffTab()

    await toggle(el, grantBox(el, 'Yuki Tan'), true)

    expect(text(el)).toContain('Failed to grant module access')
    expect(backend.staff.find((s) => s.id === YUKI)!.modules).toEqual([])
  })

  // Regression: after a failed grant the checkbox used to stay where the click
  // put it, so an access-control screen showed access that was never granted.
  // `_handleToggleStaffAccess` set `staffError` and returned without resyncing,
  // and Lit's `.checked=${hasAccess}` binding did not rewrite the property
  // because `hasAccess` never changed from the value last committed — lit-html
  // skips a PropertyPart whose value is unchanged, even though the user has
  // since mutated the DOM. The catch branch now writes the model value straight
  // back onto the input.
  it('resets the checkbox to the server value after a failed grant', async () => {
    serve({ fail: new Set([`POST /api/v1/admin/staff/${YUKI}/modules`]) })
    const el = await openStaffTab()

    await toggle(el, grantBox(el, 'Yuki Tan'), true)

    expect(grantBox(el, 'Yuki Tan').checked).toBe(false)
  })

  it('restores the checkbox after a failed revoke', async () => {
    // Same bug in reverse: the box cleared while the grant was still live.
    serve({ fail: new Set([`DELETE /api/v1/admin/staff/${DANA}/modules/ai_lm`]) })
    const el = await openStaffTab()

    await toggle(el, grantBox(el, 'Dana Ramirez'), false)

    expect(text(el)).toContain('Failed to revoke module access')
    expect(backend.staff.find((s) => s.id === DANA)!.modules).toEqual(['ai_lm'])
    expect(grantBox(el, 'Dana Ramirez').checked).toBe(true)
  })
})

describe('Staff tab — the global AI_LM kill switch', () => {
  it('reports enabled state and flips it with PUT /admin/modules/ai_lm', async () => {
    const el = await openStaffTab()
    expect(globalToggle(el).getAttribute('aria-checked')).toBe('true')

    globalToggle(el).click()
    await flush()
    await update(el, {})

    const puts = callsTo('PUT', '/api/v1/admin/modules/ai_lm')
    expect(puts).toHaveLength(1)
    expect(JSON.parse(puts[0].body!)).toEqual({ enabled: false })
    expect(globalToggle(el).getAttribute('aria-checked')).toBe('false')
    expect(text(el)).toContain('Disabled globally')
  })

  it('keeps every grant checked while the module is off — the switch suspends, it does not revoke', async () => {
    serve({ modules: [{ id: 'ai_lm', name: 'AI_LM', enabled: false }] })
    const el = await openStaffTab()

    expect(globalToggle(el).getAttribute('aria-checked')).toBe('false')
    expect(grantBox(el, 'Dana Ramirez').checked).toBe(true)
    expect(callsTo('DELETE', '/modules/')).toHaveLength(0)

    // Turning it back on restores the same roster, not an empty one.
    globalToggle(el).click()
    await flush()
    await update(el, {})
    expect(globalToggle(el).getAttribute('aria-checked')).toBe('true')
    expect(grantBox(el, 'Dana Ramirez').checked).toBe(true)
  })

  it('spells out the three-part entitlement rule so an operator is not guessing', async () => {
    const el = await openStaffTab()
    const footnote = text(el)
    expect(footnote).toContain('Active')
    expect(footnote).toContain('enabled globally')
    expect(footnote).toContain('AI_LM grant')
  })

  it('disables the toggle when the module is not in the catalog at all', async () => {
    serve({ modules: [] })
    const el = await openStaffTab()
    expect(globalToggle(el).disabled).toBe(true)
  })
})
