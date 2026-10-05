// Package rustapigen provides shared Rust API generation helpers used by
// rustrestgen and rustsdkgen.
package rustapigen

import (
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
)

// APIOutputBase contains shared Rust API output metadata.
type APIOutputBase struct {
	SchemaName   string
	CrateName    string
	TypesCrate   string
	TypesDir     string
	TypesDepPath string
	RuntimeCrate string
	// RuntimeCrateIdent is RuntimeCrate as a Rust path segment: Cargo maps
	// the hyphens of a package name to underscores in `use` statements.
	RuntimeCrateIdent string
	RuntimeDepPath    string
	Namespaces        []string
	Timestamp         string
}

// BaseOptions names a Rust API crate and the crates it depends on.
type BaseOptions struct {
	SchemaName   string
	TypesCrate   string
	TypesDir     string
	OutputDir    string
	RuntimeCrate string
	Naming       naming.Naming
	Clock        codegen.Clock
}

// NewBase returns the crate metadata every Rust API crate shares: its name,
// the types crate and the http runtime crate it depends on, and the stamp.
// The endpoints come from the shared apigen output (generator.Run's
// APIOutput), as for the Go and TypeScript servers and every SDK.
func NewBase(opts BaseOptions) APIOutputBase {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()
	if strings.TrimSpace(opts.RuntimeCrate) == "" {
		opts.RuntimeCrate = opts.Naming.HTTPRuntimeRustCrate
	}
	typesCrate := strings.TrimSpace(opts.TypesCrate)
	if typesCrate == "" {
		typesCrate = opts.Naming.RustTypesCrate(opts.SchemaName)
	}
	return APIOutputBase{
		SchemaName:        opts.SchemaName,
		CrateName:         opts.Naming.RustAPICrate(opts.SchemaName),
		TypesCrate:        typesCrate,
		TypesDir:          opts.TypesDir,
		TypesDepPath:      ResolveTypesDependencyPath(opts.OutputDir, opts.TypesDir),
		RuntimeCrate:      opts.RuntimeCrate,
		RuntimeCrateIdent: strings.ReplaceAll(opts.RuntimeCrate, "-", "_"),
		Timestamp:         opts.Clock.RFC3339(),
	}
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
