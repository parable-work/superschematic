// Package naming holds every package, module and crate name the generators
// stamp into their output. The values come from superschematic.toml at the
// schemas root; a missing file yields Default(), the superschematic and
// superscalar coordinates, so a tree with no file builds against the
// published modules.
//
// Structural suffixes (-types, -sdk, -api, the types/go and sdk/go subpaths)
// are not configurable: they describe the artifact kind and mirror the dist/
// layout in generator/paths.go. Only the organisation-specific prefixes,
// scopes and runtime coordinates live here.
//
// The same file carries [extension.<name>] tables for registered extensions.
// The loader keeps them undecoded (Naming.Extensions, ExtensionConfig) and
// rejects any other unknown top-level key.
//
// A downstream distribution that keeps older package names (a fork, or an
// extension that re-exports the authoring packages under its own scope)
// declares every name it needs in its own superschematic.toml; nothing in
// the generators carries a literal.
package naming

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// FileName is the configuration file read from the schemas root.
const FileName = "superschematic.toml"

// Naming is the set of names generated artifacts carry. Every field has a
// default; an empty field in a loaded file keeps the default.
type Naming struct {
	// GoModuleRoot prefixes every generated Go module path:
	// <root>/types/go/<name>, <root>/orm/<name>, <root>/api/<name>,
	// <root>/sdk/go/<name>.
	GoModuleRoot string `toml:"go_module_root"`

	// NpmScope is the npm scope of generated TypeScript packages and of the
	// service authoring packages schemas import from each other:
	// <scope>/<name>-types, <scope>/<name>-sdk, <scope>/<name>.
	NpmScope string `toml:"npm_scope"`

	// PythonTypesModulePrefix prefixes generated Python type modules:
	// <prefix><stem>, where stem is the schema name with '-' folded to '_'.
	PythonTypesModulePrefix string `toml:"python_types_module_prefix"`

	// PythonSDKModulePrefix and PythonSDKModuleSuffix wrap the schema stem
	// for generated Python SDK modules: <prefix><stem><suffix>.
	PythonSDKModulePrefix string `toml:"python_sdk_module_prefix"`
	PythonSDKModuleSuffix string `toml:"python_sdk_module_suffix"`

	// RustCratePrefix prefixes generated crates: <prefix><name>-types,
	// <prefix><name>-sdk, <prefix><name>-api.
	RustCratePrefix string `toml:"rust_crate_prefix"`

	// Scalar library coordinates, one per target language.
	ScalarGoModule     string `toml:"scalar_go_module"`
	ScalarNpmPackage   string `toml:"scalar_npm_package"`
	ScalarPyPIDist     string `toml:"scalar_pypi_dist"`
	ScalarPythonModule string `toml:"scalar_python_module"`
	ScalarRustCrate    string `toml:"scalar_rust_crate"`

	// Runtime module coordinates the generated Go modules require.
	SchemaIRGoModule      string `toml:"schema_ir_go_module"`
	SchemaRuntimeGoModule string `toml:"schema_runtime_go_module"`
	HTTPRuntimeGoModule   string `toml:"http_runtime_go_module"`
	HTTPRuntimeRustCrate  string `toml:"http_runtime_rust_crate"`
	PtrGoModule           string `toml:"ptr_go_module"`

	// SchemaLanguage is how generated readmes and the schema-file JSON
	// Schema name the schema language ("generated from <SchemaLanguage>
	// definitions").
	SchemaLanguage string `toml:"schema_language"`

	// PackageAuthor is the author field generated package manifests carry
	// (package.json, pyproject.toml, setup.py).
	PackageAuthor string `toml:"package_author"`

	// MetaSchemaURLPrefix prefixes the $id of the JSON Schemas the tool
	// emits and validates against (the schema-file document schema).
	MetaSchemaURLPrefix string `toml:"meta_schema_url_prefix"`

	// AuthoringPackages lists the npm packages whose exports the TypeScript
	// frontend treats as toolchain: decorators and type wrappers must
	// resolve from one of them, and the per-kind import rules apply to
	// them. OrDefault always adds ScalarNpmPackage so a fork that renames
	// the scalar package keeps it in scope.
	AuthoringPackages []string `toml:"authoring_packages"`

	// PackageAliases maps an import specifier a schema may write to the
	// authoring package that declares the symbols it re-exports, for a
	// distribution that publishes the core packages under its own names
	// (["@acme/db"] = "@superschematic/db"). The loader resolves symbols to
	// their declaring package, so the map is what lets the per-kind import
	// rules, the writer and the diagnostics speak the author's spelling.
	// Absent, every specifier is its own declaring package.
	PackageAliases map[string]string `toml:"package_aliases"`

	// AuthProvider names the registered auth provider the api generator
	// renders with. The core registers "session"; an extension that
	// registers another provider names it here.
	AuthProvider string `toml:"auth_provider"`

	// Extensions holds every [extension.<name>] table verbatim. Nothing in
	// the core reads them; Registry.ExtensionConfig(name) hands each table to
	// the extension that owns it, which validates its own keys. Nil when the
	// file declares none.
	Extensions map[string]map[string]any `toml:"extension"`

	// Cache is the [cache] table: where build-all keeps its entries and
	// which files outside the schema tree feed every cache key.
	Cache CacheConfig `toml:"cache"`

	// Paths is the [paths] table: in-tree locations generated modules point
	// replace directives at.
	Paths PathsConfig `toml:"paths"`
}

// PathsConfig is the [paths] table of superschematic.toml: where the
// runtime modules the generated code imports live in the repository, for
// generated manifests to point path dependencies at (go.mod replace, Cargo
// path, npm file:). Every key is optional and repo-relative (the repository
// root is the parent of the schemas root, as for [cache] inputs) and names
// the directory that holds the module, package or crate itself. An unset
// key emits no path dependency, so the generated manifest resolves the
// published module instead.
type PathsConfig struct {
	// ScalarGo holds the scalar library's Go module (its go.mod).
	ScalarGo string `toml:"scalar_go"`
	// ScalarTypeScript holds the scalar library's npm package (its
	// package.json).
	ScalarTypeScript string `toml:"scalar_typescript"`
	// ScalarRust holds the scalar library's Rust crate (its Cargo.toml).
	ScalarRust string `toml:"scalar_rust"`
	// SchemaIR holds the schema IR Go module.
	SchemaIR string `toml:"schema_ir"`
	// SchemaRuntimeGo holds the schema runtime Go module.
	SchemaRuntimeGo string `toml:"schema_runtime_go"`
	// HTTPRuntimeGo holds the http runtime Go module.
	HTTPRuntimeGo string `toml:"http_runtime_go"`
	// HTTPRuntimeRust holds the http runtime Rust crate.
	HTTPRuntimeRust string `toml:"http_runtime_rust"`
	// Ptr holds the ptr Go module when it is a module of its own rather
	// than a package of the schema runtime.
	Ptr string `toml:"ptr"`
}

// LocalPaths is PathsConfig resolved against a repository root: every set
// key as an absolute path, every unset key "".
type LocalPaths struct {
	ScalarGo         string
	ScalarTypeScript string
	ScalarRust       string
	SchemaIR         string
	SchemaRuntimeGo  string
	HTTPRuntimeGo    string
	HTTPRuntimeRust  string
	Ptr              string
}

// LocalPaths resolves the [paths] table against repoRoot.
func (n Naming) LocalPaths(repoRoot string) LocalPaths {
	resolve := func(rel string) string {
		if rel == "" {
			return ""
		}
		return filepath.Join(repoRoot, filepath.FromSlash(rel))
	}
	return LocalPaths{
		ScalarGo:         resolve(n.Paths.ScalarGo),
		ScalarTypeScript: resolve(n.Paths.ScalarTypeScript),
		ScalarRust:       resolve(n.Paths.ScalarRust),
		SchemaIR:         resolve(n.Paths.SchemaIR),
		SchemaRuntimeGo:  resolve(n.Paths.SchemaRuntimeGo),
		HTTPRuntimeGo:    resolve(n.Paths.HTTPRuntimeGo),
		HTTPRuntimeRust:  resolve(n.Paths.HTTPRuntimeRust),
		Ptr:              resolve(n.Paths.Ptr),
	}
}

// RelPath returns target relative to outputDir in slash form for a
// generated manifest, or "" when target is unset so the manifest omits the
// path dependency.
func RelPath(outputDir, target string) (string, error) {
	if target == "" || outputDir == "" {
		return "", nil
	}
	absOutput, err := filepath.Abs(outputDir)
	if err != nil {
		return "", fmt.Errorf("resolve output dir: %w", err)
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", target, err)
	}
	rel, err := filepath.Rel(absOutput, absTarget)
	if err != nil {
		return "", fmt.Errorf("relate %s to %s: %w", target, outputDir, err)
	}
	return filepath.ToSlash(rel), nil
}

// CacheConfig is the [cache] table of superschematic.toml. Both keys are
// optional; the zero value keeps the platform default root and hashes no
// extra files.
type CacheConfig struct {
	// Root is the build cache directory ("~" expands). Empty means the XDG
	// cache directory. The SUPERSCHEMATIC_BUILD_CACHE_DIR environment
	// variable and the --cache-root flag override it, in that order of
	// precedence.
	Root string `toml:"root"`

	// Inputs are repo-relative files hashed into every build-all cache key
	// alongside the schema tree, the tool tree and the workspace lockfile:
	// files generation reads that live outside the schema tree, so a change
	// to them cannot reuse an entry built before it.
	Inputs []string `toml:"inputs"`
}

// ExtensionConfig returns the [extension.<name>] table from the loaded file
// and whether it was declared.
func (n Naming) ExtensionConfig(name string) (map[string]any, bool) {
	cfg, ok := n.Extensions[name]
	return cfg, ok
}

// Default returns the superschematic defaults: the published module, package
// and crate coordinates of this repository and of superscalar. This is the
// defaults table: the only place these literals appear.
func Default() Naming {
	return Naming{
		GoModuleRoot:            "example.com/schemas",
		NpmScope:                "@schemas",
		PythonTypesModulePrefix: "schemas_types_",
		PythonSDKModulePrefix:   "schemas_",
		PythonSDKModuleSuffix:   "_sdk",
		RustCratePrefix:         "schemas-",
		ScalarGoModule:          "github.com/parable-work/superscalar/go",
		ScalarNpmPackage:        "superscalar",
		ScalarPyPIDist:          "superscalar",
		ScalarPythonModule:      "superscalar",
		ScalarRustCrate:         "superscalar",
		SchemaIRGoModule:        "github.com/parable-work/superschematic/ir",
		SchemaRuntimeGoModule:   "github.com/parable-work/superschematic/runtime/schema/go",
		HTTPRuntimeGoModule:     "github.com/parable-work/superschematic/runtime/http/go",
		HTTPRuntimeRustCrate:    "superschematic-http-runtime",
		PtrGoModule:             "github.com/parable-work/superschematic/runtime/schema/go/ptr",
		SchemaLanguage:          "Superschematic",
		PackageAuthor:           "superschematic",
		MetaSchemaURLPrefix:     "superschematic://",
		AuthProvider:            "session",
		AuthoringPackages: []string{
			"@superschematic/api",
			"@superschematic/db",
			"@superschematic/deploy",
			"@superschematic/schema",
			"@superschematic/schema-config",
			"superscalar",
		},
	}
}

// OrDefault returns n with every empty field filled from Default(). Callers
// that receive a zero Naming (tests, older call sites) get the defaults.
func (n Naming) OrDefault() Naming {
	d := Default()
	fill := func(dst *string, def string) {
		if *dst == "" {
			*dst = def
		}
	}
	fill(&n.GoModuleRoot, d.GoModuleRoot)
	fill(&n.NpmScope, d.NpmScope)
	fill(&n.PythonTypesModulePrefix, d.PythonTypesModulePrefix)
	fill(&n.PythonSDKModulePrefix, d.PythonSDKModulePrefix)
	fill(&n.PythonSDKModuleSuffix, d.PythonSDKModuleSuffix)
	fill(&n.RustCratePrefix, d.RustCratePrefix)
	fill(&n.ScalarGoModule, d.ScalarGoModule)
	fill(&n.ScalarNpmPackage, d.ScalarNpmPackage)
	fill(&n.ScalarPyPIDist, d.ScalarPyPIDist)
	fill(&n.ScalarPythonModule, d.ScalarPythonModule)
	fill(&n.ScalarRustCrate, d.ScalarRustCrate)
	fill(&n.SchemaIRGoModule, d.SchemaIRGoModule)
	fill(&n.SchemaRuntimeGoModule, d.SchemaRuntimeGoModule)
	fill(&n.HTTPRuntimeGoModule, d.HTTPRuntimeGoModule)
	fill(&n.HTTPRuntimeRustCrate, d.HTTPRuntimeRustCrate)
	fill(&n.PtrGoModule, d.PtrGoModule)
	fill(&n.SchemaLanguage, d.SchemaLanguage)
	fill(&n.PackageAuthor, d.PackageAuthor)
	fill(&n.MetaSchemaURLPrefix, d.MetaSchemaURLPrefix)
	fill(&n.AuthProvider, d.AuthProvider)
	if len(n.AuthoringPackages) == 0 {
		n.AuthoringPackages = append([]string(nil), d.AuthoringPackages...)
		// The default list names the default scalar package; a fork that
		// renames the scalar package replaces that entry rather than
		// keeping a package it does not ship.
		if n.ScalarNpmPackage != d.ScalarNpmPackage {
			n.AuthoringPackages = slices.DeleteFunc(n.AuthoringPackages, func(pkg string) bool { return pkg == d.ScalarNpmPackage })
		}
	}
	if !slices.Contains(n.AuthoringPackages, n.ScalarNpmPackage) {
		n.AuthoringPackages = append(n.AuthoringPackages, n.ScalarNpmPackage)
	}
	return n
}

// AuthoringScope is the npm scope shared by every authoring package other
// than the scalar package ("@superschematic" for the defaults), used to word
// the decorator-origin diagnostic. The scalar package is exempt because it is
// a separate project with its own name; AuthoringScope is "" when the
// remaining packages share no scope.
func (n Naming) AuthoringScope() string {
	scope := ""
	seen := false
	for _, pkg := range n.AuthoringPackages {
		if pkg == n.ScalarNpmPackage {
			continue
		}
		slash := strings.Index(pkg, "/")
		if slash < 0 {
			return ""
		}
		if !seen {
			scope = pkg[:slash]
			seen = true
			continue
		}
		if pkg[:slash] != scope {
			return ""
		}
	}
	return scope
}

// MetaSchemaURL returns the $id for one of the tool's JSON Schemas.
func (n Naming) MetaSchemaURL(name string) string {
	return n.MetaSchemaURLPrefix + name
}

// DeclaringPackage returns the authoring package the import specifier
// resolves to: its PackageAliases target when one is declared, else the
// specifier itself.
func (n Naming) DeclaringPackage(specifier string) string {
	if declaring, ok := n.PackageAliases[specifier]; ok {
		return declaring
	}
	return specifier
}

// Specifier returns the import specifier a schema author writes for a
// declaring package: the alphabetically first alias that maps to it, else
// the declaring package itself. The writer and the diagnostics use it so a
// distribution's authors read their own package names.
func (n Naming) Specifier(declaring string) string {
	keys := make([]string, 0, len(n.PackageAliases))
	for specifier, target := range n.PackageAliases {
		if target == declaring {
			keys = append(keys, specifier)
		}
	}
	if len(keys) == 0 {
		return declaring
	}
	sort.Strings(keys)
	return keys[0]
}

// GoTypesModule returns the module path of a schema's generated Go types.
func (n Naming) GoTypesModule(schemaName string) string {
	return n.GoModuleRoot + "/types/go/" + schemaName
}

// GoORMModule returns the module path of a DB schema's generated Go ORM.
func (n Naming) GoORMModule(schemaName string) string {
	return n.GoModuleRoot + "/orm/" + schemaName
}

// GoAPIModule returns the module path of an API schema's generated Go server
// (and of the standalone env-config module, which shares the directory).
func (n Naming) GoAPIModule(schemaName string) string {
	return n.GoModuleRoot + "/api/" + schemaName
}

// GoSDKModule returns the module path of an API schema's generated Go SDK.
func (n Naming) GoSDKModule(schemaName string) string {
	return n.GoModuleRoot + "/sdk/go/" + schemaName
}

// GoModulePrefix is GoModuleRoot with a trailing slash, the form consumers'
// go.mod require and replace lines are matched against.
func (n Naming) GoModulePrefix() string {
	return n.GoModuleRoot + "/"
}

// NpmServicePackage returns the authoring package name of a schema service,
// the name other schemas import its types from.
func (n Naming) NpmServicePackage(schemaName string) string {
	return n.NpmScope + "/" + schemaName
}

// NpmServicePackagePrefix is NpmScope with a trailing slash; imports under
// it are service references.
func (n Naming) NpmServicePackagePrefix() string {
	return n.NpmScope + "/"
}

// NpmTypesPackage returns the npm name of a schema's generated TypeScript
// types. A schema already named *-types keeps a single suffix.
func (n Naming) NpmTypesPackage(schemaName string) string {
	if strings.HasSuffix(schemaName, "-types") {
		return n.NpmScope + "/" + schemaName
	}
	return n.NpmScope + "/" + schemaName + "-types"
}

// NpmSDKPackage returns the npm name of a schema's generated TypeScript SDK.
func (n Naming) NpmSDKPackage(schemaName string) string {
	return n.NpmScope + "/" + schemaName + "-sdk"
}

// PythonTypesModule returns the Python module name for a schema stem (the
// schema name already folded to identifier characters by the caller).
func (n Naming) PythonTypesModule(stem string) string {
	return n.PythonTypesModulePrefix + stem
}

// PythonSDKModule returns the Python SDK module name for a schema stem.
func (n Naming) PythonSDKModule(stem string) string {
	return n.PythonSDKModulePrefix + stem + n.PythonSDKModuleSuffix
}

// RustTypesCrate returns the crate name of a schema's generated Rust types.
func (n Naming) RustTypesCrate(schemaName string) string {
	return n.RustCratePrefix + schemaName + "-types"
}

// RustSDKCrate returns the crate name of a schema's generated Rust SDK.
func (n Naming) RustSDKCrate(schemaName string) string {
	return n.RustCratePrefix + schemaName + "-sdk"
}

// RustAPICrate returns the crate name of a schema's generated Rust server.
func (n Naming) RustAPICrate(schemaName string) string {
	return n.RustCratePrefix + schemaName + "-api"
}

// ScalarRustCrateIdent is the scalar crate as Rust source spells it: the
// Cargo name with '-' folded to '_'.
func (n Naming) ScalarRustCrateIdent() string {
	return strings.ReplaceAll(n.ScalarRustCrate, "-", "_")
}

// Load reads <schemasRoot>/superschematic.toml. A missing file returns
// Default(); a file that exists but does not parse, or that names a key the
// struct does not have, is an error.
func Load(schemasRoot string) (Naming, error) {
	return LoadFile(filepath.Join(schemasRoot, FileName))
}

// Discover walks up from start looking for superschematic.toml and loads the
// first one found. Commands that take a file rather than the schemas root
// (format) use it; no file within the walk returns Default().
func Discover(start string) (Naming, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return Naming{}, fmt.Errorf("naming: resolve %s: %w", start, err)
	}
	if info, statErr := os.Stat(dir); statErr == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		path := filepath.Join(dir, FileName)
		if _, statErr := os.Stat(path); statErr == nil {
			return LoadFile(path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Default(), nil
		}
		dir = parent
	}
}

// LoadFile is Load for an explicit path.
func LoadFile(path string) (Naming, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Naming{}, fmt.Errorf("naming: read %s: %w", path, err)
	}
	return Parse(data, path)
}

// Parse decodes TOML bytes over Default(). name labels errors.
func Parse(data []byte, name string) (Naming, error) {
	n := Default()
	md, err := toml.Decode(string(data), &n)
	if err != nil {
		return Naming{}, fmt.Errorf("naming: parse %s: %w", name, err)
	}
	// Keys under [extension.*] decode into map[string]any, which the toml
	// package reports as undecoded; they are the extension's to validate,
	// so only keys outside that table are unknown here.
	var unknown []string
	for _, key := range md.Undecoded() {
		if len(key) >= 2 && key[0] == "extension" {
			continue
		}
		unknown = append(unknown, key.String())
	}
	if len(unknown) > 0 {
		return Naming{}, fmt.Errorf("naming: %s: unknown keys: %s", name, strings.Join(unknown, ", "))
	}
	return n.OrDefault(), nil
}

var (
	activeMu sync.RWMutex
	active   = Default()
)

// Active returns the process-wide naming, the fallback for the two packages
// with no options path: the schema writer (format) and schemadeps (the
// depfile build-all emits). Everything reached from
// generator.Run reads Options.Naming and the loader reads
// loader.WithNaming; new code should take the value as a parameter rather
// than read it here.
func Active() Naming {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return active
}

// SetActive replaces the process-wide naming. Only the CLI commands call it,
// once, after loading the file and before any work runs. Tests that call it
// must restore Default() in t.Cleanup. Empty fields fall back to the
// defaults.
func SetActive(n Naming) {
	activeMu.Lock()
	defer activeMu.Unlock()
	active = n.OrDefault()
}
