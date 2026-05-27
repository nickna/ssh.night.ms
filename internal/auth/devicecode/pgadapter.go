package devicecode

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// PoolAdapter wraps *pgxpool.Pool to satisfy the package-local pgPool
// interface. The interface is narrow so tests can stub Pool/Tx without
// pulling in the real driver — but production callers just wrap their
// existing pool.
type PoolAdapter struct {
	Pool    *pgxpool.Pool
	Queries *gen.Queries
}

func (a PoolAdapter) Begin(ctx context.Context) (pgxTx, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &poolTx{tx: tx, queries: a.Queries.WithTx(tx)}, nil
}

type poolTx struct {
	tx      pgx.Tx
	queries *gen.Queries
}

func (t *poolTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *poolTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }
func (t *poolTx) Queries() *gen.Queries              { return t.queries }
