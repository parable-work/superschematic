package sqlutil

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// The relations a read includes and a filter reaches through (D90): a
// table's to-one relations to the tables of its schema, and its one-to-many
// ones, its @hasMany fields and the to-one relations of other tables that
// point at it. Both ORMs read a relation's rows with the target's own find,
// whose statements TableStatementsOf writes, so an include sends what a
// find of the target sends, and a filter through a relation is one
// condition of the row's own WHERE, which a page's keyset and order leave
// as they are.

// ManyLimit is the rows a one-to-many include reads of a relation for a
// read's rows: the Go ORM's hydration of a @hasMany field reads them all, up
// to this many.
const ManyLimit = 1000000

// ToOne is a to-one relation of a table to a table of its schema: include
// loads the row its foreign key names, and a filter through it keeps the
// rows whose relation points at a live row of the target that the target's
// filter finds.
type ToOne struct {
	// Field is the relation field, Column its foreign key's column,
	// unquoted, and Target the table type it points at.
	Field  string
	Column string
	Target string

	// Nullable is a relation whose foreign key may be null. Optional is a
	// relation an include may find no row of: one that is Nullable, or
	// whose target is soft-deletable, since an include, as a find, leaves
	// the target's deleted rows out.
	Nullable bool
	Optional bool

	// Probe is the condition of a filter through the relation: the foreign
	// key, qualified by the table, in the keys of the target's rows of the
	// WHERE %s, which the target's filter writes, then TargetLive. A row
	// whose foreign key is null is not in it. The subquery names the target
	// by its table, and nothing outside it, so a filter through a relation
	// of a table to itself, or through a path of them, reads each level's
	// own rows.
	Probe string
	// TargetLive leaves the target's deleted rows out of the probe: the
	// target's LiveCondition, empty for a target without deletedAt.
	TargetLive string
}

// ToMany is a one-to-many relation of a table: include loads the rows of
// Source whose to-one relation Field, whose foreign key's column is Column,
// points at the table's row.
type ToMany struct {
	// Name is the include's member: the table's @hasMany field when one
	// holds the rows (HasMany), else Source in the plural, in camel case
	// (notes), with By and Field in Pascal case when Source points at the
	// table through more than one field or the plural is taken
	// (lineItemsBySubCategory), else the reference's filter name.
	Name    string
	Source  string
	Field   string
	Column  string
	HasMany bool
}

// attachRelations gives each table its to-one relations, in column order,
// and its one-to-many ones: its @hasMany fields in schema order, each
// through the first to-one relation of its target that points back, as
// the Go ORM hydrates one, then the references no @hasMany field holds, in
// the order of their filters.
func attachRelations(tables []TableModel) {
	byType := make(map[string]*TableModel, len(tables))
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	for i := range tables {
		table := &tables[i]
		for _, c := range table.Columns {
			target := byType[c.Relation]
			if c.Relation == "" || target == nil {
				continue
			}
			rel := ToOne{
				Field:    c.Field,
				Column:   c.Name,
				Target:   target.Type,
				Nullable: !c.Required,
				Optional: !c.Required || target.SoftDelete,
				Probe:    c.Qualified + " IN (SELECT " + Qualify(target.Name, target.Key) + " FROM " + target.Quoted + "%s)",
			}
			if target.SoftDelete {
				rel.TargetLive = target.Quoted + "." + target.AuditQuoted(AuditDeletedAt) + " IS NULL"
			}
			table.ToOne = append(table.ToOne, rel)
		}
	}
	for i := range tables {
		table := &tables[i]
		taken := map[string]bool{}
		for _, c := range table.Columns {
			taken[c.Field] = true
			if c.Relation != "" {
				taken[c.Field+"Id"] = true
			}
		}
		held := map[string]bool{}
		for _, rel := range table.HasMany {
			source := byType[rel.Target]
			if source == nil {
				continue
			}
			for _, c := range source.Columns {
				if c.Relation != table.Type {
					continue
				}
				table.ToMany = append(table.ToMany, ToMany{Name: rel.Field, Source: source.Type, Field: c.Field, Column: c.Name, HasMany: true})
				taken[rel.Field] = true
				held[source.Type+"."+c.Field] = true
				break
			}
		}
		through := map[string]int{}
		for _, ref := range table.ReferencedBy {
			through[ref.Source]++
		}
		for _, ref := range table.ReferencedBy {
			if held[ref.Source+"."+ref.Field] {
				continue
			}
			plural := codegen.ToCamelCase(codegen.ToPlural(codegen.ToKebabCase(ref.Source)))
			candidates := []string{plural + "By" + codegen.ToPascalCase(ref.Field), lowerFirstRune(ref.Filter)}
			if through[ref.Source] == 1 {
				candidates = append([]string{plural}, candidates...)
			}
			for _, name := range candidates {
				if taken[name] {
					continue
				}
				taken[name] = true
				column := ""
				if source := byType[ref.Source]; source != nil {
					for _, c := range source.Columns {
						if c.Field == ref.Field {
							column = c.Name
						}
					}
				}
				table.ToMany = append(table.ToMany, ToMany{Name: name, Source: ref.Source, Field: ref.Field, Column: column})
				break
			}
		}
	}
}

// lowerFirstRune is s with its first letter in lower case.
func lowerFirstRune(s string) string {
	if s == "" || s[0] < 'A' || s[0] > 'Z' {
		return s
	}
	return string(s[0]+'a'-'A') + s[1:]
}

// ToOneOf returns the table's to-one relation through field, or nil.
func (t *TableModel) ToOneOf(field string) *ToOne {
	for i := range t.ToOne {
		if t.ToOne[i].Field == field {
			return &t.ToOne[i]
		}
	}
	return nil
}

// ToManyNamed returns the table's one-to-many relation named name, or nil.
func (t *TableModel) ToManyNamed(name string) *ToMany {
	for i := range t.ToMany {
		if t.ToMany[i].Name == name {
			return &t.ToMany[i]
		}
	}
	return nil
}
