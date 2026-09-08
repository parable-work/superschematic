package verify

import (
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// checkImports enforces the kind's ForbiddenPackages on authoring imports,
// rejects authoring packages whose decorators are all restricted to other
// kinds (Registry.PackageAllowsKind), and applies the cross-kind
// type-reference rules on service imports that survive into schema.Imports
// (codegen). Authoring-only ImportSites used for @source / foreign extends
// are TypeScript compile-time and do not require a schema.config
// dependencies entry. The rules live on the registry's KindSpec and
// DecoratorSpec; this file holds no copy of them.
func checkImports(schema *ir.Schema, in Input, r *Result) {
	reg := in.registry()
	kind, _ := reg.Kind(string(schema.Kind))
	n := in.Naming.OrDefault()
	servicePackagePrefix := n.NpmServicePackagePrefix()
	codegenPackages := codegenImportPackages(schema)
	for _, site := range in.ImportSites {
		switch {
		case reg.IsAuthoringPackage(site.Package):
			declaring := n.DeclaringPackage(site.Package)
			if kind.ForbiddenPackages[declaring] || !reg.PackageAllowsKind(declaring, string(schema.Kind)) {
				r.errorAt(site.File, site.Line, site.Col,
					"a %s schema cannot import %s", schema.Kind, site.Package)
			}

		case strings.HasPrefix(site.Package, servicePackagePrefix):
			if !codegenPackages[site.Package] {
				continue
			}
			checkServiceReference(schema, kind, in, r, site)
		}
	}
}

// codegenImportPackages returns the set of service packages that appear in
// schema.Imports, the runtime/codegen dependency surface.
func codegenImportPackages(schema *ir.Schema) map[string]bool {
	pkgs := make(map[string]bool, len(schema.Imports))
	for _, imp := range schema.Imports {
		pkgs[imp.Package] = true
	}
	return pkgs
}

// checkServiceReference enforces the cross-kind type-reference rules for one
// @schemas/* import that appears in schema.Imports: the kind's
// AllowedReferences when it has a positive allowlist, else its
// DeniedReferences.
func checkServiceReference(schema *ir.Schema, kind registry.KindSpec, in Input, r *Result, site ImportSite) {
	service := serviceNameForPackage(site.Package)
	if service == schema.Name {
		return
	}
	// A kind whose files import member services as sentinels declares
	// membership, not type references, so no kind restriction applies.
	if kind.ImportsSiblingSentinels {
		return
	}

	depKind, declared := in.Dependencies[service]
	if !declared {
		r.errorAt(site.File, site.Line, site.Col,
			"%s imports %s but schema.config declares no dependency on service %q",
			schema.Name, site.Package, service)
		return
	}

	if kind.AllowedReferences != nil {
		if !kind.AllowedReferences[string(depKind)] {
			r.errorAt(site.File, site.Line, site.Col,
				"a %s schema cannot reference types from %s service %q", schema.Kind, depKind, service)
		}
		return
	}
	if kind.DeniedReferences[string(depKind)] {
		r.errorAt(site.File, site.Line, site.Col,
			"a %s schema cannot reference types from %s service %q", schema.Kind, depKind, service)
	}
}

// serviceNameForPackage derives the schema service name from a package name:
// the scope prefix is stripped ("@schemas/web-db" -> "web-db").
func serviceNameForPackage(pkg string) string {
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		return pkg[i+1:]
	}
	return pkg
}
