package sqlutil

import (
	ir "github.com/parable-work/superschematic/ir"
)

// History is what a @versioned table's history keeps (D17, D32). The sql
// generator writes its capture trigger and prune function from it, the ORM
// generator its hard delete's actor, and graphdesc gives it to each kind of
// a version graph's descriptor, so every reader of these facts computes
// them here.
type History struct {
	// RetentionDays is how many days of history the prune function keeps:
	// @versioned's retentionDays, or 0 when it declares none, and then the
	// table has no prune function.
	RetentionDays int
	// Exclude are the columns every history image leaves out, in the order
	// @versioned({ exclude }) names their fields; nil when it names none.
	Exclude []string
	// Actor is the column a delete's image names its actor in: deletedBy's
	// when the table has one, else updatedBy's. It is "" when the table has
	// neither, or when that column is excluded, since the delete's image
	// leaves an excluded column out too.
	Actor string
}

// historyActorFields are the fields whose columns a delete's image may name
// its actor in, the first the table has winning.
var historyActorFields = []string{AuditDeletedBy, AuditUpdatedBy}

// VersionedHistory is the history of td, a @versioned table, by its
// @versioned config (nil for a bare @versioned): hasColumn reports whether
// the table has a column of that name. An excluded field, and an actor
// field, is named by its column, as @column names it (D85): verification
// refuses to exclude a relation, whose column is its foreign key.
func VersionedHistory(td *ir.TypeDef, hasColumn func(column string) bool) History {
	var history History
	excluded := map[string]bool{}
	if cfg := td.VersionedConfig; cfg != nil {
		if cfg.RetentionDays != nil {
			history.RetentionDays = *cfg.RetentionDays
		}
		for _, field := range cfg.Exclude {
			column := AuditColumnIn(td, field)
			excluded[column] = true
			history.Exclude = append(history.Exclude, column)
		}
	}
	for _, field := range historyActorFields {
		if column := AuditColumnIn(td, field); hasColumn(column) {
			if !excluded[column] {
				history.Actor = column
			}
			break
		}
	}
	return history
}
