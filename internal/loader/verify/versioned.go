package verify

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// maxRetentionDays is the longest @versioned retentionDays: the largest
// Postgres INTEGER, which the generated prune function's retention_days
// is. A larger default creates a function no call can run, and the version
// graph's core refuses a descriptor that carries it.
const maxRetentionDays = math.MaxInt32

// sqlIdentifierPattern matches the unquoted snake_case identifiers the prune
// exclusion is interpolated into DDL as; anything else is rejected up front.
var sqlIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// auditFields are the fields a graph member may exclude from its history
// without a conflict unit, even when required: they are never content
// (D17), and the shell writes them on every row it rebuilds.
var auditFields = map[string]bool{"createdAt": true, "createdBy": true, "updatedAt": true, "updatedBy": true}

// checkVersioned validates the @versioned and @optimistic contracts. The
// generators rely on a single row key for history and only know how to
// version DB tables.
func checkVersioned(schema *ir.Schema, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td.Optimistic {
			switch {
			case td.Role != ir.RoleDBTable:
				r.errorf(td.Owner, "%s: @optimistic is only allowed on DB table types (this type has role %s)", td.Name, td.Role)
			case td.Versioned:
				r.errorf(td.Owner, "%s: @versioned implies @optimistic; declare one of them", td.Name)
			default:
				checkVersionFieldCollision(td, "@optimistic", r)
			}
		}
		if !td.Versioned {
			continue
		}

		if td.Role != ir.RoleDBTable {
			r.errorf(td.Owner, "%s: @versioned is only allowed on DB table types (this type has role %s)", td.Name, td.Role)
			continue
		}
		checkVersionFieldCollision(td, "@versioned", r)

		keyCount := 0
		for _, fd := range td.Fields {
			if fd.Key {
				keyCount++
			}
		}
		if keyCount != 1 {
			r.errorf(td.Owner, "%s: @versioned requires exactly one @key field, found %d", td.Name, keyCount)
		}

		if td.VersionedConfig != nil {
			checkVersionedConfig(schema, td, keyCount == 1, r)
		}
	}
}

// checkVersionFieldCollision refuses a declared field the generated
// _version field would collide with: both are Version in Go and Rust.
func checkVersionFieldCollision(td *ir.TypeDef, decorator string, r *Result) {
	for _, fd := range td.Fields {
		if fd.Name == "version" || fd.Name == "_version" {
			r.errorf(td.Owner, "%s.%s: a %s type cannot declare a field named %s; it collides with the generated _version field", td.Name, fd.Name, decorator, fd.Name)
		}
	}
}

func checkVersionedConfig(schema *ir.Schema, td *ir.TypeDef, singleKey bool, r *Result) {
	cfg := td.VersionedConfig
	if cfg.RetentionDays != nil && *cfg.RetentionDays <= 0 {
		r.errorf(td.Owner, "%s: @versioned retentionDays must be greater than 0", td.Name)
	}
	if cfg.RetentionDays != nil && *cfg.RetentionDays > maxRetentionDays {
		r.errorf(td.Owner, "%s: @versioned retentionDays must be at most %d, the largest Postgres INTEGER the prune function takes, got %d", td.Name, maxRetentionDays, *cfg.RetentionDays)
	}
	if cfg.PartitionBy != "" && cfg.PartitionBy != "month" {
		r.errorf(td.Owner, "%s: @versioned partitionBy must be \"month\" when set", td.Name)
	}
	if len(cfg.PruneKeepReferencedBy) > 0 && cfg.RetentionDays == nil {
		r.errorf(td.Owner, "%s: @versioned pruneKeepReferencedBy requires retentionDays (it only shapes the generated prune function)", td.Name)
	}
	seen := map[ir.PruneReference]bool{}
	for _, pin := range cfg.PruneKeepReferencedBy {
		// Every entry is interpolated into DDL, so each one is checked; a
		// later entry is exactly as dangerous as the first.
		for _, part := range []struct {
			name  string
			value string
		}{
			{"table", pin.Table},
			{"keyColumn", pin.KeyColumn},
			{"versionColumn", pin.VersionColumn},
		} {
			if !sqlIdentifierPattern.MatchString(part.value) {
				r.errorf(td.Owner, "%s: @versioned pruneKeepReferencedBy %s must be a snake_case SQL identifier, got %q", td.Name, part.name, part.value)
			}
		}
		if seen[*pin] {
			r.errorf(td.Owner, "%s: @versioned pruneKeepReferencedBy repeats %s(%s, %s)", td.Name, pin.Table, pin.KeyColumn, pin.VersionColumn)
		}
		seen[*pin] = true
		if singleKey {
			checkPinColumns(schema, td, pin, r)
		}
	}
	checkExclude(schema, td, r)
}

// checkPinColumns checks a pin whose table is a DB type of this schema: its
// key column holds the versioned key's type and its version column is an
// Int64. A table outside the schema is left to the syntactic checks.
func checkPinColumns(schema *ir.Schema, td *ir.TypeDef, pin *ir.PruneReference, r *Result) {
	pinned := dbTableNamed(schema, pin.Table)
	if pinned == nil {
		return
	}
	columns := tableColumns(schema, pinned)
	where := fmt.Sprintf("%s: @versioned pruneKeepReferencedBy %s", td.Name, pin.Table)

	var key *ir.FieldDef
	for _, fd := range td.Fields {
		if fd.Key {
			key = fd
		}
	}
	switch keyColumn, ok := columns[pin.KeyColumn]; {
	case !ok:
		r.errorf(td.Owner, "%s: keyColumn %s is not a column of %s", where, pin.KeyColumn, pinned.Name)
	case !sameColumnType(schema, keyColumn, key.TypeRef.Name):
		r.errorf(td.Owner, "%s: keyColumn %s must hold the key type of %s (%s)", where, pin.KeyColumn, td.Name, key.TypeRef.Name)
	}
	switch versionColumn, ok := columns[pin.VersionColumn]; {
	case !ok:
		r.errorf(td.Owner, "%s: versionColumn %s is not a column of %s", where, pin.VersionColumn, pinned.Name)
	case versionColumn != int64Column:
		r.errorf(td.Owner, "%s: versionColumn %s must be a Generic.Int64 column", where, pin.VersionColumn)
	}
}

// int64Column is the column type of a Generic.Int64 field and of the
// _version column.
const int64Column = "Generic.Int64"

// generatedIDType is the type of the id the generators give a table
// without a @key.
const generatedIDType = "Identity.UUID"

// dbTableNamed returns the DB table type of this schema whose table is
// named table (snake_case), or nil.
func dbTableNamed(schema *ir.Schema, table string) *ir.TypeDef {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if isDBTable(td) && codegen.ToSnakeCase(td.Name) == table {
			return td
		}
	}
	return nil
}

func isDBTable(td *ir.TypeDef) bool {
	return td != nil && td.Role == ir.RoleDBTable && !td.JsonField
}

// tableColumns maps each column of a DB table type to the type its values
// hold: a field's type, and a to-one relation's (or a @hasMany back
// reference's) target key type. A list, a map or a JSON column holds no
// comparable type and maps to "". A table without a @key has the generated
// UUID id.
func tableColumns(schema *ir.Schema, td *ir.TypeDef) map[string]string {
	columns := map[string]string{}
	hasKey := false
	for _, fd := range td.Fields {
		if fd.Key {
			hasKey = true
		}
		switch {
		case isTableRelation(schema, fd) && (fd.TypeRef.IsArray || fd.TypeRef.IsMap):
		case isTableRelation(schema, fd):
			columns[codegen.ToSnakeCase(fd.Name)+"_id"] = keyTypeName(schema.Types[relationTarget(fd)])
		case fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.JsonField:
			columns[codegen.ToSnakeCase(fd.Name)] = ""
		default:
			columns[codegen.ToSnakeCase(fd.Name)] = fd.TypeRef.Name
		}
	}
	if _, ok := columns["id"]; !ok && !hasKey {
		columns["id"] = generatedIDType
	}
	if td.Versioned || td.Optimistic {
		columns["_version"] = int64Column
	}
	for _, name := range sortedTypeNames(schema.Types) {
		parent := schema.Types[name]
		if !isDBTable(parent) {
			continue
		}
		for _, fd := range parent.Fields {
			if fd.HasMany && !fd.ManyToMany && fd.TypeRef.IsArray && !fd.JsonField && fd.TypeRef.Name == td.Name {
				if column := codegen.ToSnakeCase(parent.Name) + "_id"; columns[column] == "" {
					columns[column] = keyTypeName(parent)
				}
			}
		}
	}
	return columns
}

// sameColumnType reports whether a column holding columnType can hold a
// value of keyType: the same type, or two scalars stored as the same SQL
// type.
func sameColumnType(schema *ir.Schema, columnType, keyType string) bool {
	if columnType == "" {
		return false
	}
	if columnType == keyType {
		return true
	}
	a, b := schema.Scalars[columnType], schema.Scalars[keyType]
	return a != nil && b != nil && a.TypeMappings["sql"] != "" &&
		strings.EqualFold(a.TypeMappings["sql"], b.TypeMappings["sql"])
}

// isTableRelation reports whether fd refers to a DB table of this schema:
// a to-one relation (a column) or a list relation (none).
func isTableRelation(schema *ir.Schema, fd *ir.FieldDef) bool {
	return !fd.JsonField && isDBTable(schema.Types[relationTarget(fd)])
}

func relationTarget(fd *ir.FieldDef) string {
	if fd.Relation != nil && fd.Relation.Type != "" {
		return fd.Relation.Type
	}
	return fd.TypeRef.Name
}

// checkExclude validates @versioned({ exclude }): each name is a field of
// the type the history readers do not need. The readers find a row by its
// key, filter a soft-deleted image on deletedAt and find a relation's rows
// by its column, so none of those can be excluded. A graph member's
// commits are read back from its history, so it may exclude only what is
// not content: a field whose conflict unit is excluded, or an audit field.
// Revert and Merge rebuild its rows from history images, so a field it
// excludes must also be nullable; the shell writes the audit fields itself.
func checkExclude(schema *ir.Schema, td *ir.TypeDef, r *Result) {
	seen := map[string]bool{}
	for _, name := range td.VersionedConfig.Exclude {
		if seen[name] {
			r.errorf(td.Owner, "%s: @versioned exclude repeats %s", td.Name, name)
			continue
		}
		seen[name] = true
		fd := fieldNamed(td, name)
		switch {
		case fd == nil:
			r.errorf(td.Owner, "%s: @versioned exclude %q is not a field of %s", td.Name, name, td.Name)
		case fd.Key:
			r.errorf(td.Owner, "%s: @versioned exclude cannot name the key %s: history rows are found by it", td.Name, name)
		case name == "deletedAt":
			r.errorf(td.Owner, "%s: @versioned exclude cannot name deletedAt: the history readers tell a soft-deleted image by it", td.Name)
		case isTableRelation(schema, fd):
			r.errorf(td.Owner, "%s: @versioned exclude cannot name the relation %s: its as-of reader finds history rows by it", td.Name, name)
		case td.GraphMember != nil && fd.ConflictUnit != ir.ConflictUnitExcluded && !auditFields[name]:
			r.errorf(td.Owner, "%s: a @graphMember may exclude only audit fields and fields with @conflictUnit(\"excluded\"), and %s is content its commits read back from history", td.Name, name)
		case td.GraphMember != nil && fd.Required && !auditFields[name]:
			r.errorf(td.Owner, "%s: a @graphMember may exclude only nullable fields besides its audit fields, and %s is required: Revert and Merge rebuild rows from history images, which would write it as NULL", td.Name, name)
		}
	}
}
