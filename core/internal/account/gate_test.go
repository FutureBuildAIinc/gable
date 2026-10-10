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
	"strconv"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// arWritePatterns are the statements only internal/account may carry. Each is
// anchored to the statement's verb so a read (a SELECT, a WHERE clause) never
// matches.
var arWritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+customer_transactions\b`),
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+ar_applications\b`),
	regexp.MustCompile(`(?i)UPDATE\s+ar_applications\b`),
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+payment_refunds\b`),
	regexp.MustCompile(`(?i)INSERT\s+INTO\s+payments\s*\(`),
	regexp.MustCompile(`(?i)UPDATE\s+payments\s+SET\b`),
	regexp.MustCompile(`(?i)UPDATE\s+customers\s+SET\s+[^;]*\bbalance_due\b`),
	regexp.MustCompile(`(?i)UPDATE\s+invoices\s+SET\s+[^;]*\b(amount_open|status|gl_entry_id|paid_at)\b`),
	regexp.MustCompile(`(?i)UPDATE\s+credit_memos\s+SET\s+[^;]*\b(amount_open|status|applied_at|gl_entry_id)\b`),
}

func TestTheARCoreIsTheOnlyWriterOfItsTables(t *testing.T) {
	root := ".."
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			// The core itself is the one writer; its own files are the design.
			if d.Name() == "account" && filepath.ToSlash(filepath.Dir(path)) == "internal" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, re := range arWritePatterns {
				if re.MatchString(line) {
					offenders = append(offenders, filepath.Clean(path)+":"+itoa(i+1)+": "+trimmed)
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offenders {
		t.Errorf("a writer outside the AR core: %s", o)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
