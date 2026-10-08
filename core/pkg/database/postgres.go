// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package database

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Executor is an interface that matches both pgxpool.Pool and pgx.Tx
type Executor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type DB struct {
	Pool *pgxpool.Pool
}

// PoolConfig holds configurable pool parameters.
type PoolConfig struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// DefaultPoolConfig returns sensible defaults for the connection pool.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxConns:          10,
		MinConns:          2,
		MaxConnLifetime:   time.Hour,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: 1 * time.Minute,
	}
}

func Connect(connString string, opts ...PoolConfig) (*DB, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("unable to parse connection string: %w", err)
	}

	pc := DefaultPoolConfig()
	if len(opts) > 0 {
		pc = opts[0]
	}
	config.MaxConns = pc.MaxConns
	config.MinConns = pc.MinConns
	config.MaxConnLifetime = pc.MaxConnLifetime
	config.MaxConnIdleTime = pc.MaxConnIdleTime
	config.HealthCheckPeriod = pc.HealthCheckPeriod

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("unable to ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

func (db *DB) Close() {
	db.Pool.Close()
}

// RunInTx executes a function within a database transaction.
//
// The return value is NAMED, and that is load-bearing. The commit happens in the
// deferred function, which runs after the `return` statement has already set the
// result. With an unnamed return, `err = tx.Commit(ctx)` in the defer writes to a
// local that nothing reads, so a failing COMMIT was reported to the caller as
// success — every "atomic" guarantee in this repository rested on that
// assignment being visible.
//
// A commit can fail for reasons the function body never sees: a deferred
// constraint firing, serialization failure, disk or connection loss between the
// last statement and the COMMIT. Those are exactly the cases where a caller must
// not believe the write landed. `internal/payment` has a branch that logs
// "gateway charged but DB commit failed" precisely for this, and it was
// unreachable.
//
// Nested calls join the caller's transaction and do not commit; the outermost
// RunInTx owns the boundary.
func (db *DB) RunInTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	// Check if we are already in a transaction
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		// Already in a transaction, just run the function
		return fn(ctx)
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		} else if err != nil {
			_ = tx.Rollback(ctx)
		} else if cerr := tx.Commit(ctx); cerr != nil {
			err = fmt.Errorf("failed to commit transaction: %w", cerr)
		}
	}()

	// Inject tx into context
	ctxWithTx := context.WithValue(ctx, txKey{}, tx)
	err = fn(ctxWithTx)
	return err
}

// RunInSavepoint runs fn inside a savepoint of the transaction ctx carries,
// so a failure inside fn can be undone without losing the rest of the
// transaction. fn's context is the caller's: GetExecutor still resolves to
// the same transaction. On an error or panic from fn, or a failed RELEASE
// (fn swallowed a statement error, leaving the transaction aborted), the
// work rolls back to the savepoint and the error is returned (a panic is
// re-raised after the rollback). The savepoint is managed by name here
// because pgx's nested transaction marks itself closed after a failed
// RELEASE and then refuses the rollback. Outside a transaction there is
// nothing to roll back to, so fn runs in a new transaction through RunInTx.
func (db *DB) RunInSavepoint(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	if !ok {
		return db.RunInTx(ctx, fn)
	}
	name := fmt.Sprintf("gable_sp_%d", savepointSeq.Add(1))
	if _, err := tx.Exec(ctx, "SAVEPOINT "+name); err != nil {
		return fmt.Errorf("failed to open savepoint: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+name)
			panic(p)
		} else if err != nil {
			_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+name)
		} else if _, rerr := tx.Exec(ctx, "RELEASE SAVEPOINT "+name); rerr != nil {
			_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+name)
			err = fmt.Errorf("failed to release savepoint: %w", rerr)
		}
	}()
	return fn(ctx)
}

var savepointSeq atomic.Int64

type txKey struct{}

// InTx reports whether ctx carries a transaction, i.e. whether GetExecutor
// will resolve to that transaction rather than the pool. Callers that must
// behave differently on the two paths (the audit logger's cancellation
// discipline, for one) ask this instead of re-deriving it.
func InTx(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(pgx.Tx)
	return ok
}

func (db *DB) GetExecutor(ctx context.Context) Executor {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return db.Pool
}
