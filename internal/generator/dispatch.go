package generator

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/gosdkgen"
	"github.com/parable-work/superschematic/internal/generator/ormgen"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/pysdkgen"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/rustsdkgen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	ir "github.com/parable-work/superschematic/ir"
)

// Per-artifact generation steps, one per output kind. Steps record an output
// as skipped when the schema cannot produce it (e.g. an SDK for a schema with
// no operations) so `superschematic build` reports the full requested surface.

// generateTypes runs the per-language type-library generators selected by
// outputs.types.
func (r run) generateTypes() error {
	for _, lang := range r.Outputs.EnabledTypeLanguages() {
		switch lang {
		case "go":
			if err := r.measure("output.types-go", r.generateGoTypes); err != nil {
				return err
			}
		case "typescript":
			if err := r.measure("output.types-typescript", r.generateTSTypes); err != nil {
				return err
			}
		case "python":
			if err := r.measure("output.types-python", r.generatePyTypes); err != nil {
				return err
			}
		case "rust":
			if err := r.measure("output.types-rust", r.generateRustTypes); err != nil {
				return err
			}
		default:
			r.Skip("types-" + lang)
		}
	}
	return nil
}

func codegenProfilePrefixes(outputPhase string) []string {
	return []string{"generator.codegen", "generator." + outputPhase + ".codegen"}
}

// loadDependencySchemas loads the IR for every declared service dependency,
// keyed by service name. Generators use these to classify imported symbols.
func (r run) loadDependencySchemas() (map[string]*ir.Schema, error) {
	done := r.Options.Profile.Start("generator.load-dependencies")
	defer done()

	if len(r.Config.Dependencies) == 0 {
		return nil, nil
	}

	deps := make(map[string]*ir.Schema, len(r.Config.Dependencies))
	for _, dep := range r.Config.Dependencies {
		depSchema, err := r.LoadDependency(dep.Name)
		if err != nil {
			if r.Options.LoadDependency == nil {
				return nil, fmt.Errorf("generator: schema %s declares dependencies but no dependency loader is configured", r.Config.Name)
			}
			return nil, fmt.Errorf("generator: load dependency %s: %w", dep.Name, err)
		}
		deps[dep.Name] = depSchema
	}
	return deps, nil
}

// runMemo is the state one generator run shares across its pipeline: the
// dependency IRs loaded so far and the API output apigen produced. The
// context's LoadDependency and APIOutput closures are its methods.
type runMemo struct {
	r                 run
	dependencySchemas map[string]*ir.Schema
	apiOutput         *apigen.APIOutput
	apiOutputReady    bool
	envConfig         *envgen.ConfigOutput
	envConfigReady    bool
}

// buildEnvConfig resolves the schema's @envVars contract once per run for
// the generators that derive configuration from it (the standalone env
// loader, document generators). Returns (nil, nil) when the schema declares
// no @envVars type.
func (m *runMemo) buildEnvConfig() (*envgen.ConfigOutput, error) {
	if m.envConfigReady {
		return m.envConfig, nil
	}
	r := m.r
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return nil, err
	}
	output, err := envgen.GenerateWithOptions(r.Schema, envgen.Options{
		SchemaName:   r.Config.Name,
		Dependencies: deps,
		Naming:       r.Options.Naming,
	})
	if err != nil {
		return nil, fmt.Errorf("generator: env config for %s: %w", r.Config.Name, err)
	}
	m.envConfig = output
	m.envConfigReady = true
	return output, nil
}

func (m *runMemo) loadDependency(name string) (*ir.Schema, error) {
	if depSchema, ok := m.dependencySchemas[name]; ok {
		return depSchema, nil
	}
	if m.r.Options.LoadDependency == nil {
		return nil, fmt.Errorf("dependency loader is not configured")
	}
	depSchema, err := m.r.Options.LoadDependency(name)
	if err != nil {
		return nil, err
	}
	if m.dependencySchemas == nil {
		m.dependencySchemas = make(map[string]*ir.Schema)
	}
	m.dependencySchemas[name] = depSchema
	return depSchema, nil
}

// generateGoTypes emits the Go type-library module.
func (r run) generateGoTypes() error {
	var deps map[string]*ir.Schema
	depModules := map[string]string{}
	if err := r.measure("output.types-go.prepare", func() error {
		var err error
		deps, err = r.loadDependencySchemas()
		if err != nil {
			return err
		}
		depModules = make(map[string]string, len(deps))
		for name := range deps {
			depModules[name] = r.Options.Naming.GoTypesModule(name)
		}
		return nil
	}); err != nil {
		return err
	}

	var output *typegen.ModuleOutput
	if err := r.measure("output.types-go.generate", func() error {
		var err error
		output, err = typegen.Generate(r.Schema, typegen.Options{
			SchemaName:        r.Config.Name,
			ModulePath:        r.Options.Naming.GoTypesModule(r.Config.Name),
			Dependencies:      deps,
			DependencyModules: depModules,
			Naming:            r.Options.Naming,
			Clock:             r.Options.Clock,
		})
		return err
	}); err != nil {
		return fmt.Errorf("generator: go types for %s: %w", r.Config.Name, err)
	}

	dir := TypesDir(r.Options.OutputRoot, "go", r.Config.Name)
	if err := r.measure("output.types-go.prepare", func() error {
		return typegen.SetReplacePaths(output, r.Options.Paths, dir)
	}); err != nil {
		return fmt.Errorf("generator: go types for %s: %w", r.Config.Name, err)
	}
	if err := r.measure("output.types-go.write", func() error {
		return typegen.WriteTypesWithProfile(output, dir, r.Options.Profile, r.Options.SkipFormat, codegenProfilePrefixes("output.types-go")...)
	}); err != nil {
		return fmt.Errorf("generator: go types for %s: %w", r.Config.Name, err)
	}

	r.Done("types-go", dir)
	return nil
}

// generateTSTypes emits the TypeScript type package.
func (r run) generateTSTypes() error {
	var deps map[string]*ir.Schema
	depPackages := map[string]string{}
	if err := r.measure("output.types-typescript.prepare", func() error {
		var err error
		deps, err = r.loadDependencySchemas()
		if err != nil {
			return err
		}
		depPackages = make(map[string]string, len(deps))
		for name := range deps {
			depPackages[name] = r.Options.Naming.NpmTypesPackage(name)
		}
		return nil
	}); err != nil {
		return err
	}

	var output *tsgen.ModuleOutput
	if err := r.measure("output.types-typescript.generate", func() error {
		var err error
		output, err = tsgen.Generate(r.Schema, tsgen.Options{
			SchemaName:         r.Config.Name,
			Dependencies:       deps,
			DependencyPackages: depPackages,
			Naming:             r.Options.Naming,
			Clock:              r.Options.Clock,
		})
		return err
	}); err != nil {
		return fmt.Errorf("generator: typescript types for %s: %w", r.Config.Name, err)
	}

	dir := TypesDir(r.Options.OutputRoot, "typescript", r.Config.Name)
	if err := r.measure("output.types-typescript.prepare", func() error {
		return tsgen.SetScalarLibSpec(output, r.Options.Paths, dir)
	}); err != nil {
		return fmt.Errorf("generator: typescript types for %s: %w", r.Config.Name, err)
	}
	if err := r.measure("output.types-typescript.write", func() error {
		if err := tsgen.WriteTypesWithProfile(output, dir, r.Options.Profile, r.Options.SkipFormat, codegenProfilePrefixes("output.types-typescript")...); err != nil {
			return err
		}
		// Sibling packages resolve each other through file:../<schema>, so
		// the directory holding them is a Bun workspace root (see
		// tsgen.WorkspaceRootManifest).
		return tsgen.WriteWorkspaceRoot(filepath.Dir(dir), r.Options.Naming)
	}); err != nil {
		return fmt.Errorf("generator: typescript types for %s: %w", r.Config.Name, err)
	}

	r.Done("types-typescript", dir)
	return nil
}

// generatePyTypes emits the Python type package.
func (r run) generatePyTypes() error {
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return err
	}

	output, err := pygen.Generate(r.Schema, pygen.Options{
		SchemaName:   r.Config.Name,
		Dependencies: deps,
		Naming:       r.Options.Naming,
		Clock:        r.Options.Clock,
	})
	if err != nil {
		return fmt.Errorf("generator: python types for %s: %w", r.Config.Name, err)
	}

	dir := TypesDir(r.Options.OutputRoot, "python", r.Config.Name)
	if err := pygen.WriteTypes(output, dir); err != nil {
		return fmt.Errorf("generator: python types for %s: %w", r.Config.Name, err)
	}

	r.Done("types-python", dir)
	return nil
}

// generateRustTypes emits the Rust type crate.
func (r run) generateRustTypes() error {
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return err
	}

	depCrates := make(map[string]string, len(deps))
	for name := range deps {
		depCrates[name] = r.Options.Naming.RustTypesCrate(name)
	}

	output, err := rustgen.Generate(r.Schema, rustgen.Options{
		SchemaName:       r.Config.Name,
		Dependencies:     deps,
		DependencyCrates: depCrates,
		Naming:           r.Options.Naming,
		Clock:            r.Options.Clock,
	})
	if err != nil {
		return fmt.Errorf("generator: rust types for %s: %w", r.Config.Name, err)
	}

	dir := TypesDir(r.Options.OutputRoot, "rust", r.Config.Name)
	if err := rustgen.SetScalarLibPath(output, r.Options.Paths, dir); err != nil {
		return fmt.Errorf("generator: rust types for %s: %w", r.Config.Name, err)
	}
	if err := rustgen.WriteTypes(output, dir); err != nil {
		return fmt.Errorf("generator: rust types for %s: %w", r.Config.Name, err)
	}

	r.Done("types-rust", dir)
	return nil
}

// generateSQL emits SQL DDL for DB schemas.
func (r run) generateSQL() error {
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return err
	}
	output, err := sqlgen.Generate(r.Schema, sqlgen.Options{
		SchemaName:   r.Config.Name,
		Dependencies: deps,
		Clock:        r.Options.Clock,
	})
	if err != nil {
		return fmt.Errorf("generator: sql for %s: %w", r.Config.Name, err)
	}
	if output == nil {
		r.Skip("sql")
		return nil
	}

	dir := SQLDir(r.Options.OutputRoot, r.Config.Name)
	if err := sqlgen.WriteDDL(output, dir); err != nil {
		return fmt.Errorf("generator: sql for %s: %w", r.Config.Name, err)
	}

	r.Done("sql", dir)
	return nil
}

// generateORM emits the Go ORM for DB schemas.
func (r run) generateORM() error {
	// Dependencies name the imported unions a JSONB field decodes through,
	// and their types modules get replace lines in the ORM's go.mod.
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return fmt.Errorf("generator: load ORM dependencies for %s: %w", r.Config.Name, err)
	}
	var output *ormgen.ORMOutput
	if err := r.measure("output.orm.generate", func() error {
		var err error
		output, err = ormgen.Generate(r.Schema, ormgen.Options{
			SchemaName:   r.Config.Name,
			ModulePath:   r.Options.Naming.GoORMModule(r.Config.Name),
			TypesModule:  r.Options.Naming.GoTypesModule(r.Config.Name),
			Naming:       r.Options.Naming,
			Dependencies: deps,
			Clock:        r.Options.Clock,
		})
		return err
	}); err != nil {
		return fmt.Errorf("generator: orm for %s: %w", r.Config.Name, err)
	}
	if output == nil {
		r.Skip("orm")
		return nil
	}

	dir := ORMDir(r.Options.OutputRoot, r.Config.Name)
	if err := r.measure("output.orm.prepare", func() error {
		return ormgen.SetReplacePaths(output, r.Options.Paths, dir)
	}); err != nil {
		return fmt.Errorf("generator: orm for %s: %w", r.Config.Name, err)
	}
	if err := r.measure("output.orm.write", func() error {
		return ormgen.WriteORMWithProfile(output, dir, r.Options.Profile, r.Options.SkipFormat, codegenProfilePrefixes("output.orm")...)
	}); err != nil {
		return fmt.Errorf("generator: orm for %s: %w", r.Config.Name, err)
	}

	r.Done("orm", dir)
	return nil
}

// generateAPI dispatches API server generation by implementation language.
func (r run) generateAPI() error {
	if proto := r.Outputs.API.Protocol; proto != APIProtocolREST {
		return fmt.Errorf("generator: schema %s requests unsupported API protocol %q", r.Config.Name, proto)
	}
	if r.Outputs.API.Language == APILanguageRust {
		return r.generateRustAPI()
	}
	return r.generateGoAPI()
}

// resolveUpstreamAuth determines the DB schema backing authentication for a
// public API: the authDb config value when set, otherwise the single DB-kind
// dependency. Non-public schemas carry no upstream auth.
func (r run) resolveUpstreamAuth() (string, *ir.Schema, error) {
	if !r.Config.Public {
		return "", nil, nil
	}

	name := r.Config.AuthDB
	if name == "" {
		for _, dep := range r.Config.Dependencies {
			if dep.Kind != ir.SchemaKindDB {
				continue
			}
			if name != "" {
				return "", nil, fmt.Errorf("generator: public API schema %s has multiple DB dependencies; set authDb to pick the auth store", r.Config.Name)
			}
			name = dep.Name
		}
	}
	if name == "" {
		return "", nil, fmt.Errorf("generator: public API schema %s requires an authDb config value or a DB-kind dependency", r.Config.Name)
	}

	upstream, err := r.LoadDependency(name)
	if err != nil {
		if r.Options.LoadDependency == nil {
			return "", nil, fmt.Errorf("generator: schema %s requires upstream schema %s but no dependency loader is configured", r.Config.Name, name)
		}
		return "", nil, fmt.Errorf("generator: load upstream auth schema %s: %w", name, err)
	}
	return name, upstream, nil
}

// buildAPIOutput assembles the apigen output consumed by both the API server
// writer and the SDK generators. Returns (nil, nil) when the schema declares
// no operations.
func (m *runMemo) buildAPIOutput() (*apigen.APIOutput, error) {
	if m.apiOutputReady {
		return m.apiOutput, nil
	}
	r := m.r

	done := r.Options.Profile.Start("generator.build-api-output")
	defer done()

	deps, err := r.loadDependencySchemas()
	if err != nil {
		return nil, err
	}
	moduleDeps := make([]string, 0, len(deps))
	for name := range deps {
		moduleDeps = append(moduleDeps, r.Options.Naming.GoTypesModule(name))
	}
	sort.Strings(moduleDeps)

	upstreamName, upstreamIR, err := r.resolveUpstreamAuth()
	if err != nil {
		return nil, err
	}
	provider, err := r.Registry.SelectedAuthProvider()
	if err != nil {
		return nil, err
	}

	output, err := apigen.Generate(r.Schema, apigen.Options{
		SchemaName:         r.Config.Name,
		ModulePath:         r.Options.Naming.GoAPIModule(r.Config.Name),
		TypesModule:        r.Options.Naming.GoTypesModule(r.Config.Name),
		ModuleDependencies: moduleDeps,
		Dependencies:       deps,
		IsPublic:           r.Config.Public,
		UpstreamSchema:     upstreamName,
		UpstreamIR:         upstreamIR,
		Naming:             r.Options.Naming,
		Provider:           provider,
		Clock:              r.Options.Clock,
		OpenAPIHooks:       r.Registry.OpenAPIHooks(),
		ToolHooks:          r.Registry.ToolHooks(),
	})
	if err != nil {
		return nil, fmt.Errorf("generator: api for %s: %w", r.Config.Name, err)
	}
	m.apiOutput = output
	m.apiOutputReady = true
	return output, nil
}

// generateGoAPI emits the Go REST API server, route scaffolds, and the
// env-var loader. Schemas with @envVars config but no operations still get
// their env loader via the standalone path.
func (r run) generateGoAPI() error {
	var output *apigen.APIOutput
	if err := r.measure("output.api.prepare", func() error {
		var err error
		output, err = r.APIOutput()
		return err
	}); err != nil {
		return err
	}
	if output == nil {
		return r.generateEnvConfig(false)
	}
	apiOutput := *output
	output = &apiOutput

	if err := r.measure("output.api.generate-env", func() error {
		envConfig, err := envgen.GenerateWithOptions(r.Schema, envgen.Options{
			SchemaName: r.Config.Name,
			Naming:     r.Options.Naming,
		})
		if err != nil {
			return err
		}
		output.EnvConfig = envConfig
		return nil
	}); err != nil {
		return fmt.Errorf("generator: env config for %s: %w", r.Config.Name, err)
	}

	dir := APIDir(r.Options.OutputRoot, r.Config.Name)
	if err := r.measure("output.api.prepare", func() error {
		return apigen.SetReplacePaths(output, r.Options.Paths, dir)
	}); err != nil {
		return fmt.Errorf("generator: api for %s: %w", r.Config.Name, err)
	}
	if err := r.measure("output.api.write", func() error {
		return apigen.WriteAPIWithProfile(output, dir, r.Options.Profile, r.Options.SkipFormat, codegenProfilePrefixes("output.api")...)
	}); err != nil {
		return fmt.Errorf("generator: api for %s: %w", r.Config.Name, err)
	}
	r.Done("api", dir)

	if subdir := r.Outputs.API.ScaffoldsOutputDir; subdir != "" {
		scaffoldsDir := filepath.Join(r.Options.ServicePath, subdir)
		var scaffolds *codegen.ScaffoldResult
		if err := r.measure("output.api.write-scaffolds", func() error {
			var err error
			scaffolds, err = apigen.WriteScaffoldsWithProfile(output, scaffoldsDir, r.Options.Profile, r.Options.SkipFormat, codegenProfilePrefixes("output.api.write-scaffolds")...)
			return err
		}); err != nil {
			return fmt.Errorf("generator: api scaffolds for %s: %w", r.Config.Name, err)
		}
		r.Logf("  + api scaffolds: %d created, %d skipped (existing) in %s\n",
			len(scaffolds.Generated), len(scaffolds.Skipped), scaffoldsDir)
	}
	return nil
}

// generateRustAPI emits the Rust REST API server and route scaffolds. The
// Rust writer does not emit env config; the standalone env path covers it.
func (r run) generateRustAPI() error {
	provider, err := r.Registry.SelectedAuthProvider()
	if err != nil {
		return err
	}
	output, err := rustrestgen.Generate(r.Schema, rustrestgen.Options{
		SchemaName:   r.Config.Name,
		IsPublic:     r.Config.Public,
		TypesCrate:   r.Options.Naming.RustTypesCrate(r.Config.Name),
		TypesDir:     TypesDir(r.Options.OutputRoot, "rust", r.Config.Name),
		OutputDir:    APIDir(r.Options.OutputRoot, r.Config.Name),
		Naming:       r.Options.Naming,
		AuthProvider: provider,
		Clock:        r.Options.Clock,
	})
	if err != nil {
		return fmt.Errorf("generator: rust api for %s: %w", r.Config.Name, err)
	}
	if output == nil {
		return r.generateEnvConfig(true)
	}

	dir := APIDir(r.Options.OutputRoot, r.Config.Name)
	if err := rustrestgen.SetReplacePaths(output, r.Options.Paths, dir); err != nil {
		return fmt.Errorf("generator: rust api for %s: %w", r.Config.Name, err)
	}
	if err := rustrestgen.WriteAPI(output, dir); err != nil {
		return fmt.Errorf("generator: rust api for %s: %w", r.Config.Name, err)
	}
	r.Done("api-rust", dir)

	if subdir := r.Outputs.API.ScaffoldsOutputDir; subdir != "" {
		scaffoldsDir := filepath.Join(r.Options.ServicePath, subdir)
		scaffolds, err := rustrestgen.WriteScaffolds(output, scaffoldsDir)
		if err != nil {
			return fmt.Errorf("generator: rust api scaffolds for %s: %w", r.Config.Name, err)
		}
		r.Logf("  + api scaffolds: %d created, %d skipped (existing) in %s\n",
			len(scaffolds.Generated), len(scaffolds.Skipped), scaffoldsDir)
	}
	return r.generateEnvConfig(true)
}

// generateEnvConfig emits the standalone env-var loader for schemas whose API
// path does not write it: env-only schemas (no operations) and Rust API
// schemas. Silently does nothing when the schema declares no @envVars type.
func (r run) generateEnvConfig(rust bool) error {
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return err
	}

	output, err := envgen.GenerateWithOptions(r.Schema, envgen.Options{
		SchemaName:   r.Config.Name,
		Dependencies: deps,
		Naming:       r.Options.Naming,
	})
	if err != nil {
		return fmt.Errorf("generator: env config for %s: %w", r.Config.Name, err)
	}
	if output == nil {
		return nil
	}

	dir := APIDir(r.Options.OutputRoot, r.Config.Name)
	if rust {
		err = envgen.WriteRustConfig(output, dir)
	} else {
		err = envgen.WriteConfigModule(output, dir)
	}
	if err != nil {
		return fmt.Errorf("generator: env config for %s: %w", r.Config.Name, err)
	}

	r.Done("env-config", dir)
	return nil
}

// generateSDKs runs the per-language client SDK generators selected by
// outputs.sdk.
func (r run) generateSDKs() error {
	for _, lang := range r.Outputs.EnabledSDKLanguages() {
		switch lang {
		case LangTypeScript:
			if err := r.measure("output.sdk-typescript", r.generateTypeScriptSDK); err != nil {
				return err
			}
		case LangGo:
			if err := r.measure("output.sdk-go", r.generateGoSDK); err != nil {
				return err
			}
		case LangPython:
			if err := r.measure("output.sdk-python", r.generatePythonSDK); err != nil {
				return err
			}
		case LangRust:
			if err := r.measure("output.sdk-rust", r.generateRustSDK); err != nil {
				return err
			}
		default:
			r.Skip("sdk-" + lang)
		}
	}
	return nil
}

// generateTypeScriptSDK emits the TypeScript client SDK with LLM tool-call
// bindings. The SDK parses responses with the generated TS types package, so
// the type output is regenerated here for its parseable type set.
func (r run) generateTypeScriptSDK() error {
	var apiOutput *apigen.APIOutput
	if err := r.measure("output.sdk-typescript.prepare", func() error {
		var err error
		apiOutput, err = r.APIOutput()
		return err
	}); err != nil {
		return err
	}
	if apiOutput == nil {
		r.Skip("sdk-typescript")
		return nil
	}

	var deps map[string]*ir.Schema
	depPackages := map[string]string{}
	if err := r.measure("output.sdk-typescript.prepare", func() error {
		var err error
		deps, err = r.loadDependencySchemas()
		if err != nil {
			return err
		}
		depPackages = make(map[string]string, len(deps))
		for name := range deps {
			depPackages[name] = r.Options.Naming.NpmTypesPackage(name)
		}
		return nil
	}); err != nil {
		return err
	}
	var tsOutput *tsgen.ModuleOutput
	if err := r.measure("output.sdk-typescript.generate-types", func() error {
		var err error
		tsOutput, err = tsgen.Generate(r.Schema, tsgen.Options{
			SchemaName:         r.Config.Name,
			Dependencies:       deps,
			DependencyPackages: depPackages,
			Naming:             r.Options.Naming,
			Clock:              r.Options.Clock,
		})
		return err
	}); err != nil {
		return fmt.Errorf("generator: typescript sdk for %s: %w", r.Config.Name, err)
	}

	var sdkOutput *sdkgen.SDKOutput
	if err := r.measure("output.sdk-typescript.generate", func() error {
		var err error
		sdkOutput, err = sdkgen.Generate(apiOutput, tsgen.ParseableTypeNames(tsOutput), r.Options.Clock)
		return err
	}); err != nil {
		return fmt.Errorf("generator: typescript sdk for %s: %w", r.Config.Name, err)
	}

	dir := SDKDir(r.Options.OutputRoot, "typescript", r.Config.Name)
	if err := r.measure("output.sdk-typescript.write", func() error {
		return sdkgen.WriteSDKWithToolsProfiled(sdkOutput, apiOutput, dir, r.Options.Clock, r.Options.Profile, r.Options.SkipFormat, codegenProfilePrefixes("output.sdk-typescript")...)
	}); err != nil {
		return fmt.Errorf("generator: typescript sdk for %s: %w", r.Config.Name, err)
	}

	r.Done("sdk-typescript", dir)
	return nil
}

// generateGoSDK emits the Go client SDK with LLM tool-call bindings.
func (r run) generateGoSDK() error {
	var apiOutput *apigen.APIOutput
	if err := r.measure("output.sdk-go.prepare", func() error {
		var err error
		apiOutput, err = r.APIOutput()
		return err
	}); err != nil {
		return err
	}
	if apiOutput == nil {
		r.Skip("sdk-go")
		return nil
	}

	modulePath := r.Options.Naming.GoSDKModule(r.Config.Name)
	var sdkOutput *gosdkgen.SDKOutput
	if err := r.measure("output.sdk-go.generate", func() error {
		var err error
		sdkOutput, err = gosdkgen.Generate(apiOutput, modulePath, "sdk", r.Options.Clock)
		return err
	}); err != nil {
		return fmt.Errorf("generator: go sdk for %s: %w", r.Config.Name, err)
	}

	dir := SDKDir(r.Options.OutputRoot, "go", r.Config.Name)
	typesDir := TypesDir(r.Options.OutputRoot, "go", r.Config.Name)
	if err := gosdkgen.SetReplacePaths(sdkOutput, r.Options.Paths, dir); err != nil {
		return fmt.Errorf("generator: go sdk for %s: %w", r.Config.Name, err)
	}
	if err := r.measure("output.sdk-go.write", func() error {
		return gosdkgen.WriteSDKWithToolsProfiled(sdkOutput, apiOutput, dir, typesDir, r.Options.Clock, r.Options.Profile, r.Options.SkipFormat, codegenProfilePrefixes("output.sdk-go")...)
	}); err != nil {
		return fmt.Errorf("generator: go sdk for %s: %w", r.Config.Name, err)
	}

	r.Done("sdk-go", dir)
	return nil
}

// generatePythonSDK emits the Python client SDK. Package naming defaults are
// derived inside pysdkgen from the API output's schema name.
func (r run) generatePythonSDK() error {
	apiOutput, err := r.APIOutput()
	if err != nil {
		return err
	}
	if apiOutput == nil {
		r.Skip("sdk-python")
		return nil
	}

	sdkOutput, err := pysdkgen.Generate(apiOutput, "", "", r.Options.Clock)
	if err != nil {
		return fmt.Errorf("generator: python sdk for %s: %w", r.Config.Name, err)
	}

	dir := SDKDir(r.Options.OutputRoot, "python", r.Config.Name)
	if err := pysdkgen.WriteSDK(sdkOutput, dir); err != nil {
		return fmt.Errorf("generator: python sdk for %s: %w", r.Config.Name, err)
	}

	r.Done("sdk-python", dir)
	return nil
}

// generateRustSDK emits the Rust client SDK with LLM tool-call bindings.
func (r run) generateRustSDK() error {
	apiOutput, err := r.APIOutput()
	if err != nil {
		return err
	}
	if apiOutput == nil {
		r.Skip("sdk-rust")
		return nil
	}

	crateName := r.Options.Naming.RustSDKCrate(r.Config.Name)
	sdkOutput, err := rustsdkgen.Generate(apiOutput, crateName, r.Options.Naming.RustTypesCrate(r.Config.Name), r.Options.Clock)
	if err != nil {
		return fmt.Errorf("generator: rust sdk for %s: %w", r.Config.Name, err)
	}

	dir := SDKDir(r.Options.OutputRoot, "rust", r.Config.Name)
	typesDir := TypesDir(r.Options.OutputRoot, "rust", r.Config.Name)
	if err := rustsdkgen.WriteSDKWithTools(sdkOutput, apiOutput, dir, typesDir, r.Options.Clock); err != nil {
		return fmt.Errorf("generator: rust sdk for %s: %w", r.Config.Name, err)
	}

	r.Done("sdk-rust", dir)
	return nil
}
