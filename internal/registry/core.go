package registry

import (
	ir "github.com/parable-work/superschematic/ir"
)

// coreKinds returns the core kinds, each with the authoring rules
// verify/kind.go and the tsreader walker apply to it and its generator
// pipeline. The generators the pipelines name are registered by
// generator.RegisterCore.
//
// ForbiddenPackages is keyed on the declaring authoring package (pkgAPI,
// pkgDB). verify folds the import specifier a schema file writes onto its
// declaring package through Naming.DeclaringPackage before the lookup, so a
// distribution that re-exports the core packages under other names needs
// only the [package_aliases] table, not a second key here.
func coreKinds() []KindSpec {
	return []KindSpec{
		{
			Name:       string(ir.SchemaKindDB),
			StructRole: ir.RoleDBTable,
			ForbiddenPackages: map[string]bool{
				pkgAPI: true,
			},
			AllowedReferences: map[string]bool{
				string(ir.SchemaKindGeneral): true,
			},
			Pipeline: []string{"sql", "orm", "types"},
		},
		{
			Name:                 string(ir.SchemaKindAPI),
			StructRole:           ir.RoleEmbeddedStruct,
			SourceProjectionRole: ir.RoleAPIView,
			AllowsOperationSets:  true,
			ForbiddenPackages: map[string]bool{
				pkgDB: true,
			},
			AllowedReferences: map[string]bool{
				string(ir.SchemaKindDB):      true,
				string(ir.SchemaKindGeneral): true,
			},
			Pipeline: []string{"types", "api", "sdks"},
		},
		{
			Name:                 string(ir.SchemaKindGeneral),
			StructRole:           ir.RoleEmbeddedStruct,
			SourceProjectionRole: ir.RoleEmbeddedStruct,
			ForbiddenPackages: map[string]bool{
				pkgAPI: true,
				pkgDB:  true,
			},
			DeniedReferences: map[string]bool{
				string(ir.SchemaKindAPI): true,
				string(ir.SchemaKindDB):  true,
			},
			Pipeline: []string{"types", "envConfig"},
		},
	}
}
