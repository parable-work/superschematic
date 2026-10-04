package sqlmigrate

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// SourceReads returns the columns of database's tables that consumer's
// @source views read: for every view whose @source target is a table type
// of database, the column behind each field, found by its origin in model
// (a relation field reads its foreign-key column; a @virtual field reads
// nothing). Reads are sorted by table, column, then via.
//
// A target is split as the loader's @source verification splits it: the
// service is everything before the last dot. A view of a type that is not
// one of model's entity tables, such as a projection or a base class,
// reads nothing here. A nil model is an empty database and has no reads;
// a model of another service is an error.
func SourceReads(consumer *ir.Schema, database string, model *Model) ([]Read, error) {
	if consumer == nil || model == nil {
		return nil, nil
	}
	if model.Service != database {
		return nil, fmt.Errorf("reads of %s: the model is %s's", database, model.Service)
	}
	tables := make(map[string]*Table, len(model.Tables))
	for _, table := range model.Tables {
		if table.Kind == TableEntity && table.Origin != "" {
			tables[table.Origin] = table
		}
	}

	var reads []Read
	for _, view := range consumer.Types {
		if view.Source == nil {
			continue
		}
		service, typeName := splitSourceTarget(view.Source.Target)
		if service != database {
			continue
		}
		table := tables[typeName]
		if table == nil {
			continue
		}
		for _, field := range view.Fields {
			if field.Virtual || slices.Contains(view.Source.Virtual, field.Name) {
				continue
			}
			origin := typeName + "." + field.Name
			for _, column := range table.Columns {
				if column.Origin != origin {
					continue
				}
				reads = append(reads, Read{
					Reader: consumer.Name,
					Via:    view.Name + "." + field.Name,
					Table:  table.Name,
					Column: column.Name,
				})
			}
		}
	}
	sortReads(reads)
	return reads, nil
}

// MergeReads returns the reads of every set, each read once, sorted by
// table, column, reader, then via: the order SourceReads returns, extended
// to several readers. A service that is both in a schemas root and named
// with --reader reads each column once.
func MergeReads(sets ...[]Read) []Read {
	var reads []Read
	for _, set := range sets {
		reads = append(reads, set...)
	}
	sortReads(reads)
	return slices.Compact(reads)
}

func sortReads(reads []Read) {
	slices.SortFunc(reads, func(a, b Read) int {
		return cmp.Or(
			cmp.Compare(a.Table, b.Table),
			cmp.Compare(a.Column, b.Column),
			cmp.Compare(a.Reader, b.Reader),
			cmp.Compare(a.Via, b.Via),
		)
	})
}

// splitSourceTarget splits "service.Type" at its last dot, as
// verify.splitTarget does; an unqualified target has no service.
func splitSourceTarget(target string) (service, typeName string) {
	if i := strings.LastIndex(target, "."); i >= 0 {
		return target[:i], target[i+1:]
	}
	return "", target
}
