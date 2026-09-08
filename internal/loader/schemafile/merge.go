package schemafile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Merge folds a decoded document into the service schema. The owner argument
// is the document's source path relative to the service directory (or a
// runtime-mode label); it becomes the Owner of definitions that record their
// source file, matching the TypeScript walker's conventions.
//
// Duplicate definition names -- within the document's own collections against
// what the schema already holds -- are errors: a name is defined exactly once
// per service, regardless of format.
//
// Merge checks extension and document names against the core registry;
// callers holding a registry use MergeWith.
func Merge(doc *Document, schema *ir.Schema, owner string) []error {
	return MergeWith(doc, schema, owner, core())
}

// MergeWith is Merge against a registry (nil for the core one). Every
// "extensions" key must name a registered extension and every root
// "documents" key a registered document; each decorator named under a
// node's extensions runs through the registry's DecoratorSpec, the same
// validation the TypeScript frontend applies to @decorator syntax.
func MergeWith(doc *Document, schema *ir.Schema, owner string, reg *registry.Registry) []error {
	reg = orCore(reg)
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", owner, fmt.Sprintf(format, args...)))
	}

	if err := checkDocumentSlotNames(doc, reg); err != nil {
		return []error{fmt.Errorf("%s: %w", owner, err)}
	}

	if doc.Name != "" && doc.Name != schema.Name {
		fail("document name %q does not match the service name %q", doc.Name, schema.Name)
	}
	if doc.Kind != "" && doc.Kind != schema.Kind {
		fail("document kind %q does not match the service kind %q", doc.Kind, schema.Kind)
	}
	if doc.Description != "" {
		if schema.Description != "" && schema.Description != doc.Description {
			fail("document restates the schema description with different text")
		} else {
			schema.Description = doc.Description
		}
	}
	if doc.Comment != "" {
		if schema.Comment != "" && schema.Comment != doc.Comment {
			fail("document restates the schema comment with different text")
		} else {
			schema.Comment = doc.Comment
		}
	}

	for _, imp := range doc.Imports {
		mergeImport(schema, imp)
	}

	// Decode has already canonicalized a read document; a caller building
	// a Document by hand gets the same treatment so the schema never holds
	// non-canonical bytes.
	if err := ir.CanonicalizeExtensions(doc.Extensions); err != nil {
		fail("%s", err)
	} else {
		for _, key := range sortedKeys(doc.Extensions) {
			if err := mergeRaw(&schema.Extensions, key, doc.Extensions[key], "extension"); err != nil {
				fail("%s", err)
			}
		}
	}
	if err := ir.CanonicalizeDocuments(doc.Documents); err != nil {
		fail("%s", err)
	} else {
		for _, key := range sortedKeys(doc.Documents) {
			if err := mergeRaw(&schema.Documents, key, doc.Documents[key], "document"); err != nil {
				fail("%s", err)
			}
		}
	}

	for _, key := range sortedKeys(doc.Scalars) {
		def := doc.Scalars[key]
		if err := keyMatchesName(key, def.Name, "scalar"); err != nil {
			fail("%s", err)
			continue
		}
		if def.Name == "" {
			def.Name = key
		}
		if _, exists := schema.Scalars[def.Name]; exists {
			fail("duplicate scalar %q", def.Name)
			continue
		}
		schema.Scalars[def.Name] = def
	}

	for _, key := range sortedKeys(doc.Types) {
		def := doc.Types[key]
		if err := keyMatchesName(key, def.Name, "type"); err != nil {
			fail("%s", err)
			continue
		}
		if def.Name == "" {
			def.Name = key
		}
		if _, exists := schema.Types[def.Name]; exists {
			fail("duplicate type %q", def.Name)
			continue
		}
		if def.Owner == "" {
			def.Owner = owner
		}
		schema.Types[def.Name] = def
	}

	for _, key := range sortedKeys(doc.Enums) {
		def := doc.Enums[key]
		if err := keyMatchesName(key, def.Name, "enum"); err != nil {
			fail("%s", err)
			continue
		}
		if def.Name == "" {
			def.Name = key
		}
		if _, exists := schema.Enums[def.Name]; exists {
			fail("duplicate enum %q", def.Name)
			continue
		}
		// The TypeScript walker stamps enums with the service name, not the
		// file path; mirror that so formats produce identical IR.
		if def.Owner == "" {
			def.Owner = schema.Name
		}
		schema.Enums[def.Name] = def
	}

	for _, key := range sortedKeys(doc.Unions) {
		def := doc.Unions[key]
		if err := keyMatchesName(key, def.Name, "union"); err != nil {
			fail("%s", err)
			continue
		}
		if def.Name == "" {
			def.Name = key
		}
		if _, exists := schema.Unions[def.Name]; exists {
			fail("duplicate union %q", def.Name)
			continue
		}
		if existingType, exists := schema.Types[def.Name]; exists {
			if isEmptyTSForwardDeclaration(existingType) {
				delete(schema.Types, def.Name)
			} else {
				fail("union %q conflicts with existing type %q", def.Name, def.Name)
				continue
			}
		}
		schema.Unions[def.Name] = def
	}

	for _, set := range doc.OperationSets {
		duplicate := false
		for _, existing := range schema.OperationSets {
			if existing != nil && existing.Name == set.Name {
				fail("duplicate operation set %q", set.Name)
				duplicate = true
				break
			}
		}
		if !duplicate {
			schema.OperationSets = append(schema.OperationSets, set)
		}
	}

	errs = append(errs, applyDocumentDecorators(doc, schema, owner, reg)...)
	return errs
}

// checkDocumentSlotNames is checkSlotNames for a decoded Document, so a
// caller that builds one by hand (the writer's tests, a future walker) gets
// the same name check Decode gives a payload.
func checkDocumentSlotNames(doc *Document, reg *registry.Registry) error {
	if err := checkRawExtensionNames(doc.Extensions, "the document", reg); err != nil {
		return err
	}
	registered := make([]string, 0, len(reg.Documents()))
	for _, spec := range reg.Documents() {
		registered = append(registered, spec.Name)
	}
	for _, name := range sortedKeys(doc.Documents) {
		if !slices.Contains(registered, name) {
			return unknownSlotName("documents", name, "the document", registered)
		}
	}
	for _, name := range sortedKeys(doc.Types) {
		td := doc.Types[name]
		where := fmt.Sprintf("type %q", name)
		if err := checkRawExtensionNames(td.Extensions, where, reg); err != nil {
			return err
		}
		for _, fd := range td.Fields {
			if err := checkRawExtensionNames(fd.Extensions, fmt.Sprintf("%s field %q", where, fd.Name), reg); err != nil {
				return err
			}
		}
	}
	for _, set := range doc.OperationSets {
		where := fmt.Sprintf("operation set %q", set.Name)
		if err := checkRawExtensionNames(set.Extensions, where, reg); err != nil {
			return err
		}
		for _, op := range set.Operations {
			if err := checkRawExtensionNames(op.Extensions, fmt.Sprintf("%s operation %q", where, op.Name), reg); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkRawExtensionNames(exts map[string]json.RawMessage, where string, reg *registry.Registry) error {
	for _, name := range sortedKeys(exts) {
		if !slices.Contains(reg.Extensions(), name) {
			return unknownSlotName("extensions", name, where, reg.Extensions())
		}
	}
	return nil
}

// applyDocumentDecorators runs the extension decorators on every node the
// document contributed: types and their fields, operation sets and their
// operations. Core decorators are typed IR fields in the data form and never
// appear here.
func applyDocumentDecorators(doc *Document, schema *ir.Schema, owner string, reg *registry.Registry) []error {
	var errs []error
	for _, name := range sortedKeys(doc.Types) {
		td := doc.Types[name]
		where := fmt.Sprintf("type %q", td.Name)
		errs = append(errs, applyExtensionDecorators(registry.Node{Schema: schema, Type: td}, td.Extensions, registry.TargetType, schema.Kind, where, owner, reg)...)
		for _, fd := range td.Fields {
			errs = append(errs, applyExtensionDecorators(registry.Node{Schema: schema, Type: td, Field: fd}, fd.Extensions, registry.TargetField, schema.Kind, fmt.Sprintf("%s field %q", where, fd.Name), owner, reg)...)
		}
	}
	for _, set := range doc.OperationSets {
		where := fmt.Sprintf("operation set %q", set.Name)
		errs = append(errs, applyExtensionDecorators(registry.Node{Schema: schema, OperationSet: set}, set.Extensions, registry.TargetOperationSet, schema.Kind, where, owner, reg)...)
		for _, op := range set.Operations {
			errs = append(errs, applyExtensionDecorators(registry.Node{Schema: schema, OperationSet: set, Field: op}, op.Extensions, registry.TargetOperation, schema.Kind, fmt.Sprintf("%s operation %q", where, op.Name), owner, reg)...)
		}
	}
	return errs
}

func isEmptyTSForwardDeclaration(def *ir.TypeDef) bool {
	return def != nil &&
		strings.HasSuffix(def.Owner, ".schema.ts") &&
		len(def.Fields) == 0 &&
		len(def.Indexes) == 0 &&
		def.Source == nil &&
		def.Extends == "" &&
		len(def.Implements) == 0 &&
		def.RawHeritage == nil &&
		!def.IsTrait &&
		def.TraitConfig == nil &&
		!def.JsonField &&
		!def.Versioned &&
		!def.EnvVars
}

// mergeRaw copies one canonical root-level extension or document entry into
// the schema. A second document may restate a key only with identical
// content.
func mergeRaw(dst *map[string]json.RawMessage, key string, value json.RawMessage, what string) error {
	if existing, ok := (*dst)[key]; ok {
		if bytes.Equal(existing, value) {
			return nil
		}
		return fmt.Errorf("document restates %s %q with different content", what, key)
	}
	if *dst == nil {
		*dst = make(map[string]json.RawMessage)
	}
	(*dst)[key] = value
	return nil
}

// mergeImport appends an import, folding repeated packages into one entry
// with a deduplicated symbol list.
func mergeImport(schema *ir.Schema, imp ir.Import) {
	for i := range schema.Imports {
		if schema.Imports[i].Package != imp.Package {
			continue
		}
		existing := make(map[string]bool, len(schema.Imports[i].Types))
		for _, t := range schema.Imports[i].Types {
			existing[t] = true
		}
		for _, t := range imp.Types {
			if !existing[t] {
				schema.Imports[i].Types = append(schema.Imports[i].Types, t)
				existing[t] = true
			}
		}
		return
	}
	schema.Imports = append(schema.Imports, imp)
}

// keyMatchesName rejects map entries whose key disagrees with the
// definition's own name field.
func keyMatchesName(key, name, sort string) error {
	if name != "" && name != key {
		return fmt.Errorf("%s map key %q does not match the definition name %q", sort, key, name)
	}
	return nil
}

// ImportedTypeNames returns every symbol named by the schema's imports.
// These become known externals for the IR validation pass: the JSON and YAML
// formats have no compiler-resolved import system, so the named symbols are
// trusted at read time and verified at flip-gate build time when the
// dependency services load.
func ImportedTypeNames(schema *ir.Schema) map[string]bool {
	names := make(map[string]bool)
	for _, imp := range schema.Imports {
		for _, t := range imp.Types {
			names[t] = true
		}
	}
	return names
}

// sortedKeys returns map keys in sorted order so merges (and their
// duplicate-name errors) are deterministic.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
