package tsreader

import (
	"strings"

	"github.com/parable-work/superschematic/internal/loader/verify"
)

// checkImportDeclaration enforces the syntax-level import invariants on one
// import statement -- imports are always named (no wildcards, no bare
// side-effect imports) -- and records an import site for the format-agnostic
// verification pass, which owns the kind/import compatibility rules.
func (w *walker) checkImportDeclaration(stmt *astNode) {
	decl := stmt.AsImportDeclaration()
	spec := decl.ModuleSpecifier
	if spec == nil || spec.Kind != kindStringLiteral {
		w.addErr(errorAtNode(stmt, "imports must use a string module specifier"))
		return
	}
	pkg := spec.Text()

	clause := decl.ImportClause
	if clause == nil {
		w.addErr(errorAtNode(stmt, "side-effect imports are not allowed in schema files"))
		return
	}
	bindings := clause.AsImportClause().NamedBindings
	if bindings != nil && bindings.Kind == kindNamespaceImport {
		w.addErr(errorAtNode(stmt, "wildcard imports are not allowed; imports are always named in v2"))
		return
	}

	// Relative imports stay within the service; only package imports are
	// subject to the kind/import rules.
	if strings.HasPrefix(pkg, ".") {
		return
	}
	file, line, col := locationOfNode(stmt)
	w.importSites = append(w.importSites, verify.ImportSite{
		Package: pkg,
		File:    file, Line: line, Col: col,
	})
}
