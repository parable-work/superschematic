package ormgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/profile"
)

// RepositoryOutput wraps one repository with module-level metadata for the
// repository template.
type RepositoryOutput struct {
	Repository
	TypesModule  string
	Timestamp    string
	UUIDGoType   string
	UserIDGoType string
}

// VersionGraphOutput wraps one version graph with module-level metadata for
// the version graph facade template.
type VersionGraphOutput struct {
	VersionGraph
	TypesModule string
	UUIDGoType  string
	Naming      naming.Naming
}

// WriteORM writes the generated ORM module into outputDir.
func WriteORM(output *ORMOutput, outputDir string) error {
	return WriteORMWithProfile(output, outputDir, nil, false)
}

// WriteORMWithProfile writes the generated ORM module with shared codegen profiling.
func WriteORMWithProfile(output *ORMOutput, outputDir string, prof *profile.Profiler, skipFormat bool, phasePrefixes ...string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	generator := codegen.NewFileGenerator(
		templatesFS,
		templateFuncs(),
		codegen.WithProfiler(prof, phasePrefixes...),
		codegen.WithSkipFormat(skipFormat),
	)

	staticFiles := []struct {
		template string
		filename string
	}{
		{"database.tmpl", "database.go"},
		{"interfaces.tmpl", "interfaces.go"},
		{"query.tmpl", "query.go"},
		{"utils.tmpl", "utils.go"},
		{"module.tmpl", "go.mod"},
		{"readme.tmpl", "README.md"},
		{"makefile.tmpl", "Makefile"},
	}
	staticTasks := make([]func() error, 0, len(staticFiles))
	for _, file := range staticFiles {
		file := file
		staticTasks = append(staticTasks, func() error {
			if err := generateFile(generator, file.template, filepath.Join(outputDir, file.filename), output); err != nil {
				return fmt.Errorf("failed to write %s: %w", file.filename, err)
			}
			return nil
		})
	}
	if err := codegen.RunParallel(staticTasks); err != nil {
		return err
	}

	if err := cleanStaleRepositoryFiles(outputDir, output.Repositories); err != nil {
		return fmt.Errorf("failed to clean stale repository files: %w", err)
	}
	if err := cleanStaleVersionGraphFiles(outputDir, output.VersionGraphs); err != nil {
		return fmt.Errorf("failed to clean stale version graph files: %w", err)
	}

	repositoryTasks := make([]func() error, 0, len(output.Repositories))
	for _, repo := range output.Repositories {
		repo := repo
		repositoryTasks = append(repositoryTasks, func() error {
			filename := fmt.Sprintf("repository_%s.go", codegen.ToSnakeCase(repo.TypeName))
			repoOutput := RepositoryOutput{
				Repository:   repo,
				TypesModule:  output.TypesModule,
				Timestamp:    output.Timestamp,
				UUIDGoType:   output.UUIDGoType,
				UserIDGoType: output.UserIDGoType,
			}
			if err := generateFile(generator, "repository.tmpl", filepath.Join(outputDir, filename), repoOutput); err != nil {
				return fmt.Errorf("failed to write %s: %w", filename, err)
			}
			return nil
		})
	}

	for _, graph := range output.VersionGraphs {
		graph := graph
		repositoryTasks = append(repositoryTasks, func() error {
			filename := versionGraphFileName(graph)
			graphOutput := VersionGraphOutput{
				VersionGraph: graph,
				TypesModule:  output.TypesModule,
				UUIDGoType:   output.UUIDGoType,
				Naming:       output.Naming,
			}
			if err := generateFile(generator, "versiongraph_graph.tmpl", filepath.Join(outputDir, filename), graphOutput); err != nil {
				return fmt.Errorf("failed to write %s: %w", filename, err)
			}
			return nil
		})
	}

	return codegen.RunParallel(repositoryTasks)
}

func versionGraphFileName(graph VersionGraph) string {
	return "versiongraph_" + graph.FileName + ".go"
}

// cleanStaleVersionGraphFiles removes the versiongraph_*.go facades of
// graphs the schema no longer declares, and the versiongraph.go an earlier
// generator wrote the shared engine into.
func cleanStaleVersionGraphFiles(outputDir string, graphs []VersionGraph) error {
	expected := make(map[string]struct{}, len(graphs))
	for _, graph := range graphs {
		expected[versionGraphFileName(graph)] = struct{}{}
	}
	matches, err := filepath.Glob(filepath.Join(outputDir, "versiongraph*.go"))
	if err != nil {
		return err
	}
	for _, match := range matches {
		if _, ok := expected[filepath.Base(match)]; ok {
			continue
		}
		if err := os.Remove(match); err != nil {
			return err
		}
	}
	return nil
}

// cleanStaleRepositoryFiles removes repository_*.go files whose table no
// longer exists in the schema.
func cleanStaleRepositoryFiles(outputDir string, repositories []Repository) error {
	expected := make(map[string]struct{}, len(repositories))
	for _, repo := range repositories {
		expected[fmt.Sprintf("repository_%s.go", codegen.ToSnakeCase(repo.TypeName))] = struct{}{}
	}

	matches, err := filepath.Glob(filepath.Join(outputDir, "repository_*.go"))
	if err != nil {
		return err
	}

	for _, match := range matches {
		if _, ok := expected[filepath.Base(match)]; ok {
			continue
		}
		if err := os.Remove(match); err != nil {
			return err
		}
	}

	return nil
}

// generateFile generates a file from an embedded template, running .go
// outputs through gofmt.
func generateFile(generator *codegen.FileGenerator, templateName, outputPath string, data interface{}) error {
	return generator.GenerateFile(
		codegen.NewGoFileConfig(templatesFS, templateName, outputPath, data, templateFuncs()),
	)
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"toGoName": codegen.ToPascalCase,
		"lowerFirst": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
		"trimIDSuffix": func(s string) string {
			return strings.TrimSuffix(s, "ID")
		},
	}
}
