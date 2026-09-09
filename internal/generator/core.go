package generator

import (
	"github.com/parable-work/superschematic/internal/registry"
)

// RegisterCore adds the core generators to reg. The kinds are registered by
// registry.New; this half lives here because the generator closures call
// the dispatch methods of this package. Core registers no documents and no
// build-all hooks; extensions do.
//
// Registration order fixes Registry.OutputKeys: types, api, sdk is the order
// the ParseOutputs error lists.
func RegisterCore(reg *registry.Registry) error {
	specs := []registry.GeneratorSpec{
		{
			Name:      "types",
			OutputKey: "types",
			Dirs: func(c registry.GenerateContext) []string {
				var dirs []string
				for _, lang := range c.Outputs.EnabledTypeLanguages() {
					dirs = append(dirs, TypesDir(c.Options.OutputRoot, lang, c.Config.Name))
				}
				return dirs
			},
			Generate: func(c registry.GenerateContext) error {
				return run{c}.generateTypes()
			},
		},
		{
			// SQL DDL and the Go ORM are implied by the DB kind; the outputs
			// block carries no switches for them.
			Name: "sql",
			Dirs: func(c registry.GenerateContext) []string {
				return []string{SQLDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Generate: func(c registry.GenerateContext) error {
				r := run{c}
				return r.measure("output.sql", r.generateSQL)
			},
		},
		{
			Name: "orm",
			Dirs: func(c registry.GenerateContext) []string {
				return []string{ORMDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Generate: func(c registry.GenerateContext) error {
				r := run{c}
				return r.measure("output.orm", r.generateORM)
			},
		},
		{
			Name:      "api",
			OutputKey: "api",
			Dirs: func(c registry.GenerateContext) []string {
				return []string{APIDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Enabled: func(c registry.GenerateContext) (bool, string) {
				return c.Outputs.APIEnabled(), ""
			},
			Generate: func(c registry.GenerateContext) error {
				r := run{c}
				return r.measure("output.api", r.generateAPI)
			},
		},
		{
			Name:      "sdks",
			OutputKey: "sdk",
			Dirs: func(c registry.GenerateContext) []string {
				var dirs []string
				for _, lang := range c.Outputs.EnabledSDKLanguages() {
					dirs = append(dirs, SDKDir(c.Options.OutputRoot, lang, c.Config.Name))
				}
				return dirs
			},
			Generate: func(c registry.GenerateContext) error {
				return run{c}.generateSDKs()
			},
		},
		{
			// The standalone env-var loader for schemas whose kind has no API
			// path; a no-op when the schema declares no @envVars class. Rust
			// output when the schema emits Rust types and no Go types.
			Name: "envConfig",
			Dirs: func(c registry.GenerateContext) []string {
				return []string{APIDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Generate: func(c registry.GenerateContext) error {
				r := run{c}
				return r.measure("output.env-config", func() error {
					return r.generateEnvConfig(c.Outputs.TypesEnabled(LangRust) && !c.Outputs.TypesEnabled(LangGo))
				})
			},
		},
	}
	for _, spec := range specs {
		if err := reg.RegisterGenerator(spec); err != nil {
			return err
		}
	}
	return nil
}
