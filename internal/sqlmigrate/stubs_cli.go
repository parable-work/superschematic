package sqlmigrate

import ir "github.com/parable-work/superschematic/ir"

// SourceReads returns the columns of database's tables that consumer's
// @source views read: for every view whose @source target is a table type
// of database, the column behind each field, found by its origin in model
// (a relation field reads its foreign-key column; a @virtual field reads
// nothing). Reads are sorted by table, column, then via.
func SourceReads(consumer *ir.Schema, database string, model *Model) ([]Read, error) {
	return nil, errNotImplemented
}

// ParseRename parses a --rename value: "old=new" for a table, or
// "oldTable.oldColumn=newTable.newColumn" for a column.
func ParseRename(value string) (Rename, error) {
	return Rename{}, errNotImplemented
}

// SQL renders the plan as one SQL script for a reader: a header comment
// with the service, dialect and hashes, then each step's statements under
// a comment naming its index, phase, subject and hazards.
func (p *Plan) SQL() string {
	return ""
}

// Markdown renders the plan for a pull request: a hazard summary, then the
// steps by phase, each with its hazards and its SQL in a fenced block.
func (p *Plan) Markdown() string {
	return ""
}

// Unallowed returns the plan's hazards whose class is in classes and whose
// ID allow does not list, in step order.
func (p *Plan) Unallowed(classes []HazardClass, allow []string) []*Hazard {
	return nil
}
