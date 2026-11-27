package ormgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The Go ORM's per-record access (D156, D180): access.go holds the caller
// an operation's reads and writes keep to, the conditions that keep them
// to it, and the grants and the access log of the database. It is written
// for a database with an @audience table or an @accessGrants table, and
// for one with an @accessLog table, which a reveal records in. Every
// repository of such a database keeps its reads to the caller: an
// @audience table's to the rows whose audience holds them, and any
// table's, for a party or an agent run that calls through grants, to the
// rows one of its grants reaches.

// AccessModel is what access.go is generated from.
type AccessModel struct {
	// Tables are the database's tables, by type, sorted: each one's
	// audience, key and relations, which the conditions of a caller's
	// reads and a grant's paths read.
	Tables []AccessTable

	// Grants is the @accessGrants table's type, and Roles the UserRole
	// table's, whose rows a grant's role names; empty without grants.
	Grants string
	Roles  string

	// Log is the @accessLog table's type; empty without one.
	Log string

	// Held reports whether the repositories keep their reads to a caller:
	// a database with an @audience table or grants. A database with an
	// access log alone has only its log.
	Held bool
}

// AccessTable is one table as access reads it.
type AccessTable struct {
	// Type is the table's type, and Repository the Go member of its
	// repository's getter (Get<Repository>Repository).
	Type string

	// Effective and Agents are an @audience table's resolved columns,
	// qualified by its table; empty for a table no audience holds. Probe,
	// on a projection of one, holds the view to the source's rows of the
	// WHERE %s.
	Effective, Agents, Probe string

	// Key is the table's key column, qualified, and KeyKind how a grant's
	// target id binds to it: uuid, text or int. KeyGo is the key's member
	// of the table's filter, and KeyFilter its filter type, which a read of
	// a grant's target by its key names; empty KeyGo for a table whose key
	// no grant targets (not a UUID).
	Key       string
	KeyKind   string
	KeyGo     string
	KeyFilter string

	// Steps are the table's relations a path steps through: each to-one
	// relation and each one-to-many one, by name.
	Steps []AccessStep
}

// AccessStep is one relation a path steps through (D108, D156): Name its
// member, Target the table it reaches, Probe the condition that keeps this
// table's rows whose relation reaches a row of Target of the WHERE %s,
// and Live the target's live condition, empty for a target with no
// deletedAt.
type AccessStep struct {
	Name, Target, Probe, Live string
}

// accessOf is the access model of schema's tables, nil for a database
// with no @audience table, no @accessGrants table and no @accessLog.
func accessOf(schema *ir.Schema, tables []sqlutil.TableModel, repositories []Repository, projections []ProjectionRepository) (*AccessModel, error) {
	grants, log := schema.AccessGrantsTable(), schema.AccessLogTable()
	audiences := schema.AudienceTables()
	if grants == nil && log == nil && len(audiences) == 0 {
		return nil, nil
	}
	model := &AccessModel{Held: grants != nil || len(audiences) > 0}
	if grants != nil {
		model.Grants = grants.Name
		role := schema.UserRoleTable()
		if role == nil {
			return nil, fmt.Errorf("ormgen: %s is @accessGrants, and the database has no UserRole table whose rows a grant's role names (D156)", grants.Name)
		}
		model.Roles = role.Name
	}
	if log != nil {
		model.Log = log.Name
	}
	served := map[string]bool{}
	for _, repo := range repositories {
		served[repo.TypeName] = true
	}
	byType := make(map[string]*sqlutil.TableModel, len(tables))
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	for i := range tables {
		table := &tables[i]
		if !served[table.Type] {
			continue
		}
		key := table.KeyColumn()
		if key == nil {
			continue
		}
		at := AccessTable{Type: table.Type, Key: key.Qualified}
		switch key.Filter {
		case sqlutil.FilterUUID:
			at.KeyKind, at.KeyGo, at.KeyFilter = "uuid", codegen.ToPascalCase(key.Field), "UUIDFilter"
		case sqlutil.FilterInt:
			at.KeyKind = "int"
		default:
			at.KeyKind = "text"
		}
		if table.Audience != nil {
			at.Effective, at.Agents = table.Audience.Effective, table.Audience.Agents
		}
		for _, rel := range table.ToOne {
			if !served[rel.Target] {
				continue
			}
			at.Steps = append(at.Steps, AccessStep{Name: rel.Field, Target: rel.Target, Probe: rel.Probe, Live: rel.TargetLive})
		}
		for _, rel := range table.ToMany {
			source := byType[rel.Source]
			if source == nil || !served[rel.Source] {
				continue
			}
			at.Steps = append(at.Steps, AccessStep{Name: rel.Name, Target: rel.Source, Probe: ToManyProbe(table, source, rel), Live: sourceLive(source)})
		}
		model.Tables = append(model.Tables, at)
	}
	// A projection keyed by its source's key reads as its source's rows
	// the caller sees (D156); a party or an agent run reaches its rows
	// only where its role names the projection.
	for _, p := range projections {
		at := AccessTable{Type: p.TypeName, Key: p.QualifiedKey, KeyKind: "text"}
		if p.Key.IsUUIDScalar {
			at.KeyKind = "uuid"
		}
		if p.Audience != nil {
			at.Effective, at.Agents, at.Probe = p.Audience.Effective, p.Audience.Agents, p.Audience.Probe
		}
		model.Tables = append(model.Tables, at)
	}
	slices.SortFunc(model.Tables, func(a, b AccessTable) int { return strings.Compare(a.Type, b.Type) })
	return model, nil
}

// ToManyProbe is the condition of a filter through a one-to-many relation
// of table (D156, D180): the table's key in the foreign keys of the rows
// of the relation's source of the WHERE %s.
func ToManyProbe(table, source *sqlutil.TableModel, rel sqlutil.ToMany) string {
	return sqlutil.Qualify(table.Name, table.Key) + " IN (SELECT " + sqlutil.Qualify(source.Name, rel.Column) + " FROM " + source.Quoted + "%s)"
}

// sourceLive is the live condition of a soft-deletable table, empty for
// any other.
func sourceLive(table *sqlutil.TableModel) string {
	if !table.SoftDelete {
		return ""
	}
	return table.Quoted + "." + table.AuditQuoted(sqlutil.AuditDeletedAt) + " IS NULL"
}

// RequiresSchemaRuntime reports whether go.mod requires the schema
// runtime: for its fieldcrypt package, which seals and opens a schema's
// Encrypted<T> fields (D85), or its access package, which holds the
// principal a database with per-record access keeps its reads to (D180).
func (o *ORMOutput) RequiresSchemaRuntime() bool {
	return o.HasEncryptedFields || o.Access != nil || o.RowFeatures != nil || o.CrossTables != nil
}

// attachManyRelations gives each repository the one-to-many relations its
// filter reaches through (D108, D180), each named as a read includes it
// (sqlutil.ToMany), then suffix (go.orm_relation_filter_suffix, D163).
// A relation whose source has no repository is left out.
func attachManyRelations(repositories []Repository, tables []sqlutil.TableModel, suffix string) {
	byType := make(map[string]*sqlutil.TableModel, len(tables))
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	served := map[string]bool{}
	for _, repo := range repositories {
		served[repo.TypeName] = true
	}
	for i := range repositories {
		repo := &repositories[i]
		table := byType[repo.TypeName]
		if table == nil {
			continue
		}
		for _, rel := range table.ToMany {
			source := byType[rel.Source]
			if source == nil || !served[rel.Source] {
				continue
			}
			repo.ManyRelations = append(repo.ManyRelations, ManyRelation{
				ToMany:     rel,
				GoField:    codegen.ToPascalCase(rel.Name) + suffix,
				Probe:      ToManyProbe(table, source, rel),
				SourceLive: sourceLive(source),
			})
		}
	}
}

// HeldByAccess reports whether the database's repositories keep their
// reads to the caller of the operation (D156, D180): it has an @audience
// table or grants.
func (o *ORMOutput) HeldByAccess() bool {
	return o.Access != nil && o.Access.Held
}
