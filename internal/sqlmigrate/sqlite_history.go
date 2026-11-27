package sqlmigrate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
)

/*
A @versioned table on SQLite (D189): the history table Postgres keeps, its
columns in SQLite's types, and the triggers that fill it, row triggers
named as Postgres's, since SQLite has no trigger functions.

  - The bump (trg_<table>_bump_version) is an @optimistic table's
    (sqliteBumpTrigger): after an update that leaves _version as it was, it
    gives the row the next version.
  - The write's capture is a trigger per event, the Postgres trigger's name
    and the event after it (trg_<table>_capture_history_write_insert and
    _update). The insert's records the row as it is stored, read by its
    rowid, at its version. The update's fires when the update moved the
    version, the bump's own update or one that set _version itself, and
    records the row as stored at its new version: an update that leaves
    the version as it was records nothing itself, and the bump's update,
    which moves it, does. So each version a row takes is recorded once.
  - The delete's (trg_<table>_capture_history_delete) records a tombstone
    at the deleted row's version and one, the row's image as it was, as
    Postgres's does. SQLite has no setting a hard delete names its actor
    in, so the tombstone's actor is the row's own (its deleted_by, else its
    updated_by).

An image is a JSON object of the row's columns as SQLite holds them, but
the ones @versioned({ exclude }) leaves out: a column that holds JSON (a
list, a JSON value) is its JSON, and a boolean is 1 or 0. SQLite has no
form of what Postgres prunes and partitions a history with, a prune
function and a partitioned table, so a @versioned table that keeps its
history for some days, pins versions other rows name, or partitions its
history, is refused here.
*/

// sqliteHistoryRefusals lists what a @versioned table asks of its history
// that SQLite has no form of.
func sqliteHistoryRefusals(h sqlgen.HistoryTable, typeName string, refuse func(format string, args ...any)) {
	if h.Partitioned() {
		refuse("%s is @versioned with its history partitioned (partitionBy)", typeName)
	}
	if h.RetentionDays > 0 {
		refuse("%s is @versioned with a history kept %d days (retentionDays), which Postgres prunes with a function", typeName, h.RetentionDays)
	}
	if len(h.PruneKeep) > 0 {
		refuse("%s is @versioned with versions other rows pin (pruneKeepReferencedBy), which Postgres's prune keeps", typeName)
	}
}

// sqliteHistoryTable is the history table of h in SQLite's types: its
// columns as Postgres's, recorded_at stamped by the database's clock.
func sqliteHistoryTable(h sqlgen.HistoryTable, origin string, ns *pgNamespace) (*Table, error) {
	table, err := historyTable(h, origin, ns)
	if err != nil {
		return nil, err
	}
	for _, col := range table.Columns {
		// clock_timestamp() is Postgres's: SQLite's clock is the same within
		// a statement either way.
		if strings.EqualFold(col.Default, "clock_timestamp()") {
			col.Default = "now()"
		}
	}
	return table, sqliteTable(table)
}

// sqliteHistoryTriggers are the triggers that keep h, the history of
// source, on SQLite: the bump, the capture of an insert and of an update,
// and the delete's tombstone.
func sqliteHistoryTriggers(h sqlgen.HistoryTable, source *Table) []*Trigger {
	table, history, key := qs(source.Name), qs(h.Name), qs(h.KeyColumn)
	columns := "(" + key + ", _version, operation, data)"
	captured := func(operation string) string {
		return "INSERT INTO " + history + " " + columns + " SELECT " + key + ", _version, '" + operation + "', " +
			sqliteHistoryImage(source, "", nil) + " FROM " + table + " WHERE rowid = NEW.rowid;"
	}
	tombstone := "INSERT INTO " + history + " " + columns + " VALUES (OLD." + key + ", OLD._version + 1, 'DELETE', " +
		sqliteHistoryImage(source, "OLD.", map[string]string{"_version": "OLD._version + 1"}) + ");"
	origin := source.Origin
	return []*Trigger{
		{Name: h.VersionTriggerName, Table: source.Name, Origin: origin,
			Definition: sqliteBumpTrigger(sqlgen.OptimisticTable{TableName: source.Name, TriggerName: h.VersionTriggerName})},
		{Name: h.WriteTriggerName + "_insert", Table: source.Name, Origin: origin,
			Definition: "CREATE TRIGGER " + qs(h.WriteTriggerName+"_insert") + " AFTER INSERT ON " + table + " FOR EACH ROW\nBEGIN\n  " + captured("INSERT") + "\nEND"},
		{Name: h.WriteTriggerName + "_update", Table: source.Name, Origin: origin,
			Definition: "CREATE TRIGGER " + qs(h.WriteTriggerName+"_update") + " AFTER UPDATE ON " + table + " FOR EACH ROW WHEN NEW._version IS NOT OLD._version\nBEGIN\n  " + captured("UPDATE") + "\nEND"},
		{Name: h.DeleteTriggerName, Table: source.Name, Origin: origin,
			Definition: "CREATE TRIGGER " + qs(h.DeleteTriggerName) + " AFTER DELETE ON " + table + " FOR EACH ROW\nBEGIN\n  " + tombstone + "\nEND"},
	}
}

// historyImageChunk is how many columns one json_object of an image takes:
// SQLite allows a function 127 arguments unless built for more, two a
// column, so a wider table's image sets the rest with json_set.
const historyImageChunk = 60

// sqliteHistoryImage is the JSON object of table's columns, each read as
// prefix and its name (OLD., or the table's own for an empty prefix), but
// those table's @versioned leaves out, with replaced columns' values
// written as given: a column that holds JSON is its JSON.
func sqliteHistoryImage(table *Table, prefix string, replaced map[string]string) string {
	var names, values []string
	for _, col := range table.Columns {
		if slices.Contains(table.HistoryExclude, col.Name) {
			continue
		}
		value := prefix + qs(col.Name)
		switch {
		case replaced[col.Name] != "":
			value = replaced[col.Name]
		case col.Holds != "":
			value = "json(" + value + ")"
		}
		names, values = append(names, col.Name), append(values, value)
	}
	if len(names) == 0 {
		return "json_object()"
	}
	// The first chunk is a json_object, and each after it sets its members
	// with json_set, which keeps a null where json_patch would drop it.
	var image string
	for start := 0; start < len(names); start += historyImageChunk {
		end := min(start+historyImageChunk, len(names))
		var args []string
		for i := start; i < end; i++ {
			if start == 0 {
				args = append(args, literal(names[i]), values[i])
			} else {
				args = append(args, literal(`$."`+names[i]+`"`), values[i])
			}
		}
		if start == 0 {
			image = "json_object(" + strings.Join(args, ", ") + ")"
		} else {
			image = "json_set(" + image + ", " + strings.Join(args, ", ") + ")"
		}
	}
	return image
}

// sqliteSeedHistory is the step that makes a table that holds rows
// @versioned on SQLite: its history triggers, each one of the same name
// dropped first (the bump of a table that was @optimistic), and one image
// of each row the table holds, at its version, as Postgres's seeds it.
func sqliteSeedHistory(c *change) *Step {
	s := c.seed
	var statements []string
	for _, t := range s.triggers {
		if s.replaced[t.Name] {
			statements = append(statements, "DROP TRIGGER IF EXISTS "+qs(t.Name))
		}
		statements = append(statements, t.Definition)
	}
	key := qs(s.source.PrimaryKey.Columns[0])
	statements = append(statements, fmt.Sprintf("INSERT INTO %s (%s, _version, operation, data) SELECT %s, _version, 'INSERT', %s FROM %s",
		qs(s.history.Name), key, key, sqliteHistoryImage(s.source, "", nil), qs(s.source.Name)))
	step := sqliteStep(c, statements...)
	blocking(step, fmt.Sprintf("Seeding %s writes one image of every row of %s while the step holds the database's write lock.", s.history.Name, s.source.Name))
	return step
}
