package sqlgen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A UserRole table's @seedRoles (D88): the roles the table holds from its
// first migration on, so a user approved into a default role can use what
// it permits with no administrator's step first. create.sql inserts each;
// the migration planner's model (internal/sqlmigrate) holds each with its
// statements, inserts a new one, and sets the permissions of one whose
// declaration changes. The insert leaves a role of the name alone unless
// it has no permissions, as one the first registration created before the
// seed existed (D81) has, which it gives the seed's.

// RoleSeed is one role a UserRole table's @seedRoles declares.
type RoleSeed struct {
	// Origin is the table's type.
	Origin string

	Table, QuotedTable string

	// NameColumn and PermissionsColumn are the quoted columns of the
	// role's name and permissions, PermissionsType the latter's type
	// (TEXT[]).
	NameColumn        string
	PermissionsColumn string
	PermissionsType   string

	Name        string
	Permissions []string
}

// buildRoleSeeds builds each @seedRoles role of the schema's tables, in
// table order, then declaration order.
func buildRoleSeeds(tableTypes []*ir.TypeDef, tables []Table) ([]RoleSeed, error) {
	var out []RoleSeed
	for _, td := range tableTypes {
		if len(td.SeedRoles) == 0 {
			continue
		}
		var table *Table
		for i := range tables {
			if tables[i].OriginalName == td.Name && tables[i].Name == sqlutil.TableName(td) {
				table = &tables[i]
			}
		}
		if table == nil {
			return nil, fmt.Errorf("@seedRoles on %s: %s has no table", td.Name, td.Name)
		}
		column := func(field string) *Column {
			for i := range table.Columns {
				if table.Columns[i].Origin == td.Name+"."+field {
					return &table.Columns[i]
				}
			}
			return nil
		}
		name, permissions := column(ir.IdentityRoleNameField), column(ir.IdentityRolePermissionsField)
		if name == nil || permissions == nil {
			return nil, fmt.Errorf("@seedRoles on %s: the table has no column of %s or of %s", td.Name, ir.IdentityRoleNameField, ir.IdentityRolePermissionsField)
		}
		for _, seed := range td.SeedRoles {
			if seed == nil {
				continue
			}
			out = append(out, RoleSeed{
				Origin: td.Name, Table: table.Name, QuotedTable: table.QuotedName,
				NameColumn: name.QuotedName, PermissionsColumn: permissions.QuotedName, PermissionsType: permissions.Type,
				Name: seed.Name, Permissions: append([]string{}, seed.Permissions...),
			})
		}
	}
	return out, nil
}

// permissionsArray is the role's permissions as a Postgres array of the
// column's type.
func (s RoleSeed) permissionsArray() string {
	if len(s.Permissions) == 0 {
		return "'{}'::" + s.PermissionsType
	}
	literals := make([]string, len(s.Permissions))
	for i, p := range s.Permissions {
		literals[i] = stringLiteral(p)
	}
	return "ARRAY[" + strings.Join(literals, ", ") + "]::" + s.PermissionsType
}

// permissionsJSON is the role's permissions as SQLite holds a list: its
// JSON array, as a string literal.
func (s RoleSeed) permissionsJSON() string {
	data, _ := json.Marshal(append([]string{}, s.Permissions...))
	return stringLiteral(string(data))
}

// InsertSQL inserts the role on Postgres when the table has none of its
// name, and gives one of its name that has no permissions the seed's.
func (s RoleSeed) InsertSQL() string {
	return s.insertSQL(func(c string) string { return c }, s.QuotedTable, s.permissionsArray(), "EXCLUDED", "cardinality(%s) = 0")
}

// UpdateSQL sets the permissions of the role of the seed's name on
// Postgres.
func (s RoleSeed) UpdateSQL() string {
	return "UPDATE " + s.QuotedTable + " SET " + s.PermissionsColumn + " = " + s.permissionsArray() +
		" WHERE " + s.NameColumn + " = " + stringLiteral(s.Name)
}

// SQLiteInsertSQL is InsertSQL on SQLite, its identifiers quoted by quote.
func (s RoleSeed) SQLiteInsertSQL(quote func(string) string) string {
	unquote := func(c string) string { return quote(strings.Trim(c, `"`)) }
	return s.insertSQL(unquote, quote(s.Table), s.permissionsJSON(), "excluded", "json_array_length(%s) = 0")
}

// SQLiteUpdateSQL is UpdateSQL on SQLite.
func (s RoleSeed) SQLiteUpdateSQL(quote func(string) string) string {
	unquote := func(c string) string { return quote(strings.Trim(c, `"`)) }
	return "UPDATE " + quote(s.Table) + " SET " + unquote(s.PermissionsColumn) + " = " + s.permissionsJSON() +
		" WHERE " + unquote(s.NameColumn) + " = " + stringLiteral(s.Name)
}

// insertSQL renders the insert: quote quotes a column, table is quoted,
// permissions is the list's literal, excluded the dialect's name of the
// row the insert proposed, and empty the test of no permissions, a format
// of the existing row's column.
func (s RoleSeed) insertSQL(quote func(string) string, table, permissions, excluded, empty string) string {
	name, perms := quote(s.NameColumn), quote(s.PermissionsColumn)
	insert := "INSERT INTO " + table + " (" + name + ", " + perms + ") VALUES (" + stringLiteral(s.Name) + ", " + permissions + ")\nON CONFLICT (" + name + ") DO "
	if len(s.Permissions) == 0 {
		return insert + "NOTHING"
	}
	return insert + "UPDATE SET " + perms + " = " + excluded + "." + perms + " WHERE " + fmt.Sprintf(empty, table+"."+perms)
}
