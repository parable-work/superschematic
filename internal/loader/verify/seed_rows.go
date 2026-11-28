package verify

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// checkSeedRows checks @seedRows (#625, #667): it is on a table of a DB
// service; every field a row names is one the table stores in a column of
// its own, and no secret; every row gives each field a row of the table
// needs a value for, as the seed's insert writes it alone; the key names
// fields of the table that every row gives, or the table has a key, a
// unique field or a unique index every row gives; and no two rows have
// one key. Whether a value fits its field is the loader's check of the
// values a schema writes (seed_values.go there).
func checkSeedRows(schema *ir.Schema, in Input, r *Result) {
	policy := sqlutil.DDLPolicyOf(in.Naming.OrDefault())
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || td.SeedRows == nil {
			continue
		}
		if schema.Kind != ir.SchemaKindDB || td.Role != ir.RoleDBTable || td.JsonField || td.Queue != nil || td.Projection != nil || td.Source != nil {
			r.errorf(td.Owner, "%s: @seedRows seeds the rows of a table of a DB service, and %s is not one", td.Name, td.Name)
			continue
		}
		fields := map[string]*ir.FieldDef{}
		for _, fd := range td.Fields {
			if fd != nil {
				fields[fd.Name] = fd
			}
		}
		badField := map[string]bool{}
		check := func(field string) bool {
			if badField[field] {
				return false
			}
			fd := fields[field]
			why := ""
			switch {
			case fd == nil:
				why = "the table has no such field"
			case fd.Virtual, fd.HasMany, fd.ManyToMany, fd.Relation != nil, fd.References != nil, fd.TypeRef.IsMap:
				why = "it is held in no column of its own; name a field that holds a value"
			case fd.Secret, fd.Encrypted, fd.EncryptedAtRest:
				why = "it holds a secret, which a migration's SQL would carry in plain text"
			}
			if why != "" {
				r.errorf(td.Owner, "%s: @seedRows names %s.%s: %s", td.Name, td.Name, field, why)
				badField[field] = true
				return false
			}
			return true
		}
		key := td.SeedRows.KeyOf(td)
		keyOK := true
		if len(td.SeedRows.Key) > 0 {
			listed := map[string]bool{}
			for _, field := range td.SeedRows.Key {
				if listed[field] {
					r.errorf(td.Owner, "%s: @seedRows's key lists %s twice", td.Name, field)
					keyOK = false
				}
				listed[field] = true
				if !check(field) {
					keyOK = false
				}
			}
		} else if key == nil {
			r.errorf(td.Owner, "%s: @seedRows finds no fields that identify a seeded row: no row gives the table's key, and no unique field or unique index of %s is given by every row; name them with @seedRows([...], { key: [\"field\"] })", td.Name, td.Name)
			keyOK = false
		}

		scalarTypes := sqlutil.ScalarSQLTypes(schema)
		seen := map[string]int{}
		for i, row := range td.SeedRows.Rows {
			for _, field := range sortedKeys(row) {
				check(field)
			}
			for _, fd := range td.Fields {
				if fd == nil {
					continue
				}
				if _, given := row[fd.Name]; given || badField[fd.Name] || !needsValueOnCreate(schema, td, fd, scalarTypes, policy) {
					continue
				}
				if fd.Key {
					r.errorf(td.Owner, "%s: @seedRows row %d gives no %s, the table's key, which nothing generates; give it", td.Name, i, fd.Name)
					continue
				}
				r.errorf(td.Owner, "%s: @seedRows row %d gives no %s, which is required and has no default its column carries; give it, make it optional or give it a Default<T, V>", td.Name, i, fd.Name)
			}
			if !keyOK {
				continue
			}
			values := make([]any, len(key))
			complete := true
			for j, field := range key {
				value, given := row[field]
				if !given {
					r.errorf(td.Owner, "%s: @seedRows row %d gives no %s, which its key names", td.Name, i, field)
					complete = false
				}
				values[j] = value
			}
			if !complete {
				continue
			}
			data, _ := json.Marshal(values)
			if first, ok := seen[string(data)]; ok {
				r.errorf(td.Owner, "%s: @seedRows rows %d and %d have one key (%s = %s); a key names one row", td.Name, first, i, strings.Join(key, ", "), data)
				continue
			}
			seen[string(data)] = i
		}
	}
}

// sortedKeys is a row's field names in order, so the errors are stable.
func sortedKeys(row map[string]any) []string {
	names := make([]string, 0, len(row))
	for name := range row {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
