// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package testutil

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// TxContext returns a context that makes (*database.DB).RunInTx take its
// "already inside a transaction" fast path, so service methods whose whole body
// lives inside RunInTx can be unit-tested against fake repositories with no
// Postgres running.
//
// Why this is needed: RunInTx short-circuits to fn(ctx) when
// ctx.Value(<unexported tx key>) yields a pgx.Tx. That key type is unexported
// in pkg/database, so a test cannot construct it. But context.Context is an
// interface, so a test can supply a context whose Value falls back to a stub
// pgx.Tx for keys nothing else in the chain claims. Values genuinely stored on
// the parent context still win, so branchctx and friends keep working.
//
// The stub transaction is never used: RunInTx discards the value it finds, and
// the services under test talk to fake repositories rather than to
// db.GetExecutor. Handing a TxContext to code that does reach for
// db.GetExecutor would give it the stub and panic on first use — loud rather
// than silently wrong, which is the behaviour we want.
//
// This is a workaround for a testability gap, not a pattern to spread. The
// durable fix is for pkg/database to expose the transaction seam (a small
// interface, or an exported context helper) so callers can inject a no-op
// runner; changing production code was out of scope here.
func TxContext(parent context.Context) context.Context {
	return txContext{parent}
}

type txContext struct{ context.Context }

func (c txContext) Value(key any) any {
	if v := c.Context.Value(key); v != nil {
		return v
	}
	return stubTx{}
}

// stubTx satisfies pgx.Tx by embedding the interface. Every method panics with
// a nil dereference if called — deliberately, because nothing in a unit test
// should be executing SQL.
type stubTx struct{ pgx.Tx }
