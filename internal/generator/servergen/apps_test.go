package servergen_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The app fixture (D78): desk-db, the engine database; desk-app-api, a
// Rust API over it whose users are the user model's and whose operations
// have a service clause, declaring the app Backoffice; and desk-app-stack,
// which deploys it on stacktest's fake target and exposes the app.
const (
	appStack = "desk-app-stack"
	appAPI   = "desk-app-api"
	appName  = "desk-app-api-backoffice"
)

// appOrder is the order a build-all builds the app fixture in.
var appOrder = []string{"desk-db", appAPI, appStack}

// loadAppFixture loads the app fixture with the core registry and
// stacktest's fake target.
func loadAppFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range appOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(engineRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// TestAppEntrypointGolden: the API's crate declares BackofficeParts in
// apps.rs; the stack's build writes the app's entrypoint beside the
// server's, which builds desk-app-api's Deps, identity service, service
// authenticator and Implementations as the server does, opens desk-db's
// engine and deploys its document, waits for it to open, and calls the
// app crate's serve with the parts; it scaffolds the missing app crate at
// apps/backoffice, whose serve fails, and mirrors it into the output root
// (D78). Regenerate with:
//
//	go test ./internal/generator/servergen -run TestAppEntrypointGolden -update
func TestAppEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadAppFixture(t)
	f.build(t, repoRoot, rustFakePaths(repoRoot), appOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.RustEngineFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
		files[filepath.Join("server", appStack, appName, filepath.FromSlash(file))] = filepath.Join(servergen.ServerDir(out, appStack, appName), filepath.FromSlash(file))
	}
	for _, file := range []string{"Cargo.toml", "src/lib.rs", "src/apps.rs"} {
		files[filepath.Join("api", appAPI, filepath.FromSlash(file))] = filepath.Join(generator.APIDir(out, appAPI), filepath.FromSlash(file))
	}
	app := naming.Default().RustAppDir(repoRoot, appAPI, "Backoffice")
	for _, file := range []string{rustrestgen.ImplementationManifest, rustrestgen.ImplementationLib} {
		files[filepath.Join("apps", "backoffice", file)] = filepath.Join(app, file)
	}
	files[filepath.Join(servergen.MirrorRoot, appAPI+".backoffice", servergen.RustManifestFile)] = filepath.Join(servergen.AppMirrorDir(out, appAPI, "Backoffice"), servergen.RustManifestFile)
	compareGoldens(t, filepath.Join(goldenRoot, "apps"), files)
	if _, err := os.Stat(filepath.Join(servergen.AppMirrorDir(out, appAPI, "Backoffice"), filepath.FromSlash(servergen.RustMainFile))); err == nil {
		t.Errorf("the app's mirror holds a src/main.rs; the crate has none")
	}
	if _, err := os.Stat(filepath.Join(servergen.AppMirrorDir(out, appAPI, "Backoffice"), rustrestgen.ImplementationLib)); err != nil {
		t.Errorf("the app's mirror holds no src/lib.rs: %v", err)
	}
}

// TestAppOfTheStack: the stack resolves Backoffice on the fake target's
// app platform, in Rust, exposed, with desk-app-api's edges.
func TestAppOfTheStack(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadAppFixture(t)
	f.build(t, repoRoot, rustFakePaths(repoRoot), appOrder...)
	data, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "dist", "stack", appStack, "Cloud", "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	var app *ir.ResolvedDeployable
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableApp {
			app = d
		}
	}
	if app == nil {
		t.Fatalf("environment Cloud has no app: %s", data)
	}
	if app.Name != appName || app.Language != "RUST" || app.Platform != stacktest.AppPlatform || !app.Exposed || app.App == nil || app.App.Name != "Backoffice" || app.App.API != appAPI || !app.App.Enabled {
		t.Errorf("app = %s %s %s exposed %v %+v, want %s RUST %s exposed, Backoffice of %s, enabled", app.Name, app.Language, app.Platform, app.Exposed, app.App, appName, stacktest.AppPlatform, appAPI)
	}
}

// TestAppRefusals: the stack's build refuses an app that needs an
// extension this binary does not link, and an app crate that declares no
// serve; it keeps an app crate that exists as it is.
func TestAppRefusals(t *testing.T) {
	t.Run("extension", func(t *testing.T) {
		repoRoot := t.TempDir()
		f := loadAppFixture(t)
		f.schemas[appAPI].Apps[0].Extensions = []string{"topcoat"}
		var err error
		for _, name := range appOrder {
			if _, err = generator.Run(f.schemas[name], f.configs[name], f.options(repoRoot, rustFakePaths(repoRoot))); err != nil {
				break
			}
		}
		if err == nil || !strings.Contains(err.Error(), "app Backoffice of desk-app-api needs the topcoat extension, which this superschematic binary does not link; run superschematic-topcoat") {
			t.Errorf("build = %v, want the app refused for its extension", err)
		}
		if _, statErr := os.Stat(filepath.Join(servergen.StackDir(filepath.Join(repoRoot, "schemas", "dist"), appStack))); statErr == nil {
			t.Errorf("the refused build wrote the stack's entrypoints")
		}
	})

	t.Run("serve", func(t *testing.T) {
		repoRoot := t.TempDir()
		f := loadAppFixture(t)
		app := naming.Default().RustAppDir(repoRoot, appAPI, "Backoffice")
		if err := os.MkdirAll(filepath.Join(app, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(app, rustrestgen.ImplementationManifest), []byte("[package]\nname = \"backoffice\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(app, rustrestgen.ImplementationLib), []byte("pub async fn run() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var err error
		for _, name := range appOrder {
			if _, err = generator.Run(f.schemas[name], f.configs[name], f.options(repoRoot, rustFakePaths(repoRoot))); err != nil {
				break
			}
		}
		if err == nil || !strings.Contains(err.Error(), "app Backoffice of desk-app-api is the crate at "+app+", which declares no serve in src/lib.rs; add\n\n  pub async fn serve(parts: schemas_desk_app_api_api::apps::BackofficeParts) -> std::io::Result<()>") {
			t.Errorf("build = %v, want the app crate without serve refused", err)
		}
		if err := os.WriteFile(filepath.Join(app, rustrestgen.ImplementationLib), []byte("pub async fn serve(_parts: schemas_desk_app_api_api::apps::BackofficeParts) -> std::io::Result<()> { Ok(()) }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.build(t, repoRoot, rustFakePaths(repoRoot), appStack)
		manifest, err := os.ReadFile(filepath.Join(servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), appStack, appName), servergen.RustManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(manifest), "\nbackoffice = { path = \"../../../impl/desk-app-api.backoffice\" }\n") {
			t.Errorf("the app's Cargo.toml depends on no mirror of the crate named backoffice:\n%s", manifest)
		}
	})
}

// TestAppEntrypointCompiles: the app's entrypoint, its API's crate and the
// scaffolded app crate's mirror pass clippy in the output root's
// workspace, frozen to the lockfile the stack's build wrote, and the
// entrypoint's main.rs is laid out as rustfmt lays it out (D78).
func TestAppEntrypointCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	repoRoot, err := filepath.EvalSymlinks(testpaths.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	f := loadAppFixture(t)
	f.build(t, repoRoot, paths, appOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "clippy", "--locked", "--workspace", "--quiet", "--", "-D", "warnings")
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		for _, file := range []string{servergen.RustMainFile, servergen.RustEngineFile} {
			source := filepath.Join(servergen.ServerDir(out, appStack, appName), filepath.FromSlash(file))
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
			}
		}
		for _, file := range []string{"src/apps.rs"} {
			source := filepath.Join(generator.APIDir(out, appAPI), filepath.FromSlash(file))
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
			}
		}
	}
}
