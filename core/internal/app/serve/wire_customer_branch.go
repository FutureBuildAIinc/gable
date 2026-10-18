// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The customer branch wall on the routes whose request reads or writes a
// named customer's data (ADR 0007 section 5.5): the tax exemption routes and
// preview, the customer priced rules (plain and category, single and bulk)
// and the customer scoped reads that name a customer in a path, a query or a
// body. A branch bound key reaches them only while every customer the
// request names, or that the rows it acts on belong to, holds the pin among
// that customer's branches. A request that names no customer passes, except
// where the write would then be dealer wide: a pricing rule that names
// neither a customer nor a job, and a category rule (or a bulk element) whose
// target is not an account rule or that carries a tier, price every branch's
// customers, so the resolver refuses them (ErrBodyRefused). The routes no
// customer can confine (the unfiltered lists, the matrix) are REFUSED by the
// route allowlist gate before these walls run.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/pkg/branchctx"
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

// exemptionCustomerPathOf resolves the customer the exemption read's path
// names (the {customerID} value): the customer named is the customer read,
// whoever's rows they are, so it is confined exactly like a write that names
// one.
func exemptionCustomerPathOf() func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		raw := r.PathValue("customerID")
		if raw == "" {
			return nil, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, nil // the handler answers the malformed id
		}
		return []uuid.UUID{id}, nil
	}
}

// accountCustomerPathOf resolves the customer the account routes name in
// their path ({id} is the customer): the summary, the subledger and the
// statement read that customer's data, so they are confined exactly like a
// write that names one.
func accountCustomerPathOf() func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		raw := r.PathValue("id")
		if raw == "" {
			return nil, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, nil // the handler answers the malformed id
		}
		return []uuid.UUID{id}, nil
	}
}

// previewCustomerOf resolves the customers the tax preview's body names: its
// customer_id, and the customer of the ship to its ship_to_id names (round 6
// P3: a preview quoting the foreign branch's ship to prices that customer's
// delivery). A preview that names no customer and no ship to is a plain rate
// calculation and passes; a ship to that does not exist names nothing and the
// handler ignores the field.
func previewCustomerOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		var body struct {
			CustomerID string `json:"customer_id"`
			ShipToID   string `json:"ship_to_id"`
		}
		if err := middleware.DecodeOneJSONBody(r, &body); err != nil {
			return nil, err
		}
		var out []uuid.UUID
		if id, err := uuid.Parse(body.CustomerID); err == nil {
			out = append(out, id)
		}
		if raw := body.ShipToID; raw != "" {
			if shipTo, err := uuid.Parse(raw); err == nil {
				if db == nil {
					return nil, errors.New("tax preview ship to lookup: no database")
				}
				var customer uuid.UUID
				err := db.GetExecutor(ctx).QueryRow(ctx,
					`SELECT customer_id FROM customer_ship_tos WHERE id = $1`, shipTo).Scan(&customer)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
				if err == nil {
					out = append(out, customer)
				}
			}
		}
		return out, nil
	}
}

// posSyncCustomersOf resolves the customers an offline sync's body names: one
// customer per replayed sale (items[].customer_id). A sale that names no
// customer (a walk in, paid in cash) names nothing the wall can hold and
// passes; the register's branch still holds the rows the replay writes.
func posSyncCustomersOf() func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		var body struct {
			Items []struct {
				CustomerID string `json:"customer_id"`
			} `json:"items"`
		}
		if err := middleware.DecodeOneJSONBody(r, &body); err != nil {
			return nil, err
		}
		var out []uuid.UUID
		for _, item := range body.Items {
			if id, err := uuid.Parse(item.CustomerID); err == nil {
				out = append(out, id)
			}
		}
		return out, nil
	}
}

// documentCustomersOf resolves the customers a document write names: its body
// customer_id, and the customer of the job its job_id names (a projects row
// belongs to exactly one customer, and a document on the job is priced and
// posted through that customer, the same resolution the pricing rule write
// and the calculate read carry). A job that does not exist names nothing the
// wall can hold and the handler's own answer covers it.
func documentCustomersOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("document customer lookup: no database")
		}
		var body struct {
			CustomerID string `json:"customer_id"`
			JobID      string `json:"job_id"`
		}
		if err := middleware.DecodeOneJSONBody(r, &body); err != nil {
			return nil, err
		}
		var out []uuid.UUID
		if id, err := uuid.Parse(body.CustomerID); err == nil {
			out = append(out, id)
		}
		if raw := body.JobID; raw != "" {
			if job, err := uuid.Parse(raw); err == nil {
				var customer *uuid.UUID
				err := db.GetExecutor(ctx).QueryRow(ctx,
					`SELECT customer_id FROM projects WHERE id = $1`, job).Scan(&customer)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
				if customer != nil {
					out = append(out, *customer)
				}
			}
		}
		return out, nil
	}
}

// queryCustomerOf resolves the customer a query parameter names. A value that
// does not parse names nothing the wall can hold and passes to the handler's
// own validation; an absent value passes unless required says otherwise (the
// category rule list with no customer is the dealer wide list).
func queryCustomerOf(name string, required bool) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		vals := r.URL.Query()[name]
		if len(vals) == 0 {
			if required {
				return nil, middleware.ErrBodyRefused
			}
			return nil, nil
		}
		if id, err := uuid.Parse(vals[0]); err == nil {
			return []uuid.UUID{id}, nil
		}
		return nil, nil
	}
}

// pricingRuleCustomersOf resolves the customers a pricing rule create names:
// its body customer_id, and the customer of the job its job_id names (a
// projects row belongs to exactly one customer, and a rule on the job prices
// that customer whoever else it names, so both are checked). A body that
// names neither is a dealer wide write and is refused (ErrBodyRefused).
func pricingRuleCustomersOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("pricing rule customer lookup: no database")
		}
		var body struct {
			CustomerID string `json:"customer_id"`
			JobID      string `json:"job_id"`
		}
		if err := middleware.DecodeOneJSONBody(r, &body); err != nil {
			return nil, err
		}
		var out []uuid.UUID
		if id, err := uuid.Parse(body.CustomerID); err == nil {
			out = append(out, id)
		}
		if raw := body.JobID; raw != "" {
			if job, err := uuid.Parse(raw); err == nil {
				var customer *uuid.UUID
				err := db.GetExecutor(r.Context()).QueryRow(r.Context(),
					`SELECT customer_id FROM projects WHERE id = $1`, job).Scan(&customer)
				if errors.Is(err, pgx.ErrNoRows) {
					// A job that does not exist names nothing; the handler's
					// own foreign key answer covers it.
					if len(out) == 0 {
						return nil, middleware.ErrBodyRefused
					}
					return out, nil
				}
				if err != nil {
					return nil, err
				}
				if customer != nil {
					out = append(out, *customer)
				}
			}
		}
		if len(out) == 0 {
			return nil, middleware.ErrBodyRefused
		}
		return out, nil
	}
}

// quoteRowCustomerOf resolves the customer of the quote the path names, so
// the conversion is held to the quote's own customer, rechecked where the
// quote becomes an order (round 11 P2-1: a quote a caller moved onto another
// branch's customer must not convert at the pin's branch). A quote that does
// not exist names no customer and the handler's own 404 answers.
func quoteRowCustomerOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("quote customer lookup: no database")
		}
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			return nil, nil // the handler answers the malformed id
		}
		var customer *uuid.UUID
		err = db.GetExecutor(ctx).QueryRow(ctx, `SELECT customer_id FROM quotes WHERE id = $1`, id).Scan(&customer)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if customer == nil {
			return nil, nil
		}
		return []uuid.UUID{*customer}, nil
	}
}

// draftPayloadCustomerOf resolves the customer the stored payload of the draft
// the path names carries. A draft that does not exist, or whose payload names
// no customer, names nothing the wall can hold and the promotion's own answer
// covers it.
func draftPayloadCustomerOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("draft customer lookup: no database")
		}
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			return nil, nil // the handler answers the malformed id
		}
		var raw *string
		err = db.GetExecutor(ctx).QueryRow(ctx, `SELECT payload->>'customer_id' FROM drafts WHERE id = $1`, id).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if raw == nil {
			return nil, nil
		}
		customer, err := uuid.Parse(*raw)
		if err != nil {
			return nil, nil
		}
		return []uuid.UUID{customer}, nil
	}
}

// categoryRuleRowCustomerOf resolves the customer a category rule row is
// scoped to. A row that does not exist names nothing (the handler's own 404
// answers); a row that is not an account rule is dealer wide and is refused,
// whether or not it also names a customer (the legacy tier row migration 107
// leaves behind prices every branch's customers of its tier), as is a row
// that names no customer.
func categoryRuleRowCustomerOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("category rule customer lookup: no database")
		}
		raw := r.PathValue("id")
		if raw == "" {
			return nil, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, nil // the handler answers the malformed id
		}
		var (
			customer   *uuid.UUID
			targetType string
		)
		err = db.GetExecutor(r.Context()).QueryRow(r.Context(),
			`SELECT customer_id, target_type FROM category_pricing_rules WHERE id = $1`, id).Scan(&customer, &targetType)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if customer == nil || targetType != "ACCOUNT" {
			return nil, middleware.ErrBodyRefused
		}
		return []uuid.UUID{*customer}, nil
	}
}

// bulkRuleCustomersOf resolves the customers a bulk delete's rule ids belong
// to. An id that does not parse is left to the handler's own 400; an id that
// names a row that is not an account rule, or that names no customer, is a
// dealer wide delete and refuses the whole request.
func bulkRuleCustomersOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("category rule customer lookup: no database")
		}
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
		var dealerWide int
		if err := db.GetExecutor(r.Context()).QueryRow(r.Context(),
			`SELECT COUNT(*) FROM category_pricing_rules WHERE id = ANY($1) AND (customer_id IS NULL OR target_type <> 'ACCOUNT')`, ids).
			Scan(&dealerWide); err != nil {
			return nil, err
		}
		if dealerWide > 0 {
			return nil, middleware.ErrBodyRefused
		}
		rows, err := db.GetExecutor(r.Context()).Query(r.Context(),
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

// categoryRuleBodyCustomers resolves the customers a category rule create (or
// a bulk element) names, holding the request to the same target the handler
// parses: every element must be an account rule (target_type "account") that
// names no profile or level. A profile or level rule prices the customers of
// that program across every branch, so such an element is refused however
// many branches of the customer it names hold the pin; a target that is not
// an account rule names no branch the wall can hold and is refused with it.
// Both refusals are ErrBodyRefused, so the bound key never reaches the
// handler on them.
func categoryRuleBodyCustomers(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("key branch wall: read body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	element := func(e struct {
		TargetType     string `json:"target_type"`
		CustomerID     string `json:"customer_id"`
		PriceProfileID string `json:"price_profile_id"`
		PriceLevelID   string `json:"price_level_id"`
	}) ([]uuid.UUID, error) {
		if t, ok := pricing.ParseTargetType(e.TargetType); !ok || t != pricing.TargetTypeAccount {
			return nil, middleware.ErrBodyRefused
		}
		if e.PriceProfileID != "" || e.PriceLevelID != "" {
			return nil, middleware.ErrBodyRefused
		}
		id, err := uuid.Parse(e.CustomerID)
		if err != nil {
			return nil, middleware.ErrBodyRefused
		}
		return []uuid.UUID{id}, nil
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		var many []struct {
			TargetType     string `json:"target_type"`
			CustomerID     string `json:"customer_id"`
			PriceProfileID string `json:"price_profile_id"`
			PriceLevelID   string `json:"price_level_id"`
		}
		if err := json.Unmarshal(raw, &many); err != nil {
			return nil, middleware.ErrBodyRefused
		}
		out := make([]uuid.UUID, 0, len(many))
		for _, m := range many {
			ids, err := element(m)
			if err != nil {
				return nil, err
			}
			out = append(out, ids...)
		}
		return out, nil
	}
	var one struct {
		TargetType     string `json:"target_type"`
		CustomerID     string `json:"customer_id"`
		PriceProfileID string `json:"price_profile_id"`
		PriceLevelID   string `json:"price_level_id"`
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, middleware.ErrBodyRefused
	}
	return element(one)
}

// exemptionBodyCustomers resolves the customer the exemption create's body
// names (the exclusive write resolver; the read wall shares it through its
// own body arm).
func exemptionBodyCustomers() func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		var body struct {
			CustomerID string `json:"customer_id"`
		}
		if err := middleware.DecodeOneJSONBody(r, &body); err != nil {
			return nil, err
		}
		if id, err := uuid.Parse(body.CustomerID); err == nil {
			return []uuid.UUID{id}, nil
		}
		return nil, nil
	}
}

// exemptionRowCustomers resolves the customer the exemption delete's row
// belongs to (the exclusive write resolver).
func exemptionRowCustomers(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	customerOf := exemptionCustomerOf(db)
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		raw := r.PathValue("id")
		if raw == "" {
			return nil, nil
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, nil // the handler answers the malformed id
		}
		customer, err := customerOf(ctx, id)
		if err != nil {
			return nil, err
		}
		if customer == nil {
			return nil, nil
		}
		return []uuid.UUID{*customer}, nil
	}
}

// userCustomerWall holds the customers a request names to the user's reach
// (SEC-NOHEADER-1, ADR 0007 section 5.7), the user side of the key wall
// above: with a context branch the customer must hold that branch among its
// customer_branches rows, with none and a user the customer must hold one of
// the user's granted branches, and with neither (an administrator or owner
// with no header, the single branch switch, the dev caller) every customer
// passes, exactly the predicate /customers/{id} reads under. A customer the
// wall hides answers like a missing one: 404 when the path or the row names
// it, 403 naming customer_id when the body does. A bound key keeps its own
// wall and is not held here. A body that is not exactly one complete JSON
// value (unparsable, truncated, or a trailing value after the object) is
// refused here with 400 and never handed on, since the handler would read only
// its first value (r8 P1-1).
func userCustomerWall(db *database.DB, resolve func(ctx context.Context, r *http.Request) ([]uuid.UUID, error), body bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if _, bound := branchctx.KeyBranchFromContext(ctx); bound {
				next.ServeHTTP(w, r)
				return
			}
			branch, grants := middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)
			if branch == nil && grants == nil {
				next.ServeHTTP(w, r)
				return
			}
			customers, err := resolve(ctx, r)
			if errors.Is(err, middleware.ErrBodyRefused) {
				// A body that is not exactly one JSON value is refused here,
				// never handed on: the handler reads only the first value, so
				// passing it would act on a customer this wall never saw
				// (r8 P1-1).
				httpx.WriteError(w, r, httpx.BadRequest("the request body must be exactly one JSON value"))
				return
			}
			if err != nil {
				httpx.WriteError(w, r, fmt.Errorf("customer branch lookup: %w", err))
				return
			}
			for _, id := range customers {
				var held bool
				if err := db.GetExecutor(ctx).QueryRow(ctx,
					`SELECT EXISTS (SELECT 1 FROM customers c WHERE c.id = $1 AND `+customer.Wall("c", 2, 3)+`)`,
					id, branch, grants).Scan(&held); err != nil {
					httpx.WriteError(w, r, fmt.Errorf("customer branch lookup: %w", err))
					return
				}
				if held {
					continue
				}
				if body {
					httpx.WriteError(w, r, &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
						Message: "customer_id is outside the branches this caller may reach",
						Details: []httpx.FieldError{{Field: "customer_id", Code: httpx.CodeForbidden, Message: "not a customer this caller may reach"}}})
					return
				}
				httpx.WriteError(w, r, httpx.NotFound("customer not found"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// taxCustomerWall composes the tax role guard with the customer branch wall:
// the exemption create's body names the customer, the delete's row belongs
// to one, the read's path names one, and the preview's body names one when
// it names any. Every route runs behind the branch middleware, so a user is
// held to the customer's branches whether or not a header is sent
// (userCustomerWall) and a bound key to its pin (the key wall).
func taxCustomerWall(wall *branchWall, db *database.DB, roles ...string) func(http.Handler) http.Handler {
	role := middleware.RequireRole(roles...)
	keyWall := wall.keyWall
	create := middleware.Compose(role, wall.mw, userCustomerWall(db, exemptionBodyCustomers(), true), keyWall.CustomerBranchExclusive(exemptionBodyCustomers()))
	remove := middleware.Compose(role, wall.mw, userCustomerWall(db, exemptionRowCustomers(db), false), keyWall.CustomerBranchExclusive(exemptionRowCustomers(db)))
	read := middleware.Compose(role, wall.mw, userCustomerWall(db, exemptionCustomerPathOf(), false), keyWall.CustomerBranch(exemptionCustomerPathOf()))
	preview := middleware.Compose(role, wall.mw, userCustomerWall(db, previewCustomerOf(db), true), keyWall.CustomerBranch(previewCustomerOf(db)))
	return func(next http.Handler) http.Handler {
		createHandler, removeHandler, readHandler, previewHandler, roleHandler :=
			create(next), remove(next), read(next), preview(next), role(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Pattern {
			case "POST /api/v1/tax/exemptions":
				createHandler.ServeHTTP(w, r)
			case "DELETE /api/v1/tax/exemptions/{id}":
				removeHandler.ServeHTTP(w, r)
			case "GET /api/v1/tax/exemptions/{customerID}":
				readHandler.ServeHTTP(w, r)
			case "POST /api/v1/tax/preview":
				previewHandler.ServeHTTP(w, r)
			default:
				roleHandler.ServeHTTP(w, r)
			}
		})
	}
}

// calculateCustomersOf resolves the customers the price calculation names:
// its query customer_id, and the customer of the job its job_id names (a job
// rule prices that job's customer, the same resolution the rule write
// carries, round 5 F-3). A job that does not exist names nothing the wall can
// hold and the handler's own answer covers it; a query that names neither
// passes (a retail calculation with no customer scope).
func calculateCustomersOf(db *database.DB) func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
	return func(ctx context.Context, r *http.Request) ([]uuid.UUID, error) {
		if db == nil {
			return nil, errors.New("price calculation customer lookup: no database")
		}
		q := r.URL.Query()
		var out []uuid.UUID
		if vals := q["customer_id"]; len(vals) > 0 {
			if id, err := uuid.Parse(vals[0]); err == nil {
				out = append(out, id)
			}
		}
		if vals := q["job_id"]; len(vals) > 0 {
			if job, err := uuid.Parse(vals[0]); err == nil {
				var customer *uuid.UUID
				err := db.GetExecutor(ctx).QueryRow(ctx,
					`SELECT customer_id FROM projects WHERE id = $1`, job).Scan(&customer)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
				if customer != nil {
					out = append(out, *customer)
				}
			}
		}
		return out, nil
	}
}

// pricingRulesCustomerWall composes the pricing role guard with the branch
// middleware (P1-1, review r1 argon: without it BranchFromContext is nil on
// every pricing route, so CheckPayloadBranch refuses every branch-checked
// write for human users and BranchIDForQuery never answers on reads), the
// customer branch wall on the rule create (its body names the customer, its
// job names one, and a body naming neither is dealer wide and refused), and
// the counter's roles on the calculation (ADR 0006 section 5.6: calculate is
// open to every role the counter routes admit; the rule create stays
// admin/owner). The unfiltered rule list is REFUSED by the route allowlist
// gate and never reaches these arms.
func pricingRulesCustomerWall(keyWall *middleware.KeyBranchWall, mw func(http.Handler) http.Handler, db *database.DB, roles ...string) func(http.Handler) http.Handler {
	role := middleware.Compose(middleware.RequireRole(roles...), mw)
	counterRole := middleware.Compose(middleware.RequireRole("admin", "owner", "finance", "sales", "cashier"), mw)
	create := middleware.Compose(role, keyWall.CustomerBranchExclusive(pricingRuleCustomersOf(db)))
	calculate := middleware.Compose(counterRole, keyWall.CustomerBranch(calculateCustomersOf(db)))
	return func(next http.Handler) http.Handler {
		createHandler, calculateHandler, roleHandler := create(next), calculate(next), role(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Pattern {
			case "POST /api/v1/pricing/rules":
				createHandler.ServeHTTP(w, r)
			case "GET /api/v1/pricing/calculate":
				calculateHandler.ServeHTTP(w, r)
			default:
				roleHandler.ServeHTTP(w, r)
			}
		})
	}
}

// categoryRulesCustomerWall composes the category pricing role guard with the
// customer branch wall: the list's query names the customer (the unfiltered
// list is refused by the gate, so the parameter is required here), the create
// and the bulk upsert name one per element (a tier target names none and is
// refused), the update, the delete and the audit act on a row that belongs
// to one (the audit read is a read, so a customer shared with another branch
// still passes it, as the list and resolve reads do), the bulk delete's ids
// belong to theirs, and the resolve preview
// names a customer when its query carries one. The matrix is REFUSED by the
// route allowlist gate; the categories routes are stated limits.
// pricingCatalogWall guards the pricing catalogue routes (ADR 0006 section
// 5.6): the writes are admin, owner and finance; the reads add sales; the
// calculate read is open to every role the counter routes admit, since the
// counter prices through it.
func pricingCatalogWall(keyWall *middleware.KeyBranchWall, mw func(http.Handler) http.Handler, db *database.DB) func(http.Handler) http.Handler {
	read := middleware.Compose(middleware.RequireRole("admin", "owner", "finance", "sales"), mw)
	write := middleware.Compose(middleware.RequireRole("admin", "owner", "finance"), mw)
	return func(next http.Handler) http.Handler {
		readHandler, writeHandler := read(next), write(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The reads add sales; every write is finance's. The calculate
			// read's 5-role dispatch lives in pricingRulesCustomerWall, which
			// registers it.
			if r.Method == http.MethodGet {
				readHandler.ServeHTTP(w, r)
				return
			}
			writeHandler.ServeHTTP(w, r)
		})
	}
}

func categoryRulesCustomerWall(keyWall *middleware.KeyBranchWall, db *database.DB, roles ...string) func(http.Handler) http.Handler {
	role := middleware.RequireRole(roles...)
	body := middleware.Compose(role, keyWall.CustomerBranchExclusive(categoryRuleBodyCustomers))
	byRow := middleware.Compose(role, keyWall.CustomerBranchExclusive(categoryRuleRowCustomerOf(db)))
	auditRead := middleware.Compose(role, keyWall.CustomerBranch(categoryRuleRowCustomerOf(db)))
	byBulkIDs := middleware.Compose(role, keyWall.CustomerBranchExclusive(bulkRuleCustomersOf(db)))
	list := middleware.Compose(role, keyWall.CustomerBranch(queryCustomerOf("customer_id", true)))
	resolve := middleware.Compose(role, keyWall.CustomerBranch(queryCustomerOf("customer_id", false)))
	return func(next http.Handler) http.Handler {
		bodyHandler, rowHandler, bulkHandler, listHandler, resolveHandler, auditHandler, roleHandler :=
			body(next), byRow(next), byBulkIDs(next), list(next), resolve(next), auditRead(next), role(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Pattern {
			case "GET /api/v1/pricing/category-rules":
				listHandler.ServeHTTP(w, r)
			case "POST /api/v1/pricing/category-rules", "POST /api/v1/pricing/category-rules/bulk":
				bodyHandler.ServeHTTP(w, r)
			case "DELETE /api/v1/pricing/category-rules/bulk":
				bulkHandler.ServeHTTP(w, r)
			case "PUT /api/v1/pricing/category-rules/{id}", "DELETE /api/v1/pricing/category-rules/{id}":
				rowHandler.ServeHTTP(w, r)
			case "GET /api/v1/pricing/category-rules/{id}/audit":
				auditHandler.ServeHTTP(w, r)
			case "GET /api/v1/pricing/resolve":
				resolveHandler.ServeHTTP(w, r)
			default:
				roleHandler.ServeHTTP(w, r)
			}
		})
	}
}
