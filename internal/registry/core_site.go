package registry

import (
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// siteGenerator is the Site kind's one generator: it writes the site's
// typed browser config into its code, and scaffolds the code once when it
// is missing. It is registered by generator.RegisterCore, with the other
// core generators.
const siteGenerator = "site"

// siteKind is the core Site kind (docs/stack-model.md, section 8.10, D55):
// a static site, a directory a front-end build writes, served as files. A
// site is a service so that a stack's `deploy` names it by its sentinel,
// and its config names the APIs it calls (`calls`) and how it builds
// (`site`). Its schema declares nothing: its code is TypeScript at its
// implementation path, in the Bun workspace, which imports the SDKs of the
// APIs it calls, and the kind's one generator writes the code's typed
// browser config there.
func siteKind() KindSpec {
	return KindSpec{
		Name:       string(ir.SchemaKindSite),
		StructRole: ir.RoleEmbeddedStruct,
		ForbiddenPackages: map[string]bool{
			pkgAPI: true,
			pkgDB:  true,
		},
		Pipeline: []string{siteGenerator},
		Verify:   verifySite,
	}
}

// verifySite refuses a Site schema that declares anything, and a site
// config that does not check (ir.SiteConfig.Check): a site's types are
// those of the APIs it calls, which their SDKs carry.
func verifySite(schema *ir.Schema, r VerifyReporter) {
	var declared []string
	for name := range schema.Types {
		declared = append(declared, name)
	}
	for name := range schema.Enums {
		declared = append(declared, name)
	}
	for name := range schema.Unions {
		declared = append(declared, name)
	}
	for name := range schema.Scalars {
		declared = append(declared, name)
	}
	for _, set := range schema.OperationSets {
		if set != nil {
			declared = append(declared, set.Name)
		}
	}
	sort.Strings(declared)
	for _, name := range declared {
		r.Errorf("", "Site service %s declares %s; a Site schema declares nothing, and its code imports the types of the APIs it calls from their SDKs (D55)", schema.Name, name)
	}
	if schema.Site != nil {
		if err := schema.Site.Check(); err != nil {
			r.Errorf("", "Site service %s: %v", schema.Name, err)
		}
	}
}
