package apigen

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/profile"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// WriteAPI writes the generated API module into outputDir as a flat,
// importable Go module:
//
//	{schema-name}/
//	├── go.mod          # Module definition
//	├── interfaces.go   # Implementation interfaces per namespace
//	├── routes.go       # RegisterRoutes() with handler factories
//	├── middleware.go   # Auth/tenant middleware + ORM store adapters (public only)
//	├── openapi.go      # Embedded OpenAPI spec constant
//	├── openapi.json    # Standalone spec for downstream tooling
//	├── index.go        # Index page HTML
//	├── rapidoc.go      # API docs page HTML
//	├── errors.go       # Error shims over psgen-http-runtime
//	├── response.go     # Response shims over psgen-http-runtime
//	├── context.go      # Request context shims
//	├── constants.go    # SystemUserID (public schemas with a UUID scalar)
//	└── fileupload.go   # Multipart upload helpers (only with file uploads)
func WriteAPI(output *APIOutput, outputDir string) error {
	return WriteAPIWithProfile(output, outputDir, nil, false)
}

// WriteAPIWithProfile writes the generated API module with shared codegen profiling.
func WriteAPIWithProfile(output *APIOutput, outputDir string, prof *profile.Profiler, skipFormat bool, phasePrefixes ...string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}
	funcs, err := templateFuncs(output.Provider)
	if err != nil {
		return err
	}
	generator := codegen.NewFileGenerator(
		templatesFS,
		funcs,
		codegen.WithProfiler(prof, phasePrefixes...),
		codegen.WithSkipFormat(skipFormat),
	)
	providerGenerator := codegen.NewFileGenerator(
		output.Provider.Templates(),
		funcs,
		codegen.WithProfiler(prof, phasePrefixes...),
		codegen.WithSkipFormat(skipFormat),
	)

	staticFiles := []struct {
		template string
		filename string
	}{
		{"module.tmpl", "go.mod"},
		{"interfaces.tmpl", "interfaces.go"},
		{"routes.tmpl", "routes.go"},
		{"openapi.tmpl", "openapi.go"},
		{"index.tmpl", "index.go"},
		{"rapidoc.tmpl", "rapidoc.go"},
		{"errors.tmpl", "errors.go"},
		{"response.tmpl", "response.go"},
		{"context.tmpl", "context.go"},
	}
	staticTasks := make([]func() error, 0, len(staticFiles))
	for _, file := range staticFiles {
		file := file
		staticTasks = append(staticTasks, func() error {
			if err := generateFile(generator, templatesFS, file.template, filepath.Join(outputDir, file.filename), output, funcs); err != nil {
				return fmt.Errorf("failed to write %s: %w", file.filename, err)
			}
			return nil
		})
	}
	if err := codegen.RunParallel(staticTasks); err != nil {
		return err
	}

	if output.OpenAPISpecRaw != "" {
		if err := os.WriteFile(filepath.Join(outputDir, "openapi.json"), []byte(output.OpenAPISpecRaw), 0o644); err != nil {
			return fmt.Errorf("failed to write openapi.json: %w", err)
		}
	}

	conditionalFiles := []codegen.ConditionalFile{
		{Condition: output.IsPublic, Template: "middleware.tmpl", Filename: "middleware.go"},
		{Condition: output.HasConstants(), Template: "constants.tmpl", Filename: "constants.go"},
		{Condition: output.HasFileUpload, Template: "fileupload.tmpl", Filename: "fileupload.go"},
	}
	if err := codegen.WriteConditionalFilesParallel(conditionalFiles, outputDir, func(templateName, outputPath string) error {
		return generateFile(generator, templatesFS, templateName, outputPath, output, funcs)
	}); err != nil {
		return err
	}
	if err := codegen.WriteConditionalFilesParallel(output.Provider.Files(output), outputDir, func(templateName, outputPath string) error {
		return generateFile(providerGenerator, output.Provider.Templates(), templateName, outputPath, output, funcs)
	}); err != nil {
		return err
	}

	if output.EnvConfig != nil {
		if err := envgen.WriteConfig(output.EnvConfig, outputDir); err != nil {
			return fmt.Errorf("failed to write env config: %w", err)
		}
	}

	return nil
}

// WriteScaffolds writes route-impl scaffold files into scaffoldsDir, skipping
// files that already exist so service-owned implementations are preserved.
func WriteScaffolds(output *APIOutput, scaffoldsDir string) (*codegen.ScaffoldResult, error) {
	return WriteScaffoldsWithProfile(output, scaffoldsDir, nil, false)
}

// WriteScaffoldsWithProfile writes route-impl scaffold files with shared codegen profiling.
func WriteScaffoldsWithProfile(output *APIOutput, scaffoldsDir string, prof *profile.Profiler, skipFormat bool, phasePrefixes ...string) (*codegen.ScaffoldResult, error) {
	funcs, err := templateFuncs(output.Provider)
	if err != nil {
		return nil, err
	}
	generator := codegen.NewFileGenerator(
		templatesFS,
		funcs,
		codegen.WithProfiler(prof, phasePrefixes...),
		codegen.WithSkipFormat(skipFormat),
	)
	return codegen.WriteScaffolds(codegen.ScaffoldConfig[EndpointInfo]{
		ScaffoldsDir: scaffoldsDir,
		ReadmeData:   output,
		Namespaces:   output.Namespaces,
		Endpoints:    output.Endpoints,
		EndpointNamespace: func(ep EndpointInfo) string {
			return ep.Namespace
		},
		NamespaceData: func(namespace string) any {
			return NamespaceOutput{
				SchemaName:  output.SchemaName,
				ModulePath:  output.ModulePath,
				TypesModule: output.TypesModule,
				ORMModule:   output.ORMModule,
				IsPublic:    output.IsPublic,
				Namespace:   namespace,
				Naming:      output.Naming,
			}
		},
		EndpointData: func(namespace string, ep EndpointInfo) any {
			return EndpointOutput{
				SchemaName:  output.SchemaName,
				ModulePath:  output.ModulePath,
				TypesModule: output.TypesModule,
				ORMModule:   output.ORMModule,
				IsPublic:    output.IsPublic,
				Endpoint:    ep,
				Naming:      output.Naming,
			}
		},
		EndpointFileName: func(ep EndpointInfo) string {
			return codegen.ToSnakeCase(ep.ShortImplName) + ".go"
		},
		ImplFileName: "implementation.go",
		GenerateFile: func(templateName, outputPath string, data any) error {
			return generateFile(generator, templatesFS, templateName, outputPath, data, funcs)
		},
	})
}

// SetReplacePaths computes the go.mod replace directive paths for the local
// runtime modules based on the resolved scalarLibPath and the output
// directory where the generated module will live. Generated artifacts keep
// importing the existing v1 runtime packages; the dependency flip is
// sequenced in a separate workstream.
func SetReplacePaths(output *APIOutput, scalarLibPath, outputDir string) error {
	if scalarLibPath == "" || outputDir == "" {
		return nil
	}

	absScalarLib, err := filepath.Abs(scalarLibPath)
	if err != nil {
		return fmt.Errorf("resolve scalar-lib absolute path: %w", err)
	}
	absOutputDir, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve output dir absolute path: %w", err)
	}

	rel := func(target string) (string, error) {
		relPath, err := filepath.Rel(absOutputDir, filepath.Clean(target))
		if err != nil {
			return "", err
		}
		return filepath.ToSlash(relPath), nil
	}

	if output.ScalarLibReplacePath, err = rel(filepath.Join(absScalarLib, "go")); err != nil {
		return fmt.Errorf("compute relative scalar-lib path: %w", err)
	}
	if output.SchemaIRReplacePath, err = rel(filepath.Join(absScalarLib, "..", "schema-ir", "go")); err != nil {
		return fmt.Errorf("compute relative schema-ir path: %w", err)
	}
	if output.SchemaRuntimeReplacePath, err = rel(filepath.Join(absScalarLib, "..", "schema-runtime", "go")); err != nil {
		return fmt.Errorf("compute relative schema-runtime path: %w", err)
	}
	if output.HTTPRuntimeReplacePath, err = rel(filepath.Join(absScalarLib, "..", "http-runtime", "go")); err != nil {
		return fmt.Errorf("compute relative http-runtime path: %w", err)
	}
	if output.PtrReplacePath, err = rel(filepath.Join(absScalarLib, "..", "..", "..", "services", "pkg", "ptr")); err != nil {
		return fmt.Errorf("compute relative ptr path: %w", err)
	}

	return nil
}

// generateFile generates a file from an embedded template, running .go
// outputs through gofmt.
func generateFile(generator *codegen.FileGenerator, fs embed.FS, templateName, outputPath string, data any, funcs template.FuncMap) error {
	return generator.GenerateFile(
		codegen.NewGoFileConfig(fs, templateName, outputPath, data, funcs),
	)
}

// templateFuncs returns apigen-specific template functions merged on top of
// the shared base set by codegen.GenerateFile, plus the provider's own and
// authSnippet, which renders the provider's hook-point snippets.
func templateFuncs(provider AuthProvider) (template.FuncMap, error) {
	funcs := template.FuncMap{
		"base":                    filepath.Base,
		"toGoName":                codegen.ToPascalCase,
		"toKebabCase":             codegen.ToKebabCase,
		"toPackageName":           toPackageName,
		"trimPrefix":              strings.TrimPrefix,
		"splitLines":              splitLines,
		"isPlainImage":            isPlainImage,
		"isImageWithTransparency": isImageWithTransparency,
	}
	for name, fn := range provider.Funcs() {
		funcs[name] = fn
	}
	snippet, err := authSnippetFunc(provider, funcs)
	if err != nil {
		return nil, err
	}
	funcs["authSnippet"] = snippet
	return funcs, nil
}

// AuthSnippetFunc returns the authSnippet template function for provider:
// what the core templates call at their hook points, with the core and
// provider template functions in scope. Provider tests render single
// snippets through it.
func AuthSnippetFunc(provider AuthProvider) (func(name string, data any) (string, error), error) {
	funcs, err := templateFuncs(provider)
	if err != nil {
		return nil, err
	}
	return funcs["authSnippet"].(func(name string, data any) (string, error)), nil
}

func authSnippetFunc(provider AuthProvider, funcs template.FuncMap) (func(name string, data any) (string, error), error) {
	snippets, err := parseAuthSnippets(provider, funcs)
	if err != nil {
		return nil, err
	}
	return func(name string, data any) (string, error) {
		var buf bytes.Buffer
		if err := snippets.ExecuteTemplate(&buf, name, data); err != nil {
			return "", fmt.Errorf("auth provider %s: snippet %s: %w", provider.Name(), name, err)
		}
		return buf.String(), nil
	}, nil
}

// parseAuthSnippets parses every template under the provider's templates/
// into one set, so a {{ define }} in any file is callable, and checks that
// the set defines every hook point in AuthSnippets.
func parseAuthSnippets(provider AuthProvider, funcs template.FuncMap) (*template.Template, error) {
	set := template.New("authSnippets").Funcs(funcs)
	entries, err := fs.ReadDir(provider.Templates(), "templates")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("auth provider %s: read templates: %w", provider.Name(), err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmpl") {
			continue
		}
		content, err := fs.ReadFile(provider.Templates(), "templates/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("auth provider %s: read template %s: %w", provider.Name(), entry.Name(), err)
		}
		if _, err := set.New(entry.Name()).Parse(string(content)); err != nil {
			return nil, fmt.Errorf("auth provider %s: parse template %s: %w", provider.Name(), entry.Name(), err)
		}
	}
	var missing []string
	for _, name := range AuthSnippets {
		if set.Lookup(name) == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("auth provider %s: templates define no snippet for %s", provider.Name(), strings.Join(missing, ", "))
	}
	return set, nil
}

// toPackageName converts a schema or namespace name to a valid Go package
// name (e.g. "fixture-api" -> "fixtureapi").
func toPackageName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "api"
	}
	return b.String()
}

// splitLines splits doc text into lines for comment rendering.
func splitLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// isPlainImage reports an image upload field that does not require
// transparency.
func isPlainImage(field FileUploadField) bool {
	return field.Category == "image" &&
		(field.ImageConstraints == nil || !field.ImageConstraints.RequireTransparency)
}

// isImageWithTransparency reports an image upload field that requires an
// alpha channel (logos, icons).
func isImageWithTransparency(field FileUploadField) bool {
	return field.Category == "image" &&
		field.ImageConstraints != nil &&
		field.ImageConstraints.RequireTransparency
}
