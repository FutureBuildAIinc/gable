-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 085: Link products to the category tree.
--
-- Migration 049 created product_categories, added products.category_id, and
-- backfilled it — but only for the rows that existed WHEN 049 RAN. Every
-- product seeded afterwards has category_id IS NULL, which is the whole
-- catalog in a freshly seeded database: all 71 demo products, every one of
-- them unlinked.
--
-- The visible consequence is that the browsable category tree the portal now
-- exposes reports product_count = 0 on every node, and a subtree filter
-- returns nothing. The tree is real, the products are real, and nothing joins
-- them.
--
-- This re-applies 049's rule rather than inventing a new one:
--   1. case-insensitive match of the flat products.category display string
--      against product_categories.name
--   2. anything still unlinked goes to 'general', exactly as 049's fallback did
--
-- Note what this deliberately does NOT do. Three of the demo catalog's flat
-- categories — 'Cornice', 'Millwork' and 'Sheet Goods' — have no node in the
-- tree 049 seeded, so they land in General. A migration must not invent a
-- dealer's taxonomy: creating a 'Sheet Goods' node here would be this file
-- deciding how a lumberyard organises its yard. The fix for those three is a
-- dealer creating the nodes through POST /api/v1/pricing/categories and
-- re-pointing the products, which is a merchandising decision with a UI for
-- it.
--
-- Idempotent: the WHERE clause only touches rows that are still NULL, so a
-- product a dealer has since re-categorised by hand is never moved back.
--
-- Rollback: migrations/down/085_link_products_to_category_tree_down.sql.

UPDATE products p
SET category_id = pc.id
FROM product_categories pc
WHERE p.category_id IS NULL
  AND p.category IS NOT NULL
  AND LOWER(TRIM(p.category)) = LOWER(pc.name)
  AND pc.is_active = true;

UPDATE products
SET category_id = (SELECT id FROM product_categories WHERE slug = 'general')
WHERE category_id IS NULL;
