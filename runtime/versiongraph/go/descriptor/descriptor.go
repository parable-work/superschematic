// Package descriptor is the Go form of a graph descriptor (version 3): the
// JSON document the core checks (versiongraph.Validate) and the engine and
// the storage adapters read. Each reader takes what it needs of it: the
// engine each kind's role columns, the Postgres adapter its tables, the
// SQLite adapter each kind's columns and history.
package descriptor

import (
	"encoding/json"
	"fmt"
)

// Version is the descriptor version this package reads.
const Version = 3

// Descriptor is a graph descriptor.
type Descriptor struct {
	Version int    `json:"version"`
	Graph   string `json:"graph"`
	Root    Root   `json:"root"`

	RefTable      string `json:"refTable"`
	CommitTable   string `json:"commitTable"`
	PatchTable    string `json:"patchTable"`
	ReleaseTable  string `json:"releaseTable"`
	SnapshotTable string `json:"snapshotTable"`

	Kinds []Kind `json:"kinds"`
}

// Root is the graph's root: its table and that table's key column.
type Root struct {
	Table string `json:"table"`
	Key   string `json:"key"`
}

// Kind is one kind of row in the graph: its tables, its role columns, its
// columns' value classes and its history.
type Kind struct {
	Kind         string `json:"kind"`
	Table        string `json:"table"`
	HistoryTable string `json:"historyTable"`

	Key       string `json:"key"`
	ID        string `json:"id"`
	Ref       string `json:"ref"`
	Root      string `json:"root"`
	Tombstone string `json:"tombstone"`
	Version   string `json:"version"`
	Author    string `json:"author"`

	Excluded []string `json:"excluded"`
	// ReleasedTable is set for a kind that keeps the released tree's rows
	// in a table of its own (D139), "" for one that keeps none.
	ReleasedTable string   `json:"releasedTable"`
	History       *History `json:"history"`
	// Columns maps each declared column to its value class.
	Columns map[string]string `json:"columns"`
}

// History is a kind's history: how long its rows' history is kept, the
// columns it leaves out, and the column that names a change's actor.
type History struct {
	RetentionDays *int      `json:"retentionDays"`
	Exclude       *[]string `json:"exclude"`
	Actor         string    `json:"actor"`
}

// Read decodes raw. It checks only that raw is a descriptor of Version;
// the core checks the rest.
func Read(raw json.RawMessage) (Descriptor, error) {
	var d Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return Descriptor{}, fmt.Errorf("read the descriptor: %w", err)
	}
	if d.Version != Version {
		return Descriptor{}, fmt.Errorf("descriptor version %d; this adapter reads version %d", d.Version, Version)
	}
	return d, nil
}
