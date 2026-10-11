// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// The single-writer gate (ADR 0005 section 9.3): the AR core is the only
// writer of the subledger (customer_transactions), customers.balance_due,
// ar_applications, payment_refunds, the payments rows the acts own, and the
// status and open-amount columns of invoices and credit memos. A writer
// anywhere else can move the ledger the core cannot see, so this test fails
// on the first stray write it finds, naming the file and line.

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// arWritePatterns are the statements only internal/account may carry. Each is
// anchored to the statement's verb so a read (a SELECT, a WHERE clause) never
// matches. A statement may run over several lines, so a file is matched whole;
// the SET list ends at a semicolon or the quote that closes the SQL string.
var arWritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+customer_transactions\b`),
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+ar_applications\b`),
	regexp.MustCompile(`(?i)UPDATE\s+ar_applications\b`),
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+payment_refunds\b`),
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+payments\s*\(`),
	regexp.MustCompile(`(?i)UPDATE\s+payments\s+SET\b`),
	regexp.MustCompile("(?i)UPDATE\\s+customers\\s+SET\\s+[^;`\"]*\\bbalance_due\\b"),
	regexp.MustCompile("(?i)UPDATE\\s+invoices\\s+SET\\s+[^;`\"]*\\b(amount_open|status|gl_entry_id|paid_at)\\b"),
	regexp.MustCompile("(?i)UPDATE\\s+credit_memos\\s+SET\\s+[^;`\"]*\\b(amount_open|status|applied_at|gl_entry_id)\\b"),
}

// scanForARWriters walks root (skipping hidden directories, the core directory
// and test files) and answers one entry per statement that writes what only
// the core may write: path, line of the statement's verb, its first line.
func scanForARWriters(root, coreDir string) ([]string, error) {
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			// The core itself is the one writer; its own files are the design.
			if filepath.Clean(path) == filepath.Clean(coreDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines { // a comment line carries no statement: blank it, keep the line count
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				lines[i] = ""
			}
		}
		body := strings.Join(lines, "\n")
		for _, re := range arWritePatterns {
			for _, at := range re.FindAllStringIndex(body, -1) {
				n := strings.Count(body[:at[0]], "\n")
				offenders = append(offenders, filepath.Clean(path)+":"+strconv.Itoa(n+1)+": "+strings.TrimSpace(strings.Split(body[at[0]:], "\n")[0]))
			}
		}
		return nil
	})
	return offenders, err
}

func TestTheARCoreIsTheOnlyWriterOfItsTables(t *testing.T) {
	offenders, err := scanForARWriters("..", "../account")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offenders {
		t.Errorf("a writer outside the AR core: %s", o)
	}
}

// The gate must be able to fail: a stray writer outside the core, one spread
// over several lines, and a stray inside a hidden or test file the walk skips.
func TestTheGateCatchesAStrayWriter(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("account/own.go", "package account\nvar _ = `UPDATE customers SET balance_due = 0`\n")
	write("order/stray.go", "package order\nvar _ = `UPDATE customers SET balance_due = 0`\n")
	write("invoice/multi.go", "package invoice\nvar _ = `\n\tUPDATE invoices\n\tSET updated_at = NOW(),\n\t    status = 'PAID'\n\tWHERE id = $1`\n")
	write("quote/quiet.go", "package quote\n// UPDATE customers SET balance_due = 0 in a comment\nvar _ = `UPDATE invoices SET notes = 'x' WHERE id = $1`\n")
	write("quote/quiet_test.go", "package quote\nvar _ = `UPDATE customers SET balance_due = 0`\n")
	write(".hidden/h.go", "package h\nvar _ = `UPDATE customers SET balance_due = 0`\n")
	got, err := scanForARWriters(root, filepath.Join(root, "account"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(root, "invoice/multi.go") + ":3: UPDATE invoices",
		filepath.Join(root, "order/stray.go") + ":2: UPDATE customers SET balance_due = 0`",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("offenders =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
