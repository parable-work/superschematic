package ir

import (
	"fmt"
	"sort"
)

// validateConfig holds options that modify validation behavior.
type validateConfig struct {
	knownExternals         map[string]bool
	scalarAliasIdx         ScalarAliasIndex
	strictScalarResolution bool
}

// ValidateOption configures schema validation behavior.
type ValidateOption func(*validateConfig)

// WithKnownExternals supplies a set of external type names that should be
// treated as resolved during validation. This is used for types imported
// from other schemas that are not present in the current schema definition.
func WithKnownExternals(externals map[string]bool) ValidateOption {
	return func(cfg *validateConfig) {
		cfg.knownExternals = externals
	}
}

// WithScalarAliasIndex supplies a scalar alias index for resolving short
// scalar aliases (e.g., "Slug") and namespaced canonical names (e.g.,
// "Identity.UUID") during validation.
func WithScalarAliasIndex(index ScalarAliasIndex) ValidateOption {
	return func(cfg *validateConfig) {
		cfg.scalarAliasIdx = index
	}
}

// WithStrictScalarResolution requires scalar alias lookups to resolve
// unambiguously. When enabled, a short alias that maps to multiple
// namespaced scalars (e.g., "Slug" matching both "Identity.Slug" and
// "Content.Slug") is treated as unresolvable.
func WithStrictScalarResolution() ValidateOption {
	return func(cfg *validateConfig) {
		cfg.strictScalarResolution = true
	}
}

// Validate checks the schema for dangling type references and malformed
// imports. It walks all TypeRef.Name values in types, operations, and union
// member lists, verifying that each resolves to a known definition, and
// verifies that every import is a named import.
//
// Resolution sources (checked in order):
//   - schema.Scalars
//   - schema.Types
//   - schema.Enums
//   - schema.Unions
//   - Known externals (via [WithKnownExternals])
//   - Scalar alias index (via [WithScalarAliasIndex])
func (s *Schema) Validate(opts ...ValidateOption) []error {
	cfg := &validateConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var errs []error

	errs = append(errs, s.validateImports()...)

	for _, name := range sortedStringMapKeys(s.Types) {
		errs = append(errs, s.validateTypeDef(cfg, s.Types[name])...)
	}

	for _, set := range s.OperationSets {
		if set == nil {
			continue
		}
		errs = append(errs, s.validateOperationSet(cfg, set)...)
	}

	for _, name := range sortedStringMapKeys(s.Unions) {
		u := s.Unions[name]
		for _, memberName := range u.Types {
			if !s.isResolvable(cfg, memberName) {
				errs = append(errs, fmt.Errorf("union %s references unknown member type %q", u.Name, memberName))
			}
		}
	}

	for _, name := range sortedStringMapKeys(s.CompositeDefaults) {
		def := s.CompositeDefaults[name]
		if def == nil {
			errs = append(errs, fmt.Errorf("composite default %q is nil", name))
			continue
		}
		if def.Type != name {
			errs = append(errs, fmt.Errorf("composite default key %q does not match target type %q", name, def.Type))
		}
		if _, ok := s.Types[def.Type]; !ok {
			errs = append(errs, fmt.Errorf("composite default %q references unknown local type %q", name, def.Type))
		}
	}

	return errs
}

// validateImports enforces the named-import invariant: every import names its
// package and lists at least one symbol, and the "*" wildcard does not exist
// in v2.
func (s *Schema) validateImports() []error {
	var errs []error
	for i, imp := range s.Imports {
		if imp.Package == "" {
			errs = append(errs, fmt.Errorf("import[%d] has an empty package", i))
		}
		if len(imp.Types) == 0 {
			errs = append(errs, fmt.Errorf("import of %q lists no symbols; imports are always named in v2", imp.Package))
		}
		for _, typeName := range imp.Types {
			switch typeName {
			case "*":
				errs = append(errs, fmt.Errorf("import of %q uses the %q wildcard; imports are always named in v2", imp.Package, "*"))
			case "":
				errs = append(errs, fmt.Errorf("import of %q lists an empty symbol name", imp.Package))
			}
		}
	}
	return errs
}

func (s *Schema) validateTypeDef(cfg *validateConfig, td *TypeDef) []error {
	var errs []error
	for _, f := range td.Fields {
		if !s.isResolvable(cfg, f.TypeRef.Name) {
			errs = append(errs, fmt.Errorf("%s.%s references unknown type %q", td.Name, f.Name, f.TypeRef.Name))
		}
		if f.PlatformDefault != "" && f.PlatformDefault != f.TypeRef.Name {
			errs = append(errs, fmt.Errorf("%s.%s platform default %q does not match field type %q", td.Name, f.Name, f.PlatformDefault, f.TypeRef.Name))
		}
		if f.Relation != nil && f.Relation.OnDelete != "" {
			switch f.Relation.OnDelete {
			case "CASCADE", "RESTRICT", "NO ACTION":
			default:
				errs = append(errs, fmt.Errorf("%s.%s relation onDelete %q is invalid (want CASCADE, RESTRICT, or NO ACTION)", td.Name, f.Name, f.Relation.OnDelete))
			}
		}
		for _, arg := range f.Arguments {
			if !s.isResolvable(cfg, arg.TypeRef.Name) {
				errs = append(errs, fmt.Errorf("%s.%s argument %q references unknown type %q", td.Name, f.Name, arg.Name, arg.TypeRef.Name))
			}
		}
	}
	return errs
}

func (s *Schema) validateOperationSet(cfg *validateConfig, set *OperationSet) []error {
	var errs []error
	for _, op := range set.Operations {
		if !s.isResolvable(cfg, op.TypeRef.Name) {
			errs = append(errs, fmt.Errorf("%s.%s references unknown type %q", set.Name, op.Name, op.TypeRef.Name))
		}
		for _, arg := range op.Arguments {
			if !s.isResolvable(cfg, arg.TypeRef.Name) {
				errs = append(errs, fmt.Errorf("%s.%s argument %q references unknown type %q", set.Name, op.Name, arg.Name, arg.TypeRef.Name))
			}
		}
	}
	return errs
}

func (s *Schema) isResolvable(cfg *validateConfig, name string) bool {
	if _, ok := s.Scalars[name]; ok {
		return true
	}
	if _, ok := s.Types[name]; ok {
		return true
	}
	if _, ok := s.Enums[name]; ok {
		return true
	}
	if _, ok := s.Unions[name]; ok {
		return true
	}
	if cfg.knownExternals != nil && cfg.knownExternals[name] {
		return true
	}
	res := cfg.scalarAliasIdx.Resolve(name)
	if res.Status == ScalarAliasResolutionResolved {
		return true
	}
	if res.Status == ScalarAliasResolutionAmbiguous && !cfg.strictScalarResolution {
		return true
	}
	return false
}

func sortedStringMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
