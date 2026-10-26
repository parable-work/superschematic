package generator

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The keys of a DB service's Encrypted<T> fields (D85) are an @envVars
// Secret<string> its config names, outputs.sql.encryption.key, by the type
// that declares the field and the field's name, as a stack names the
// secret. Every API whose servers, jobs and workers open the DB's ORM holds
// that field in its @envVars, so each deployable reads the one secret each
// environment sets, which the stack already binds to every deployable of
// the API, and the entrypoint hands it to the ORM it opens.

// hasEncryptedFields reports whether schema has a field Encrypted<T>
// stores.
func hasEncryptedFields(schema *ir.Schema) bool {
	if schema == nil {
		return false
	}
	for _, td := range schema.Types {
		for _, fd := range td.Fields {
			if fd != nil && fd.EncryptedAtRest {
				return true
			}
		}
	}
	return false
}

// suggestedFieldKeys is the key a DB service's config could name, for an
// error that asks for one.
func suggestedFieldKeys(service string) string {
	return "FieldKeys." + strings.ToUpper(codegen.ToSnakeCase(strings.ReplaceAll(service, "-", "_"))) + "_FIELD_KEYS"
}

// EncryptionKeyDocs is the docs section that says where the keys of
// Encrypted<T> fields come from, which the key's errors point at.
const EncryptionKeyDocs = "https://parable-work.github.io/superschematic/guides/database-tables/#encrypted-fields"

// missingEncryptionKey is the error of a DB service with Encrypted<T>
// fields whose config names no key. It says where the class that declares
// the field lives, which the key alone does not: each API on the database
// holds it in its @envVars, so it is declared in the API, or in a General
// service whose @envVars class the API's extends.
func missingEncryptionKey(service string) error {
	return fmt.Errorf("generator: %s has Encrypted<T> fields, whose keys come from an @envVars Secret<string> its config names; set outputs.sql.encryption.key in %s's config to the field's declaring type and name, such as %q, and declare that @envVars class in each API on %s, or in a General service whose @envVars class each such API's extends (see %s)",
		service, service, suggestedFieldKeys(service), service, EncryptionKeyDocs)
}

// checkEncryptionKey refuses the build of a DB schema with an Encrypted<T>
// field whose config names no key: no API could open its ORM with one.
func (r run) checkEncryptionKey() error {
	if hasEncryptedFields(r.Schema) && r.Outputs.EncryptionKey() == "" {
		return missingEncryptionKey(r.Config.Name)
	}
	return nil
}

// databaseConfig is the config of db, the API's database, or nil when the
// build has none, as for a build of IR alone.
func (r run) databaseConfig(db, why string) (*schemaconfig.SchemaConfig, error) {
	var cfg *schemaconfig.SchemaConfig
	if r.Options.DependencyConfig != nil {
		cfg, _ = r.Options.DependencyConfig(db)
	}
	if cfg == nil && r.Options.LoadDependencyConfig != nil {
		loaded, err := r.Options.LoadDependencyConfig(db)
		if err != nil {
			return nil, fmt.Errorf("generator: read the config of %s, the database of %s, %s: %w", db, r.Config.Name, why, err)
		}
		cfg = loaded
	}
	return cfg, nil
}

// fieldKeysField is the field of the API's @envVars class that holds the
// keys of the Encrypted<T> fields of db, whose schema is dbSchema: nil when
// db has none, or the build has no config for it. It refuses an API whose
// @envVars lacks the secret db's config names, or holds a field of its
// name that is another secret or no Secret<string>.
func (r run) fieldKeysField(db string, dbSchema *ir.Schema) (*ir.FieldDef, error) {
	if !hasEncryptedFields(dbSchema) {
		return nil, nil
	}
	cfg, err := r.databaseConfig(db, "whose Encrypted<T> fields its ORM opens")
	if err != nil || cfg == nil {
		return nil, err
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
	if err != nil {
		return nil, fmt.Errorf("generator: schema config for %s: %w", db, err)
	}
	key := outputs.EncryptionKey()
	if key == "" {
		return nil, missingEncryptionKey(db)
	}
	typeName, field, _ := registry.SplitEncryptionKey(key)
	needs := fmt.Sprintf("%s opens the ORM of %s, whose Encrypted<T> fields take their keys from the secret %s (outputs.sql.encryption.key in %s's config)", r.Config.Name, db, key, db)
	fix := fmt.Sprintf("declare %s: Secret<string> in an @envVars class %s, in %s's schema or in a General service %s depends on, and make %s's @envVars class %s or extend it (see %s)",
		field, typeName, r.Config.Name, r.Config.Name, r.Config.Name, typeName, EncryptionKeyDocs)
	for _, name := range slices.Sorted(maps.Keys(r.Schema.Types)) {
		td := r.Schema.Types[name]
		if !td.EnvVars {
			continue
		}
		for _, fd := range td.Fields {
			if fd == nil || fd.Name != field {
				continue
			}
			declared := fd.InheritedFrom
			if declared == "" {
				declared = td.Name
			}
			switch {
			case declared != typeName:
				return nil, fmt.Errorf("generator: %s, and its @envVars field %s is declared by %s, so it is the secret %s.%s; %s", needs, field, declared, declared, field, fix)
			case !fd.Secret || fd.TypeRef.Name != "string" || fd.TypeRef.IsArray || fd.TypeRef.IsMap || !fd.Required:
				return nil, fmt.Errorf("generator: %s, and its @envVars field %s is no required Secret<string>; %s", needs, field, fix)
			}
			return fd, nil
		}
	}
	return nil, fmt.Errorf("generator: %s, and its @envVars class holds no field %s; %s", needs, field, fix)
}
