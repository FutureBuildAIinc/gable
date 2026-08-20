// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The ad-hoc report builder turns user-supplied JSON into SQL. Column names,
// aggregations and comparison operators are interpolated into the statement as
// text — only filter VALUES are bound as parameters — so the whitelists are the
// only thing standing between a report definition and arbitrary SQL.
//
// These are CORRECTNESS tests: each one asserts that a definition the whitelist
// must not accept is rejected *before* any SQL is executed.
//
// Test strategy: BuildAndExecuteQuery takes a *pgxpool.Pool and executes at the
// end, so the tests hand it a real pool pointed at a closed port. A definition
// that is rejected fails during validation with a specific message; a
// definition that is accepted gets as far as the connection attempt and fails
// with "failed to execute dynamic query". The two are unambiguous, which is
// what lets these tests prove the whitelist ran without needing Postgres.

const execFailurePrefix = "failed to execute dynamic query"

// unreachablePool returns a pool whose Query always fails to connect, quickly.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(),
		"postgres://nobody:nobody@127.0.0.1:1/nodb?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("building the offline pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// assertRejectedBeforeExecution fails unless the definition was refused by the
// builder itself rather than by the (unreachable) database.
func assertRejectedBeforeExecution(t *testing.T, err error, wantSubstr string) {
	t.Helper()
	if err == nil {
		t.Fatal("definition was accepted; want a validation error")
	}
	if strings.Contains(err.Error(), execFailurePrefix) {
		t.Fatalf("definition reached SQL execution (%v); the whitelist should have refused it first", err)
	}
	if wantSubstr != "" && !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want it to mention %q", err, wantSubstr)
	}
}

// assertReachedExecution fails unless the definition passed validation.
func assertReachedExecution(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the offline pool to fail the query, got no error at all")
	}
	if !strings.Contains(err.Error(), execFailurePrefix) {
		t.Fatalf("definition was rejected by validation (%v); it should have been accepted", err)
	}
}

// --- entity whitelist ----------------------------------------------------

func TestBuildAndExecuteQuery_EntityWhitelist(t *testing.T) {
	pool := unreachablePool(t)
	valid := &ReportDefinition{Columns: []ReportColumn{{Field: "id"}}}

	t.Run("known entities are accepted", func(t *testing.T) {
		for _, entity := range []string{"invoices", "orders", "inventory"} {
			def := &ReportDefinition{Columns: []ReportColumn{{Field: "id"}}}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, entity)
			assertReachedExecution(t, err)
		}
	})

	t.Run("unknown entities are refused", func(t *testing.T) {
		hostile := []string{
			"users",
			"customers",
			"gl_journal_lines",
			"pg_shadow",
			"invoices; DROP TABLE invoices",
			"invoices UNION SELECT * FROM customer_users",
			"INVOICES", // the map is case-sensitive on purpose
			"",
			" invoices ",
		}
		for _, entity := range hostile {
			_, err := BuildAndExecuteQuery(context.Background(), pool, valid, entity)
			assertRejectedBeforeExecution(t, err, "unknown entity type")
		}
	})
}

// --- column whitelist ----------------------------------------------------

func TestBuildAndExecuteQuery_ColumnWhitelist(t *testing.T) {
	pool := unreachablePool(t)

	t.Run("whitelisted columns are accepted", func(t *testing.T) {
		for _, field := range []string{"id", "invoice_number", "status", "total_amount", "created_at", "customer_id", "customer_name"} {
			def := &ReportDefinition{Columns: []ReportColumn{{Field: field}}}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertReachedExecution(t, err)
		}
	})

	t.Run("injection attempts in a column field are refused", func(t *testing.T) {
		hostile := []string{
			"i.id, (SELECT password_hash FROM customer_users LIMIT 1)",
			"id; DROP TABLE invoices; --",
			"id) UNION SELECT password_hash, email, '', '', now(), null, '' FROM customer_users --",
			"*",
			"i.*",
			"1=1",
			"id/**/",
			"pg_sleep(10)",
			"CASE WHEN (SELECT count(*) FROM customer_users)>0 THEN 1 ELSE 0 END",
			"total_amount\nFROM customers --",
			"", // an unnamed column is not a column
		}
		for _, field := range hostile {
			def := &ReportDefinition{Columns: []ReportColumn{{Field: field}}}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertRejectedBeforeExecution(t, err, "invalid column")
		}
	})

	// A field valid on one entity must not be valid on another; the schema is
	// per-entity, so leaking across it would join tables the caller never asked
	// for.
	t.Run("columns do not leak between entities", func(t *testing.T) {
		crossovers := []struct{ entity, field string }{
			{"invoices", "product_name"}, // inventory-only
			{"invoices", "order_number"}, // orders-only
			{"orders", "invoice_number"},
			{"inventory", "total_amount"},
			{"inventory", "customer_name"},
		}
		for _, tc := range crossovers {
			def := &ReportDefinition{Columns: []ReportColumn{{Field: tc.field}}}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, tc.entity)
			assertRejectedBeforeExecution(t, err, "invalid column")
		}
	})

	t.Run("a definition with no columns is refused", func(t *testing.T) {
		for _, def := range []*ReportDefinition{
			{},
			{Columns: []ReportColumn{}},
		} {
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertRejectedBeforeExecution(t, err, "no columns selected")
		}
	})
}

// --- aggregation whitelist ----------------------------------------------

func TestBuildAndExecuteQuery_AggregationWhitelist(t *testing.T) {
	pool := unreachablePool(t)

	t.Run("the five supported aggregations are accepted in any case", func(t *testing.T) {
		for _, agg := range []string{"SUM", "COUNT", "AVG", "MIN", "MAX", "sum", "Count", "aVg"} {
			def := &ReportDefinition{Columns: []ReportColumn{{Field: "total_amount", Aggregation: agg}}}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertReachedExecution(t, err)
		}
	})

	t.Run("anything else is refused", func(t *testing.T) {
		hostile := []string{
			"SUM(i.total_amount) FROM invoices; DROP TABLE invoices; SELECT SUM",
			"COUNT(*) , (SELECT password_hash FROM customer_users LIMIT 1)",
			"STRING_AGG",
			"ARRAY_AGG",
			"pg_sleep",
			"SUM;",
			"SUM ",
			"SUM(1) --",
			"'SUM'",
		}
		for _, agg := range hostile {
			def := &ReportDefinition{Columns: []ReportColumn{{Field: "total_amount", Aggregation: agg}}}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertRejectedBeforeExecution(t, err, "invalid aggregation")
		}
	})
}

// --- filter field and operator whitelist --------------------------------

func TestBuildAndExecuteQuery_FilterWhitelist(t *testing.T) {
	pool := unreachablePool(t)
	col := []ReportColumn{{Field: "id"}}

	t.Run("supported operators are accepted", func(t *testing.T) {
		for _, op := range []string{"=", "!=", ">", "<", ">=", "<=", "LIKE"} {
			def := &ReportDefinition{
				Columns: col,
				Filters: []ReportFilter{{Field: "status", Operator: op, Value: "PAID"}},
			}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertReachedExecution(t, err)
		}
	})

	t.Run("unsupported and hostile operators are refused", func(t *testing.T) {
		hostile := []string{
			"= 1 OR 1=1 --",
			"IS NOT",
			"; DROP TABLE invoices; --",
			"= ANY (SELECT id FROM customer_users) --",
			"like",  // the comparison is case-sensitive
			"ILIKE", // only the LIKE spelling is mapped
			"IN",    // documented in the model comment but never implemented
			"NOT IN",
			"BETWEEN",
			"==",
			"",
		}
		for _, op := range hostile {
			def := &ReportDefinition{
				Columns: col,
				Filters: []ReportFilter{{Field: "status", Operator: op, Value: "PAID"}},
			}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertRejectedBeforeExecution(t, err, "unsupported operator")
		}
	})

	t.Run("filter fields go through the same whitelist as columns", func(t *testing.T) {
		hostile := []string{
			"id) OR (1=1",
			"(SELECT password_hash FROM customer_users LIMIT 1)",
			"password_hash",
			"product_name", // belongs to a different entity
			"",
		}
		for _, field := range hostile {
			def := &ReportDefinition{
				Columns: col,
				Filters: []ReportFilter{{Field: field, Operator: "=", Value: "x"}},
			}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertRejectedBeforeExecution(t, err, "invalid filter field")
		}
	})

	// The filter VALUE is the one part of a definition that is bound as a
	// parameter, so hostile values must be accepted (and neutralised) rather
	// than rejected. This test proves the value is not being concatenated: a
	// classic payload gets all the way to execution instead of blowing up the
	// SQL string.
	t.Run("hostile filter values are bound, not interpolated", func(t *testing.T) {
		payloads := []any{
			"' OR '1'='1",
			"'; DROP TABLE invoices; --",
			"\\'; SELECT pg_sleep(10); --",
			"100%",
			nil,
			12345,
			true,
		}
		for _, v := range payloads {
			def := &ReportDefinition{
				Columns: col,
				Filters: []ReportFilter{{Field: "status", Operator: "=", Value: v}},
			}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertReachedExecution(t, err)
		}
	})
}

// --- grouping whitelist --------------------------------------------------

func TestBuildAndExecuteQuery_GroupingWhitelist(t *testing.T) {
	pool := unreachablePool(t)

	t.Run("whitelisted groupings are accepted", func(t *testing.T) {
		def := &ReportDefinition{
			Columns:   []ReportColumn{{Field: "customer_name"}, {Field: "total_amount", Aggregation: "SUM"}},
			Groupings: []ReportGrouping{{Field: "customer_name"}},
		}
		_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
		assertReachedExecution(t, err)
	})

	t.Run("hostile groupings are refused", func(t *testing.T) {
		hostile := []string{
			"1",           // ordinal grouping is not offered
			"c.name, 1=1", //
			"customer_name) --",
			"password_hash",
			"",
		}
		for _, field := range hostile {
			def := &ReportDefinition{
				Columns:   []ReportColumn{{Field: "customer_name"}},
				Groupings: []ReportGrouping{{Field: field}},
			}
			_, err := BuildAndExecuteQuery(context.Background(), pool, def, "invoices")
			assertRejectedBeforeExecution(t, err, "invalid grouping field")
		}
	})
}

// CORRECTNESS: validation must be complete — one bad element anywhere in an
// otherwise-valid definition must stop the whole thing, not be dropped from an
// otherwise-executed query.
func TestBuildAndExecuteQuery_OneBadElementRejectsTheWholeDefinition(t *testing.T) {
	pool := unreachablePool(t)

	tests := []struct {
		name string
		def  *ReportDefinition
		want string
	}{
		{
			name: "second column is hostile",
			def: &ReportDefinition{Columns: []ReportColumn{
				{Field: "id"},
				{Field: "id) UNION SELECT password_hash FROM customer_users --"},
			}},
			want: "invalid column",
		},
		{
			name: "second filter is hostile",
			def: &ReportDefinition{
				Columns: []ReportColumn{{Field: "id"}},
				Filters: []ReportFilter{
					{Field: "status", Operator: "=", Value: "PAID"},
					{Field: "status", Operator: "OR 1=1 --", Value: "x"},
				},
			},
			want: "unsupported operator",
		},
		{
			name: "second grouping is hostile",
			def: &ReportDefinition{
				Columns:   []ReportColumn{{Field: "customer_name"}},
				Groupings: []ReportGrouping{{Field: "customer_name"}, {Field: "1"}},
			},
			want: "invalid grouping field",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildAndExecuteQuery(context.Background(), pool, tc.def, "invoices")
			assertRejectedBeforeExecution(t, err, tc.want)
		})
	}
}

// CORRECTNESS: the schema maps are the whitelist. If a field is advertised in
// entitySchemas it must have a base query to hang off, and vice versa —
// otherwise a caller gets "no base query defined" from a field the UI offered.
func TestEntitySchemasAndBaseQueriesAgree(t *testing.T) {
	for entity := range entitySchemas {
		if _, ok := entityBaseQuery[entity]; !ok {
			t.Errorf("entity %q has a column schema but no base query", entity)
		}
	}
	for entity := range entityBaseQuery {
		if _, ok := entitySchemas[entity]; !ok {
			t.Errorf("entity %q has a base query but no column schema", entity)
		}
	}
}

// CORRECTNESS: every whitelisted SQL expression must be a plain, qualified
// column reference. The expression is interpolated into the SELECT list
// verbatim, so a whitelist entry containing a space, a parenthesis, a comma or
// a comment marker would be a stored injection that no amount of input
// validation could catch.
func TestEntitySchemaExpressionsAreBareColumnReferences(t *testing.T) {
	forbidden := []string{" ", "(", ")", ",", ";", "'", "\"", "--", "/*", "*"}

	for entity, schema := range entitySchemas {
		for field, expr := range schema {
			for _, bad := range forbidden {
				if strings.Contains(expr, bad) {
					t.Errorf("entitySchemas[%q][%q] = %q contains %q; whitelist expressions are interpolated into SQL and must be bare alias.column references",
						entity, field, expr, bad)
				}
			}
			if !strings.Contains(expr, ".") {
				t.Errorf("entitySchemas[%q][%q] = %q is not table-qualified", entity, field, expr)
			}
			// The field name is also interpolated, as the output alias.
			for _, bad := range forbidden {
				if strings.Contains(field, bad) {
					t.Errorf("entitySchemas[%q] key %q contains %q; field names become SQL aliases", entity, field, bad)
				}
			}
		}
	}
}
