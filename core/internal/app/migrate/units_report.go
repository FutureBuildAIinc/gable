// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package migrate

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"

	"github.com/gablelbm/gable/internal/config"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// unitCodeRule is the code vocabulary a stored unit value must match to
// enter the catalogue (ADR 0006 section 2.1).
var unitCodeRule = regexp.MustCompile(`^[A-Z]{1,6}$`)

// unitsReportColumns are the columns A1 (migration 098) and B0 (C3-2B)
// collect: each table and column whose stored unit values the catalogue
// migration normalises, inserts as dealer units or refuses on. The counter
// and document line tables are listed here ahead of B0 so an operator sees
// them all in one pass; the columns B0 has not converted yet simply report
// zero rows off the enum.
type unitsReportColumn struct {
	table  string
	column string
	query  string
}

func unitsReportColumns() []unitsReportColumn {
	text := func(table, column string) unitsReportColumn {
		return unitsReportColumn{table: table, column: column,
			query: fmt.Sprintf(`SELECT upper(trim(%s)) AS val, count(*) AS n FROM %s WHERE %s IS NOT NULL GROUP BY 1`, column, table, column)}
	}
	cast := func(table, column string) unitsReportColumn {
		return unitsReportColumn{table: table, column: column,
			query: fmt.Sprintf(`SELECT upper(trim(%s::text)) AS val, count(*) AS n FROM %s GROUP BY 1`, column, table)}
	}
	return []unitsReportColumn{
		cast("products", "uom_primary"),
		cast("quote_lines", "uom"),
		text("quote_lines", "price_uom"),
		// B0's columns (C3-2B); listed so one report answers the whole
		// question an operator asks before upgrading.
		text("pos_line_items", "uom"),
		text("pos_return_lines", "uom"),
		text("order_lines", "uom"),
		text("order_lines", "price_uom"),
		text("invoice_lines", "uom"),
		text("invoice_lines", "price_uom"),
		text("credit_memo_lines", "uom"),
		text("credit_memo_lines", "price_uom"),
	}
}

// RunUnitsReport is the read only pre flight report of A1 (ADR 0006
// section 8): per table and column, every stored unit value the catalogue
// migration would insert as a new dealer unit, and every value that would
// abort it, with its row count. An operator runs it before upgrading and
// corrects the free text values it names through the application or a
// reviewed SQL statement; the migration itself never guesses.
func RunUnitsReport() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping DB: %v", err)
	}
	if code := unitsReport(db); code != 0 {
		os.Exit(code)
	}
}

func unitsReport(db *sql.DB) int {
	fmt.Println("Units pre flight report (A1 of migration 098; C3-2B extends it to the counter and document lines)")
	fmt.Println()
	// The catalogue as it stands; before 098 there is none, and every
	// valid code is new.
	catalogued := map[string]bool{}
	rows, err := db.Query(`SELECT code FROM units`)
	if err == nil {
		for rows.Next() {
			var code string
			if rows.Scan(&code) == nil {
				catalogued[code] = true
			}
		}
		rows.Close()
	}

	type entry struct {
		value string
		n     int64
	}
	newUnits := map[string]int64{}
	var aborts []entry

	for _, c := range unitsReportColumns() {
		rows, err := db.Query(c.query)
		if err != nil {
			// A table B0 has not converted yet (or does not exist on this
			// deployment) reports nothing.
			fmt.Printf("%s.%s: not present on this database\n", c.table, c.column)
			continue
		}
		for rows.Next() {
			var val string
			var n int64
			if err := rows.Scan(&val, &n); err != nil {
				rows.Close()
				log.Fatalf("scan %s.%s: %v", c.table, c.column, err)
			}
			if !unitCodeRule.MatchString(val) {
				aborts = append(aborts, entry{value: fmt.Sprintf("%s.%s = %q (%d rows)", c.table, c.column, val, n), n: n})
				continue
			}
			if !catalogued[val] {
				newUnits[val] += n
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			log.Fatalf("read %s.%s: %v", c.table, c.column, err)
		}
	}

	fmt.Println()
	if len(newUnits) == 0 && len(aborts) == 0 {
		fmt.Println("Every stored unit value is already a unit of the catalogue.")
		return 0
	}
	if len(newUnits) > 0 {
		codes := make([]string, 0, len(newUnits))
		for code := range newUnits {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		fmt.Println("Values that would be inserted as new dealer units (dimension COUNT, no standard size), with their row counts:")
		for _, code := range codes {
			fmt.Printf("  %s (%d rows across the columns above)\n", code, newUnits[code])
		}
		fmt.Println()
	}
	if len(aborts) > 0 {
		fmt.Println("Values that would ABORT the migration (they do not match ^[A-Z]{1,6}$); correct them first:")
		for _, a := range aborts {
			fmt.Printf("  %s\n", a.value)
		}
		return 1
	}
	return 0
}
