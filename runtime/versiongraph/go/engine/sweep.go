package engine

import (
	"context"
	"errors"
	"time"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// DefaultDiscardGrace is how long after a ref is discarded Sweep keeps its
// member rows, unless SweepOptions.DiscardGrace says otherwise.
const DefaultDiscardGrace = 7 * 24 * time.Hour

// SweepOptions configure a maintenance pass.
type SweepOptions struct {
	// Actor is who the pass writes as: the discards it makes and the
	// deletes of discarded refs' rows record it.
	Actor string
	// DiscardGrace is how long after a ref is discarded its member rows are
	// kept; 0 is DefaultDiscardGrace.
	DiscardGrace time.Duration
	// AbandonAfter, when positive, discards every live change set with no
	// write for that long. 0 discards none.
	AbandonAfter time.Duration
	// PruneBatch caps how many history images of each kind the pass
	// prunes; 0 is no cap.
	PruneBatch int
}

// SweepReport is what one pass did. Each count is keyed by kind and holds
// only kinds with a nonzero count.
type SweepReport struct {
	// Skipped is true when another pass held the graph's sweep lock, and
	// this one did nothing.
	Skipped bool `json:"skipped"`
	// Abandoned is how many idle change sets the pass discarded.
	Abandoned int `json:"abandoned"`
	// CollectedRefs is how many discarded refs had member rows the pass
	// deleted, and CollectedRows how many rows of each kind it deleted.
	CollectedRefs int              `json:"collectedRefs"`
	CollectedRows map[string]int64 `json:"collectedRows"`
	// Pruned is how many history images of each kind the pass pruned.
	Pruned map[string]int64 `json:"pruned"`
	// Snapshots is how many missing snapshots the pass wrote.
	Snapshots int `json:"snapshots"`
}

// Sweep runs one maintenance pass in one transaction, under the graph's
// sweep lock; when another pass holds it, Sweep does nothing and reports
// Skipped. In order, the pass discards the change sets idle past
// AbandonAfter, leaving one a write reaches after the pass read it;
// deletes the member rows of refs discarded longer ago than DiscardGrace,
// keeping their ref rows and commits as the audit trail; prunes each
// kind's history past its declared retention, keeping every row version a
// patch or a snapshot pins; and writes each snapshot the graph's rules
// call for and it lacks. Nothing calls Sweep unless a service does.
func (e *Engine) Sweep(ctx context.Context, opts SweepOptions) (*SweepReport, error) {
	actor, err := actorID(opts.Actor)
	if err != nil {
		return nil, err
	}
	grace := opts.DiscardGrace
	if grace == 0 {
		grace = DefaultDiscardGrace
	}
	var report *SweepReport
	err = e.transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		report = &SweepReport{CollectedRows: map[string]int64{}, Pruned: map[string]int64{}}
		locked, err := tx.SweepLock(ctx)
		if err != nil {
			return err
		}
		if !locked {
			report.Skipped = true
			return nil
		}
		if opts.AbandonAfter > 0 {
			idle, err := tx.IdleDrafts(ctx, opts.AbandonAfter)
			if err != nil {
				return err
			}
			for _, ref := range idle {
				// A write that reached the ref after IdleDrafts read it moved
				// its version, so the ref is no longer idle: leave it.
				err := tx.DiscardRef(ctx, ref.ID, ref.Version, actor)
				if errors.Is(err, storage.ErrVersionConflict) {
					continue
				}
				if err != nil {
					return err
				}
				report.Abandoned++
			}
		}
		discarded, err := tx.DiscardedRefs(ctx, grace)
		if err != nil {
			return err
		}
		for _, ref := range discarded {
			var rows int64
			for _, k := range e.kinds {
				n, err := tx.RemoveRefRows(ctx, k.Name, ref.ID, actor)
				if err != nil {
					return err
				}
				if n > 0 {
					report.CollectedRows[k.Name] += n
					rows += n
				}
			}
			if rows > 0 {
				report.CollectedRefs++
			}
		}
		for _, k := range e.kinds {
			n, err := tx.Prune(ctx, k.Name, 0, opts.PruneBatch)
			if err != nil {
				return err
			}
			if n > 0 {
				report.Pruned[k.Name] = n
			}
		}
		report.Snapshots, err = e.backfill(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// backfill writes every snapshot the graph's rules call for and it lacks:
// of a tagged commit (Release takes only tagged ones, so this covers a
// released commit), and of a commit snapshotEvery commits past the nearest
// snapshot on its chain. It visits each commit after its parent, so every
// walk it takes stops at a snapshot at most snapshotEvery commits away. It
// returns how many snapshots it wrote.
func (e *Engine) backfill(ctx context.Context, tx storage.Tx) (int, error) {
	nodes, err := tx.Commits(ctx)
	if err != nil {
		return 0, err
	}
	byID := make(map[string]storage.CommitNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	// distance is each visited commit's distance from the nearest snapshot
	// on its chain, 0 for a snapshotted commit.
	distance := make(map[string]int, len(nodes))
	written := 0
	for _, start := range nodes {
		// Visit start's unvisited ancestors from the oldest down.
		var path []storage.CommitNode
		for at, ok := start, true; ok; at, ok = byID[at.Parent] {
			if _, seen := distance[at.ID]; seen {
				break
			}
			path = append(path, at)
			if at.Parent == "" {
				break
			}
		}
		for i := len(path) - 1; i >= 0; i-- {
			n := path[i]
			if n.Snapshot {
				distance[n.ID] = 0
				continue
			}
			d := distance[n.Parent] + 1
			if n.Tagged || d >= e.snapshotEvery {
				took, err := e.ensureSnapshot(ctx, tx, storage.Commit{ID: n.ID})
				if err != nil {
					return 0, err
				}
				if took {
					written++
				}
				d = 0
			}
			distance[n.ID] = d
		}
	}
	return written, nil
}

// RunSweeper runs a Sweep pass now and then once every interval until ctx
// is done, and returns ctx's error. Each pass takes the graph's sweep lock,
// so one replica sweeps at a time and a pass that finds it held is
// skipped. onPass, when not nil, receives each pass's report or error; an
// error does not stop the sweeper.
func (e *Engine) RunSweeper(ctx context.Context, interval time.Duration, opts SweepOptions, onPass func(*SweepReport, error)) error {
	if interval <= 0 {
		return errors.New("engine: a sweeper's interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		report, err := e.Sweep(ctx, opts)
		if onPass != nil {
			onPass(report, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
