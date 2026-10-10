// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The customer branch wall on the routes whose request writes a named
// customer's data (ADR 0007 section 5.5, the lead's ruling on the sweep): the
// tax exemption writes and the customer priced rules, plain and category,
// single and bulk. A branch bound key reaches them only while the customer
// its request names, or that the row it acts on belongs to, holds the pin
// among that customer's branches; the reads keep the role guard alone.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// exemptionCustomerOf resolves the customer a tax exemption row belongs to.
// A row that does not exist names no customer (the handler's own 404
// answers).
func exemptionCustomerOf(db *database.DB) func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	return func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("tax exemption customer lookup: no database")
		}
		var customer *uuid.UUID
		err := db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT customer_id FROM tax_exemptions WHERE id = $1`, id).Scan(&customer)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return customer, nil
	}
}

// categoryRuleCustomerOf resolves the customer a category pricing rule row is
// scoped to. A row that does not exist, or that targets a tier or an account
// rather than a customer, names no customer.
func categoryRuleCustomerOf(db *database.DB) func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	return func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("category rule customer lookup: no database")
		}
		var customer *uuid.UUID
		err := db.GetExecutor(ctx).QueryRow(ctx,
			`SELECT customer_id FROM category_pricing_rules WHERE id = $1`, id).Scan(&customer)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return customer, nil
	}
}

// bulkRuleCustomersOf resolves the customers a bulk delete's rule ids belong
// to. A body that is not exactly one complete JSON value is refused for a
// bound key (ErrBodyRefused), and an id that does not parse is left to the
// handler's own 400.
func bulkRuleCustomersOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := middleware.DecodeOneJSONBody(r, &body); err != nil {
			return nil, err
		}
		ids := make([]uuid.UUID, 0, len(body.IDs))
		for _, raw := range body.IDs {
			if id, err := uuid.Parse(raw); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return nil, nil
		}
		rows, err := db.GetExecutor(ctx).Query(ctx,
			`SELECT DISTINCT customer_id FROM category_pricing_rules WHERE id = ANY($1) AND customer_id IS NOT NULL`, ids)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			out = append(out, id)
		}
		return out, rows.Err()
	}
}

// taxCustomerWall composes the tax role guard with the customer branch wall
// on the exemption writes: the create's body names the customer, the delete's
// row belongs to one. The preview and the exemption read keep the role guard
// alone.
func taxCustomerWall(keyWall *middleware.KeyBranchWall, db *database.DB, roles ...string) func(http.Handler) http.Handler {
	role := middleware.RequireRole(roles...)
	create := middleware.Compose(role, keyWall.CustomerBodyBranch())
	remove := middleware.Compose(role, keyWall.CustomerRowBranch(exemptionCustomerOf(db)))
	return func(next http.Handler) http.Handler {
		createHandler, removeHandler, roleHandler := create(next), remove(next), role(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/tax/exemptions":
				createHandler.ServeHTTP(w, r)
			case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/tax/exemptions/"):
				removeHandler.ServeHTTP(w, r)
			default:
				roleHandler.ServeHTTP(w, r)
			}
		})
	}
}

// pricingRulesCustomerWall composes the pricing role guard with the customer
// branch wall on the rule create (its body names the customer). The reads and
// the calculation keep the role guard alone; a rule that names no customer is
// dealer wide and passes.
func pricingRulesCustomerWall(keyWall *middleware.KeyBranchWall, roles ...string) func(http.Handler) http.Handler {
	role := middleware.RequireRole(roles...)
	create := middleware.Compose(role, keyWall.CustomerBodyBranch())
	return func(next http.Handler) http.Handler {
		createHandler, roleHandler := create(next), role(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/pricing/rules" {
				createHandler.ServeHTTP(w, r)
				return
			}
			roleHandler.ServeHTTP(w, r)
		})
	}
}

// categoryRulesCustomerWall composes the category pricing role guard with the
// customer branch wall on the rule writes: the create's body names the
// customer, the update and the delete act on a row that belongs to one, the
// bulk upsert names one per element, and the bulk delete's ids belong to
// theirs. The reads keep the role guard alone.
func categoryRulesCustomerWall(keyWall *middleware.KeyBranchWall, db *database.DB, roles ...string) func(http.Handler) http.Handler {
	role := middleware.RequireRole(roles...)
	wall := middleware.Compose(role, keyWall.CustomerBodyBranch())
	byRow := middleware.Compose(role, keyWall.CustomerRowBranch(categoryRuleCustomerOf(db)))
	byBulkIDs := middleware.Compose(role, keyWall.CustomerBranch(bulkRuleCustomersOf(db)))
	return func(next http.Handler) http.Handler {
		wallHandler, rowHandler, bulkHandler, roleHandler := wall(next), byRow(next), byBulkIDs(next), role(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pricing/category-rules/bulk":
				wallHandler.ServeHTTP(w, r) // each element names its customer
			case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/pricing/category-rules/bulk":
				bulkHandler.ServeHTTP(w, r) // the rows the ids name belong to theirs
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pricing/category-rules":
				wallHandler.ServeHTTP(w, r)
			case (r.Method == http.MethodPut || r.Method == http.MethodDelete) &&
				strings.HasPrefix(r.URL.Path, "/api/v1/pricing/category-rules/"):
				rowHandler.ServeHTTP(w, r)
			default:
				roleHandler.ServeHTTP(w, r)
			}
		})
	}
}
