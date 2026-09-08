package codegen

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/parable-work/superschematic/internal/profile"
)

// BaseTemplateFuncs returns the base set of template functions shared across
// all generators. These are language-agnostic string manipulation functions.
// Each generator can extend this with language-specific functions by merging
// with the returned FuncMap.
func BaseTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		// String manipulation
		"lower":     strings.ToLower,
		"upper":     strings.ToUpper,
		"title":     TitleCase,
		"titleCase": TitleCase,
		"trimSpace": strings.TrimSpace,
		"join":      func(strs []string, sep string) string { return strings.Join(strs, sep) },
		"hasPrefix": strings.HasPrefix,
		"hasSuffix": strings.HasSuffix,
		"replace":   strings.ReplaceAll,
		"contains":  strings.Contains,
		"split":     strings.Split,

		// Case conversion (language-agnostic)
		"toSnakeCase":  ToSnakeCase,
		"toCamelCase":  ToCamelCase,
		"toPascalCase": ToPascalCase,

		// Formatting utilities
		"formatComment": FormatComment,
		"escapeString":  EscapeString,
	}
}

// FormatComment formats a name and description into a single-line or
// multi-line comment. The prefix parameter specifies the comment style
// (e.g., "// " for Go, "# " for Python). If description is empty, returns
// just the name with the prefix. If description contains newlines, each line
// is prefixed appropriately.
func FormatComment(prefix, name, description string) string {
	if description == "" {
		return prefix + name
	}

	lines := strings.Split(description, "\n")
	if len(lines) == 1 {
		return prefix + name + " - " + strings.TrimSpace(description)
	}

	var formatted []string
	formatted = append(formatted, prefix+name+" - "+strings.TrimSpace(lines[0]))
	for i := 1; i < len(lines); i++ {
		formatted = append(formatted, prefix+strings.TrimSpace(lines[i]))
	}
	return strings.Join(formatted, "\n")
}

// DocText selects the documentation text for a definition: an explicit
// description wins, otherwise the node-attached comment carries the doc.
// Node comments become doc comments in all generated languages.
func DocText(description, comment string) string {
	if strings.TrimSpace(description) != "" {
		return description
	}
	return comment
}

// EscapeString escapes special characters in a string for safe use in
// generated code. Handles quotes, newlines, carriage returns, and tabs.
func EscapeString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

// PermissionToPascalConstName converts a dot-separated permission string into
// a PascalCase constant name.
//
// Example: "tenant.users.read" -> "TenantUsersRead".
func PermissionToPascalConstName(permission string) string {
	parts := strings.Split(permission, ".")
	var result strings.Builder

	for _, part := range parts {
		if len(part) > 0 {
			result.WriteString(strings.ToUpper(part[:1]))
			result.WriteString(part[1:])
		}
	}

	return result.String()
}

// PermissionToUpperSnakeConstName converts a dot-separated permission string
// into an UPPER_SNAKE_CASE constant name.
//
// Example: "tenant.users.read" -> "TENANT_USERS_READ".
func PermissionToUpperSnakeConstName(permission string) string {
	parts := strings.Split(permission, ".")
	return strings.ToUpper(strings.Join(parts, "_"))
}

// GenerateFileConfig holds configuration for GenerateFile.
type GenerateFileConfig struct {
	// TemplatesFS is the embedded filesystem containing the generator's
	// templates at "templates/<templateName>".
	TemplatesFS embed.FS

	// TemplateName is the name of the template file (e.g., "scalars.tmpl").
	TemplateName string

	// OutputPath is the full path where the generated file will be written.
	OutputPath string

	// Data is the data passed to the template for execution.
	Data interface{}

	// CustomFuncs are additional template functions to merge with
	// BaseTemplateFuncs. These override base functions on name conflicts.
	CustomFuncs template.FuncMap

	// FormatOutput optionally formats rendered bytes before writing output.
	// If set, templates are executed to memory first, then passed through
	// this function.
	FormatOutput func([]byte) ([]byte, error)

	// FormatProfileKind labels FormatOutput timings by language or formatter
	// family, for example "go" or "typescript".
	FormatProfileKind string

	// WriteUnformattedOnFormatError writes unformatted output for debugging
	// when formatting fails. Only used when FormatOutput is set.
	WriteUnformattedOnFormatError bool

	// OutputDirExists skips creating the output file's parent directory. Use
	// only when the writer created its complete directory tree up front.
	OutputDirExists bool
}

// FileGenerator renders files from one template set, caching parsed templates
// for the lifetime of a writer invocation.
type FileGenerator struct {
	templatesFS   embed.FS
	funcs         template.FuncMap
	templateMu    sync.Mutex
	templates     map[string]*template.Template
	profile       *profile.Profiler
	phasePrefixes []string
	skipFormat    bool
}

// FileGeneratorOption configures a reusable file generator.
type FileGeneratorOption func(*FileGenerator)

// WithProfiler records shared template, format, and write timings for files
// rendered by this generator. Multiple prefixes record the same measurement
// under aggregate and output-specific phase names without rerunning work.
func WithProfiler(prof *profile.Profiler, phasePrefixes ...string) FileGeneratorOption {
	return func(g *FileGenerator) {
		g.profile = prof
		g.phasePrefixes = phasePrefixes
	}
}

// WithSkipFormat disables FormatOutput for every file rendered by this
// generator. It is intended for CI paths where generated bytes are consumed
// immediately and do not need developer-friendly formatting.
func WithSkipFormat(skipFormat bool) FileGeneratorOption {
	return func(g *FileGenerator) {
		g.skipFormat = skipFormat
	}
}

// NewFileGenerator creates a reusable template-backed file generator.
func NewFileGenerator(templatesFS embed.FS, customFuncs template.FuncMap, opts ...FileGeneratorOption) *FileGenerator {
	funcs := make(template.FuncMap)
	for k, v := range BaseTemplateFuncs() {
		funcs[k] = v
	}
	for k, v := range customFuncs {
		funcs[k] = v
	}
	g := &FileGenerator{
		templatesFS: templatesFS,
		funcs:       funcs,
		templates:   make(map[string]*template.Template),
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// NewFileConfig creates a base GenerateFileConfig.
func NewFileConfig(
	templatesFS embed.FS,
	templateName, outputPath string,
	data interface{},
	customFuncs template.FuncMap,
) GenerateFileConfig {
	return GenerateFileConfig{
		TemplatesFS:  templatesFS,
		TemplateName: templateName,
		OutputPath:   outputPath,
		Data:         data,
		CustomFuncs:  customFuncs,
	}
}

// NewGoFileConfig creates a GenerateFileConfig and enables gofmt formatting
// for .go outputs.
func NewGoFileConfig(
	templatesFS embed.FS,
	templateName, outputPath string,
	data interface{},
	customFuncs template.FuncMap,
) GenerateFileConfig {
	cfg := NewFileConfig(templatesFS, templateName, outputPath, data, customFuncs)
	if strings.HasSuffix(outputPath, ".go") {
		cfg.FormatOutput = format.Source
		cfg.FormatProfileKind = "go"
		cfg.WriteUnformattedOnFormatError = true
	}
	return cfg
}

// GenerateFile generates a file from an embedded template.
func GenerateFile(cfg GenerateFileConfig) error {
	return NewFileGenerator(cfg.TemplatesFS, cfg.CustomFuncs).GenerateFile(cfg)
}

// GenerateFile generates one file using the generator's cached templates.
func (g *FileGenerator) GenerateFile(cfg GenerateFileConfig) error {
	var output []byte
	if err := g.measure("template", func() error {
		tmpl, err := g.template(cfg.TemplateName)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, cfg.Data); err != nil {
			return fmt.Errorf("failed to execute template %s: %w", cfg.TemplateName, err)
		}
		output = buf.Bytes()
		return nil
	}); err != nil {
		return err
	}

	if cfg.FormatOutput != nil && !g.skipFormat {
		if err := g.measureFormat(cfg.FormatProfileKind, func() error {
			formattedOutput, formatErr := cfg.FormatOutput(output)
			if formatErr != nil {
				if cfg.WriteUnformattedOnFormatError {
					_ = os.WriteFile(cfg.OutputPath, output, 0o644)
					return fmt.Errorf("failed to format file %s: %w (unformatted output written for debugging)", cfg.OutputPath, formatErr)
				}
				return fmt.Errorf("failed to format output file %s: %w", cfg.OutputPath, formatErr)
			}
			output = formattedOutput
			return nil
		}); err != nil {
			return err
		}
	}

	return g.measure("write", func() error {
		if !cfg.OutputDirExists {
			if err := os.MkdirAll(filepath.Dir(cfg.OutputPath), 0o755); err != nil {
				return fmt.Errorf("failed to create output directory for %s: %w", cfg.OutputPath, err)
			}
		}
		if err := os.WriteFile(cfg.OutputPath, output, 0o644); err != nil {
			return fmt.Errorf("failed to write output file %s: %w", cfg.OutputPath, err)
		}
		return nil
	})
}

func (g *FileGenerator) measure(phase string, fn func() error) error {
	return g.measurePhases([]string{phase}, fn)
}

func (g *FileGenerator) measureFormat(formatKind string, fn func() error) error {
	phases := []string{"format"}
	if formatKind != "" {
		phases = append(phases, "format."+formatKind)
	}
	return g.measurePhases(phases, fn)
}

func (g *FileGenerator) measurePhases(phases []string, fn func() error) error {
	if !g.profile.Enabled() {
		return fn()
	}

	started := time.Now()
	err := fn()
	duration := time.Since(started)

	for _, prefix := range g.normalizedPhasePrefixes() {
		for _, phase := range phases {
			g.profile.Record(prefix+"."+phase, duration)
		}
	}
	return err
}

func (g *FileGenerator) normalizedPhasePrefixes() []string {
	if len(g.phasePrefixes) == 0 {
		return []string{"codegen"}
	}

	prefixes := make([]string, 0, len(g.phasePrefixes))
	seen := make(map[string]struct{}, len(g.phasePrefixes))
	for _, prefix := range g.phasePrefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			prefix = "codegen"
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func (g *FileGenerator) template(templateName string) (*template.Template, error) {
	g.templateMu.Lock()
	defer g.templateMu.Unlock()

	if tmpl, ok := g.templates[templateName]; ok {
		return tmpl, nil
	}

	templateContent, err := g.templatesFS.ReadFile("templates/" + templateName)
	if err != nil {
		return nil, fmt.Errorf("failed to read template %s: %w", templateName, err)
	}

	tmpl, err := template.New(templateName).Funcs(g.funcs).Parse(string(templateContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %s: %w", templateName, err)
	}
	g.templates[templateName] = tmpl
	return tmpl, nil
}

// ConditionalFile describes a template-backed generated file that is
// optionally emitted.
type ConditionalFile struct {
	Condition bool
	Template  string
	Filename  string
}

// WriteConditionalFiles generates files when Condition is true and removes
// stale files when Condition is false.
func WriteConditionalFiles(
	files []ConditionalFile,
	outputDir string,
	generateFn func(templateName, outputPath string) error,
) error {
	for _, file := range files {
		outputPath := filepath.Join(outputDir, file.Filename)
		if file.Condition {
			if err := generateFn(file.Template, outputPath); err != nil {
				return fmt.Errorf("failed to generate %s: %w", file.Filename, err)
			}
			continue
		}

		if err := os.Remove(outputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to remove %s: %w", file.Filename, err)
		}
	}

	return nil
}

// WriteConditionalFilesParallel generates present conditional files
// concurrently while preserving sequential stale-file removal.
func WriteConditionalFilesParallel(
	files []ConditionalFile,
	outputDir string,
	generateFn func(templateName, outputPath string) error,
) error {
	tasks := make([]func() error, 0, len(files))
	for _, file := range files {
		file := file
		outputPath := filepath.Join(outputDir, file.Filename)
		if file.Condition {
			tasks = append(tasks, func() error {
				if err := generateFn(file.Template, outputPath); err != nil {
					return fmt.Errorf("failed to generate %s: %w", file.Filename, err)
				}
				return nil
			})
			continue
		}

		if err := os.Remove(outputPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to remove %s: %w", file.Filename, err)
		}
	}

	return RunParallel(tasks)
}

// RunParallel runs independent tasks with bounded parallelism and returns the
// lowest-indexed error so failures remain deterministic.
func RunParallel(tasks []func() error) error {
	if len(tasks) == 0 {
		return nil
	}
	if len(tasks) == 1 {
		return tasks[0]()
	}

	workerCount := min(runtime.GOMAXPROCS(0), len(tasks))
	jobs := make(chan int)
	errs := make([]error, len(tasks))

	var wg sync.WaitGroup
	wg.Add(workerCount)
	for range workerCount {
		go func() {
			defer wg.Done()
			for index := range jobs {
				errs[index] = tasks[index]()
			}
		}()
	}

	for index := range tasks {
		jobs <- index
	}
	close(jobs)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// MergeTemplateFuncs merges multiple FuncMaps into one.
// Later maps override earlier maps for duplicate keys.
func MergeTemplateFuncs(funcMaps ...template.FuncMap) template.FuncMap {
	result := make(template.FuncMap)
	for _, fm := range funcMaps {
		for k, v := range fm {
			result[k] = v
		}
	}
	return result
}
