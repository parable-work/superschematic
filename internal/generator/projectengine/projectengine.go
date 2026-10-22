// Package projectengine builds the project's engine crate of a schema
// whose types compose a behavior the core's engine archive does not run
// (D58): the work queue's, the commerce behaviors' (D70), or an
// extension's. A project's engine is a value its build assembles, not a
// registry the linker collects: the core's behaviors, the work queue's
// and the commerce ones when the schema uses one of them, and each
// extension behavior the schema composes, from the Rust crate its
// BehaviorSpec names (registry.BehaviorRust). A schema that composes the
// core's behaviors alone needs no crate: the release's engine carries
// them. The work queue's and the commerce crates are the release's too,
// and the core's Node-API addon carries both, but the core's archive,
// which a Go server links, leaves them out, so a schema that composes one
// of their behaviors gets a crate whose archive holds it.
//
// The crate is <output root>/engine/rust/<schema>, named
// Naming.RustEngineCrate(schema) (<prefix><schema>-engine):
//
//   - Cargo.toml depends on superschematic-engine,
//     superschematic-engine-behaviors and, when used,
//     superschematic-engine-workqueue and superschematic-engine-commerce,
//     by path at the [paths] keys engine_rust, engine_behaviors_rust,
//     engine_workqueue_rust and engine_commerce_rust, or by version when a
//     key is unset, all without their database features,
//     which the crate's own sqlite and postgres features (both by default)
//     turn on; and on each extension's crate by path, its BehaviorRust.Path
//     resolved against the project, the parent of the schemas root.
//   - src/lib.rs re-exports superschematic_engine, and gives COMPOSED, the
//     behaviors the schema's types compose; behaviors(), the BehaviorSet
//     for EngineOptions::behaviors; and TOOLS and tools(), the D11 options
//     of the binary that wrote it (enginedoc.ToolsFor), for
//     EngineOptions::tools, under which an extension's operations that name
//     its own invocation policy's values register. Its test opens the
//     engine on SQLite with both and finds every composed behavior
//     registered. This library is what a Rust server links.
//
// A host of another language links a binding of the engine built over the
// library's behaviors(): a crate of its own in a directory of the crate's,
// a member of the workspace the crate's Cargo.toml roots (Members), so the
// library and its bindings build from one lock file into one target
// directory, wherever the output root lies:
//
//   - ffi/ (FFIDir), <name>-ffi: the engine's C ABI over behaviors(),
//     export_engine_c_abi!(behaviors = <ident>::behaviors) from
//     superschematic-engine-abi at [paths] engine_abi_rust, the static
//     archive a Go server links in the core's place. Its test opens an
//     engine through the archive's own eng_open, as the Go binding does,
//     and finds every composed behavior registered. scripts/engine-archive.sh
//     builds and stages it:
//
//     scripts/engine-archive.sh --crate <crate>/ffi --out <dir>
//
//   - napi/ (NapiDir), <name>-napi: the engine's Node-API addon over
//     behaviors(), export_engine_napi!(behaviors = <ident>::behaviors) from
//     superschematic-engine-napi at [paths] engine_napi_rust, a cdylib with
//     the napi-build script a TypeScript server loads in the core addon's
//     place, by naming the .node file (the engine option addon, or
//     SUPERSCHEMATIC_ENGINE_ADDON). An engine registers the core's
//     behaviors when it opens and each other one its options' behaviors
//     name. Node-API symbols resolve only when Node.js or Bun loads it, so
//     it has no Rust test; scripts/engine-napi.sh builds it, and a
//     TypeScript test opens an engine on it:
//
//     scripts/engine-napi.sh --crate <crate>/napi --out <dir>
//
// The workspace has no lock file of its own: the first build resolves
// one. superschematic-engine-napi's manifest pins napi-derive-backend, the
// one dependency a fresh resolve breaks, so it resolves in every workspace
// that builds an addon over it. The workspace's dist profile is the one the
// release builds its own archive and addon with (fat LTO, one codegen
// unit, stripped), which scripts/engine-napi.sh --dist names.
//
// Build writes nothing when an extension behavior the schema composes names
// no Rust crate, or names one the project does not hold, and says why: an
// extension's behavior runs in the engine from its crate alone (D58), so no
// engine can run such a schema, and an engine refuses it as it refuses any
// schema that composes a behavior it is not built with.
package projectengine

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/enginedoc"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The engine's crates, which the project's engine crate depends on, the C
// ABI's, which its ffi member does, and the Node-API addon's, which its
// napi member does.
const (
	EngineCrate    = "superschematic-engine"
	BehaviorsCrate = "superschematic-engine-behaviors"
	WorkQueueCrate = "superschematic-engine-workqueue"
	CommerceCrate  = "superschematic-engine-commerce"
	ABICrate       = "superschematic-engine-abi"
	NapiCrate      = "superschematic-engine-napi"
	// NapiBuildVersion is napi-build's, which the napi member's build
	// script runs: the version superscalar's lock file and the engine's
	// hold, as superschematic-engine-napi pins its napi crates.
	NapiBuildVersion = "2.5.0"
	// EngineVersion is the version the crates have when no [paths] key
	// names a checkout of them: the engine workspace's.
	EngineVersion = "0.0.0"
)

// Header opens every file the crate holds, by which a build knows a crate
// in the directory is its own.
const Header = "Code generated by superschematic. DO NOT EDIT."

// The files of the crate, and its members'.
const (
	ManifestFile = "Cargo.toml"
	LibFile      = "src/lib.rs"
	BuildFile    = "build.rs"
	// FFIDir is the member that builds the C ABI's archive.
	FFIDir          = "ffi"
	FFIManifestFile = FFIDir + "/" + ManifestFile
	FFILibFile      = FFIDir + "/" + LibFile
	// NapiDir is the member that builds the Node-API addon.
	NapiDir          = "napi"
	NapiManifestFile = NapiDir + "/" + ManifestFile
	NapiLibFile      = NapiDir + "/" + LibFile
	NapiBuildFile    = NapiDir + "/" + BuildFile
)

// Members are the directories of the bindings the crate's workspace
// holds beside the library, in the order its Cargo.toml lists them.
var Members = []string{FFIDir, NapiDir}

// files are the files a build writes, by their path in the crate, with
// the template that renders each.
var files = []struct{ path, template string }{
	{ManifestFile, "Cargo.toml.tmpl"},
	{LibFile, "lib.rs.tmpl"},
	{FFIManifestFile, "ffi.Cargo.toml.tmpl"},
	{FFILibFile, "ffi.lib.rs.tmpl"},
	{NapiManifestFile, "napi.Cargo.toml.tmpl"},
	{NapiLibFile, "napi.lib.rs.tmpl"},
	{NapiBuildFile, "napi.build.rs.tmpl"},
}

//go:embed templates/*.tmpl
var templatesFS embed.FS

var templates = template.Must(template.New("projectengine").Funcs(template.FuncMap{
	"quote":     quoteTOML,
	"rawString": rustutil.RawString,
}).ParseFS(templatesFS, "templates/*.tmpl"))

// Options configures Build.
type Options struct {
	// SchemaName is the schema's service name.
	SchemaName string
	// Registry declares the behaviors, their crates and the tool options:
	// the registry the build runs with. Required.
	Registry *registry.Registry
	// Naming names the crate.
	Naming naming.Naming
	// RepositoryRoot is the project, the parent of the schemas root, which
	// an extension crate's path is relative to. Empty, no extension crate
	// can be found, and Build writes nothing.
	RepositoryRoot string
	// Paths locates the engine's crates for path dependencies; an unset
	// key names the crate's version instead.
	Paths naming.LocalPaths
	// Dir is the crate's directory, which every path dependency is
	// relative to: from its physical path (naming.PhysicalRelPath), since
	// cargo run in the crate reads its manifest there, as macOS reports a
	// temporary directory under /private/var rather than /var.
	Dir string
	// RustVersion is the toolchain the crate states as its rust-version.
	RustVersion string
}

// Crate is a schema's project engine crate.
type Crate struct {
	// Name is the Cargo package name and Ident the crate as Rust spells it.
	Name, Ident string
	// Schema is the schema's service name.
	Schema string
	// RustVersion is the crate's rust-version.
	RustVersion string
	// Composed are the behaviors the schema's types compose, sorted by
	// name.
	Composed []string
	// Engine, Behaviors, WorkQueue and Commerce are the engine's crates
	// the crate depends on; WorkQueue is nil when no composed behavior is
	// the work queue's, and Commerce when none is a commerce behavior
	// (D70). ABI is the C ABI's, which the ffi member depends on, and Napi
	// the Node-API addon's, which the napi member does.
	Engine, Behaviors *Dependency
	WorkQueue         *Dependency
	Commerce          *Dependency
	ABI               *Dependency
	Napi              *Dependency
	// Members are the workspace's members beside the crate (Members).
	Members []string
	// Extensions are the extensions' crates, sorted by crate name.
	Extensions []Extension
	// Tools are the binary's tool options as JSON, the shape the engine
	// document's tools have.
	Tools string
}

// Dependency is one of the engine's crates: by path when Path is set,
// else by EngineVersion.
type Dependency struct {
	Crate string
	Path  string
}

// Spec is the dependency's Cargo.toml value, without the crate's database
// features: the project's engine crate's features turn them on.
func (d Dependency) Spec() string {
	if d.Path != "" {
		return fmt.Sprintf("{ path = %s, default-features = false }", quoteTOML(d.Path))
	}
	return fmt.Sprintf("{ version = %s, default-features = false }", quoteTOML(EngineVersion))
}

// Extension is an extension's crate and the behaviors the crate registers
// from it.
type Extension struct {
	Crate string
	// Path is the crate's directory relative to the crate's own.
	Path string
	// Behaviors are the composed behaviors it implements, sorted by name.
	Behaviors []Registration
}

// Registration is one extension behavior the crate registers.
type Registration struct {
	// Name is the behavior's registered name; Extension the extension that
	// declares it.
	Name, Extension string
	// Register is the Rust expression BehaviorSet::with takes.
	Register string
}

// Build returns the project engine crate of a schema, or nil when it needs
// none: no type composes a behavior of the work queue, of the commerce
// package or of an extension.
// A nil crate with a non-empty reason is one the schema would need and
// Build cannot write, because an extension behavior it composes names no
// Rust crate, or names one the project does not hold. It fails when a type
// composes a behavior the registry does not declare.
func Build(schema *ir.Schema, opts Options) (*Crate, string, error) {
	reg := opts.Registry
	if reg == nil {
		return nil, "", errors.New("projectengine: no registry")
	}
	composed := composedBehaviors(schema)
	if len(composed) == 0 {
		return nil, "", nil
	}
	c := &Crate{
		Name:        opts.Naming.RustEngineCrate(opts.SchemaName),
		Schema:      opts.SchemaName,
		RustVersion: opts.RustVersion,
		Composed:    composed,
		Members:     Members,
	}
	c.Ident = strings.ReplaceAll(c.Name, "-", "_")
	var missing []string
	crates := map[string]*Extension{}
	for _, name := range composed {
		b, ok := reg.Behavior(name)
		if !ok {
			return nil, "", fmt.Errorf("projectengine: %s composes behavior %s, which the registry does not declare", opts.SchemaName, name)
		}
		switch {
		case b.Extension == "" && b.Package == registry.WorkQueuePackage:
			c.WorkQueue = &Dependency{Crate: WorkQueueCrate}
		case b.Extension == "" && b.Package == registry.CommercePackage:
			c.Commerce = &Dependency{Crate: CommerceCrate}
		case b.Extension == "":
		case !b.Rust.Declared():
			missing = append(missing, fmt.Sprintf("%s (extension %s) names no Rust crate, and an extension's behavior runs from its crate alone", name, b.Extension))
		default:
			ext := crates[b.Rust.Crate]
			if ext == nil {
				dir, why := crateDir(opts.RepositoryRoot, b)
				if why != "" {
					missing = append(missing, why)
					continue
				}
				rel, err := naming.PhysicalRelPath(opts.Dir, dir)
				if err != nil {
					return nil, "", fmt.Errorf("projectengine: crate %s of %s: %w", b.Rust.Crate, name, err)
				}
				ext = &Extension{Crate: b.Rust.Crate, Path: rel}
				crates[b.Rust.Crate] = ext
			}
			ext.Behaviors = append(ext.Behaviors, Registration{Name: name, Extension: b.Extension, Register: b.Rust.Register})
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Sprintf("no engine can run %s's behaviors, so no engine crate is written and an engine refuses the schema: %s (D58)", opts.SchemaName, strings.Join(missing, "; ")), nil
	}
	if len(crates) == 0 && c.WorkQueue == nil && c.Commerce == nil {
		return nil, "", nil
	}
	for _, ext := range crates {
		c.Extensions = append(c.Extensions, *ext)
	}
	sort.Slice(c.Extensions, func(i, j int) bool { return c.Extensions[i].Crate < c.Extensions[j].Crate })

	var err error
	if c.Engine, err = dependency(EngineCrate, opts.Paths.EngineRust, opts.Dir); err != nil {
		return nil, "", err
	}
	if c.Behaviors, err = dependency(BehaviorsCrate, opts.Paths.EngineBehaviorsRust, opts.Dir); err != nil {
		return nil, "", err
	}
	if c.WorkQueue != nil {
		if c.WorkQueue, err = dependency(WorkQueueCrate, opts.Paths.EngineWorkQueueRust, opts.Dir); err != nil {
			return nil, "", err
		}
	}
	if c.Commerce != nil {
		if c.Commerce, err = dependency(CommerceCrate, opts.Paths.EngineCommerceRust, opts.Dir); err != nil {
			return nil, "", err
		}
	}
	// The members' dependencies, relative to their own directories.
	if c.ABI, err = dependency(ABICrate, opts.Paths.EngineABIRust, filepath.Join(opts.Dir, FFIDir)); err != nil {
		return nil, "", err
	}
	if c.Napi, err = dependency(NapiCrate, opts.Paths.EngineNapiRust, filepath.Join(opts.Dir, NapiDir)); err != nil {
		return nil, "", err
	}

	tools, err := enginedoc.ToolsFor(schema, reg)
	if err != nil {
		return nil, "", fmt.Errorf("projectengine: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tools); err != nil {
		return nil, "", fmt.Errorf("projectengine: encode the tool options: %w", err)
	}
	c.Tools = strings.TrimSuffix(buf.String(), "\n")
	return c, "", nil
}

// composedBehaviors lists the behaviors the schema's types compose, once
// each, sorted by name.
func composedBehaviors(schema *ir.Schema) []string {
	seen := map[string]bool{}
	var names []string
	for _, td := range codegen.EngineTypes(schema) {
		for _, ref := range td.Behaviors {
			if !seen[ref.Name] {
				seen[ref.Name] = true
				names = append(names, ref.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// crateDir resolves an extension behavior's crate against the project and
// checks the project holds it: its Cargo.toml is there. It returns why not
// when it cannot.
func crateDir(repositoryRoot string, b registry.Behavior) (string, string) {
	if repositoryRoot == "" {
		return "", fmt.Sprintf("%s's crate %s is at %s in the project, and the build names no project", b.Name, b.Rust.Crate, b.Rust.Path)
	}
	dir := filepath.Join(repositoryRoot, filepath.FromSlash(b.Rust.Path))
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err != nil {
		return "", fmt.Sprintf("%s's crate %s is not at %s in the project (%s holds no %s)", b.Name, b.Rust.Crate, b.Rust.Path, dir, ManifestFile)
	}
	return dir, ""
}

// dependency is one of the engine's crates, by path relative to the
// directory of the crate that depends on it when its [paths] key is set.
func dependency(crate, dir, from string) (*Dependency, error) {
	rel, err := naming.PhysicalRelPath(from, dir)
	if err != nil {
		return nil, fmt.Errorf("projectengine: %s: %w", crate, err)
	}
	return &Dependency{Crate: crate, Path: rel}, nil
}

// Features lists, for one database feature, the features of the engine's
// crates it turns on.
func (c *Crate) Features(feature string) string {
	deps := []*Dependency{c.Engine, c.Behaviors}
	if c.WorkQueue != nil {
		deps = append(deps, c.WorkQueue)
	}
	if c.Commerce != nil {
		deps = append(deps, c.Commerce)
	}
	quoted := make([]string, len(deps))
	for i, d := range deps {
		quoted[i] = quoteTOML(d.Crate + "/" + feature)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// FFIName is the Cargo package name of the ffi member, which the archive
// reports as its project (eng_open's and the abi call's project).
func (c *Crate) FFIName() string { return c.Name + "-ffi" }

// FFIFeatures lists, for one database feature, the features of the ffi
// member's dependencies it turns on: the library's and the C ABI's.
func (c *Crate) FFIFeatures(feature string) string {
	return "[" + quoteTOML(c.Name+"/"+feature) + ", " + quoteTOML(c.ABI.Crate+"/"+feature) + "]"
}

// NapiName is the Cargo package name of the napi member, which the addon
// reports as its project (its project()).
func (c *Crate) NapiName() string { return c.Name + "-napi" }

// NapiFeatures lists, for one database feature, the features of the napi
// member's dependencies it turns on: the library's and the addon's.
func (c *Crate) NapiFeatures(feature string) string {
	return "[" + quoteTOML(c.Name+"/"+feature) + ", " + quoteTOML(c.Napi.Crate+"/"+feature) + "]"
}

// NapiBuild is napi-build's version requirement, exact.
func (c *Crate) NapiBuild() string { return quoteTOML("=" + NapiBuildVersion) }

// Registrations are the extension behaviors the crate registers, sorted by
// name: the order behaviors() adds them in after the core's.
func (c *Crate) Registrations() []Registration {
	var out []Registration
	for _, ext := range c.Extensions {
		out = append(out, ext.Behaviors...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Files renders the crate's files, by their path in the crate.
func (c *Crate) Files() (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, file := range files {
		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, file.template, c); err != nil {
			return nil, fmt.Errorf("projectengine: %s: %w", file.path, err)
		}
		out[file.path] = buf.Bytes()
	}
	return out, nil
}

// sourceDirs are the directories of the crate's sources, which a build
// replaces whole, so a file an earlier build wrote and this one does not
// goes.
func sourceDirs(dir string) []string {
	out := []string{filepath.Join(dir, "src")}
	for _, member := range Members {
		out = append(out, filepath.Join(dir, member, "src"))
	}
	return out
}

// Write writes the crate into dir, replacing the files a build wrote
// before. What cargo writes beside them, the lock file and the target
// directory, stays.
func (c *Crate) Write(dir string) error {
	rendered, err := c.Files()
	if err != nil {
		return err
	}
	for _, src := range sourceDirs(dir) {
		if err := os.RemoveAll(src); err != nil {
			return fmt.Errorf("projectengine: %w", err)
		}
	}
	for _, file := range files {
		path := filepath.Join(dir, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("projectengine: %w", err)
		}
		if err := os.WriteFile(path, rendered[file.path], 0o644); err != nil {
			return fmt.Errorf("projectengine: %w", err)
		}
	}
	return nil
}

// RemoveStale removes the crate an earlier build wrote into dir, when the
// schema no longer needs one, its members' files with it, and the
// directories above it up to root that are left empty. A directory whose
// Cargo.toml a build did not write is left alone.
func RemoveStale(dir, root string) error {
	manifest, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("projectengine: %w", err)
	}
	if !bytes.HasPrefix(manifest, []byte("# "+Header)) {
		return nil
	}
	stale := sourceDirs(dir)
	for _, file := range files {
		stale = append(stale, filepath.Join(dir, filepath.FromSlash(file.path)))
	}
	for _, path := range stale {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("projectengine: remove the stale %s: %w", path, err)
		}
	}
	for _, member := range Members {
		if err := removeIfEmpty(filepath.Join(dir, member)); err != nil {
			return err
		}
	}
	for d := dir; d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if entries, err := os.ReadDir(d); err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(d); err != nil {
			return fmt.Errorf("projectengine: remove the empty %s: %w", d, err)
		}
	}
	return nil
}

// removeIfEmpty removes dir when it is there and empty.
func removeIfEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return nil
	}
	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("projectengine: remove the empty %s: %w", dir, err)
	}
	return nil
}

// quoteTOML quotes s as a TOML basic string, which is a Rust string
// literal too.
func quoteTOML(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
