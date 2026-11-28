package loader

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// validateSeedRowValues checks each value a table's @seedRows gives
// (#625) against its field, as a default is checked: its type, its
// scalar's rules and the field's own, since the seed's insert writes it
// into the table as it is, past every validator. A null is a value of an
// optional field alone. It runs after the verification pass, which has
// checked that each field a row names is one of the table's, so the
// fields the loader fills (a notifier's part, D153) are there.
func validateSeedRowValues(schema *ir.Schema, externalEnums map[string]*ir.EnumDef) error {
	var errs []error
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil || td.SeedRows == nil {
			continue
		}
		scalarTypes := sqlutil.ScalarSQLTypes(schema)
		fields := map[string]*ir.FieldDef{}
		for _, fd := range td.Fields {
			if fd != nil {
				fields[fd.Name] = fd
			}
		}
		for i, row := range td.SeedRows.Rows {
			names := make([]string, 0, len(row))
			for field := range row {
				names = append(names, field)
			}
			sort.Strings(names)
			for _, field := range names {
				fd := fields[field]
				if fd == nil {
					continue
				}
				value := row[field]
				var err error
				column := strings.ToUpper(sqlutil.ColumnType(schema, fd, scalarTypes))
				switch {
				case value == nil:
					if fd.Required {
						err = errors.New("it is null, and the field is required")
					}
				case (column == "JSONB" || column == "JSON") && !fd.JsonField && !sqlutil.IsJSONFieldType(schema, fd.TypeRef.Name):
					// A JSON scalar's column holds any JSON value, which its
					// insert writes as its text.
				default:
					v := jsonNumbers(value)
					if err = validateCompositeValue(schema, externalEnums, fd.TypeRef, v, field); err == nil {
						err = validateCompositeFieldConstraints(fd.ValueRules, fd.TypeRef, v, field)
					}
				}
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %s: @seedRows row %d's %s: %w", schema.Name, name, i, field, err))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// jsonNumbers is value with every number a json.Number, as the composite
// checks read numbers from JSON: a decorator's argument holds float64.
func jsonNumbers(value any) any {
	switch v := value.(type) {
	case float64:
		return json.Number(strconv.FormatFloat(v, 'f', -1, 64))
	case int:
		return json.Number(strconv.Itoa(v))
	case int64:
		return json.Number(strconv.FormatInt(v, 10))
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = jsonNumbers(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = jsonNumbers(item)
		}
		return out
	}
	return value
}
