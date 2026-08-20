---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
name: add-a-test
description: Add real test coverage to a Gable module that has none — a table-driven Go test under backend/internal/<module>/ or a vitest test under app/src/ — with the correct SPDX header and no Postgres required. Use when the user says "add a test", "write tests for the invoice module", "this module has no coverage", "help me contribute a test", "what should I test", "improve test coverage", "/write-a-test", or wants a small, safe first contribution.
---

# add-a-test — the best first contribution in this repo

About half the backend modules still ship zero tests. Adding one good test is a genuinely
valuable, low-risk, easy-to-review contribution — and the REUSE + coverage plumbing is already
in place, so there's nothing to set up.

It is also easy to do badly. The house rules in §5 exist because a bad test is worse than no
test: it makes CI green while asserting nothing, and it makes the next refactor harder.

---

## 1 · Find an untested module

Never trust a hardcoded list — compute it:

```bash
cd backend
for d in internal/*/; do
  [ -z "$(ls $d*_test.go 2>/dev/null)" ] && echo "no tests: ${d%/}"
done
```

```bash
find app/src -name '*.test.ts' -o -name '*.test.tsx'            # what the frontend covers
```

(`internal/testutil` will show up as "no tests" — it's a shared helper package, not a domain
module. Skip it.)

**Pick by value, not by ease.** In rough priority order — check first whether someone has
already covered it:

1. **`invoice`** — tax resolution and rounding. `invoice.CreateInvoice` resolves the rate from
   the branch (`locations.default_tax_rate`) and falls back to `invoice.DefaultTaxRate =
   0.0825`. Both paths, plus rounding, are worth pinning down.
2. **`account`** — `PostTransaction` is the *single writer* to the AR subledger and
   `customers.balance_due`. It should be the best-tested function in the repo.
3. **`customer`** — the credit-limit gate feeds order blocking.
4. **`quote`** — line totals, discounts, expiry.
5. **`portal`** — the dollars-vs-cents boundary with the ERP side (see the money table in
   `CLAUDE.md`); a test here guards a bug class that has already shipped once.
6. **Frontend pages and services** — `app/src/lib/utils.ts` (`formatCents`), the router, and
   `fetchClient` already have suites; the page components and the remaining services under
   `app/src/services/` do not.

## 2 · Read before you write

```bash
M=invoice
head -80 backend/internal/$M/model.go        # types, statuses, constants
grep -n "func (s \*Service)" backend/internal/$M/service.go
sed -n '1,120p' backend/internal/$M/service.go
```

Then study a module that already tests well — copy its idiom rather than inventing one:

```bash
sed -n '1,80p' backend/internal/tax/service_test.go        # mock repo + table-driven
ls backend/internal/purchase_order/*_test.go               # 6 test files, good examples
ls backend/internal/pricing/*_test.go backend/internal/gl/*_test.go
```

## 3 · Test the logic, not the database

**Do not write a test that needs Postgres.** The valuable, fast, reviewable tests are pure
logic — tax computation, rounding, state transitions, credit gates, formatting.

The established pattern here: define a mock that satisfies the repository interface the
service depends on, construct the service with it, and table-test the behaviour. That's
exactly what `backend/internal/tax/service_test.go` does.

If the service takes a concrete `*Repository` rather than an interface, you have two honest
options:

- Test the pure functions and methods that don't need the repo at all, or
- Note in the PR that testing further requires extracting a narrow interface, and let a
  maintainer decide. **Do not refactor production code to make a test easier without saying
  so prominently** — that turns a "add a test" PR into a design change.

### Go test skeleton

<!-- REUSE-IgnoreStart -->

```go
// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice

import (
	"testing"
)

func TestCalculateTax(t *testing.T) {
	tests := []struct {
		name      string
		subtotal  int64   // cents
		rate      float64
		want      int64   // cents
		wantErr   bool
	}{
		{name: "BC 12% on $100.00", subtotal: 10000, rate: 0.12, want: 1200},
		{name: "default 8.25% fallback", subtotal: 10000, rate: DefaultTaxRate, want: 825},
		{name: "rounds half up at the half cent", subtotal: 333, rate: 0.05, want: 17},
		{name: "zero subtotal is zero tax", subtotal: 0, rate: 0.12, want: 0},
		{name: "negative rate is rejected", subtotal: 10000, rate: -0.1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calculateTax(tt.subtotal, tt.rate)
			if (err != nil) != tt.wantErr {
				t.Fatalf("calculateTax() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("calculateTax(%d, %v) = %d, want %d", tt.subtotal, tt.rate, got, tt.want)
			}
		})
	}
}
```

<!-- REUSE-IgnoreEnd -->

Adjust the function name and signature to what actually exists — **read the source, don't
copy this blindly.** If `calculateTax` isn't the real name, the test won't compile and you'll
have wasted the reviewer's time.

### Vitest skeleton

Config is already in place: `app/vitest.config.ts` (jsdom, globals on), setup at
`app/src/test/setup.ts`. Put the file next to what it tests, e.g.
`app/src/services/QuoteService.test.ts`. Read an existing suite for the house idiom:

```bash
ls app/src/lib/*.test.ts app/src/services/*.test.ts app/src/*.test.ts 2>/dev/null
sed -n '1,60p' app/src/lib/utils.test.ts
```

<!-- REUSE-IgnoreStart -->

```ts
// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest'
import { formatCents } from './utils'

describe('formatCents', () => {
  it('renders an ERP cents field as dollars', () => {
    expect(formatCents(73887)).toBe('$738.87')
  })

  it('always shows two decimals', () => {
    expect(formatCents(100)).toBe('$1.00')
    expect(formatCents(0)).toBe('$0.00')
  })

  it('treats null, undefined and non-finite input as zero', () => {
    expect(formatCents(null)).toBe('$0.00')
    expect(formatCents(undefined)).toBe('$0.00')
    expect(formatCents(NaN)).toBe('$0.00')
  })
})
```

<!-- REUSE-IgnoreEnd -->

> `formatCents` uses `toLocaleString` with the ambient locale, so any assertion involving
> thousands separators is locale-sensitive. Either pin the locale in the test or avoid
> grouping in the assertion — and say in the PR which you did.

## 4 · Run it

```bash
make vet && make build
make test-short                    # no Postgres needed
cd backend && go test -race ./internal/invoice/...

make fe-test
make fe-typecheck
```

Prove the test can fail. Temporarily break the code under test, confirm the test goes red,
then put it back. A test that passes against broken code is not a test.

## 5 · House rules

**No tautological tests.** A test must be able to fail for a real reason.

```go
// Worthless — asserts arithmetic, not the system.
if got := 2 + 2; got != 4 { t.Error("math is broken") }

// Worthless — restates the implementation.
want := subtotal * rate
if got := calculateTax(subtotal, rate); got != want { ... }
```

Assert against a value you worked out **independently** — from the spec, from a real invoice,
from a hand calculation.

**Label characterization tests.** If you're pinning down what the code *currently* does
because nobody knows what it *should* do, say so in the test name and a comment:

```go
// Characterization test: documents current behaviour, NOT a specification.
// Rounding here is half-away-from-zero; whether that's correct for tax is unconfirmed.
// See issue #NNN.
func TestCalculateTax_CharacterizesRounding(t *testing.T) { ... }
```

That's an honest, useful contribution. Silently freezing a bug as "expected" is not.

**If you find a bug, report it — don't paper over it.** If reality disagrees with what the
code should obviously do, do **not** write `want: theWrongValue` and move on. Stop, and use
the **`report-an-issue`** skill. Then either:

- Submit the failing test marked `t.Skip("fails — see #NNN")` with the issue linked, or
- Fix the bug and the test in one PR, saying clearly that you changed behaviour.

Both are fine. Quietly encoding the bug as expected is not.

**Don't test the mock.** If your assertions only exercise your own fake, delete the test.

**Don't add flaky tests.** No wall-clock sleeps, no dependence on ordering of a Go map, no
network calls, no reliance on today's date. If you need time, inject it.

**Keep the diff small.** One module per PR. A test PR that also refactors is a refactor PR.

## 6 · Ship it

```bash
git fetch origin
git switch -c test/invoice-tax-rounding origin/staging
git add backend/internal/invoice/service_test.go
git commit -m "test(invoice): table-driven coverage for tax rate resolution and rounding"
```

> If `git switch` fails with `invalid reference: origin/staging`, this clone doesn't have
> `staging` yet — branch from `origin/main` and still open the PR **against `staging`**.

Then run **`check-my-contribution`** (it verifies the SPDX header matches the directory —
`backend/internal/` → `LicenseRef-OpenLBM-Commons-1.0`, `app/` →
`LicenseRef-OpenLBM-Surface-1.0`) and open the PR against `staging`.

In the PR description, state plainly:

- Which module and which behaviour is now covered.
- Anything you found that looks wrong (with the issue link).
- Anything you **couldn't** test and why.

---

## Ground rules

- Tests must be able to fail. Prove it by breaking the code once.
- No Postgres in unit tests.
- Characterization tests are labelled as such, in the name and a comment.
- A discovered bug gets an issue, never a `want:` that encodes it.
- Correct SPDX header for the directory.
- One module per PR; PR targets `staging`.
