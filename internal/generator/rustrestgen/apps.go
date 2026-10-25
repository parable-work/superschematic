package rustrestgen

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The apps of a Rust API (D78): src/apps.rs declares `<App>Parts` per @app
// class, what the app's entrypoint builds as the API's server builds its
// own and hands to the app's program: the API's Deps, its Implementations,
// the engine of its engine database once it has opened, and the address
// the app listens on. The program is a library crate the project writes,
// at the naming file's [implementation_paths] rust_app template, which
// declares
//
//	pub async fn serve(parts: <api>::apps::<App>Parts) -> std::io::Result<()>
//
// and which the build scaffolds once when it is missing.

// ServeFunc is the function an app's crate declares in its src/lib.rs,
// which the app's entrypoint calls with the app's parts (D78).
const ServeFunc = "serve"

// AppsModule is the module of the API crate that declares the apps' parts.
const AppsModule = "apps"

// AppInfo is an app of the API: its @app class, Name, and Parts, the
// struct of src/apps.rs its program's serve takes.
type AppInfo struct {
	Name  string
	Parts string
}

// PartsName is the struct of the app's parts: its name in Pascal case and
// Parts.
func (a AppInfo) PartsName() string { return rustutil.ToPascalCase(a.Name) + "Parts" }

// appsOf reads the API's apps, sorted by name.
func appsOf(schema *ir.Schema) []AppInfo {
	var apps []AppInfo
	for _, app := range schema.Apps {
		if app == nil {
			continue
		}
		info := AppInfo{Name: app.Name}
		info.Parts = info.PartsName()
		apps = append(apps, info)
	}
	slices.SortFunc(apps, func(a, b AppInfo) int { return strings.Compare(a.Name, b.Name) })
	return apps
}

// HasApps reports whether the API declares an app, for which the crate
// writes src/apps.rs. Its parts hold the API's Deps, so an API crate
// without them declares none.
func (o *APIOutput) HasApps() bool { return o.Deps != nil && len(o.Apps) > 0 }

// App returns the app named name, or nil.
func (o *APIOutput) App(name string) *AppInfo {
	for i := range o.Apps {
		if o.Apps[i].Name == name {
			return &o.Apps[i]
		}
	}
	return nil
}

// APICrateIdent is the API crate as Rust source spells it.
func (o *APIOutput) APICrateIdent() string {
	return strings.ReplaceAll(o.CrateName, "-", "_")
}

// AppsEngine is the engine's crate as Rust source spells it when the
// API's Deps hold the engine of its engine database, whose opened engine
// each app's parts hold; empty without one.
func (o *APIOutput) AppsEngine() string {
	if o.Deps == nil || o.Deps.Engine == nil {
		return ""
	}
	return o.Deps.Engine.CrateIdent()
}

// AppScaffold is what the scaffold of an app's crate is written from.
type AppScaffold struct {
	*APIOutput
	App AppInfo

	// Crate is the crate's name (Naming.RustAppImplementationCrate), and
	// APIPath the API crate's directory relative to the crate's, which its
	// Cargo.toml depends on.
	Crate   string
	APIPath string
}

// WriteAppScaffold writes the crate of the app named app of the API at dir
// when no crate is there yet, reporting whether it wrote one: Cargo.toml,
// which depends on the API crate at apiDir by its path relative to dir,
// for the crate's editor and its own `cargo check`, and src/lib.rs, whose
// serve listens on the parts' address and answers every request with 501
// Not Implemented, so the stack runs the app, ready, until the project
// writes it. A stack's app builds
// against the crate's mirror under its output root, whose Cargo.toml names
// that root's API crate (D65, D78).
func WriteAppScaffold(output *APIOutput, app, dir, apiDir string, names naming.Naming) (bool, error) {
	info := output.App(app)
	if info == nil || !output.HasApps() {
		return false, fmt.Errorf("rustrestgen: %s declares no app %s whose parts its crate holds", output.SchemaName, app)
	}
	exists, err := ImplementationExists(dir)
	if err != nil || exists {
		return false, err
	}
	view := AppScaffold{APIOutput: output, App: *info, Crate: names.OrDefault().RustAppImplementationCrate(output.SchemaName, app)}
	if view.APIPath, err = naming.RelPath(dir, apiDir); err != nil {
		return false, fmt.Errorf("rustrestgen: the API crate's path from app %s of %s: %w", app, output.SchemaName, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return false, fmt.Errorf("create the crate of app %s: %w", app, err)
	}
	for _, file := range []struct{ template, name string }{
		{"app-cargo.tmpl", ImplementationManifest},
		{"app-lib.tmpl", ImplementationLib},
	} {
		if _, err := codegen.GenerateFileIfNotExists(generateFile, file.template, filepath.Join(dir, file.name), view); err != nil {
			return false, fmt.Errorf("write the scaffold of app %s: %w", app, err)
		}
	}
	return true, nil
}
