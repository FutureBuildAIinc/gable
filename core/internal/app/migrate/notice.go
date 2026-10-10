// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package migrate

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// noticeWriter is the destination each migration's NOTICE is written to.
// It is a variable so tests in this package can substitute a
// bytes.Buffer (the migrate runner would otherwise write to os.Stdout,
// which a test cannot capture). It is unexported: no caller outside
// the migrate package should reach for the variable, the public way is
// the setter below.
var noticeWriter io.Writer

var (
	noticeMu       sync.Mutex
	noticeFileBase = ""
)

// openPool builds a pgxpool whose connection config routes every
// NOTICE through the notice handler that prints each one to noticeWriter
// (or to stdout if nil) prefixed with the migration file name. PR 70
// review round 7 N1.
func openPool(url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 2
	cfg.MinConns = 0
	cfg.ConnConfig.OnNotice = noticeHandler()
	return pgxpool.NewWithConfig(context.Background(), cfg)
}

// setTxNoticeHandler sets the package variable the OnNotice callback
// reads so every notice raised by the migration body is prefixed with
// the file's base name. The pgx pool config OnNotice fires on every
// notice from the connection, including the ones raised inside the
// transaction; the base is set just before the transaction runs.
func setTxNoticeHandler(base string) {
	noticeMu.Lock()
	noticeFileBase = base
	noticeMu.Unlock()
}

// resetNoticeHandler clears the base so a notice outside the migrate
// loop (the create schema_migrations statement, the ping) does not
// carry a previous migration's name.
func resetNoticeHandler() {
	noticeMu.Lock()
	noticeFileBase = ""
	noticeMu.Unlock()
}

// setNoticeWriter swaps the destination writer the notice handler reads.
// The package mutex guards it so a concurrent handler does not race a
// test that swaps the buffer between calls. The writer is set to nil
// between tests so the runner's own os.Stdout stays in effect when no
// test is in scope.
func setNoticeWriter(w io.Writer) {
	noticeMu.Lock()
	noticeWriter = w
	noticeMu.Unlock()
}

// noticeHandler returns the OnNotice callback the pool installs. It
// reads the destination base and writer from the package variables so
// tests can swap them and migrate can update the base per migration.
func noticeHandler() func(*pgconn.PgConn, *pgconn.Notice) {
	return func(_ *pgconn.PgConn, n *pgconn.Notice) {
		if n == nil {
			return
		}
		noticeMu.Lock()
		base := noticeFileBase
		w := noticeWriter
		noticeMu.Unlock()
		if w == nil {
			return
		}
		if base == "" {
			base = "migrate"
		}
		fmt.Fprintf(w, "[%s] NOTICE: %s\n", base, n.Message)
	}
}
