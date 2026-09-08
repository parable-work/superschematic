package registry

import (
	ir "github.com/parable-work/superschematic/ir"
)

// coreKinds returns the kinds psgen has always known, each with the
// authoring rules verify/kind.go and the tsreader walker apply to it and the
// generator pipeline generator.Run used to select in its kind switch. The
// generators the pipelines name are registered by generator.RegisterCore.
//
// ForbiddenPackages is keyed on the import specifier a schema file writes
// (verify.ImportSite.Package). The core packages are declared as
// @superschematic/* (pkgAPI, pkgDB) and re-exported as @psgen/*, and the
// schemas tsconfig maps both, so each rule names both spellings until a
// Naming alias map folds the specifier onto the declaring package
// (docs/extension-model.md item 29).
func coreKinds() []KindSpec {
	return []KindSpec{
		{
			Name:       string(ir.SchemaKindDB),
			StructRole: ir.RoleDBTable,
			ForbiddenPackages: map[string]bool{
				"@psgen/api": true,
				pkgAPI:       true,
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
				"@psgen/db": true,
				pkgDB:       true,
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
				"@psgen/api": true,
				"@psgen/db":  true,
				pkgAPI:       true,
				pkgDB:        true,
			},
			DeniedReferences: map[string]bool{
				string(ir.SchemaKindAPI): true,
				string(ir.SchemaKindDB):  true,
			},
			Pipeline: []string{"types", "envConfig"},
		},
	}
}
