package rustgen

import ir "github.com/parable-work/superschematic/ir"

// JSONFieldAdapter is the scalar crate's lossless Generic.JSON deserializer,
// as a path serde's deserialize_with accepts. It parses a value through
// serde_json's RawValue, so every number keeps the digits it was written
// with, and it handles a Value inside Option, Vec and HashMap.
func (o ModuleOutput) JSONFieldAdapter() string {
	return o.Naming.ScalarRustCrateIdent() + "::scalars::json_scalar::serde::deserialize"
}

// containsGenericJSON reports whether the type, union or scalar named name
// reaches a Generic.JSON field, following member and field types through
// the schema and its dependencies (imported members and recursive types
// included). A union buffers its input before it picks a member, so a union
// that reaches Generic.JSON must read that buffer losslessly.
func containsGenericJSON(schema *ir.Schema, name string, dependencies map[string]*ir.Schema, seen map[string]bool) bool {
	if name == "Generic.JSON" {
		return true
	}
	owner := runtimeSymbolOwner(schema, name, dependencies, map[string]bool{})
	if owner == nil {
		return false
	}
	key := owner.Name + "/" + name
	if seen[key] {
		return false
	}
	seen[key] = true
	if def := owner.Types[name]; def != nil {
		for _, field := range def.Fields {
			if field != nil && containsGenericJSON(owner, field.TypeRef.Name, dependencies, seen) {
				return true
			}
		}
	}
	if def := owner.Unions[name]; def != nil {
		for _, member := range def.Types {
			if containsGenericJSON(owner, member, dependencies, seen) {
				return true
			}
		}
	}
	return false
}
