package generator

import (
	"encoding/json"

	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// sqlOutputSchema is the JSON Schema of outputs.sql. registry.ParseOutputs
// checks the dialects themselves, so it can say why it refuses a list.
var sqlOutputSchema = json.RawMessage(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"migrationsDir": {"type": "string"},
		"viewOwner": {"type": "string", "pattern": "^[a-z][a-z0-9_]*$"},
		"dialects": {"type": "array", "items": {"type": "string"}}
	}
}`)

// typesGenerator is the name of the core generator behind outputs.types.
const typesGenerator = "types"

// ormGenerator is the name of the core generator of the Go ORM.
const ormGenerator = "orm"

// RegisterCore adds the core generators to reg, and the core's one target,
// `local`, with its platforms, connectors and provisioner
// (internal/stack/local), which a binary with no extension linked runs
// `stack dev` on. The kinds are registered by registry.New; this half lives
// here because the generator closures call the dispatch methods of this
// package, and the local target's provisioner plans migrations with
// sqlmigrate, which registry cannot import. Core registers no documents and
// no build-all hooks; extensions do.
//
// Registration order fixes Registry.OutputKeys: types, sql, api, sdk is the
// order the ParseOutputs error lists.
func RegisterCore(reg *registry.Registry) error {
	specs := []registry.GeneratorSpec{
		{
			Name:      typesGenerator,
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
			// block has no switch for them. outputs.sql places and owns the
			// generated projection view migrations and lists the dialects
			// the DDL is written for.
			Name:         "sql",
			OutputKey:    "sql",
			OutputSchema: sqlOutputSchema,
			Dirs: func(c registry.GenerateContext) []string {
				return []string{SQLDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Generate: func(c registry.GenerateContext) error {
				r := run{c}
				return r.measure("output.sql", r.generateSQL)
			},
		},
		{
			Name: ormGenerator,
			Dirs: func(c registry.GenerateContext) []string {
				return []string{ORMDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Generate: func(c registry.GenerateContext) error {
				r := run{c}
				return r.measure("output.orm", r.generateORM)
			},
		},
		{
			Name:       "api",
			OutputKey:  "api",
			ReadsCalls: true,
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
			// path; a no-op when the schema declares no @envVars class. Its
			// language follows outputs.types (envLoaderLanguage).
			Name: "envConfig",
			Dirs: func(c registry.GenerateContext) []string {
				return []string{APIDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Generate: func(c registry.GenerateContext) error {
				r := run{c}
				return r.measure("output.env-config", func() error {
					return r.generateEnvConfig(envLoaderLanguage(c.Outputs))
				})
			},
		},
		{
			// The Stack kind's one output: each environment of the stack,
			// resolved, at stack/<stack>/<environment>/environment.json
			// (docs/stack-model.md, section 5.1).
			Name:  stackgen.Name,
			Kinds: []string{string(ir.SchemaKindStack)},
			Dirs: func(c registry.GenerateContext) []string {
				return []string{stackgen.OutDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Generate: func(c registry.GenerateContext) error {
				return run{c}.measure("output.stack", func() error { return stackgen.Generate(c) })
			},
		},
	}
	for _, spec := range specs {
		if err := reg.RegisterGenerator(spec); err != nil {
			return err
		}
	}
	return local.Register(reg)
}

// envLoaderLanguage picks the language of the standalone env-var loader
// from outputs.types: Go when the Go types are on, Rust when only the Rust
// types are, and "" (the values schema alone) when neither is. The Go
// loader requires, replaces and imports the Go types module, so without
// that module it could not compile. The TypeScript loader is not standalone:
// generateTSTypes writes it into the TypeScript types package.
func envLoaderLanguage(outputs *registry.Outputs) string {
	switch {
	case outputs.TypesEnabled(LangGo):
		return LangGo
	case outputs.TypesEnabled(LangRust):
		return LangRust
	default:
		return ""
	}
}
