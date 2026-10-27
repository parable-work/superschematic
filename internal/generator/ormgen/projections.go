package ormgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A projection the ORM reads (D90): projections.go declares its row, a
// struct of the ORM's package, its filter, its sort and page types, and a
// read-only repository over its view, whose statements are sqlutil's
// TableStatementsOf of the view as sqlutil.ViewsOf models it, the
// statements the TypeScript repository runs too. Database.<Type>() reads
// it over the pool, and <Type>In(tx) in a transaction.

// ProjectionRepository is a projection's repository.
type ProjectionRepository struct {
	// TypeName is the projection's type, Name its repository's,
	// Relation the view as a reader names it, Doc the class's comment.
	TypeName string
	Name     string
	Relation string
	Doc      string

	// Fields are the view's columns, Members them as the scan templates
	// read them, Key the key's field and SortFields the fields FindPage
	// orders by, the key first.
	Fields     []Field
	Members    []ColumnMember
	Key        Field
	SortFields []Field

	// SQL are the view's statements, of which the repository runs the
	// reads.
	SQL sqlutil.TableStatements
}

// Comment is the row struct's doc comment: what it is, then the class's
// comment, each line commented.
func (p ProjectionRepository) Comment() string {
	out := "// " + p.TypeName + " is a row of the " + p.Relation + " view (D90)."
	if p.Doc != "" {
		out += "\n//"
		for _, line := range strings.Split(p.Doc, "\n") {
			out += "\n// " + strings.TrimSpace(line)
		}
	}
	return out
}

// SortFieldList names the sort fields, as Parse<Type>Sort's error lists
// them, as a table's repository does (Repository.SortFieldList).
func (p ProjectionRepository) SortFieldList() string {
	return Repository{SortFields: p.SortFields}.SortFieldList()
}

// SortExample is the sort field a doc comment shows.
func (p ProjectionRepository) SortExample() string {
	return Repository{SortFields: p.SortFields}.SortExample()
}

// StructType is the field's type in a projection's row struct: what the
// scan templates write into it, a pointer for a nullable enum, scalar or
// boolean, a slice for a list, the value otherwise.
func (f Field) StructType() string {
	switch {
	case f.IsArray:
		return "[]" + f.GoType
	case f.IsNullableEnum || f.IsNullableScalar || f.IsNullableBool:
		return "*" + f.GoType
	}
	return f.GoType
}

// FilterType is the Go filter of the field's column in a projection's
// filter, or "" for a column it holds no condition on: query.go's filter
// of its kind.
func (f Field) FilterType() string {
	switch f.FilterKind {
	case sqlutil.FilterUUID:
		return "UUIDFilter"
	case sqlutil.FilterString, sqlutil.FilterJSON:
		return "StringFilter"
	case sqlutil.FilterInt:
		return "Int64Filter"
	case sqlutil.FilterFloat:
		return "FloatFilter"
	case sqlutil.FilterBool:
		return "BoolFilter"
	case sqlutil.FilterDateTime:
		return "DateTimeFilter"
	case sqlutil.FilterDate:
		return "DateFilter"
	case sqlutil.FilterDuration:
		return "DurationFilter"
	case sqlutil.FilterPoint:
		return "GeoPointFilter"
	case sqlutil.FilterDecimal:
		return "DecimalFilter"
	}
	return ""
}

// ProjectionsUse reports whether projections.go uses the package what
// names: "sql" for a nullable text column's sql.NullString, "pgtype" for a
// date's or a point's scan, "time" for an interval's or a time key's, and
// "types" for a column of a scalar or an enum of the types package.
func (o *ORMOutput) ProjectionsUse(what string) bool {
	for _, p := range o.Projections {
		for _, f := range p.Fields {
			switch what {
			case "sql":
				if !f.IsRequired && !f.IsArray && f.GoType == "string" && !f.IsJSONField && !f.IsDateLike && !f.IsInterval && !f.IsGeoPoint && !f.IsVector {
					return true
				}
			case "pgtype":
				if f.IsGeoPoint || f.IsGeoPointList || (f.IsDateLike && !f.IsArray && !f.IsJSONField && !f.IsVector) {
					return true
				}
			case "time":
				if f.IsInterval || (f.Name == p.Key.Name && f.IsDateTimeScalar) {
					return true
				}
			case "types":
				if strings.Contains(f.StructType(), "types.") {
					return true
				}
			}
		}
	}
	return false
}

// projectionRepositories are the repositories of schema's projections with
// a key, sorted by name, each column classified as a table's column of its
// type (extractField), then held to the view sqlutil models: the same
// columns, filters and sorts.
func projectionRepositories(schema *ir.Schema, dependencies map[string]*ir.Schema, scalars map[string]scalarLookup, unions map[string]bool) ([]ProjectionRepository, error) {
	views, err := sqlutil.ViewsOf(schema, dependencies)
	if err != nil {
		return nil, err
	}
	var out []ProjectionRepository
	for i := range views {
		view := &views[i]
		td := schema.Types[view.Type]
		p := ProjectionRepository{
			TypeName: view.Type,
			Name:     view.Type + "Repository",
			Relation: td.Projection.Relation(),
			Doc:      codegen.DocText(td.Description, td.Comment),
			SQL:      sqlutil.TableStatementsOf(view),
		}
		if err := p.SQL.CheckFormats(); err != nil {
			return nil, err
		}
		for _, fd := range td.Fields {
			field := extractField(fd, schema, scalars, unions)
			// A view's column is no table's audit field or key, whatever
			// its name: it reads, filters and sorts as its type does.
			field.IsAuditField, field.IsAuditTimestamp, field.IsPrimaryKey = false, false, false
			column := view.Column(field.DBName)
			if column == nil {
				return nil, fmt.Errorf("ormgen: projection %s's field %s has no column %s in the view sqlutil models", view.Type, fd.Name, field.DBName)
			}
			field.FilterKind = goFilterKind(column.Filter)
			field.SQL = sqlutil.ColumnStatementsOf(column)
			field.Sortable = column.Sortable
			p.Fields = append(p.Fields, field)
		}
		for i := range p.Fields {
			p.Members = append(p.Members, ColumnMember{Field: &p.Fields[i]})
			if p.Fields[i].Name == view.KeyField {
				p.Key = p.Fields[i]
			}
		}
		p.SortFields = append(p.SortFields, p.Key)
		for _, f := range p.Fields {
			if f.Sortable && f.Name != view.KeyField {
				p.SortFields = append(p.SortFields, f)
			}
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b ProjectionRepository) int {
		switch {
		case a.TypeName < b.TypeName:
			return -1
		case a.TypeName > b.TypeName:
			return 1
		}
		return 0
	})
	return out, nil
}
