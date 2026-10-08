// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// The browsable category tree is product_categories (migration 049) —
// PostgreSQL ltree, already seeded, already the substrate the category pricing
// cascade resolves against. Nothing new is invented here; the portal was
// simply never given a way to read it, so `category` (a flat display string on
// products) was the only thing browse could group by.
//
// The tree is built in Go from a single flat SELECT rather than with a
// recursive CTE. The category set is small (a dozen rows in the seed, tens in
// a real dealer) and one query with an in-memory assembly is cheaper and much
// easier to read than a recursive query whose ordering has to be re-derived.

// ListCategoryTree returns the active category hierarchy with per-node product
// counts.
//
// ProductCount is the SUBTREE count, not the count of products linked directly
// to that node: a contractor clicking "Lumber" expects the number of things
// they will see, and framing lumber is under lumber. The count is computed by
// rolling child totals up after the tree is assembled, so it stays consistent
// with what the ?category_id= filter actually returns.
func (s *Service) ListCategoryTree(ctx context.Context) ([]CategoryNodeDTO, error) {
	rows, err := s.repo.ListProductCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load categories: %w", err)
	}
	return buildCategoryTree(rows), nil
}

// categoryRow is a flat product_categories row plus its direct product count.
type categoryRow struct {
	ID          uuid.UUID
	ParentID    *uuid.UUID
	Name        string
	Slug        string
	Path        string
	SortOrder   int
	DirectCount int
}

// ListProductCategories reads the active category rows with the number of
// products linked directly to each.
//
// LEFT JOIN + COUNT on the product id (not *) so an empty category counts 0
// rather than 1. is_active is honoured: a category a dealer has retired must
// not appear in a customer's browse tree.
func (r *PostgresRepository) ListProductCategories(ctx context.Context) ([]categoryRow, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT pc.id, pc.parent_id, pc.name, pc.slug, pc.path::text, pc.sort_order,
		       COUNT(p.id)
		FROM product_categories pc
		LEFT JOIN products p ON p.category_id = pc.id
		WHERE pc.is_active = true
		GROUP BY pc.id, pc.parent_id, pc.name, pc.slug, pc.path, pc.sort_order
		ORDER BY pc.path ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list product categories: %w", err)
	}
	defer rows.Close()

	out := make([]categoryRow, 0)
	for rows.Next() {
		var c categoryRow
		if err := rows.Scan(&c.ID, &c.ParentID, &c.Name, &c.Slug, &c.Path, &c.SortOrder, &c.DirectCount); err != nil {
			return nil, fmt.Errorf("failed to scan category: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("category rows error: %w", err)
	}
	return out, nil
}

// buildCategoryTree assembles the flat rows into a forest.
//
// A row whose parent_id names a category that is not in the input — an
// inactive parent, or a dangling reference — is promoted to a root rather than
// dropped. Losing a whole branch because one node was retired would hide
// products from browse with no signal that anything was missing.
func buildCategoryTree(rows []categoryRow) []CategoryNodeDTO {
	byID := make(map[uuid.UUID]*categoryRow, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}

	children := make(map[uuid.UUID][]*categoryRow)
	roots := make([]*categoryRow, 0)
	for i := range rows {
		row := &rows[i]
		if row.ParentID != nil {
			if _, ok := byID[*row.ParentID]; ok {
				children[*row.ParentID] = append(children[*row.ParentID], row)
				continue
			}
		}
		roots = append(roots, row)
	}

	sortRows := func(rs []*categoryRow) {
		sort.SliceStable(rs, func(i, j int) bool {
			if rs[i].SortOrder != rs[j].SortOrder {
				return rs[i].SortOrder < rs[j].SortOrder
			}
			return rs[i].Name < rs[j].Name
		})
	}
	sortRows(roots)
	for k := range children {
		sortRows(children[k])
	}

	// visited guards against a parent cycle in the data. Without it a
	// self-referencing or mutually-referencing pair would recurse until the
	// stack died — a data problem should not be able to take the API down.
	visited := make(map[uuid.UUID]bool, len(rows))

	var build func(row *categoryRow, depth int) CategoryNodeDTO
	build = func(row *categoryRow, depth int) CategoryNodeDTO {
		node := CategoryNodeDTO{
			ID:           row.ID,
			Name:         row.Name,
			Slug:         row.Slug,
			Path:         row.Path,
			Depth:        depth,
			SortOrder:    row.SortOrder,
			ProductCount: row.DirectCount,
			Children:     make([]CategoryNodeDTO, 0),
		}
		if visited[row.ID] {
			return node
		}
		visited[row.ID] = true

		for _, child := range children[row.ID] {
			c := build(child, depth+1)
			node.ProductCount += c.ProductCount
			node.Children = append(node.Children, c)
		}
		return node
	}

	tree := make([]CategoryNodeDTO, 0, len(roots))
	for _, root := range roots {
		tree = append(tree, build(root, 0))
	}

	// Anything still unvisited is in a parent CYCLE — every member of the
	// cycle has a parent that exists, so none of them was picked up as a root
	// and none of them was reachable from one. Promoting them is the same
	// choice as promoting an orphan: a data problem must not make a dealer's
	// products silently unbrowsable. Rows are already sorted by path from the
	// query, so the promotion order is deterministic.
	for i := range rows {
		if !visited[rows[i].ID] {
			tree = append(tree, build(&rows[i], 0))
		}
	}

	return tree
}

// categoryPathForID resolves a category id to its ltree path, which is what
// the catalog's subtree filter compares against. Returns "" when the id is
// unknown, so a bad filter yields an empty catalog rather than an error page.
func (r *PostgresRepository) categoryPathForID(ctx context.Context, id uuid.UUID) (string, error) {
	var path string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT path::text FROM product_categories WHERE id = $1`, id).Scan(&path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}
