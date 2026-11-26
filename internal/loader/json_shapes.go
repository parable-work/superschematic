package loader

import (
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// inferJSONShapes marks as @jsonField each class of a DB schema that is a
// JSON shape though it does not say so: a class with no @key, and nothing
// else a table declares, that the schema holds only in @jsonField fields
// and in the fields of other JSON shapes. Such a class is the type of a
// JSONB column, never a table of its own; built as one, it was a table no
// row was written to, and a list of another such class in it failed the
// build's table model ("lacks @hasMany or @manyToMany"), which turned off
// every behavior's checks of the database. A class some other field holds
// (a relation, a list of rows, an API's view) stays a table, as does one no
// field holds. It reports the classes it marked, sorted.
func inferJSONShapes(schema *ir.Schema) []string {
	if schema == nil || schema.Kind != ir.SchemaKindDB {
		return nil
	}
	bases := map[string]bool{}
	for _, td := range schema.Types {
		if td != nil && td.Extends != "" {
			bases[td.Extends] = true
		}
	}
	candidates := map[string]bool{}
	for name, td := range schema.Types {
		if mayBeJSONShape(td) && !bases[name] {
			candidates[name] = true
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	// Each candidate's holders: the type and field of each field whose
	// type is the candidate, in a list or a map or alone.
	type holder struct {
		owner *ir.TypeDef
		field *ir.FieldDef
	}
	holders := map[string][]holder{}
	for _, td := range schema.Types {
		if td == nil {
			continue
		}
		for _, fd := range td.Fields {
			if fd != nil && candidates[fd.TypeRef.Name] {
				holders[fd.TypeRef.Name] = append(holders[fd.TypeRef.Name], holder{td, fd})
			}
		}
	}
	// A candidate is a shape once every holder is a @jsonField field or a
	// field of a shape, explicit or inferred; a shape another one holds
	// becomes one on a later round.
	var marked []string
	for changed := true; changed; {
		changed = false
		for name := range candidates {
			held := holders[name]
			if len(held) == 0 {
				continue
			}
			json := true
			for _, h := range held {
				if h.owner.Name == name || (!h.field.JsonField && !h.owner.JsonField) || h.field.Relation != nil || h.field.HasMany || h.field.ManyToMany {
					json = false
					break
				}
			}
			if !json {
				continue
			}
			schema.Types[name].JsonField = true
			delete(candidates, name)
			marked = append(marked, name)
			changed = true
		}
	}
	slices.Sort(marked)
	return marked
}

// mayBeJSONShape reports whether td, a type of a DB schema, declares
// nothing a JSON shape could not: a table's role, no @key field, and none
// of a table's own declarations (traits and behaviors, indexes, raw SQL, a
// table name, a queue, a projection, a version graph, the user model, an
// audience, the access tables, references, attachments, events and the
// rest), nor is one the loader added.
func mayBeJSONShape(td *ir.TypeDef) bool {
	if td == nil || td.Role != ir.RoleDBTable || td.JsonField || td.IsTrait || td.Origin != "" || len(td.Fields) == 0 {
		return false
	}
	if len(td.Implements) > 0 || len(td.Behaviors) > 0 || len(td.Indexes) > 0 || len(td.RawSQL) > 0 || td.SQLName != "" ||
		len(td.SeedRoles) > 0 || len(td.EnqueueOnChange) > 0 || len(td.StateMachines) > 0 || len(td.Fences) > 0 ||
		len(td.Events) > 0 || len(td.CalendarSources) > 0 {
		return false
	}
	if td.Source != nil || td.VersionedConfig != nil || td.VersionGraph != nil || td.GraphMember != nil ||
		td.User != nil || td.UserRole != nil || td.IdempotencyKeys != nil || td.BehaviorTable != nil || td.BehaviorQueue != nil ||
		td.Projection != nil || td.Queue != nil || td.Audience != nil || td.AccessGrants != nil || td.AccessLog != nil ||
		td.RowReferences != nil || td.Attachments != nil || td.RowTable != nil || td.Notifier != nil ||
		td.ChannelIdentities != nil || td.Agents != nil || td.Party != nil || td.Conversations != nil ||
		td.RunRequests != nil || td.Searchable != nil || td.Query != nil {
		return false
	}
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			return false
		}
	}
	return true
}
