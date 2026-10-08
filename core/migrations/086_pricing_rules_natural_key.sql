-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 086: Give pricing_rules a natural key, and collapse the duplicates that its
-- absence has already produced.
--
-- cmd/seed/main.go's pricing-rules block writes its six demo rules with
-- `ON CONFLICT DO NOTHING`. pricing_rules has never had a unique constraint, so
-- there is nothing for a conflict to be ON: every row inserts, every time. The
-- clause reads like an upsert and behaves like a plain INSERT, which is the
-- worst of both — re-running the seeder against a working database silently
-- multiplies the rule set. A database seeded N times carries N copies of
-- "Lumber Qty Break 100+".
--
-- Duplicates are not cosmetic here. GetMatchingRules returns every matching
-- row and the pricing waterfall picks a winner among them; ListBreakQuantities
-- has to say DISTINCT to keep a contractor from seeing the same rung fifty
-- times (see its comment in internal/pricing/repository.go, which points at
-- this). Every consumer is paying to work around a missing constraint.
--
-- THE KEY
--
--   (name, rule_type, product_id, customer_id, job_id, category, min_quantity)
--
-- Scope plus rung. The five scope columns are what decide WHICH lines a rule
-- reaches; name is how a human refers to it; min_quantity is which rung of a
-- ladder it is. Two rows agreeing on all seven are the same rule written
-- twice, and there is no legitimate reason to hold both.
--
-- min_quantity is in the key deliberately. A dealer who names both rungs of a
-- ladder "Volume Discount" and distinguishes them only by threshold has made
-- two genuinely different rules; excluding min_quantity would reject the second
-- one. Including it costs nothing against the seeder, whose duplicate rows are
-- byte-identical.
--
-- NULLS NOT DISTINCT is the point of the whole constraint. Four of the seven
-- key columns are nullable and the seeder leaves three of them NULL on every
-- row, so under Postgres' default (NULLS DISTINCT) every row would still be
-- unique to the index and nothing would be prevented. Requires PostgreSQL 15+;
-- this project runs 16 (docker-compose.yml and .github/workflows both pin
-- postgres:16-alpine).
--
-- What the key deliberately does NOT do is normalise case or whitespace on
-- name/category. Rules called 'Roofing' and 'roofing' remain two rows. Folding
-- them would need an expression index, would make every ON CONFLICT clause in
-- the codebase restate the expressions exactly, and would silently merge two
-- rules a dealer typed differently on purpose. Case-variant categories are a
-- merchandising data-quality question, not the duplicate-accumulation bug this
-- migration exists to close — and note the scope predicate in
-- internal/pricing/repository.go already matches categories case-insensitively,
-- so the two spellings price identically either way.
--
-- COPING WITH THE DUPLICATES ALREADY THERE
--
-- The constraint cannot be added to a table that violates it, and any working
-- database seeded more than once does. Step 1 collapses each group to its
-- OLDEST row: created_at ascending, id ascending to break a same-timestamp tie
-- deterministically. Oldest rather than newest because the surviving id is the
-- one that has been in the system longest and is the likeliest to appear in an
-- operator's notes or a screenshot. Nothing references pricing_rules(id) — no
-- foreign key in any migration points at it — so the delete cannot orphan a
-- row. PARTITION BY groups NULLs together, which is exactly the NULLS NOT
-- DISTINCT semantics the constraint then enforces.
--
-- This migration does NOT touch discount_pct or margin_floor_pct values. The
-- seeder was writing 0.10 for "10% off" into a column the pricing engine reads
-- as a percent, and that is fixed at the writer (cmd/seed/main.go). Repairing
-- the values here would mean guessing which 0.10 in a dealer's table is a
-- mis-scaled 10% and which is a deliberate tenth of a percent, and a migration
-- must not guess at money. The demo rows repair themselves on the next
-- `go run ./cmd/seed`, because this constraint is what finally makes that
-- block's upsert real.
--
-- Idempotent: guarded on pg_constraint, so re-running is a no-op.
-- Rollback: migrations/down/086_pricing_rules_natural_key_down.sql.

-- 1. Collapse existing duplicates, keeping the oldest row of each group.
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY name, rule_type, product_id, customer_id, job_id, category, min_quantity
               ORDER BY created_at ASC, id ASC
           ) AS rn
    FROM pricing_rules
)
DELETE FROM pricing_rules
WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

-- 2. Add the natural key.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'pricing_rules_scope_key'
          AND conrelid = 'pricing_rules'::regclass
    ) THEN
        ALTER TABLE pricing_rules
            ADD CONSTRAINT pricing_rules_scope_key
            UNIQUE NULLS NOT DISTINCT
            (name, rule_type, product_id, customer_id, job_id, category, min_quantity);
    END IF;
END$$;
