package verify

import (
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkSourceProjections runs the full @source structural verification on
// every projection in the schema. For each type V decorated @source(S):
//
//  1. S resolves to its flattened TypeDef: same-service targets resolve from
//     the schema itself, cross-service targets from Input.ExternalTypes (the
//     TypeScript frontend supplies these through compiler resolution).
//  2. A field of V that matches a field of S by name must be type-assignable
//     (hard error otherwise).
//  3. A field of V that matches nothing in S must be marked @virtual (hard
//     error otherwise).
//  4. Removal is silent: V omitting a field of S is the intended pattern.
//     The exception is opt-in: omitting a field S marks @sourceMustProject
//     draws a warning.
//  5. The derived SourceRef metadata (Virtual, OmittedFromSource) is
//     recomputed and recorded, so the IR generators see is exactly what
//     verification proved.
//
// The data formats have no compiler: a cross-service target outside
// Input.ExternalTypes is structurally unverifiable. When the document's
// imports block declares the target type, the recorded SourceRef stands as
// authored (it round-trips from a verified authoring) and the structural
// checks are skipped; an undeclared target is a hard error -- verification
// is never skipped silently.
//
// This is what makes the "no mirrors of a schema-defined shape" rule
// machine-checked rather than a review-time convention.
func checkSourceProjections(schema *ir.Schema, in Input, r *Result) {
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td.Source == nil {
			continue
		}
		if kind, _ := in.registry().Kind(string(schema.Kind)); kind.SourceProjectionRole == "" {
			r.errorf(td.Owner, "%s: @source projections are only allowed in API or General schemas (this service is kind %s)", td.Name, schema.Kind)
			continue
		}

		src, ok := resolveSourceType(schema, in, td.Source.Target)
		if !ok {
			if !importedTypeNames(schema)[td.Source.Target] {
				r.errorf(td.Owner, "%s: cannot resolve @source target %q", td.Name, td.Source.Target)
			}
			continue
		}

		sourceByName := make(map[string]*ir.FieldDef, len(src.Fields))
		for _, sf := range src.Fields {
			sourceByName[sf.Name] = sf
		}

		var virtual []string
		viewNames := make(map[string]bool, len(td.Fields))
		for _, f := range td.Fields {
			viewNames[f.Name] = true
			if f.Virtual {
				virtual = append(virtual, f.Name)
				continue
			}
			sf, onSource := sourceByName[f.Name]
			if !onSource {
				r.errorf(td.Owner, "%s.%s is not in source type %s and is not marked @virtual",
					td.Name, f.Name, td.Source.Target)
				continue
			}
			if !typeRefsCompatible(schema, in, f.TypeRef, sf.TypeRef) {
				r.errorf(td.Owner, "%s.%s type %q is not assignable to source type %s.%s %q",
					td.Name, f.Name, typeRefString(f.TypeRef), td.Source.Target, sf.Name, typeRefString(sf.TypeRef))
			}
		}

		var omitted []string
		for _, sf := range src.Fields {
			if viewNames[sf.Name] {
				continue
			}
			omitted = append(omitted, sf.Name)
			if sf.SourceMustProject {
				r.warnf(td.Owner, "%s omits %s.%s, which is marked @sourceMustProject",
					td.Name, td.Source.Target, sf.Name)
			}
		}

		td.Source.Virtual = virtual
		td.Source.OmittedFromSource = omitted
	}
}

// resolveSourceType resolves a service-qualified @source target to its
// flattened TypeDef. Unqualified targets and targets qualified with the
// schema's own name resolve locally; anything else resolves through the
// reader-supplied externals.
func resolveSourceType(schema *ir.Schema, in Input, target string) (*ir.TypeDef, bool) {
	service, typeName := splitTarget(target)
	if service == "" || service == schema.Name {
		td, ok := schema.Types[typeName]
		return td, ok
	}
	td, ok := in.ExternalTypes[target]
	return td, ok
}

// splitTarget splits "service.Type" into its parts; an unqualified target
// returns an empty service.
func splitTarget(target string) (service, typeName string) {
	if i := strings.LastIndex(target, "."); i >= 0 {
		return target[:i], target[i+1:]
	}
	return "", target
}

// importedTypeNames collects the service-qualified type names the schema's
// imports blocks declare.
func importedTypeNames(schema *ir.Schema) map[string]bool {
	names := make(map[string]bool)
	for _, imp := range schema.Imports {
		service := serviceNameForPackage(imp.Package)
		for _, t := range imp.Types {
			names[service+"."+t] = true
		}
	}
	return names
}

// typeRefsCompatible is the assignability check between a projection field
// and its source field. Scalars and enums are nominal. Object references may
// either have the same name or be a local @source projection that reaches the
// source field's type. The latter permits an aggregate projection to replace
// Property[] with Perception[] without weakening the field-level source proof.
func typeRefsCompatible(schema *ir.Schema, in Input, view, source ir.TypeRef) bool {
	if view.IsArray != source.IsArray || view.IsMap != source.IsMap {
		return false
	}
	if unqualifiedName(view.Name) == unqualifiedName(source.Name) {
		return true
	}
	return sourceProjectionReaches(schema, in, view.Name, source.Name, map[string]bool{})
}

func sourceProjectionReaches(
	schema *ir.Schema,
	in Input,
	viewName string,
	sourceName string,
	visited map[string]bool,
) bool {
	if visited[viewName] {
		return false
	}
	visited[viewName] = true

	viewType, ok := resolveSourceType(schema, in, viewName)
	if !ok || viewType.Source == nil {
		return false
	}
	target := viewType.Source.Target
	if unqualifiedName(target) == unqualifiedName(sourceName) {
		return true
	}
	return sourceProjectionReaches(schema, in, target, sourceName, visited)
}

func unqualifiedName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// typeRefString renders a TypeRef for messages.
func typeRefString(ref ir.TypeRef) string {
	s := ref.Name
	if ref.IsArray {
		s += "[]"
	}
	if ref.IsMap {
		s = "Map<string, " + s + ">"
	}
	return s
}

// sortedTypeNames returns map keys in deterministic order.
func sortedTypeNames[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
