// Package rustapigen provides shared Rust API generation helpers used by
// rustrestgen and rustsdkgen.
package rustapigen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// APIOutputBase contains shared Rust API output metadata.
type APIOutputBase struct {
	SchemaName     string
	CrateName      string
	TypesCrate     string
	TypesDir       string
	TypesDepPath   string
	RuntimeCrate   string
	RuntimeDepPath string
	Namespaces     []string
	Timestamp      string
}

// GenerateOutput contains shared generation output and extracted endpoints.
type GenerateOutput struct {
	Base      APIOutputBase
	Endpoints []apigen.EndpointInfo
}

// Options configures shared Rust API endpoint extraction.
type Options struct {
	SchemaName     string
	IsPublic       bool
	UpstreamSchema string
	UpstreamIR     *ir.Schema
	TypesCrate     string
	TypesDir       string
	OutputDir      string
	RuntimeCrate   string
	Naming         naming.Naming
	// AuthProvider is the auth provider apigen derives the endpoint auth
	// data with. Required.
	AuthProvider apigen.AuthProvider
	Clock        codegen.Clock
}

// Generate runs common Rust API generation setup and endpoint extraction.
func Generate(schema *ir.Schema, opts Options) (*GenerateOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()
	if strings.TrimSpace(opts.RuntimeCrate) == "" {
		opts.RuntimeCrate = opts.Naming.HTTPRuntimeRustCrate
	}

	apiOutput, err := apigen.Generate(schema, apigen.Options{
		SchemaName:     opts.SchemaName,
		IsPublic:       opts.IsPublic,
		UpstreamSchema: opts.UpstreamSchema,
		UpstreamIR:     opts.UpstreamIR,
		Provider:       opts.AuthProvider,
		Clock:          opts.Clock,
	})
	if err != nil {
		return nil, fmt.Errorf("extract endpoints: %w", err)
	}
	if apiOutput == nil || len(apiOutput.Endpoints) == 0 {
		return nil, nil
	}

	typesCrate := strings.TrimSpace(opts.TypesCrate)
	if typesCrate == "" {
		typesCrate = opts.Naming.RustTypesCrate(opts.SchemaName)
	}

	return &GenerateOutput{
		Base: APIOutputBase{
			SchemaName:   opts.SchemaName,
			CrateName:    opts.Naming.RustAPICrate(opts.SchemaName),
			TypesCrate:   typesCrate,
			TypesDir:     opts.TypesDir,
			TypesDepPath: ResolveTypesDependencyPath(opts.OutputDir, opts.TypesDir),
			RuntimeCrate: opts.RuntimeCrate,
			Timestamp:    opts.Clock.RFC3339(),
		},
		Endpoints: apiOutput.Endpoints,
	}, nil
}

// ResolveTypesDependencyPath resolves the Rust types crate dependency path.
func ResolveTypesDependencyPath(outputDir string, typesDir string) string {
	typesDir = strings.TrimSpace(typesDir)
	if typesDir == "" {
		return "../types"
	}

	relPath, err := filepath.Rel(outputDir, typesDir)
	if err != nil {
		return "../types"
	}

	relPath = filepath.ToSlash(relPath)
	if relPath == "." || relPath == "" {
		return "../types"
	}
	return relPath
}

// SortedNamespaces returns a deterministic sorted namespace list from a set.
func SortedNamespaces(namespaceSet map[string]struct{}) []string {
	namespaces := make([]string, 0, len(namespaceSet))
	for ns := range namespaceSet {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	return namespaces
}

// TemplateFuncs returns shared Rust template functions merged with optional extras.
func TemplateFuncs(extras template.FuncMap) template.FuncMap {
	funcs := template.FuncMap{
		"toPascalCase":  rustutil.ToPascalCase,
		"toSnakeCase":   rustutil.ToSnakeCase,
		"toPackageName": rustutil.ToPackageName,
		"splitLines":    rustutil.SplitLines,
	}

	for name, fn := range extras {
		funcs[name] = fn
	}

	return funcs
}
