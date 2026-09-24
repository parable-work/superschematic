package verify

import (
	"regexp"

	ir "github.com/parable-work/superschematic/ir"
)

// sqlIdentifierPattern matches the unquoted snake_case identifiers the prune
// exclusion is interpolated into DDL as; anything else is rejected up front.
var sqlIdentifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// checkVersioned validates the @versioned contract. The generators rely on a
// single row key and only know how to version DB tables.
func checkVersioned(schema *ir.Schema, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if !td.Versioned {
			continue
		}

		if td.Role != ir.RoleDBTable {
			r.errorf(td.Owner, "%s: @versioned is only allowed on DB table types (this type has role %s)", td.Name, td.Role)
			continue
		}

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
			checkVersionedConfig(td, r)
		}
	}
}

func checkVersionedConfig(td *ir.TypeDef, r *Result) {
	cfg := td.VersionedConfig
	if cfg.RetentionDays != nil && *cfg.RetentionDays <= 0 {
		r.errorf(td.Owner, "%s: @versioned retentionDays must be greater than 0", td.Name)
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
	}
}
