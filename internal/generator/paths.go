package generator

import "path/filepath"

// The shadow output layout mirrors the schemas root's dist/ structure
// under a root that nothing consumes until the flip (dist by default).
// At the flip the shadow root is renamed to the canonical dist/; the
// per-artifact layout is unchanged. There is no graphql/ directory: the
// GraphQL generator is not ported.

// TypesDir returns the type-library output directory for a language.
func TypesDir(root, lang, schemaName string) string {
	return filepath.Join(root, "types", lang, schemaName)
}

// SQLDir returns the SQL DDL output directory.
func SQLDir(root, schemaName string) string {
	return filepath.Join(root, "sql", schemaName)
}

// ORMDir returns the Go ORM output directory.
func ORMDir(root, schemaName string) string {
	return filepath.Join(root, "orm", schemaName)
}

// APIDir returns the REST API server output directory.
func APIDir(root, schemaName string) string {
	return filepath.Join(root, "api", schemaName)
}

// RulesDir returns the directory of a DB schema's rules crate (D103's step
// 5): the behaviors' core with the behaviors its tables compose that the
// core's crate does not ship.
func RulesDir(root, lang, schemaName string) string {
	return filepath.Join(root, "rules", lang, schemaName)
}

// SDKDir returns the client SDK output directory for a language.
func SDKDir(root, lang, schemaName string) string {
	return filepath.Join(root, "sdk", lang, schemaName)
}
