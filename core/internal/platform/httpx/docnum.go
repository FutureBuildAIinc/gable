// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httpx

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DefaultDocNumberWidth is the zero-padded width of a document number: the
// sequence's first values read Q-000001, and a sequence that outgrows six
// digits simply produces longer numbers, never truncation or wraparound.
const DefaultDocNumberWidth = 6

// Querier is the database seam this package needs: one row-returning call,
// taken through the caller's own transaction when one is open (every read
// and write inside a transaction goes through that transaction, never
// through the pool beside it). pgx.Tx, *pgxpool.Pool, and the repository's
// broader Executor all satisfy it structurally.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NextDocumentNumber takes the next value of the named Postgres sequence
// through the passed-in querier and formats it as the entity's document
// number (ADR 0001 §8): prefix, hyphen, zero-padded value, so
// NextDocumentNumber(ctx, tx, "quote_number_seq", "Q", 6) returns Q-000001
// on a fresh sequence.
//
// The querier is the caller's: pass the transaction the create path already
// holds, or the pool when no transaction is open. The sequence is consumed
// with nextval through a parameter, so the number is spent even when the
// transaction rolls back: sequences are not transactional, gaps are
// expected, and a document number is an identifier for people, not a
// counter for audits.
//
// The sequence itself is created by the converting module's migration, not
// here; a backfill migration numbers existing rows from the sequence's
// start. Arguments are server-side constants and validated as such: the
// sequence name is a plain lowercase identifier, the prefix is one to four
// uppercase letters, and the width is at least one.
func NextDocumentNumber(ctx context.Context, q Querier, sequence, prefix string, width int) (string, error) {
	if err := validateSequenceName(sequence); err != nil {
		return "", err
	}
	if err := validateDocPrefix(prefix); err != nil {
		return "", err
	}
	if width < 1 {
		return "", fmt.Errorf("document number width must be at least 1, got %d", width)
	}
	if q == nil {
		return "", fmt.Errorf("document number querier is nil")
	}

	var n int64
	if err := q.QueryRow(ctx, "SELECT nextval($1)", sequence).Scan(&n); err != nil {
		return "", fmt.Errorf("nextval on %s: %w", sequence, err)
	}
	return fmt.Sprintf("%s-%0*d", prefix, width, n), nil
}

// validateSequenceName accepts a plain, unquoted Postgres identifier:
// lowercase letters, digits, underscores, not starting with a digit. The
// name arrives from the caller's own constant, and the check keeps a typo
// from ever reaching the database as structure.
func validateSequenceName(name string) error {
	if name == "" {
		return fmt.Errorf("sequence name is empty")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c == '_', i > 0 && c >= '0' && c <= '9':
		default:
			return fmt.Errorf("sequence name %q is not a plain lowercase identifier", name)
		}
	}
	return nil
}

// validateDocPrefix accepts one to four uppercase letters: the short
// entity prefixes (Q for quotes, and the rest as their modules convert).
func validateDocPrefix(prefix string) error {
	if len(prefix) < 1 || len(prefix) > 4 {
		return fmt.Errorf("document number prefix %q must be 1 to 4 characters", prefix)
	}
	for i := 0; i < len(prefix); i++ {
		if prefix[i] < 'A' || prefix[i] > 'Z' {
			return fmt.Errorf("document number prefix %q must be uppercase letters", prefix)
		}
	}
	return nil
}

// NextGaplessNumber takes the next value of a gapless series through the
// caller's transaction and formats it like NextDocumentNumber (ADR 0005
// section 4.1): `UPDATE document_counters SET next_value = next_value + 1
// WHERE series = $1 RETURNING next_value - 1`. The row stays locked from the
// mint to the commit, so the minting transactions serialize, and a rollback
// undoes the increment with it: no number is lost, the next mint takes it.
// That is the whole difference from a sequence, and its cost: take the mint
// late in the act, after every other row lock and immediately before the
// document insert (ADR 0005 section 11, step 8).
//
// q must be the caller's open transaction. Outside a transaction the
// statement would commit on its own and a failure after the mint would leave
// a gap, which is the one thing this function exists to prevent; the helper
// cannot tell the two apart through the Querier seam, so the rule is the
// caller's, and the invoice and credit memo repositories pass the executor
// the platform database hands out inside RunInTx (never the pool).
//
// The series is seeded by the migration that introduces the counter
// (document_counters); a series with no row is an error naming it, never a
// number from nowhere. The series name is a server side constant: lowercase
// letters, digits, underscores, hyphens and colons (a per branch series is
// "invoice:<branch code>"). The prefix and width follow NextDocumentNumber.
func NextGaplessNumber(ctx context.Context, q Querier, series, prefix string, width int) (string, error) {
	if err := validateSeriesName(series); err != nil {
		return "", err
	}
	if err := validateDocPrefix(prefix); err != nil {
		return "", err
	}
	if width < 1 {
		return "", fmt.Errorf("document number width must be at least 1, got %d", width)
	}
	if q == nil {
		return "", fmt.Errorf("document number querier is nil")
	}
	var n int64
	err := q.QueryRow(ctx,
		`UPDATE document_counters SET next_value = next_value + 1 WHERE series = $1 RETURNING next_value - 1`,
		series).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("gapless series %q has no counter row", series)
		}
		return "", fmt.Errorf("gapless counter %s: %w", series, err)
	}
	return fmt.Sprintf("%s-%0*d", prefix, width, n), nil
}

// validateSeriesName accepts a counter series key: lowercase letters, digits,
// underscores, hyphens and colons, not starting with a digit.
func validateSeriesName(name string) error {
	if name == "" {
		return fmt.Errorf("series name is empty")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c == '_', c == '-', c == ':', i > 0 && c >= '0' && c <= '9':
		default:
			return fmt.Errorf("series name %q is not a plain lowercase key", name)
		}
	}
	return nil
}
