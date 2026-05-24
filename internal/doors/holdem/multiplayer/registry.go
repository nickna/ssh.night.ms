package multiplayer

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
	"github.com/nickna/ssh.night.ms/internal/doors"
)

// Registry tracks the live coordinators in this process. One Registry per
// node. Tables are created on demand; deleted when the last subscriber
// leaves and no seats are occupied (handled by the screen-side calls into
// Drop()).
//
// When configured with a Persistence backend, the registry restores every
// table from the holdem_tables row on Start and writes a snapshot back to
// the same row on graceful Shutdown.
type Registry struct {
	mu      sync.Mutex
	tables  map[int64]*tableEntry
	nextID  atomic.Int64

	rootCtx     context.Context
	persistence *gen.Queries
	ledger      Ledger
	logger      *slog.Logger
}

type tableEntry struct {
	coord  *Coordinator
	ctx    context.Context
	cancel context.CancelFunc
}

// NewRegistry binds the registry to a long-lived ctx (typically the server
// shutdown ctx) so every coordinator we spin up dies on shutdown.
// Persistence, ledger, and logger are optional; when persistence is nil the
// registry is purely in-memory and table state evaporates on restart; when
// ledger is nil settled hands aren't persisted (useful in tests).
func NewRegistry(rootCtx context.Context, persistence *gen.Queries, ledger Ledger, logger *slog.Logger) *Registry {
	return &Registry{
		tables:      make(map[int64]*tableEntry),
		rootCtx:     rootCtx,
		persistence: persistence,
		ledger:      ledger,
		logger:      logger,
	}
}

// installSettlementHook wires the ledger to a coordinator. Kicked off the
// caller's goroutine: the actual DB write happens in a fresh goroutine so
// the coordinator's actor loop is never blocked on Postgres.
func (r *Registry) installSettlementHook(coord *Coordinator) {
	if r.ledger == nil {
		return
	}
	ledger := r.ledger
	rootCtx := r.rootCtx
	logger := r.logger
	coord.SetOnSettlement(func(s Settlement) {
		go func() {
			ctx, cancel := context.WithTimeout(rootCtx, 5*time.Second)
			defer cancel()
			if err := ledger.SettleHand(ctx, s); err != nil && logger != nil {
				logger.Error("holdem mp: settle failed",
					"table", s.TableID, "hand", s.HandNumber, "err", err)
			}
		}()
	})
}

// Restore rebuilds the in-memory registry from the holdem_tables table.
// Called once during server startup after the schema migrations have run.
// Tables with no seated users are dropped (the row gets cleaned up on
// next Persist).
func (r *Registry) Restore(ctx context.Context) error {
	if r.persistence == nil {
		return nil
	}
	rows, err := r.persistence.ListHoldemTables(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var snap Snapshot
		if err := json.Unmarshal(row.Snapshot, &snap); err != nil {
			if r.logger != nil {
				r.logger.Warn("holdem restore: bad snapshot", "id", row.ID, "err", err)
			}
			continue
		}
		hasSeats := false
		for _, s := range snap.Seats {
			if s.UserID != 0 && s.ChipsHand > 0 {
				hasSeats = true
				break
			}
		}
		if !hasSeats {
			_ = r.persistence.DeleteHoldemTable(ctx, row.ID)
			continue
		}
		game := RestoreFromSnapshot(snap, doors.CryptoRng{})
		coord := &Coordinator{
			TableID: row.ID,
			Name:    row.Name,
			cmds:    make(chan command, 16),
			game:    game,
		}
		r.installSettlementHook(coord)
		runCtx, cancel := context.WithCancel(r.rootCtx)
		r.tables[row.ID] = &tableEntry{coord: coord, ctx: runCtx, cancel: cancel}
		go coord.Run(runCtx)
		// Make sure nextID stays above the restored ids so Create doesn't
		// collide.
		if cur := r.nextID.Load(); row.ID > cur {
			r.nextID.Store(row.ID)
		}
		if r.logger != nil {
			r.logger.Info("holdem restored", "id", row.ID, "name", row.Name)
		}
	}
	return nil
}

// Persist writes every active table back to holdem_tables. Called on
// graceful shutdown so the tables survive a restart.
func (r *Registry) Persist(ctx context.Context) error {
	if r.persistence == nil {
		return nil
	}
	r.mu.Lock()
	entries := make([]*tableEntry, 0, len(r.tables))
	for _, e := range r.tables {
		entries = append(entries, e)
	}
	r.mu.Unlock()
	for _, e := range entries {
		// Pull a fresh snapshot through the actor so we don't race with
		// in-flight commands.
		reply := make(chan cmdReply, 1)
		select {
		case e.coord.cmds <- command{kind: cmdSnapshot, user: 0, reply: reply}:
		case <-time.After(500 * time.Millisecond):
			continue
		}
		<-reply // we don't actually need the snapshot — the next call grabs it
		snap := e.coord.game.SnapshotState()
		body, err := json.Marshal(snap)
		if err != nil {
			if r.logger != nil {
				r.logger.Warn("holdem persist: marshal", "id", e.coord.TableID, "err", err)
			}
			continue
		}
		err = r.persistence.UpsertHoldemTable(ctx, gen.UpsertHoldemTableParams{
			ID:         e.coord.TableID,
			Name:       e.coord.Name,
			CapSeats:   int32(e.coord.game.SeatCount()),
			SmallBlind: e.coord.game.BigBlind() / 2,
			BigBlind:   e.coord.game.BigBlind(),
			Snapshot:   body,
		})
		if err != nil && r.logger != nil {
			r.logger.Warn("holdem persist: write", "id", e.coord.TableID, "err", err)
		}
	}
	return nil
}

// Create allocates a new table with the given config + spawns its
// coordinator goroutine. Returns the live coordinator.
func (r *Registry) Create(name string, capSeats int, sb, bb int32) *Coordinator {
	id := r.nextID.Add(1)
	coord := NewCoordinator(id, name, capSeats, sb, bb)
	r.installSettlementHook(coord)
	ctx, cancel := context.WithCancel(r.rootCtx)
	r.mu.Lock()
	r.tables[id] = &tableEntry{coord: coord, ctx: ctx, cancel: cancel}
	r.mu.Unlock()
	go coord.Run(ctx)
	return coord
}

// Get returns the coordinator for tableID, or nil if it's been dropped.
func (r *Registry) Get(tableID int64) *Coordinator {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.tables[tableID]; ok {
		return e.coord
	}
	return nil
}

// List returns a snapshot of every active table. The slice is freshly
// allocated; the underlying coordinators are live.
type TableInfo struct {
	ID       int64
	Name     string
	CapSeats int
	Occupied int
	BB       int32
}

func (r *Registry) List() []TableInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TableInfo, 0, len(r.tables))
	for _, e := range r.tables {
		seats := e.coord.game.Seats()
		occ := 0
		for _, s := range seats {
			if s.UserID != 0 {
				occ++
			}
		}
		out = append(out, TableInfo{
			ID:       e.coord.TableID,
			Name:     e.coord.Name,
			CapSeats: e.coord.game.SeatCount(),
			Occupied: occ,
			BB:       e.coord.game.BigBlind(),
		})
	}
	return out
}

// Drop removes a table from the registry and cancels its goroutine. Idempotent.
func (r *Registry) Drop(tableID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.tables[tableID]; ok {
		e.cancel()
		delete(r.tables, tableID)
	}
}
